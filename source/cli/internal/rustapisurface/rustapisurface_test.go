package rustapisurface

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/fixture"
)

// The gate and component a test asks for, which every finding carries through
// untouched.
const (
	testGate      = "api surface"
	testComponent = "probe"
)

// Every case is read over the runs recorded from cargo-semver-checks itself, so
// what this package reports is tied to what the tool was measured saying rather
// than to a second fixture that could drift from it. The two tree roots are
// written back into the recorded output as the directories the run actually
// compared, which is the only substitution the recording made.
func TestReportLocatesEachBreakShape(t *testing.T) {
	cases := []struct {
		shape string
		// rule is the lint that fired.
		rule string
		// message is what the finding says: the lint's title and the witness,
		// with the tree root dropped from the path.
		message string
		// site identifies the claim independently of the line it sits on.
		site string
		// removed is a symbol the head no longer declares, located at its
		// merge-base declaration.
		removed bool
		// line is the declaration the finding must point at, and declares is
		// what that line must name.
		line     int
		declares string
	}{
		{
			shape:    "removed-function",
			rule:     "function_missing",
			message:  "pub fn removed or renamed: function probe::removed, previously in file src/lib.rs:4",
			site:     "function_missing\x1ffunction probe::removed, previously in file",
			removed:  true,
			line:     4,
			declares: "removed",
		},
		{
			shape:    "changed-signature",
			rule:     "function_parameter_count_changed",
			message:  "pub fn parameter count changed: probe::changed now takes 2 parameters instead of 1, in src/lib.rs:9",
			site:     "function_parameter_count_changed\x1fprobe::changed now takes 2 parameters instead of 1, in",
			line:     9,
			declares: "changed",
		},
		{
			shape:    "trait-method-added",
			rule:     "trait_method_added",
			message:  "pub trait method added: trait method probe::Store::put in file src/lib.rs:19",
			site:     "trait_method_added\x1ftrait method probe::Store::put in file",
			line:     19,
			declares: "put",
		},
		{
			shape:    "removed-struct-field",
			rule:     "struct_pub_field_missing",
			message:  "pub struct's pub field removed or renamed: field timeout of struct Config, previously in file src/lib.rs:22",
			site:     "struct_pub_field_missing\x1ffield timeout of struct Config, previously in file",
			removed:  true,
			line:     22,
			declares: "timeout",
		},
	}
	for _, c := range cases {
		t.Run(c.shape, func(t *testing.T) {
			run := recorded(t, "probe", c.shape)
			got := run.result
			if got.Outcome != Broken {
				t.Fatalf("Outcome = %v, want Broken (%v): %s", got.Outcome, Broken, got.Reason)
			}
			if len(got.Findings) != 1 {
				t.Fatalf("got %d findings, want 1: %+v", len(got.Findings), got.Findings)
			}
			f := got.Findings[0]
			if f.Gate != testGate || f.Component != testComponent {
				t.Errorf("Gate/Component = %q/%q, want %q/%q", f.Gate, f.Component, testGate, testComponent)
			}
			if f.Rule != c.rule {
				t.Errorf("Rule = %q, want %q", f.Rule, c.rule)
			}
			if f.Message != c.message {
				t.Errorf("Message = %q, want %q", f.Message, c.message)
			}
			if f.Site != c.site {
				t.Errorf("Site = %q, want %q", f.Site, c.site)
			}
			if f.Path != "src/lib.rs" {
				t.Errorf("Path = %q, want the tree-relative %q", f.Path, "src/lib.rs")
			}
			if f.Line != c.line {
				t.Errorf("Line = %d, want the declaration at %d", f.Line, c.line)
			}
			// A symbol that is gone is located in the merge-base, and the
			// finding says so — review renders the line as the merge-base's off
			// exactly this.
			switch {
			case c.removed && (len(f.Detail) != 1 || f.Detail[0] != removalDetail):
				t.Errorf("Detail = %q, want the removal note", f.Detail)
			case !c.removed && len(f.Detail) != 0:
				t.Errorf("Detail = %q, want none for a symbol the head still declares", f.Detail)
			}
			// The declaration the line names is read back, so a fixture edit
			// that moves it fails here rather than silently pointing a reader at
			// the wrong code.
			tree := run.head
			if c.removed {
				tree = run.base
			}
			if declared := line(t, filepath.Join(tree, f.Path), f.Line); !strings.Contains(declared, c.declares) {
				t.Errorf("line %d of %s is %q, which does not declare %s", f.Line, f.Path, declared, c.declares)
			}
		})
	}
}

// A compatible change and a change nothing outside the crate can see are one
// answer, and it is the tool's own: under --release-type minor only a major lint
// can fail, so an addition and a private module's new signature both come back
// as a surface that was compared and is unbroken.
func TestACompatibleRunReportsNothing(t *testing.T) {
	for _, shape := range []string{"compatible-addition", "unreachable-change"} {
		t.Run(shape, func(t *testing.T) {
			got := recorded(t, "probe", shape).result
			if got.Outcome != Unbroken {
				t.Fatalf("Outcome = %v, want Unbroken (%v): %s", got.Outcome, Unbroken, got.Reason)
			}
			if len(got.Findings) != 0 {
				t.Errorf("findings = %+v, want none", got.Findings)
			}
		})
	}
}

// The version bump is the reason --release-type minor is passed unconditionally.
// One line in a file the change under review owns decides what the tool is
// allowed to report, and the recorded run without the flag — version-bump-derived
// — is the same head tree reporting nothing at all.
func TestAVersionBumpDoesNotClearTheBreak(t *testing.T) {
	got := recorded(t, "probe", "version-bump").result
	if got.Outcome != Broken {
		t.Fatalf("Outcome = %v, want Broken (%v): %s", got.Outcome, Broken, got.Reason)
	}
	if len(got.Findings) != 1 || got.Findings[0].Rule != "function_missing" {
		t.Fatalf("findings = %+v, want one function_missing", got.Findings)
	}
	if derived := recorded(t, "probe", "version-bump-derived").result; derived.Outcome != Unbroken || len(derived.Findings) != 0 {
		t.Fatalf("the run whose release type the tool derived reported %v/%+v; the recording has it reporting nothing, which is what the flag exists to prevent",
			derived.Outcome, derived.Findings)
	}
	if !argvHas(argv("base", "head"), "--release-type", "minor") {
		t.Error("argv passes no --release-type minor, so the tool decides from the two manifests what it may report")
	}
}

// A run that could not be made is its own outcome, naming the reason. Neither of
// these is the author's to fix and neither is ever green: a base tree that does
// not build, and a component that opted in with no library target.
func TestARunThatCouldNotBeMadeIsUnmeasurable(t *testing.T) {
	cases := []struct {
		crate, shape string
		// said is what the tool's own account must still carry.
		said string
		// silent is progress the account must have dropped, since the reason
		// travels into a referral rather than into a log.
		silent string
	}{
		{
			crate:  "probe",
			shape:  "base-does-not-build",
			said:   "running cargo-doc on crate 'probe' failed",
			silent: "Parsed",
		},
		{
			crate: "probebin",
			shape: "no-library-target",
			said:  "no crates with library targets selected, nothing to semver-check",
		},
	}
	for _, c := range cases {
		t.Run(c.shape, func(t *testing.T) {
			got := recorded(t, c.crate, c.shape).result
			if got.Outcome != Unmeasurable {
				t.Fatalf("Outcome = %v, want Unmeasurable (%v)", got.Outcome, Unmeasurable)
			}
			if len(got.Findings) != 0 {
				t.Errorf("findings = %+v, want none", got.Findings)
			}
			if !strings.Contains(got.Reason, c.said) {
				t.Errorf("Reason = %q, which does not say %q", got.Reason, c.said)
			}
			if c.silent != "" && strings.Contains(got.Reason, c.silent) {
				t.Errorf("Reason = %q, which carries the progress above the error", got.Reason)
			}
		})
	}
}

// Only 0 and 100 are answers. Every other exit is a comparison that did not
// happen, and reporting it as a clean surface is the green of a gate that never
// ran.
func TestAnyOtherExitIsUnmeasurable(t *testing.T) {
	for _, code := range []int{-1, 1, 2, 101, 127} {
		got := readRun(code, "", "error: something else entirely\n", Request{})
		if got.Outcome != Unmeasurable {
			t.Errorf("exit %d: Outcome = %v, want Unmeasurable (%v)", code, got.Outcome, Unmeasurable)
		}
		if !strings.Contains(got.Reason, "something else entirely") {
			t.Errorf("exit %d: Reason = %q, which does not carry what the tool said", code, got.Reason)
		}
	}
}

// The exit code claims a major lint failed and the report names nothing, so the
// two disagree. That is output this package cannot read, which is a comparison
// lydite cannot stand behind — never an unbroken surface.
func TestABreakingExitWithNoReportIsUnmeasurable(t *testing.T) {
	got := readRun(exitBroken, "some shape this parser does not know\n", "", Request{})
	if got.Outcome != Unmeasurable {
		t.Fatalf("Outcome = %v, want Unmeasurable (%v)", got.Outcome, Unmeasurable)
	}
	if !strings.Contains(got.Reason, "names none") {
		t.Errorf("Reason = %q, which does not say the report named no change", got.Reason)
	}
}

// Every occurrence a lint reported is its own claim: it is one symbol a reader
// opens one file at, and collapsing them would report fewer changes than the
// tool did.
func TestEveryWitnessIsItsOwnClaim(t *testing.T) {
	head := t.TempDir()
	stdout := "\n--- failure function_missing: pub fn removed or renamed ---\n\n" +
		"Description:\nA publicly-visible function cannot be imported by its prior path.\n" +
		"        ref: https://doc.rust-lang.org/cargo/reference/semver.html#item-remove\n\n" +
		"Failed in:\n" +
		"  function probe::one, previously in file " + filepath.Join(head, "src/lib.rs") + ":4\n" +
		"  function probe::two, previously in file " + filepath.Join(head, "src/other.rs") + ":11\n"
	got := report(stdout, Request{Gate: testGate, Component: testComponent, HeadDir: head})
	if len(got) != 2 {
		t.Fatalf("got %d findings, want one per witness: %+v", len(got), got)
	}
	if got[0].Path != "src/lib.rs" || got[0].Line != 4 || got[1].Path != "src/other.rs" || got[1].Line != 11 {
		t.Errorf("located at %s:%d and %s:%d, want src/lib.rs:4 and src/other.rs:11",
			got[0].Path, got[0].Line, got[1].Path, got[1].Line)
	}
	// The description is not a claim. It says the same thing for every
	// occurrence, and a finding's detail is read as saying the line is the
	// merge-base's.
	for _, f := range got {
		if strings.Contains(f.Message, "prior path") || len(f.Detail) != 0 {
			t.Errorf("finding %+v carries the block's description", f)
		}
	}
}

// A witness naming no path either tree holds is still the claim the tool made,
// and it carries no location: a guessed line points a reader at code the claim is
// not about.
func TestAWitnessOutsideBothTreesCarriesNoLocation(t *testing.T) {
	stdout := "--- failure inferred_default_impl_removed: impl Default removed ---\n\nFailed in:\n" +
		"  probe::Config no longer implements Default\n"
	got := report(stdout, Request{Gate: testGate, BaseDir: t.TempDir(), HeadDir: t.TempDir()})
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if got[0].Path != "" || got[0].Line != 0 {
		t.Errorf("located at %s:%d, want no location at all", got[0].Path, got[0].Line)
	}
	if got[0].Message != "impl Default removed: probe::Config no longer implements Default" {
		t.Errorf("Message = %q, which is not the lint's title and the tool's own sentence", got[0].Message)
	}
}

// The invocation is what lydite owns. Two real trees, the release type forced,
// and no package selection — a component's surface is whatever cargo selects
// from its directory, the same rule every other Rust check follows.
func TestArgvComparesTwoTreesWithNoPackageSelection(t *testing.T) {
	got := argv("/tmp/base", "/tmp/head")
	if got[0] != "semver-checks" {
		t.Errorf("argv[0] = %q, want the subcommand a --root-installed binary takes as its own first argument", got[0])
	}
	want := []string{
		"--manifest-path", filepath.Join("/tmp/head", "Cargo.toml"),
		"--baseline-root", "/tmp/base",
		"--release-type", "minor",
		"--color", "never",
	}
	for i := 0; i+1 < len(want); i += 2 {
		if !argvHas(got, want[i], want[i+1]) {
			t.Errorf("argv = %q, missing %s %s", got, want[i], want[i+1])
		}
	}
	for _, flag := range []string{"--package", "--workspace", "--baseline-version", "--baseline-rev", "--exclude"} {
		if slices.Contains(got, flag) {
			t.Errorf("argv = %q, which selects packages or a baseline of its own with %s", got, flag)
		}
	}
}

// refusal keeps exactly reasonLines lines starting at the error, and drops
// anything past that — proven at the boundary itself, not just well inside or
// well outside it.
func TestRefusalHoldsTheBoundaryAtExactlyReasonLines(t *testing.T) {
	held := []string{"error: could not build"}
	for i := 1; i < reasonLines; i++ {
		held = append(held, "progress line "+strconv.Itoa(i))
	}
	want := strings.Join(held, "\n")

	if got := refusal(want); got != want {
		t.Errorf("refusal held at exactly %d lines from the error = %q, want every line kept", reasonLines, got)
	}

	crossed := strings.Join(append(append([]string{}, held...), "one line too many"), "\n")
	if got := refusal(crossed); got != want {
		t.Errorf("refusal(%d lines from the error) = %q, want the first %d kept and the rest dropped", reasonLines+1, got, reasonLines)
	}
}

// A command that never ran, or died on a signal, has no ExitError to read a
// code out of — errors.As fails on it exactly as it does on a plain error —
// and that answer takes the same "not one of the two known codes" path as an
// exit code the tool actually returned.
func TestExitCodeIsMinusOneForAnErrorWithNoExitCode(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, errors.New("boom")} {
		if got := exitCode(executil.Result{Err: err}); got != -1 {
			t.Errorf("exitCode(%v) = %d, want -1", err, got)
		}
	}
}

// A witness is indented under its `Failed in:` header, so a line that is not
// ends the block: it is the tool's own output resuming, and a later line that
// looks indented again must not be read as a second witness of the same
// failure.
func TestAnUnindentedLineEndsTheWitnessBlock(t *testing.T) {
	stdout := "--- failure function_missing: pub fn removed or renamed ---\n\nFailed in:\n" +
		"  function probe::one, previously in file src/lib.rs:4\n" +
		"done with function_missing\n" +
		"  function probe::two, previously in file src/other.rs:11\n"
	got := report(stdout, Request{Gate: testGate, Component: testComponent})
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: the block ended at the unindented line", len(got))
	}
	if !strings.Contains(got[0].Message, "probe::one") {
		t.Errorf("Message = %q, want the witness before the unindented line", got[0].Message)
	}
}

// A block-opening line whose inner text has no `": "` separator yields the
// whole of it as the lint id and no title, rather than inventing a split that
// is not there and losing part of the id.
func TestLintAndTitleWithNoSeparatorIsAllID(t *testing.T) {
	id, title := lintAndTitle("--- failure something_without_a_colon ---")
	if id != "something_without_a_colon" || title != "" {
		t.Errorf("lintAndTitle = %q, %q, want the whole inner text as the id and no title", id, title)
	}
}

// A finding whose block carried no title is the tool's own sentence alone,
// with no leading ": " introducing nothing.
func TestMessageWithNoTitleIsTheWitnessAlone(t *testing.T) {
	if got := message("", "probe::Config no longer implements Default"); got != "probe::Config no longer implements Default" {
		t.Errorf("message = %q, want the witness with no title prefix", got)
	}
}

// locate is asked about a tree this comparison was not given a root for —
// BaseDir or HeadDir left empty — and answers that it found nothing rather
// than matching an empty prefix against every witness.
func TestLocateWithNoRootFindsNothing(t *testing.T) {
	prefix, path, line, ok := locate("probe::Config no longer implements Default", "")
	if ok || prefix != "" || path != "" || line != 0 {
		t.Errorf("locate(_, %q) = %q, %q, %d, %v, want the zero value and false", "", prefix, path, line, ok)
	}
}

// A witness naming the root but no `:<line>` at all has nowhere a line number
// could be, so locate must not claim to have found one.
func TestLocateWithNoLineNumberFindsNothing(t *testing.T) {
	root := t.TempDir()
	prefix, path, line, ok := locate("function probe::one, previously in file "+filepath.Join(root, "src/lib.rs"), root)
	if ok || prefix != "" || path != "" || line != 0 {
		t.Errorf("locate found %q, %q, %d, %v in a witness with no line number, want the zero value and false", prefix, path, line, ok)
	}
}

// A witness naming the root and a trailing colon, but not a number after it,
// is not a location either: the colon can belong to the tool's own prose.
func TestLocateWithANonNumericSuffixFindsNothing(t *testing.T) {
	root := t.TempDir()
	prefix, path, line, ok := locate("function probe::one, previously in file "+filepath.Join(root, "src/lib.rs")+":notanumber", root)
	if ok || prefix != "" || path != "" || line != 0 {
		t.Errorf("locate found %q, %q, %d, %v with a non-numeric line, want the zero value and false", prefix, path, line, ok)
	}
}

// A witness that names the root at the very start of the text has an empty
// prefix, which is a real location and not the "not found" the same code path
// reports for a negative index — the two must not collapse into each other.
func TestLocateFindsALocationAtTheStartOfTheText(t *testing.T) {
	root := t.TempDir()
	text := root + string(os.PathSeparator) + "src/lib.rs:4"
	prefix, path, line, ok := locate(text, root)
	if !ok || prefix != "" || path != "src/lib.rs" || line != 4 {
		t.Errorf("locate(%q, %q) = %q, %q, %d, %v, want \"\", \"src/lib.rs\", 4, true", text, root, prefix, path, line, ok)
	}
}

// A witness whose path segment is empty — the root, a separator, then
// immediately the colon — still carries a real line number at index 0 rather
// than being read as "no colon found": the two must not collapse either.
func TestLocateFindsAnEmptyPathAtLineZero(t *testing.T) {
	root := t.TempDir()
	text := "in " + root + string(os.PathSeparator) + ":42"
	prefix, path, line, ok := locate(text, root)
	if !ok || prefix != "in " || path != "" || line != 42 {
		t.Errorf("locate(%q, %q) = %q, %q, %d, %v, want \"in \", \"\", 42, true", text, root, prefix, path, line, ok)
	}
}

// A path EvalSymlinks cannot resolve — one that does not exist — is returned
// unchanged, since resolved exists to recognise a tree under a second name and
// not to invent one.
func TestResolvedFallsBackToTheOriginalPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist")
	if got := resolved(path); got != path {
		t.Errorf("resolved(%q) = %q, want the path unchanged", path, got)
	}
}

// The comparison's environment carries what the caller composed plus the
// ambient variables cargo and rustup need to find their own state — and
// nothing else this process's own environment holds, a secret an unrelated
// caller set included.
func TestIsolatedEnvCarriesOnlyCheckAndTheAllowedAmbientVars(t *testing.T) {
	t.Setenv("HOME", "/home/probe")
	t.Setenv("GITHUB_TOKEN", "leaked-if-this-test-fails")
	t.Setenv("SOME_OTHER_AMBIENT_VAR", "also-leaked-if-this-test-fails")

	allowed := map[string]bool{}
	for _, k := range isolatedAmbientVars {
		allowed[k] = true
	}

	got := isolatedEnv([]string{"PATH=/composed/bin"})

	foundComposed, foundHome := false, false
	for _, kv := range got {
		if kv == "PATH=/composed/bin" {
			foundComposed = true
			continue
		}
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !allowed[k] || v != os.Getenv(k) {
			t.Errorf("isolatedEnv carried %q, which is neither the composed environment nor an allowed ambient variable at its real value", kv)
			continue
		}
		if k == "HOME" {
			foundHome = true
		}
	}
	if !foundComposed {
		t.Error("isolatedEnv dropped the composed environment")
	}
	if !foundHome {
		t.Error("isolatedEnv dropped HOME")
	}
}

// A key the composed environment already declares is not also carried at its
// ambient value: the two must not both reach the child, where which one a
// given libc's getenv() prefers is not lydite's to decide.
func TestIsolatedEnvDoesNotOverrideADeclaredAmbientKey(t *testing.T) {
	t.Setenv("HOME", "/ambient/home")

	got := isolatedEnv([]string{"PATH=/composed/bin", "HOME=/declared/home"})

	seen := 0
	for _, kv := range got {
		if strings.HasPrefix(kv, "HOME=") {
			seen++
			if kv != "HOME=/declared/home" {
				t.Errorf("HOME entry = %q, want the declared value to win", kv)
			}
		}
	}
	if seen != 1 {
		t.Errorf("isolatedEnv carried HOME %d times, want exactly the one the composed environment declared", seen)
	}
}

// A component whose composed environment declares no PATH — an already-
// satisfied toolchain, or provisioning off — still gets one: falling back to
// this process's own is what lets the child find cargo and rustc at all.
func TestIsolatedEnvFallsBackToTheAmbientPathWhenCheckDeclaresNone(t *testing.T) {
	t.Setenv("PATH", "/ambient/bin")

	got := isolatedEnv(nil)

	seen := 0
	for _, kv := range got {
		if kv == "PATH=/ambient/bin" {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("isolatedEnv with no declared PATH carried %d ambient PATH entries, want exactly 1", seen)
	}
}

// A subprocess run through isolatedEnv can actually find a binary on the
// ambient PATH when the composed environment names none — proving the
// fallback works end to end, not just that isolatedEnv's slice contains the
// right string.
func TestIsolatedEnvLetsASubprocessFindABinaryOnTheAmbientPath(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "probe-tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\necho found\n"), 0o700); err != nil { // #nosec G306 -- a script the test is about to execute
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	res := executil.RunQuietIsolatedEnv(context.Background(), t.TempDir(), isolatedEnv(nil), "probe-tool")
	if !res.Ok() {
		t.Fatalf("probe-tool: %v (stderr: %s)", res.Err, res.Stderr)
	}
	if strings.TrimSpace(res.Output) != "found" {
		t.Errorf("output = %q, want the script's own output", res.Output)
	}
}

// run is one recorded invocation, replayed over the trees it compared.
type run struct {
	base, head string
	result     Result
}

// recorded replays the run captured for one shape.
//
// Both trees are materialised because the recorded output names them, and
// because the line each finding points at is read back out of the tree it is in.
// The tool prints the roots it was handed, so the recording's placeholders are
// written back as the directories this test compared — real absolute paths, which
// is what the parser sees in a live run.
func recorded(t *testing.T, crate, shape string) run {
	t.Helper()
	r := run{base: trees(t, crate, "base"), head: trees(t, crate, "base")}
	// probebin's head is a tree of its own rather than an overlay, since a
	// binary-only crate has no library to change.
	if crate == "probebin" {
		r.head = trees(t, crate, "head")
	} else {
		overlay(t, filepath.Join("testdata", crate, headTree(shape)), r.head)
	}
	replace := strings.NewReplacer("$BASE", r.base, "$HEAD", r.head)
	r.result = readRun(
		exitCodes(t)[shape],
		replace.Replace(recording(t, shape+".stdout.txt")),
		replace.Replace(recording(t, shape+".stderr.txt")),
		Request{Gate: testGate, Component: testComponent, BaseDir: r.base, HeadDir: r.head},
	)
	return r
}

// headTree names the overlay a recorded run compared, for the run whose head
// tree is not the one its own name would find: the derived-release-type run is
// the version-bump tree, compared without --release-type minor.
func headTree(shape string) string {
	if shape == "version-bump-derived" {
		return "version-bump"
	}
	return shape
}

// trees materialises one probe tree, restoring each file's real name.
func trees(t *testing.T, crate, which string) string {
	t.Helper()
	return fixture.Tree(t, filepath.Join("testdata", crate, which))
}

// overlay writes one shape's files over a materialised base tree, which is how
// the head tree of each recorded run was built: a shape carries only what it
// changes.
func overlay(t *testing.T, dir, onto string) {
	t.Helper()
	src := fixture.Tree(t, dir)
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == src {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(onto, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		data, err := os.ReadFile(path) // #nosec G304 -- a fixture this test just materialised
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatalf("overlaying %s onto %s: %v", dir, onto, err)
	}
}

// recording is one captured stream, verbatim.
func recording(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name)) // #nosec G304 -- a fixture named by this test
	if err != nil {
		t.Fatalf("reading the recorded %s: %v", name, err)
	}
	return string(data)
}

// exitCodes is what each recorded run exited with, read from the recording
// rather than restated here: the code and the streams are one measurement.
func exitCodes(t *testing.T) map[string]int {
	t.Helper()
	codes := map[string]int{}
	for _, line := range strings.Split(recording(t, "exit-codes.txt"), "\n") {
		shape, code, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(code)
		if err != nil {
			t.Fatalf("exit-codes.txt: %q is not a shape and an exit code", line)
		}
		codes[shape] = n
	}
	return codes
}

// line is one line of a materialised tree, by number.
func line(t *testing.T, name string, number int) string {
	t.Helper()
	data, err := os.ReadFile(name) // #nosec G304 -- a fixture this test just materialised
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	lines := strings.Split(string(data), "\n")
	if number < 1 || number > len(lines) {
		t.Fatalf("%s has %d lines, and the finding points at %d", name, len(lines), number)
	}
	return lines[number-1]
}

// argvHas reports whether argv carries flag immediately followed by value.
func argvHas(args []string, flag, value string) bool {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) && args[i+1] == value {
			return true
		}
	}
	return false
}
