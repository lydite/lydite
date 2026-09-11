package rust

import (
	"os"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/fixture"
)

// crateUnderClippy is the crate the captured report names, materialised.
func crateUnderClippy(t *testing.T) string {
	t.Helper()
	return fixture.Tree(t, "testdata/clippyprobe")
}

// clippyFixture is cargo's real NDJSON from `cargo clippy --all-targets
// --message-format json` over a crate with a lib and a test target, captured
// from the pinned toolchain rather than written by hand.
func clippyFixture(t *testing.T) []clippyMessage {
	t.Helper()
	data, err := os.ReadFile("testdata/clippy.ndjson")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	return decodeClippy(strings.NewReader(string(data)))
}

func TestClippyReportsEachLintOnceWhenCargoEmitsItPerTarget(t *testing.T) {
	messages := clippyFixture(t)
	var diagnostics int
	for _, m := range messages {
		if m.Reason == "compiler-message" {
			diagnostics++
		}
	}
	if diagnostics != 4 {
		t.Fatalf("fixture holds %d diagnostics, want the 4 cargo emits for 2 lints", diagnostics)
	}

	got := clippyFindings(crateUnderClippy(t), messages)
	if len(got) != 2 {
		t.Fatalf("clippyFindings gave %d claims, want 2 — cargo compiles the crate as a library and as its own test harness and reports every lint under both", len(got))
	}
	for _, f := range got {
		if f.Ordinal != 0 {
			t.Errorf("%s got ordinal %d: the copies must be collapsed before numbering, or two real claims of one rule on one line become indistinguishable from a double-emit", f.Rule, f.Ordinal)
		}
	}
}

func TestClippyDistinguishesTwoLintsOfOneRule(t *testing.T) {
	// Two genuinely distinct claims: one rule, one file, different lines. The
	// collapse keys on the span as well as the rule, so neither is lost.
	messages := []clippyMessage{
		clippyMessageOf("clippy::ptr_arg", "src/lib.rs", 1, 13),
		clippyMessageOf("clippy::ptr_arg", "src/lib.rs", 9, 13),
	}
	got := clippyFindings(t.TempDir(), messages)
	if len(got) != 2 {
		t.Fatalf("clippyFindings gave %d claims, want 2 — a rule firing twice in one file is two claims", len(got))
	}
	// Their site is the rule with the source text it fired on, which is the
	// same text for both here. The ordinal is the whole of what keeps them
	// apart; without it they hash alike and finding.Set drops one.
	if got[0].Fingerprint() == got[1].Fingerprint() {
		t.Error("the two claims share a fingerprint, so the second is dropped as a duplicate")
	}
}

func TestClippyReadsTheRealReport(t *testing.T) {
	got := clippyFindings(crateUnderClippy(t), clippyFixture(t))
	if len(got) != 2 {
		t.Fatalf("got %d claims, want 2", len(got))
	}
	first := got[0]
	if first.Gate != "cargo clippy" {
		t.Errorf("gate = %q, want cargo clippy", first.Gate)
	}
	if first.Rule != "clippy::ptr_arg" {
		t.Errorf("rule = %q, want clippy::ptr_arg", first.Rule)
	}
	if first.Path != "src/lib.rs" {
		t.Errorf("path = %q, want src/lib.rs — cargo names files from the workspace root", first.Path)
	}
	if first.Line != 1 {
		t.Errorf("line = %d, want 1", first.Line)
	}
	if first.Severity != "error" {
		t.Errorf("severity = %q, want error — clippy's own word, carried unchanged", first.Severity)
	}
	if !strings.Contains(first.Message, "&Vec") {
		t.Errorf("message = %q, want clippy's own wording", first.Message)
	}
	if len(first.Detail) == 0 || !strings.Contains(strings.Join(first.Detail, "\n"), "--> src/lib.rs:1:13") {
		t.Errorf("detail = %q, want the diagnostic cargo renders", first.Detail)
	}
}

func TestClippyLocatesAClaimAtItsPrimarySpan(t *testing.T) {
	// A secondary span points at context the author is not being asked to
	// change — the definition a call violates — so a claim located there
	// would put a review thread on innocent code.
	m := clippyMessageOf("clippy::needless_range_loop", "src/lib.rs", 3, 14)
	m.Message.Spans = append([]clippySpan{{FileName: "src/other.rs", LineStart: 99, IsPrimary: false}}, m.Message.Spans...)
	got := clippyFindings(t.TempDir(), []clippyMessage{m})
	if len(got) != 1 {
		t.Fatalf("got %d claims, want 1", len(got))
	}
	if got[0].Path != "src/lib.rs" || got[0].Line != 3 {
		t.Errorf("located at %s:%d, want src/lib.rs:3 — the primary span", got[0].Path, got[0].Line)
	}
}

func TestClippyKeepsADiagnosticWhoseRuleItDoesNotRecognise(t *testing.T) {
	// A rustc diagnostic with no clippy lint name, and a lint lydite has
	// never heard of. Both are reported: a parser that drops what it does not
	// recognise is how a gate stops gating.
	unnamed := clippyMessageOf("", "src/lib.rs", 4, 1)
	unnamed.Message.Code = nil
	got := clippyFindings(t.TempDir(), []clippyMessage{
		unnamed,
		clippyMessageOf("clippy::a_lint_from_a_later_toolchain", "src/lib.rs", 5, 1),
	})
	if len(got) != 2 {
		t.Fatalf("got %d claims, want 2 — neither an unnamed diagnostic nor an unknown lint may be dropped", len(got))
	}
}

func TestClippySkipsADiagnosticThatLocatesNothing(t *testing.T) {
	// cargo's build summary ("could not compile") carries no span. It is the
	// reason the row fails, and cargo's exit status is what reports it; a
	// claim on line zero of the crate root is one nothing can anchor.
	spanless := clippyMessageOf("", "", 0, 0)
	spanless.Message.Spans = nil
	if got := clippyFindings(t.TempDir(), []clippyMessage{spanless}); len(got) != 0 {
		t.Fatalf("got %d claims, want none", len(got))
	}
}

func TestDecodeClippySkipsOnlyTheMalformedLine(t *testing.T) {
	// A build script printing to stdout puts its own text into this stream.
	// A decoder that stopped there would cost the run every finding after it,
	// which is how a gate stops gating.
	msg := func(name string) string {
		return `{"reason":"compiler-message","message":{"message":"` + name +
			`","spans":[{"file_name":"a.rs","line_start":1,"is_primary":true}]}}`
	}
	stream := msg("first") + "\n" +
		"a build script wrote this\n" +
		msg("second") + "\n"
	got := decodeClippy(strings.NewReader(stream))
	if len(got) != 2 {
		t.Fatalf("got %d messages, want the 2 that parsed either side of the bad line", len(got))
	}
	if got[1].Message.Message != "second" {
		t.Errorf("second message = %q, want the one after the malformed line", got[1].Message.Message)
	}
}

func TestDecodeClippySkipsALineOverTheCap(t *testing.T) {
	// A line too long to buffer is skipped like any other unreadable one, and
	// the stream is still read to the end.
	huge := `{"reason":"compiler-message","message":{"message":"` + strings.Repeat("x", maxNDJSONLine) + `"}}`
	stream := huge + "\n" +
		`{"reason":"compiler-message","message":{"message":"after","spans":[{"file_name":"a.rs","line_start":1,"is_primary":true}]}}` + "\n"
	got := decodeClippy(strings.NewReader(stream))
	if len(got) != 1 || got[0].Message.Message != "after" {
		t.Fatalf("got %d messages, want the one after the oversized line", len(got))
	}
}

// clippyMessageOf is a diagnostic with one primary span, for the cases a
// captured report does not contain.
func clippyMessageOf(rule, file string, line, col int) clippyMessage {
	m := clippyMessage{Reason: "compiler-message", Message: &clippyDiagnostic{}}
	if rule != "" {
		m.Message.Code = &struct {
			Code string `json:"code"`
		}{Code: rule}
	}
	m.Message.Level = "error"
	m.Message.Message = "a lint"
	m.Message.Spans = []clippySpan{{FileName: file, LineStart: line, ColumnStart: col, IsPrimary: true}}
	return m
}

func TestClippyArgvIsTwoPassesOverTheSameLints(t *testing.T) {
	// The pair is what keeps the terminal output and the data in agreement:
	// the same lints under both, the format flag on the data pass alone, and
	// `-D warnings` after the `--` on both so the verdict cannot depend on
	// which pass is read.
	text := strings.Join(clippyArgv(false), " ")
	data := strings.Join(clippyArgv(true), " ")
	if text != "clippy --all-targets -- -D warnings" {
		t.Errorf("text pass = %q", text)
	}
	if data != "clippy --all-targets --message-format json -- -D warnings" {
		t.Errorf("data pass = %q", data)
	}
	// --all-targets is what compiles the crate as its own test harness, which
	// is where the double emission clippyFindings collapses comes from.
	for _, argv := range [][]string{clippyArgv(false), clippyArgv(true)} {
		if !slices.Contains(argv, "--all-targets") {
			t.Errorf("argv = %q, want --all-targets on both passes", argv)
		}
	}
}

func TestClippyEndIsZeroForASingleLineSpan(t *testing.T) {
	// EndLine is zero for a claim about one line, so a consumer does not have
	// to tell a one-line span from a zero-length one.
	cases := []struct {
		name    string
		span    clippySpan
		wantEnd int
	}{
		{"one line", clippySpan{LineStart: 3, LineEnd: 3}, 0},
		{"no end stated", clippySpan{LineStart: 3}, 0},
		{"a real span", clippySpan{LineStart: 3, LineEnd: 7}, 7},
		{"an end before the start is not a span", clippySpan{LineStart: 7, LineEnd: 3}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clippyEnd(tc.span); got != tc.wantEnd {
				t.Errorf("clippyEnd(%+v) = %d, want %d", tc.span, got, tc.wantEnd)
			}
		})
	}
}

func TestClippyPrimaryFallsBackToTheFirstSpan(t *testing.T) {
	// A diagnostic that marks none of its spans primary still locates
	// something, and the first is the best guess available.
	got, ok := clippyPrimary([]clippySpan{{FileName: "a.rs", LineStart: 1}, {FileName: "b.rs", LineStart: 2}})
	if !ok || got.FileName != "a.rs" {
		t.Errorf("clippyPrimary = %+v, %v, want the first span", got, ok)
	}
	if _, ok := clippyPrimary(nil); ok {
		t.Error("clippyPrimary claimed a span for a diagnostic with none")
	}
}
