// Package rustapisurface compares a Rust crate's public API between two trees
// on disk and reports every incompatible change as a located finding.
//
// The comparison is `cargo semver-checks` at the version lydite pins, run as a
// subprocess: it is a Rust crate, so there is no library to call from Go and no
// cgo to reach one with. `--release-type minor` is passed unconditionally,
// which is the whole of the compatible-change filter — the tool's own
// catalogue types each lint `major` or `minor`, and under that flag only a
// `major` one can fail whatever the two manifests' versions say. Deriving the
// release type instead would let a version bump in the change under review turn
// a removed function into nothing reported at all. See [ADR 0040]'s amendment.
//
// The package knows nothing about git, components or verdicts. It is handed two
// directories that already hold the crate checked out, and hands back raw
// findings for a caller to finish.
//
// [ADR 0040]: ../../../../docs/adr/0040-an-undeclared-go-api-break-fails-and-a-declared-one-is-referred.md
package rustapisurface

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"lydite/lydite/internal/cargotool"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/rust"
)

// Outcome is how much one comparison could say.
//
// Three states, because a surface that could not be compared reports no
// findings and so does one that was compared and found unbroken. Collapsing the
// two would render a gate that never ran as the green of one that ran and
// passed.
type Outcome int

const (
	// Unmeasurable is the zero value on purpose: a Result nobody filled in says
	// the surface was not compared, never that it is clean.
	Unmeasurable Outcome = iota
	// Unbroken is a comparison that ran and found no incompatible change.
	Unbroken
	// Broken is a comparison that ran and found at least one.
	Broken
)

// Result is what one comparison said.
type Result struct {
	Outcome Outcome
	// Findings are the incompatible changes, and are non-empty exactly when
	// Outcome is Broken.
	Findings []finding.Finding
	// Reason is why the surface could not be compared, in the words of whatever
	// refused — the tool's own account on stderr, or the install that failed.
	// Empty for every other outcome.
	Reason string
}

// The exit codes that are answers. `cargo-semver-checks` has no machine-
// readable output at all, so these and the block structure on stdout are the
// whole of the contract. Any other code is a run that could not be made.
const (
	exitUnbroken = 0
	exitBroken   = 100
)

// removalDetail is what a finding says when the symbol it names is gone from
// the head tree, so the line it carries is the merge-base's.
//
// The same note in the same words internal/apisurface's removals carry:
// review's rendering reads a finding's detail as saying the line is to be
// opened in the merge-base rather than in the reader's own checkout, and one
// gate cannot mean two things by it.
const removalDetail = "removed in this change; located at its declaration in the merge-base"

// Request is one component's comparison.
//
// A struct rather than internal/apisurface's positional arguments, because a
// subprocess needs what an in-process library does not: two environments, which
// are deliberately not the same one, and somewhere to put the output of an
// install a developer would otherwise watch in silence.
type Request struct {
	// BaseDir and HeadDir are directories already holding the crate checked
	// out, each at the root cargo resolves the package from. Materialising the
	// base tree is the caller's work; this package does no git.
	BaseDir, HeadDir string
	// Gate and Component name the caller's Finding.Gate and Finding.Component.
	// Both are supplied rather than fixed here because this package knows
	// nothing about report rows or declared components.
	Gate, Component string
	// Env is the environment the comparison runs under. Check reaches the tool
	// and so both rustdoc builds — the component's resolved toolchain and the
	// environment its declaration asks for. Install reaches nothing but
	// lydite's own provisioning of the pinned tool, and carries none of what
	// the scanned repository supplied: see executil.Env.
	Env executil.Env
	// Progress is where the source build of the pinned tool goes. Nil discards
	// it.
	Progress io.Writer
}

// tool is the pinned cargo-semver-checks.
//
// The version is internal/rust's, read from the Cargo.toml Dependabot watches
// rather than restated here: two copies of a pin agree until one of them is
// bumped.
//
// No Prebuilt, so this one is built from source. cargo-semver-checks publishes
// release archives, and lydite is about to put this binary on PATH and execute
// it — the digest is read out of band or the archive is not used, which is the
// rule cargo-llvm-cov's pin already states.
var tool = cargotool.Tool{Name: "cargo-semver-checks", Version: rust.CargoSemverChecksVersion}

// Compare reports every incompatible change to the public API of the Rust crate
// between req.BaseDir and req.HeadDir.
//
// There is no error return. Every way this comparison fails is one fact for the
// caller — the surface could not be compared, and here is what said so — and a
// second channel carrying half of them would leave a caller that handled only
// the other half silently reporting a pass.
//
// The findings come back raw. Path is relative to the tree the symbol was
// located in — HeadDir, or BaseDir for a removal — and **not** to the scan root,
// so a caller that knows the component's directory rebases them the way scan's
// labelled step does for every other producer. Ordinal and Anchor are left at
// their zero values for the same reason: both are decisions made over a report's
// whole set and against the lines the change touched, neither of which this
// package is given.
//
// [lydite:exclude_from_coverage][the proving ground installs and runs the pinned
// cargo-semver-checks over two real trees; a unit test here would build two
// rustdoc trees with the machine's own cargo — what is lydite's to get right is
// the invocation, which argv states, and what is done with the output, which
// report and the recorded runs test directly]
func Compare(ctx context.Context, req Request) Result {
	progress := req.Progress
	if progress == nil {
		progress = io.Discard
	}
	if err := tool.Install(ctx, req.Env.Install, progress); err != nil {
		return Result{Reason: "installing cargo-semver-checks " + tool.Version + ": " + err.Error()}
	}
	bin, err := tool.Binary()
	if err != nil {
		return Result{Reason: "locating the installed cargo-semver-checks: " + err.Error()}
	}
	// The binary directly rather than through `cargo semver-checks`: a
	// --root-installed subcommand is named plainly cargo-semver-checks and is
	// not on the PATH cargo searches, and it takes the subcommand as its own
	// first argument.
	res := executil.RunQuietEnv(ctx, req.HeadDir, req.Env.Check, bin, argv(req.BaseDir, req.HeadDir)...)
	return readRun(exitCode(res), res.Output, res.Stderr, req)
}

// argv is the invocation, as argv.
//
// The head manifest selects the packages — no package or member flags, so a
// component's surface is the surface of whatever cargo selects from its
// directory, which is the rule every other Rust check already follows.
// --baseline-root takes the checked-out merge-base tree and resolves the head
// manifest's package within it by name, so a workspace root works as one side
// of the comparison. Colour is off because this output is parsed.
func argv(baseDir, headDir string) []string {
	return []string{
		"semver-checks",
		"--manifest-path", filepath.Join(headDir, "Cargo.toml"),
		"--baseline-root", baseDir,
		"--release-type", "minor",
		"--color", "never",
	}
}

// readRun turns one recorded run into a Result.
//
// Split from the invocation so the mapping is testable over the runs that were
// captured from the real tool rather than over a live cargo.
func readRun(code int, stdout, stderr string, req Request) Result {
	switch code {
	case exitUnbroken:
		return Result{Outcome: Unbroken}
	case exitBroken:
		findings := report(stdout, req)
		if len(findings) == 0 {
			// The exit code says a `major` lint failed, so a report naming
			// nothing is output this parser does not understand — which is a
			// comparison lydite cannot read, never a clean surface.
			return Result{Reason: "cargo-semver-checks reported a breaking change and its report names none:\n" + refusal(stdout+stderr)}
		}
		return Result{Outcome: Broken, Findings: findings}
	default:
		return Result{Reason: "cargo-semver-checks could not compare the two trees (exit " + strconv.Itoa(code) + "):\n" + refusal(stderr)}
	}
}

// exitCode is the status the command exited with, and -1 for a command that
// never ran or died on a signal. Neither is an answer, and both take the same
// path as an exit code that is not one.
func exitCode(res executil.Result) int {
	if res.Err == nil {
		return exitUnbroken
	}
	var exit *exec.ExitError
	if errors.As(res.Err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

// The block structure on stdout, which is what a finding is read out of.
const (
	failureOpen   = "--- failure "
	failureClose  = " ---"
	witnessHeader = "Failed in:"
)

// report is every failing lint's every occurrence, as a located claim.
//
// A block opens with `--- failure <lint_id>: <title> ---`, carries a
// description, and ends with `Failed in:` and one indented witness line per
// occurrence. Each witness is its own finding: it is one symbol a reader opens
// one file at, which is what a finding is.
//
// The description is not carried. It says the same thing for every occurrence
// of the lint and the lint id already names it, while a finding's detail is
// read by review as saying the line is the merge-base's.
func report(stdout string, req Request) []finding.Finding {
	var out []finding.Finding
	var lint, title string
	witnesses := false
	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, failureOpen) && strings.HasSuffix(trimmed, failureClose):
			lint, title = lintAndTitle(trimmed)
			witnesses = false
		case trimmed == witnessHeader:
			witnesses = true
		case !witnesses || lint == "" || trimmed == "":
			// Anything before the first block, and the description within one.
		case line == trimmed:
			// A witness is indented under its header, so an unindented line is
			// the run's own output and the block is over.
			witnesses = false
		default:
			out = append(out, claim(readWitness(trimmed, req.BaseDir, req.HeadDir), lint, title, req))
		}
	}
	return out
}

// lintAndTitle reads the lint id and its title out of a block's opening line.
//
// A line whose remainder is not the two of them yields the whole of it as the
// id and no title: the id is what identifies the claim, and inventing a split
// that is not there would lose it.
func lintAndTitle(line string) (string, string) {
	inner := strings.TrimSuffix(strings.TrimPrefix(line, failureOpen), failureClose)
	id, title, ok := strings.Cut(inner, ": ")
	if !ok {
		return inner, ""
	}
	return id, title
}

// claim is one witness as a finding.
func claim(w witness, lint, title string, req Request) finding.Finding {
	f := finding.Finding{
		Gate:      req.Gate,
		Component: req.Component,
		Rule:      lint,
		// The tool's own words, which are the claim: the title it gives the
		// lint and the sentence it wrote about this occurrence. Rewording
		// either would put lydite's paraphrase where cargo-semver-checks'
		// statement was.
		Message: message(title, w.text),
		Path:    w.path,
		Line:    w.line,
		// The lint and what it fired on, without the location: a claim keeps
		// its identity across an edit that moves the line it sits on. Two
		// occurrences of one lint differ in the witness, which is why the
		// witness is here and the line is not.
		Site: lint + "\x1f" + w.site,
	}
	if w.removed {
		f.Detail = []string{removalDetail}
	}
	return f
}

// message is what the finding says, with the title dropped when the block
// carried none rather than a colon introducing nothing.
func message(title, witness string) string {
	if title == "" {
		return witness
	}
	return title + ": " + witness
}

// witness is one occurrence a failing lint reported.
type witness struct {
	// text is the tool's sentence with the tree root dropped from the path it
	// names, so a finding carries `src/lib.rs:4` rather than the throwaway
	// worktree the comparison ran in.
	text string
	// site is the same sentence with the location removed entirely.
	site string
	// path is relative to the tree the occurrence is in, and line is where in
	// it. Both are zero for a witness naming no location this package can
	// resolve, because a guessed line points a reader at code the claim is not
	// about.
	path string
	line int
	// removed reports that the location is in the merge-base tree, which is
	// what says the symbol is gone from the head.
	//
	// The tree the path is under, rather than the lint's own prose: each of the
	// 254 lints words a removal its own way — `previously in file …`, `now
	// takes 2 parameters instead of 1, in …` — and reading the tree is one rule
	// that covers all of them.
	removed bool
}

// readWitness locates one witness line within the two trees it could be in.
func readWitness(text, baseDir, headDir string) witness {
	w := witness{text: text, site: text}
	for _, tree := range []struct {
		root    string
		removed bool
	}{{root: headDir}, {root: baseDir, removed: true}} {
		prefix, path, line, ok := locate(text, tree.root)
		if !ok {
			continue
		}
		w.path, w.line, w.removed = path, line, tree.removed
		w.text = prefix + path + ":" + strconv.Itoa(line)
		w.site = strings.TrimRight(prefix, " ,")
		return w
	}
	return w
}

// locate finds the `<root>/<path>:<line>` a witness ends in, and returns
// everything the sentence said before it.
//
// The root is what the path is recognised by, rather than a trailing
// `<something>:<number>` pattern: the tool is handed both roots on its command
// line and echoes them back, and a sentence that happens to end in a colon and
// a number is not a location. The symlink-resolved root is tried too, for a tree
// reached by one name and reported under another.
func locate(text, root string) (prefix, path string, line int, ok bool) {
	if root == "" {
		return "", "", 0, false
	}
	for _, candidate := range []string{root, resolved(root)} {
		at := strings.LastIndex(text, candidate+string(os.PathSeparator))
		if at < 0 {
			continue
		}
		rest := text[at+len(candidate)+1:]
		cut := strings.LastIndex(rest, ":")
		if cut < 0 {
			continue
		}
		n, err := strconv.Atoi(rest[cut+1:])
		if err != nil {
			continue
		}
		return text[:at], filepath.ToSlash(rest[:cut]), n, true
	}
	return "", "", 0, false
}

// resolved follows any symlink in a path, so that a tree reached by one name and
// reported by the tool under another is still recognised.
func resolved(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}

// reasonLines caps how much of the tool's own account of a refusal travels into
// a caller's referral. Enough to name the cause, and not two rustdoc builds'
// progress.
const reasonLines = 10

// refusal is what the tool said about why it would not run.
//
// stderr carries per-crate progress before anything went wrong, so the account
// starts at the first line the tool marked as an error and whatever ran fine
// above it is dropped. A stream marking none is kept from the end, where what it
// said last is.
func refusal(stderr string) string {
	lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
	from := max(len(lines)-reasonLines, 0)
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "error") {
			from = i
			break
		}
	}
	lines = lines[from:]
	if len(lines) > reasonLines {
		lines = lines[:reasonLines]
	}
	return strings.Join(lines, "\n")
}
