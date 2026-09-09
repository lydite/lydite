package coverage

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/cover"

	"lydite/lydite/internal/annotation"
	"lydite/lydite/internal/executil"
)

// LineHits maps a repo-relative file path (forward-slash, matching git's own
// path convention) to a map of line number -> hit count, as reported by one
// ecosystem's coverage tooling. Lydite treats this as the single common
// intermediate shape all three ecosystems converge on, however each one's
// native report format got there (a Go coverage profile, an lcov file).
type LineHits map[string]map[int]int

// hunkHeader matches a unified diff hunk header's new-file half, e.g.
// "@@ -12,3 +15,4 @@" -> start line 15, length 4 (length defaults to 1 when
// omitted, e.g. "@@ -12 +15 @@").
var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// ChangedLines returns, per repo-relative file path, the line numbers added
// or modified by dir's working tree relative to mergeBase — only files whose
// name ends in one of exts are considered. Deleted lines and unchanged
// context lines never count; `--unified=0` already drops context lines from
// the diff itself, so only "@@" headers and "+" lines need parsing. This
// deliberately does no language-aware filtering (comments, blank lines,
// imports) — that happens later, when these line numbers are intersected
// with a coverage report, since PatchPercent counts only lines the report
// actually mentions. Keeping the filtering there rather than here means it
// happens once per report format, in the one place that knows whether that
// format lists non-executable lines: lcov never does, a Go profile's block
// spans do, so ParseGoProfile drops them explicitly.
func ChangedLines(ctx context.Context, dir, mergeBase string, exts ...string) (map[string][]int, error) {
	// -c diff.mnemonicPrefix=false pins the "+++ b/<path>" header form
	// parseUnifiedDiff expects, regardless of the caller's own git config —
	// mnemonicPrefix=true would emit "+++ w/<path>" instead, which
	// parseUnifiedDiff's "b/" strip wouldn't recognize.
	//
	// --relative makes the emitted paths relative to dir rather than the
	// repository root — they must line up with everything else patch coverage
	// works in --dir-relative terms: crate/package prefixes, lcov paths
	// normalized against dir, Go profile paths under the module at dir. When
	// dir IS the repo root the flag changes nothing; when it's a subdirectory
	// (wardnet runs lydite with --dir source), root-relative "source/..."
	// keys would match no crate prefix and every changed line silently
	// vanished from the patch gate's denominator. It also scopes the diff to
	// dir, which is exactly the gate's remit — changes outside --dir aren't
	// measured, so they can't be gated.
	// core.quotePath=false as well as the prefix pin, and for the same class
	// of reason internal/gitdiff pins it: with git's default a changed file
	// whose name is not ASCII is emitted quoted and octal-escaped
	// (`+++ "b/caf\303\251.go"`), the `b/` strip does not match the leading
	// quote, and the key matches nothing in the report — so those changed
	// lines drop silently out of the patch denominator.
	args := []string{
		"-c", "core.quotePath=false",
		"-c", "diff.mnemonicPrefix=false",
		"diff", "--relative", "--unified=0", mergeBase + "..HEAD",
	}
	if len(exts) > 0 {
		args = append(args, "--")
		for _, ext := range exts {
			args = append(args, "*"+ext)
		}
	}
	// Quiet, because a diff is data this parses and not output anyone
	// watches: streamed, the whole patch lands in the middle of the report,
	// and under --json in the middle of the document.
	r := executil.RunQuiet(ctx, dir, "git", args...)
	if !r.Ok() {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), r.Err)
	}
	return parseUnifiedDiff(r.Output), nil
}

func parseUnifiedDiff(diff string) map[string][]int {
	changed := map[string][]int{}
	var file string
	var nextLine, remaining int
	scanner := bufio.NewScanner(strings.NewReader(diff))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "+++ "):
			path := strings.TrimPrefix(line, "+++ ")
			path = strings.TrimPrefix(path, "b/")
			if path == "/dev/null" {
				file = ""
				continue
			}
			file = filepath.ToSlash(path)
		case strings.HasPrefix(line, "@@ "):
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			start, _ := strconv.Atoi(m[1])
			length := 1
			if m[2] != "" {
				length, _ = strconv.Atoi(m[2])
			}
			nextLine, remaining = start, length
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			if file != "" && remaining > 0 {
				changed[file] = append(changed[file], nextLine)
				nextLine++
				remaining--
			}
		}
	}
	return changed
}

// ParseLCOV extracts an lcov trace file's two quantities in one pass: the
// summed line counts, and the per-file per-line hits. It is the format
// cargo-llvm-cov's --lcov and Istanbul/Vitest's lcov reporter both emit
// natively — "SF:<path>", "DA:<line>,<hits>" pairs and an "LF:"/"LH:" summary
// per file, terminated by "end_of_record". File paths are normalized relative
// to baseDir when absolute, so they line up with git's repo-relative paths.
//
// The counts come from LF and LH, never from tallying the DA lines. Measured
// against the proving ground's three-crate workspace the LF/LH sums are 30 of
// 57, matching cargo-llvm-cov's own JSON export exactly, while the DA lines
// number 55: a line carrying more than one record is one line to LF and two to
// a DA tally. Counting DA would report a denominator smaller than the tool's
// own, in a number the gate then compares against a baseline recorded by that
// same tool.
func ParseLCOV(data []byte, baseDir string) (LineCount, LineHits) {
	var count LineCount
	hits := LineHits{}
	var file string
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "SF:"):
			file = normalizeRelPath(baseDir, strings.TrimPrefix(line, "SF:"))
			if _, ok := hits[file]; !ok {
				hits[file] = map[int]int{}
			}
		case strings.HasPrefix(line, "DA:"):
			if file == "" {
				continue
			}
			parts := strings.SplitN(strings.TrimPrefix(line, "DA:"), ",", 2)
			if len(parts) != 2 {
				continue
			}
			lineNo, err1 := strconv.Atoi(parts[0])
			hitCount, err2 := strconv.Atoi(strings.SplitN(parts[1], ",", 2)[0])
			if err1 != nil || err2 != nil {
				continue
			}
			// The greater count wins, never the later one. A line carrying
			// more than one record is exactly what makes an lcov's LF differ
			// from a tally of its DA lines — measured at 57 against 55 on the
			// proving ground — so "DA:10,1" followed by "DA:10,0" is a line
			// LH counts as hit. Taking the last would have the patch gate
			// score it uncovered while the aggregate scored it covered, two
			// figures disagreeing about one line. ParseGoProfile takes the
			// max for the same reason.
			if prev, seen := hits[file][lineNo]; !seen || hitCount > prev {
				hits[file][lineNo] = hitCount
			}
		case strings.HasPrefix(line, "LF:"):
			if n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "LF:"))); err == nil {
				count.Total += n
			}
		case strings.HasPrefix(line, "LH:"):
			if n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "LH:"))); err == nil {
				count.Covered += n
			}
		case line == "end_of_record":
			file = ""
		}
	}
	return count, hits
}

// GoModuleProfile locates one component's Go coverage profile, together with
// what turning that profile's package-qualified file names into scan-root
// relative paths takes: the module path to strip off the front, and the
// component's own directory to put back on.
//
// Both halves are carried rather than derived, because neither implies the
// other. A module path need not resemble where the module sits —
// `wardnet.network/go` lives under `sdk/wardnet-go` — so nothing about the
// path says which directory to put back.
type GoModuleProfile struct {
	// Profile is the path to the module's `go test -coverprofile` output.
	Profile string
	// ModuleName is the module's path, as `go list -m` reports it.
	ModuleName string
	// RelDir is the component's directory relative to the scan root, "" when
	// the component is rooted at the scan root itself.
	RelDir string
}

// goProfile reads one module's coverage profile and produces every quantity a
// gate takes from it: the counts the aggregate and floor read, the per-line
// hits the patch and complexity gates read, and the lines that actually ran.
//
// One pass, because the three are three views of one file and each one used to
// re-read and re-parse every source file the profile mentions. Parsing rather
// than shelling out to `go tool cover -func` is what lets any of it work from
// anywhere: that command resolves each entry's package-qualified name through
// the module graph, so it only succeeds from inside the module the profile came
// from — from a monorepo root it fails outright, which is how Go coverage came
// to be silently absent from wardnet's gate. The number is the same either way;
// it is already in the file.
//
// It is also the only place a generated file can be dropped, which `go tool
// cover` offers no way to do.
func goProfile(src GoModuleProfile, root string) (Report, error) {
	profiles, err := cover.ParseProfiles(src.Profile)
	if err != nil {
		return Report{}, fmt.Errorf("parsing the coverage profile at %s: %w", src.Profile, err)
	}
	out := Report{Hits: LineHits{}, Executed: LineHits{}}
	for _, p := range profiles {
		rel := goRelPath(p.FileName, src)
		abs := filepath.Join(root, filepath.FromSlash(rel))
		// Generated code is nobody's to hand-test, and it lands in large,
		// entirely-uncovered blocks — wardnet's regenerated REST client alone
		// accounts for 983 of one PR's 1007 changed Go lines. Counting it
		// would make the patch gate a measure of how much code was generated,
		// and mutating it would ask a generator's author for an assertion.
		if isGeneratedGoFile(abs) {
			continue
		}
		// A Go profile records blocks, not statements: every line between a
		// block's braces lands in the report, comments and blank lines
		// included. lcov lists only executable lines, so "absent from the
		// report" safely means "not executable" there and needs no filtering;
		// here it would count a comment added inside an uncovered function as
		// an uncovered new line, and a comment-only pull request would score
		// 0% patch coverage and fail the gate.
		skip := nonExecutableLines(abs)
		// The lines this file's author declared the suite does not measure.
		// Read once for the whole file, because the aggregate, the per-line
		// hits and the executed set all answer to it.
		excluded, err := excludedGoLines(abs, annotation.Coverage)
		if err != nil {
			return Report{}, err
		}
		hits, executed := map[int]int{}, map[int]int{}
		for _, b := range p.Blocks {
			// Out of the denominator as well as the numerator. A declaration
			// says this suite does not measure the function, so counting its
			// statements as uncovered would report the author's own statement
			// back as a hole they have to fill — the reading that makes an
			// exclusion worth nothing.
			if !excluded[b.StartLine] {
				out.Lines.Total += b.NumStmt
				if b.Count > 0 {
					out.Lines.Covered += b.NumStmt
				}
			}
			for line := b.StartLine; line <= b.EndLine; line++ {
				if skip[line] {
					continue
				}
				// The greater count wins, never the later one. A line
				// carrying more than one block is exactly what makes an
				// lcov's LF differ from a tally of its DA lines — measured at
				// 57 against 55 on the proving ground — so a block with no
				// hits following one with some leaves the line hit. Taking
				// the last would have the patch gate score it uncovered while
				// the aggregate scored it covered, two figures disagreeing
				// about one line.
				//
				// `max` against the zero an absent key already reads as,
				// rather than a comparison guarded by whether the key was
				// seen: a hit count is never negative, so the two are the
				// same rule and this one has no boundary to get wrong.
				executed[line] = max(executed[line], b.Count)
				if excluded[line] {
					continue
				}
				hits[line] = max(hits[line], b.Count)
			}
		}
		out.Hits[rel] = hits
		out.Executed[rel] = executed
	}
	return out, nil
}

// ParseGoProfile is one module's per-line hits as the coverage gates see them:
// generated files gone, non-executable lines gone, and every line a
// `[lydite:exclude_from_coverage]` declaration covers gone with them.
//
// Mutation reads Report.Executed instead, which is this with those declared
// lines put back — a declaration names one gate and answers no other.
func ParseGoProfile(src GoModuleProfile, root string) (LineHits, error) {
	rep, err := goProfile(src, root)
	return rep.Hits, err
}

// goProfileLines is one module's covered and total statements: the two numbers
// behind the ratio `go tool cover -func` prints on its `total:` line, minus
// generated files and minus what a declaration excludes.
func goProfileLines(src GoModuleProfile, root string) (LineCount, error) {
	rep, err := goProfile(src, root)
	return rep.Lines, err
}

// goRelPath turns a profile entry's package-qualified file name into a path
// relative to --dir: strip the module path every entry in that module's
// profile is prefixed with, then put back the module's own directory.
//
// The two are unrelated strings. `wardnet.network/go/anomalies.go` in a
// module rooted at `sdk/wardnet-go` is `sdk/wardnet-go/anomalies.go` on disk,
// and nothing about the module path says so — which is why RelDir is carried
// alongside rather than derived.
func goRelPath(fileName string, src GoModuleProfile) string {
	rel := filepath.ToSlash(strings.TrimPrefix(fileName, src.ModuleName+"/"))
	if src.RelDir == "" {
		return rel
	}
	return path.Join(filepath.ToSlash(src.RelDir), rel)
}

// generatedGoHeader matches the comment Go reserves for machine-written
// files (https://golang.org/s/generatedcode). Keying off that line is what
// the rest of the ecosystem does — golangci-lint and Codecov both do — so
// lydite follows the convention rather than inventing a filename pattern
// like `*.gen.go`, which only catches whatever one generator happens to be
// configured to emit.
var generatedGoHeader = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// isGeneratedGoFile reports whether path carries the generated-code header.
//
// The convention puts that line before the package clause, so the scan stops
// there rather than reading whole files — the line is only meaningful in the
// header, and a matching string further down (this file's own doc comment,
// say) means nothing. An unreadable file is treated as not generated: the
// safe direction is to keep counting a file lydite can't classify.
func isGeneratedGoFile(path string) bool {
	f, err := os.Open(path) // #nosec G304 -- path is derived from lydite's own coverage profile, not user input
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	return isGeneratedGo(f)
}

// IsGeneratedGoSource is the same question asked of source already in hand,
// which is the form a mutation run needs: it has read the file to parse it,
// and opening it a second time would let the two answers disagree about a
// file rewritten in between.
func IsGeneratedGoSource(src []byte) bool { return isGeneratedGo(bytes.NewReader(src)) }

// isGeneratedGo is the one implementation of the rule. Both entry points read
// through it so a file and its bytes cannot be classified differently.
func isGeneratedGo(r io.Reader) bool {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "package ") {
			return false
		}
		if generatedGoHeader.MatchString(line) {
			return true
		}
	}
	return false
}

// nonExecutableLines returns the 1-indexed lines of a Go source file that can
// hold no statement: blank lines, and lines whose only content is a `//`
// comment. An unreadable file yields an empty set, degrading to the old
// count-every-line-in-the-block behavior rather than failing the whole patch
// report over one missing source file.
//
// `/* */` comments are deliberately not tracked: doing it correctly means
// lexing (a `/*` inside a string literal opens nothing), and the payoff is
// small — inside a function body, where this matters, Go code overwhelmingly
// uses `//`. A line starting with `*` is likewise left alone: `*p = x` is a
// pointer assignment, not a comment continuation, and wrongly dropping an
// executable line would understate the denominator and let real untested code
// through the gate. Over-counting a rare block comment is the safe direction
// to err in; under-counting a statement is not.
func nonExecutableLines(path string) map[int]bool {
	f, err := os.Open(path) // #nosec G304 -- path comes from lydite's own coverage profile, not user input
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	skip := map[int]bool{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for line := 1; scanner.Scan(); line++ {
		trimmed := strings.TrimSpace(scanner.Text())
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			skip[line] = true
		}
	}
	if scanner.Err() != nil {
		return nil
	}
	return skip
}

// normalizeRelPath converts an absolute path under baseDir (as many coverage
// tools emit) into a repo-relative, forward-slash path matching git's own
// convention. A path that's already relative (or outside baseDir) is passed
// through unchanged, only slash-normalized. baseDir is resolved to an
// absolute path first — filepath.Rel errors when one argument is absolute
// and the other isn't, and baseDir is commonly relative (lydite's own
// --dir defaults to ".").
func normalizeRelPath(baseDir, path string) string {
	if filepath.IsAbs(path) {
		absBase, err := filepath.Abs(baseDir)
		if err == nil {
			if rel, err := filepath.Rel(absBase, path); err == nil && !strings.HasPrefix(rel, "..") {
				return filepath.ToSlash(rel)
			}
		}
	}
	return filepath.ToSlash(path)
}

// PatchPercent cross-references changed with hits: hit/total counts only
// coverable lines (lines hits actually has an entry for) among those
// changed — a changed comment/blank/import line simply has no entry in hits,
// so it's excluded automatically. total == 0 means no coverable line was
// touched by this diff (e.g. the change was comments/whitespace/imports only
// or the file has no matching hits at all) — there is nothing to gate on.
func PatchPercent(changed map[string][]int, hits LineHits) (hit, total int) {
	for file, lines := range changed {
		fileHits, ok := hits[file]
		if !ok {
			continue
		}
		for _, line := range lines {
			count, ok := fileHits[line]
			if !ok {
				continue
			}
			total++
			if count > 0 {
				hit++
			}
		}
	}
	return hit, total
}

// Run is a stretch of changed lines the report covers with no test.
//
// It is the unit a patch-coverage claim is made about, and it is deliberately
// coarser than the line the gate counts. A three-hundred-line untested
// addition is one thing to do and not three hundred, neighbouring uncovered
// lines have nothing to tell them apart, and a claim per line is a surface
// nobody reads.
type Run struct {
	File        string
	First, Last int
	// Lines is how many lines between First and Last the report speaks for
	// and reports as uncovered. It is not the span: a comment or a blank line
	// written inside an untested block lies between the two ends and counts
	// towards neither side of PatchPercent, so a claim measured by the span
	// would say more lines are untested than a gate ever counted.
	Lines int
}

// Uncovered groups the changed lines no test reached into contiguous runs.
//
// A run ends where the change does or where cover resumes: at a changed line
// the report says was executed, or at a line the change never touched, since
// unchanged code between two untested stretches makes them two stretches. A
// changed line the report knows nothing about — a comment, a blank, an import
// — neither extends a run nor breaks one, exactly as it counts towards neither
// side of PatchPercent.
//
// It consults no function boundary, and that is a limit rather than an
// oversight: lydite parses Go and reads lcov for the other two, so a rule that
// asked which function a line was in would answer for one language and guess
// for the rest. Breaking at unchanged code is what a function boundary would
// mostly have bought anyway, since a change that stops and resumes elsewhere
// has left the first place behind.
func Uncovered(changed map[string][]int, hits LineHits) []Run {
	var runs []Run
	for _, file := range slices.Sorted(maps.Keys(changed)) {
		fileHits, ok := hits[file]
		if !ok {
			continue
		}
		lines := slices.Clone(changed[file])
		slices.Sort(lines)
		// A line named twice is one line. Left in, the second copy reads as a
		// gap against the first — the run flushes and reopens — and one
		// stretch is reported as two overlapping claims.
		lines = slices.Compact(lines)

		open := false
		var first, last, prev, count int
		flush := func() {
			if open {
				runs = append(runs, Run{File: file, First: first, Last: last, Lines: count})
				open = false
			}
		}
		for _, line := range lines {
			if open && line != prev+1 {
				// Unchanged code sits between this line and the last one the
				// change touched, so whatever was untested before it ended
				// there.
				flush()
			}
			prev = line
			hitCount, coverable := fileHits[line]
			switch {
			case !coverable:
				// Nothing the report speaks for. It cannot end a run, or a
				// comment written inside an untested block would split it.
			case hitCount > 0:
				flush()
			case open:
				last, count = line, count+1
			default:
				open, first, last, count = true, line, line, 1
			}
		}
		flush()
	}
	return runs
}
