package rust

import (
	"context"
	"fmt"
	"io"
	"strings"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
)

// clippyMessage is one line of `cargo --message-format json`.
//
// cargo emits several kinds of line and only `compiler-message` carries a
// diagnostic; the rest report build progress. A line lydite cannot parse is
// skipped rather than failing the parse, because cargo interleaves its own
// output kinds and a future one must not cost the run its findings.
type clippyMessage struct {
	Reason  string            `json:"reason"`
	Message *clippyDiagnostic `json:"message"`
}

// clippyDiagnostic is one lint, as rustc's diagnostic format states it.
type clippyDiagnostic struct {
	Code *struct {
		// Code is the lint's name. A rustc diagnostic that is not a clippy
		// lint carries no code at all, which is why this is a pointer.
		Code string `json:"code"`
	} `json:"code"`
	Level   string       `json:"level"`
	Message string       `json:"message"`
	Spans   []clippySpan `json:"spans"`
	// Rendered is the exact text cargo prints for this diagnostic, and is
	// what the finding carries as its Detail — so a claim read out of the
	// document says what clippy said, word for word.
	Rendered string `json:"rendered"`
}

type clippySpan struct {
	FileName    string `json:"file_name"`
	LineStart   int    `json:"line_start"`
	LineEnd     int    `json:"line_end"`
	ColumnStart int    `json:"column_start"`
	IsPrimary   bool   `json:"is_primary"`
}

// clippyFindings is every diagnostic as a located claim, with cargo's
// double-emit collapsed.
//
// `--all-targets` compiles the crate once as a library and once as its own
// test harness, and clippy reports every lint in the shared source under both.
// The two messages are identical in every field — target name, kind, span,
// rule and rendered text alike — so nothing downstream can tell them apart.
// Fingerprints cannot: finding.Number gives two claims alike in path and site
// the ordinals 0 and 1, which is exactly how it keeps two genuinely distinct
// lints of one rule on one line apart, so both copies hash differently and
// both survive. Left uncollapsed this reports double the count and puts two
// review threads on one line.
//
// The collapse is therefore here, keyed on what identifies the lint — file,
// span and rule — and before the numbering, so the ordinals are assigned over
// the real claims rather than over the copies.
func clippyFindings(dir string, messages []clippyMessage) []finding.Finding {
	src := finding.NewSource(dir)
	seen := map[string]bool{}
	var out []finding.Finding
	for i := range messages {
		m := messages[i]
		if m.Reason != "compiler-message" || m.Message == nil {
			continue
		}
		rule := ""
		if m.Message.Code != nil {
			rule = m.Message.Code.Code
		}
		span, ok := clippyPrimary(m.Message.Spans)
		if !ok {
			// A diagnostic with no span locates nothing — cargo's build
			// summary ("could not compile") is the standing example. The row
			// still fails on cargo's exit status, which is what reports it.
			continue
		}
		path := strings.TrimPrefix(strings.ReplaceAll(span.FileName, "\\", "/"), "./")
		key := fmt.Sprintf("%s\x1f%s\x1f%d:%d:%d", path, rule,
			span.LineStart, span.ColumnStart, span.LineEnd)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, finding.Finding{
			Gate:     GateClippy,
			Path:     path,
			Line:     span.LineStart,
			EndLine:  clippyEnd(span),
			Rule:     rule,
			Severity: m.Message.Level,
			Message:  m.Message.Message,
			Detail:   excerpt(m.Message.Rendered),
			Site:     rule + "\x1f" + src.Line(path, span.LineStart),
		})
	}
	finding.Number(out)
	return out
}

// clippyPrimary is the span the diagnostic is actually about.
//
// A lint carries one primary span and any number of secondary ones pointing at
// context — the definition a call violates, the trait it comes from — and
// locating the claim at a secondary span puts it on code the author is not
// being asked to change. The first span is the fallback for a diagnostic that
// marks none as primary.
func clippyPrimary(spans []clippySpan) (clippySpan, bool) {
	for _, s := range spans {
		if s.IsPrimary {
			return s, true
		}
	}
	if len(spans) > 0 {
		return spans[0], true
	}
	return clippySpan{}, false
}

// clippyEnd is the span's last line, or zero when the span is one line.
func clippyEnd(s clippySpan) int {
	if s.LineEnd > s.LineStart {
		return s.LineEnd
	}
	return 0
}

// decodeClippy reads cargo's newline-delimited JSON, skipping what will not
// parse.
//
// A malformed line costs only itself: cargo writes its own diagnostics into
// this stream when a build script prints to stdout, and losing every finding
// after such a line is how a gate stops gating.
func decodeClippy(r io.Reader) []clippyMessage { return decodeNDJSON[clippyMessage](r) }

// excerpt is a tool's rendered diagnostic as the lines a reader sees.
func excerpt(rendered string) []string {
	rendered = strings.TrimRight(rendered, "\n")
	if rendered == "" {
		return nil
	}
	return strings.Split(rendered, "\n")
}

// clippyArgv is the invocation, as argv.
//
// --message-format json is what makes the run readable as data, and cargo's
// own rendered diagnostic survives inside each message rather than being lost
// with the human stream it replaces. `-D warnings` after the `--` is what makes
// a lint fail the run, so this argv's exit status is the row's verdict.
// --all-targets compiles the crate as its own test harness, which is where the
// double emission clippyFindings collapses comes from.
func clippyArgv() []string {
	return []string{"clippy", "--all-targets", "--message-format", "json", "--", "-D", "warnings"}
}

// runClippy runs clippy once, as data.
//
// cargo has no flag that writes a machine-readable report to a file while
// still printing for a human, and no output-file flag at all:
// --message-format json replaces the stream rather than copying it. So the
// JSON run is the only run — its exit status decides the row and its messages
// are the findings — and what a developer reads is the Detail report() prints
// from them, built out of the very diagnostics cargo would have rendered.
// [lydite:exclude_from_coverage][the proving ground runs clippy over a real
// crate on every run; a unit test here would run the machine's own cargo
// rather than lydite's invocation, which clippyArgv states and
// TestClippyArgvIsOneJSONRunOverTheLints asserts — everything done with the
// output is clippyResult, which the captured reports test directly]
func runClippy(ctx context.Context, dir string, env []string) executil.Result {
	// RunQuiet, because the stream is JSON: streaming it would put a wall of
	// machine-readable text where a developer expects clippy's own annotated
	// source, which arrives instead as the row's Detail.
	return clippyResult(dir, named(GateClippy, executil.RunQuietEnv(ctx, dir, env, "cargo", clippyArgv()...)))
}

// clippyResult is one run read as claims, with the Detail a failing row needs
// rendered from them.
//
// cargo's exit status stays the verdict and a finding count never becomes one:
// a build that does not compile fails with diagnostics that locate nothing, and
// a run whose report lists no claim can still be a failure that has to say why.
//
// Crashed is read off the diagnostics, never off the exit status, which is 101
// for a run that found a lint as much as for one that would not build: a
// failing run is a whole answer only when every error it states is a lint.
func clippyResult(dir string, r executil.Result) executil.Result {
	messages := decodeClippy(strings.NewReader(r.Output))
	r.Findings = clippyFindings(dir, messages)
	r.Crashed = !r.Ok() && !clippyOnlyLinted(messages)
	if r.Ok() {
		return r
	}
	if detail := findingsDetail(r.Findings); detail != "" {
		r.Detail = detail
		return r
	}
	if notes := clippyNotes(messages); notes != "" {
		r.Detail = notes
		return r
	}
	r.Detail = unreadable(GateClippy, r.Err)
	return r
}

// clippyOnlyLinted reports whether a failing run failed on lints alone.
//
// Under `-D warnings` a lint is an error-level diagnostic whose code names the
// lint — `clippy::ptr_arg`, `unused_variables`. A diagnostic rustc raises
// because the code is not valid Rust carries an error code of the form E0277,
// or none at all for a parse error, and a crate that stops there is a crate
// clippy linted none of. A failing run stating no error-level diagnostic at
// all failed before rustc said anything — a manifest cargo could not resolve,
// a build script that died — and is no answer either.
func clippyOnlyLinted(messages []clippyMessage) bool {
	linted := false
	for i := range messages {
		m := messages[i]
		if m.Reason != "compiler-message" || m.Message == nil ||
			!strings.HasPrefix(m.Message.Level, "error") {
			continue
		}
		if m.Message.Code == nil || m.Message.Code.Code == "" || rustcErrorCode(m.Message.Code.Code) {
			return false
		}
		linted = true
	}
	return linted
}

// rustcErrorCode reports whether code is one of rustc's own error codes — an
// E followed by digits — rather than a lint's name.
func rustcErrorCode(code string) bool {
	digits, ok := strings.CutPrefix(code, "E")
	if !ok || digits == "" {
		return false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// clippyNotes is the text of the diagnostics that locate nothing.
//
// cargo closes a failed build with spanless messages — "could not compile", "For
// more information about this error" — which clippyFindings drops because a
// claim on no line anchors nowhere. They are the whole of what a run that failed
// without locating anything has to say, and each is emitted once per target, so
// repeats are collapsed the way the located claims are.
func clippyNotes(messages []clippyMessage) string {
	seen := map[string]bool{}
	var b strings.Builder
	for i := range messages {
		m := messages[i]
		if m.Reason != "compiler-message" || m.Message == nil {
			continue
		}
		if _, located := clippyPrimary(m.Message.Spans); located {
			continue
		}
		text := strings.TrimRight(m.Message.Rendered, "\n")
		if text == "" {
			text = m.Message.Message
		}
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		b.WriteString(text)
		b.WriteByte('\n')
	}
	return b.String()
}
