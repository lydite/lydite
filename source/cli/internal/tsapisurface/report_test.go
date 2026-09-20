package tsapisurface

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The gate and component a test asks for, which every finding carries through
// untouched.
const (
	testGate      = "api surface"
	testComponent = "probe"
)

// observed is where the reports recorded from api-extractor itself live.
const observed = "testdata/probe/observed"

// theProbe is the package every recorded report was taken from.
var theProbe = pkg{rel: ".", name: "@lydite-probe/surface"}

// Every shape is read over the reports recorded from api-extractor itself, so
// what this package reports is tied to what the tool was measured saying rather
// than to a second fixture that could drift from it. The head report is the
// recorded base with the recorded diff applied, which is what makes a change to
// either recording a change to what these cases claim.
func TestEachProbeShapeIsClassified(t *testing.T) {
	cases := []struct {
		shape string
		// rule and key are what the one finding says, and an empty rule is a
		// shape that must produce none.
		rule string
		key  string
	}{
		{shape: "removed-export", rule: ruleRemoved, key: "function removed"},
		{shape: "narrowed-parameter", rule: ruleChanged, key: "function narrowed"},
		{shape: "widened-return", rule: ruleChanged, key: "function widened"},
		{shape: "removed-interface-member", rule: ruleChanged, key: "interface Store"},
		{shape: "added-required-member", rule: ruleChanged, key: "interface Store"},
		{shape: "optional-to-required", rule: ruleChanged, key: "interface Config"},
		// The added function is an addition and reported as nothing; the
		// optional field is a change to the interface's declaration, which the
		// rule reports whichever way the type moved.
		{shape: "compatible-addition", rule: ruleChanged, key: "interface Config"},
		// The one shape every other candidate mechanism fired on: the same
		// names and the same signatures reached through a different re-export
		// form, which api-extractor resolves away.
		{shape: "reexport-only"},
		// The break exists only in the emitted declaration, and nowhere in the
		// entry point's own text.
		{shape: "generated-dts", rule: ruleChanged, key: "function inferred"},
		// A version bump alongside the removal, which silences nothing.
		{shape: "version-bump", rule: ruleRemoved, key: "function removed"},
	}
	for _, c := range cases {
		t.Run(c.shape, func(t *testing.T) {
			base, head := reports(t, c.shape)
			findings := compare(base, head, theProbe, ".", request())
			if c.rule == "" {
				if len(findings) != 0 {
					t.Fatalf("got %d finding(s), want none: %+v", len(findings), findings)
				}
				return
			}
			if len(findings) != 1 {
				t.Fatalf("got %d finding(s), want 1: %+v", len(findings), findings)
			}
			f := findings[0]
			if f.Rule != c.rule {
				t.Errorf("rule = %q, want %q", f.Rule, c.rule)
			}
			if want := c.key + ": " + messages[c.rule]; f.Message != want {
				t.Errorf("message = %q, want %q", f.Message, want)
			}
			if f.Gate != testGate || f.Component != testComponent {
				t.Errorf("gate/component = %q/%q, want %q/%q", f.Gate, f.Component, testGate, testComponent)
			}
		})
	}
}

// A removal carries the merge-base's own declaration, because that text exists
// nowhere in the tree a reader has checked out.
func TestARemovalCarriesTheDeclarationThatIsGone(t *testing.T) {
	base, head := reports(t, "removed-export")
	findings := compare(base, head, theProbe, ".", request())
	if len(findings) != 1 {
		t.Fatalf("got %d finding(s), want 1", len(findings))
	}
	detail := strings.Join(findings[0].Detail, "\n")
	for _, want := range []string{
		"the . entry point of @lydite-probe/surface",
		locationDetail,
		"removed in this change; the merge-base declared:",
		"    export function removed(n: number): number;",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail does not carry %q:\n%s", want, detail)
		}
	}
	if strings.Contains(detail, "and this change declares:") {
		t.Errorf("a removal quotes a head declaration that does not exist:\n%s", detail)
	}
}

// A change carries both declarations, because what changed is the difference
// between them.
func TestAChangeCarriesBothDeclarations(t *testing.T) {
	base, head := reports(t, "narrowed-parameter")
	findings := compare(base, head, theProbe, ".", request())
	if len(findings) != 1 {
		t.Fatalf("got %d finding(s), want 1", len(findings))
	}
	detail := strings.Join(findings[0].Detail, "\n")
	for _, want := range []string{
		"the merge-base declared:",
		"    export function narrowed(value: string | number): string;",
		"and this change declares:",
		"    export function narrowed(value: string): string;",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail does not carry %q:\n%s", want, detail)
		}
	}
}

// api-extractor's report names no source file, so the claim points at the one
// file that is the package's own in both trees.
func TestAClaimIsLocatedAtThePackageManifest(t *testing.T) {
	base, head := reports(t, "removed-export")
	member := pkg{rel: "packages/core", name: "@probe/core"}
	findings := compare(base, head, member, "./sub", request())
	if len(findings) != 1 {
		t.Fatalf("got %d finding(s), want 1", len(findings))
	}
	f := findings[0]
	if f.Path != "packages/core/package.json" {
		t.Errorf("path = %q, want packages/core/package.json", f.Path)
	}
	if f.Line != manifestLine {
		t.Errorf("line = %d, want %d", f.Line, manifestLine)
	}
	if want := "packages/core\x1f./sub\x1f" + ruleRemoved + "\x1ffunction removed"; f.Site != want {
		t.Errorf("site = %q, want %q", f.Site, want)
	}
}

// Two entry points of one package name the same declaration, and each claim is
// its own: a fingerprint derived from the site alone would merge them.
func TestTwoEntryPointsOfOnePackageAreTwoClaims(t *testing.T) {
	base, head := reports(t, "removed-export")
	first := compare(base, head, theProbe, ".", request())
	second := compare(base, head, theProbe, "./extra", request())
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("got %d and %d finding(s), want 1 each", len(first), len(second))
	}
	if first[0].Site == second[0].Site {
		t.Errorf("both entry points produced the site %q", first[0].Site)
	}
}

// A package whose entry point the head no longer names has no report there, and
// every declaration under it is gone.
func TestAWithdrawnEntryPointRemovesEveryDeclaration(t *testing.T) {
	base := declarations(recorded(t, "base.api-report.txt"))
	findings := compare(base, nil, theProbe, "./gone", request())
	if len(findings) != len(base) {
		t.Fatalf("got %d finding(s), want %d", len(findings), len(base))
	}
	for _, f := range findings {
		if f.Rule != ruleRemoved {
			t.Errorf("%s reported as %q, want %q", f.Message, f.Rule, ruleRemoved)
		}
	}
}

// A package the merge-base did not have is every declaration added, and an
// addition is compatible.
func TestAnEntryPointOnlyThisChangeNamesReportsNothing(t *testing.T) {
	head := declarations(recorded(t, "base.api-report.txt"))
	if findings := compare(nil, head, theProbe, "./new", request()); len(findings) != 0 {
		t.Fatalf("got %d finding(s), want none: %+v", len(findings), findings)
	}
}

// The report's own header names the package, and its comments are
// api-extractor's annotations rather than declarations.
func TestTheHeaderAndTheCommentsAreNotDeclarations(t *testing.T) {
	ds := declarations(recorded(t, "base.api-report.txt"))
	if len(ds) != 6 {
		t.Fatalf("got %d declaration(s), want 6: %+v", len(ds), ds)
	}
	for _, d := range ds {
		if strings.Contains(d.text, "//") {
			t.Errorf("declaration %q carries a comment line:\n%s", d.key, d.text)
		}
		if strings.Contains(d.text, "\r") {
			t.Errorf("declaration %q carries a carriage return:\n%q", d.key, d.text)
		}
	}
	keys := make([]string, 0, len(ds))
	for _, d := range ds {
		keys = append(keys, d.key)
	}
	want := "interface Config, function inferred, function narrowed, function removed, interface Store, function widened"
	if got := strings.Join(keys, ", "); got != want {
		t.Errorf("keys = %q, want %q", got, want)
	}
}

// Anything outside the fenced block is the report's own prose, which names the
// package and would make a rename read as every declaration removed at once.
func TestNothingOutsideTheFenceIsADeclaration(t *testing.T) {
	report := "## API Report File for \"@probe/surface\"\n\nexport function outside(): void;\n\n```ts\n\nexport function inside(): void;\n\n```\n\nexport function after(): void;\n"
	ds := declarations(report)
	if len(ds) != 1 || ds[0].key != "function inside" {
		t.Fatalf("got %+v, want the one declaration inside the fence", ds)
	}
}

// An overload set is two declarations under one key, paired in report order, so
// that a change to one of them is one change and not two.
func TestAnOverloadSetPairsInReportOrder(t *testing.T) {
	base := "```ts\n\nexport function f(a: string): void;\n\nexport function f(a: number): void;\n\n```\n"
	head := "```ts\n\nexport function f(a: string): void;\n\nexport function f(a: boolean): void;\n\n```\n"
	findings := compare(declarations(base), declarations(head), theProbe, ".", request())
	if len(findings) != 1 {
		t.Fatalf("got %d finding(s), want 1: %+v", len(findings), findings)
	}
	if findings[0].Rule != ruleChanged {
		t.Errorf("rule = %q, want %q", findings[0].Rule, ruleChanged)
	}
}

// One overload dropped is one removal, and the overloads that stayed are not
// reported alongside it.
func TestADroppedOverloadIsOneRemoval(t *testing.T) {
	base := "```ts\n\nexport function f(a: string): void;\n\nexport function f(a: number): void;\n\n```\n"
	head := "```ts\n\nexport function f(a: string): void;\n\n```\n"
	findings := compare(declarations(base), declarations(head), theProbe, ".", request())
	if len(findings) != 1 {
		t.Fatalf("got %d finding(s), want 1: %+v", len(findings), findings)
	}
	if findings[0].Rule != ruleRemoved {
		t.Errorf("rule = %q, want %q", findings[0].Rule, ruleRemoved)
	}
}

// An interface and a function of one name are two declarations, so an edit to
// one of them is not read as an edit to both.
func TestAKindIsPartOfTheKey(t *testing.T) {
	base := "```ts\n\nexport interface Thing {\n    a: string;\n}\n\nexport function Thing(): void;\n\n```\n"
	head := "```ts\n\nexport interface Thing {\n    a: number;\n}\n\nexport function Thing(): void;\n\n```\n"
	findings := compare(declarations(base), declarations(head), theProbe, ".", request())
	if len(findings) != 1 {
		t.Fatalf("got %d finding(s), want 1: %+v", len(findings), findings)
	}
	if want := "interface Thing: " + messages[ruleChanged]; findings[0].Message != want {
		t.Errorf("message = %q, want %q", findings[0].Message, want)
	}
}

func TestDeclarationKeyReadsEachDeclarationForm(t *testing.T) {
	cases := []struct{ line, want string }{
		{"export function removed(n: number): number;", "function removed"},
		{"export interface Store {", "interface Store"},
		{"export declare const version: string;", "const version"},
		{"export abstract class Base<T> implements Thing {", "class Base"},
		{"export type Alias = string;", "type Alias"},
		{"export enum Colour {", "enum Colour"},
		{"export namespace outer {", "namespace outer"},
		{"declare function bare(): void;", "function bare"},
		{"retries?: number;", "retries"},
		// A name-end character at the name's own first byte truncates it to
		// nothing, which falls back to the whole line as the key — exactly as
		// an unrecognised declaration does.
		{"declare const : number;", "declare const : number;"},
	}
	for _, c := range cases {
		if got := declarationKey(c.line, c.line); got != c.want {
			t.Errorf("declarationKey(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

// A declaration this parser cannot name is keyed by its own text, so it pairs
// only with a byte-identical one — which reports a change to it as a removal
// rather than pairing it with something unrelated.
func TestAnUnreadableDeclarationIsKeyedByItsText(t *testing.T) {
	line := "export"
	if got := declarationKey(line, "export"); got != "export" {
		t.Errorf("declarationKey of a line with no name = %q, want the text", got)
	}
	base := "```ts\n\nexport\n\n```\n"
	head := "```ts\n\nexport default\n\n```\n"
	findings := compare(declarations(base), declarations(head), theProbe, ".", request())
	if len(findings) != 1 || findings[0].Rule != ruleRemoved {
		t.Fatalf("got %+v, want one removal", findings)
	}
}

// request is the caller's side of a comparison, which a finding carries
// through.
func request() Request {
	return Request{Gate: testGate, Component: testComponent}
}

// reports is one shape's two reports: the base recorded from api-extractor, and
// the head that recording's own diff produces from it.
func reports(t *testing.T, shape string) (base, head []declaration) {
	t.Helper()
	text := recorded(t, "base.api-report.txt")
	return declarations(text), declarations(patched(t, text, recorded(t, shape+".api-report.diff.txt")))
}

// recorded is one recorded file, verbatim — line endings included, because
// api-extractor's own are what the diffs were taken against.
func recorded(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(observed, name))
	if err != nil {
		t.Fatalf("reading the recorded %s: %v", name, err)
	}
	return string(data)
}

// patched applies a recorded unified diff to the recorded base report.
//
// The head report is derived rather than recorded a second time so that the
// evidence these cases rest on is the one diff ADR 0040's amendment cites: a
// head report committed beside it could be edited into agreeing with an
// implementation the diff does not support.
func patched(t *testing.T, base, diff string) string {
	t.Helper()
	baseLines := strings.Split(base, "\n")
	var out []string
	at := 0
	lines := strings.Split(diff, "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "@@") {
			continue
		}
		for at < hunkStart(t, lines[i])-1 {
			out = append(out, baseLines[at])
			at++
		}
		for i+1 < len(lines) && !strings.HasPrefix(lines[i+1], "@@") && lines[i+1] != "" {
			i++
			body := lines[i]
			switch body[0] {
			case '+':
				out = append(out, body[1:])
			case '-', ' ':
				if baseLines[at] != body[1:] {
					t.Fatalf("hunk line %q does not match the base's %q", body[1:], baseLines[at])
				}
				if body[0] == ' ' {
					out = append(out, baseLines[at])
				}
				at++
			default:
				t.Fatalf("unrecognised hunk line %q", body)
			}
		}
	}
	return strings.Join(append(out, baseLines[at:]...), "\n")
}

// hunkStart is the line of the base file a `@@ -start,count +start,count @@`
// header opens at.
func hunkStart(t *testing.T, header string) int {
	t.Helper()
	fields := strings.Fields(header)
	if len(fields) < 2 || !strings.HasPrefix(fields[1], "-") {
		t.Fatalf("unrecognised hunk header %q", header)
	}
	start, _, _ := strings.Cut(strings.TrimPrefix(fields[1], "-"), ",")
	n, err := strconv.Atoi(start)
	if err != nil {
		t.Fatalf("unrecognised hunk header %q: %v", header, err)
	}
	return n
}
