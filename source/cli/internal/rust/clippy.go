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
			Gate:     "cargo clippy",
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

// clippyArgv is one of the two passes, as argv.
//
// The pair is the decision this file makes and the thing a regression would
// silently undo: the same lints under both, `--message-format json` on the
// data pass alone, and `-D warnings` after the `--` on both so the verdict
// does not depend on which pass is read. --all-targets is what compiles the
// crate as its own test harness, which is where the double emission comes
// from.
func clippyArgv(asJSON bool) []string {
	argv := []string{"clippy", "--all-targets"}
	if asJSON {
		argv = append(argv, "--message-format", "json")
	}
	return append(argv, "--", "-D", "warnings")
}

// runClippy runs clippy twice: once for the terminal and the verdict, once
// for the data.
//
// cargo has no flag that writes a machine-readable report to a file while
// still printing for a human, and no output-file flag at all:
// --message-format json replaces the stream rather than copying it. Streaming
// that to a terminal would put a wall of JSON where a developer expects
// clippy's own annotated source, so the first pass is the one that streams and
// decides the row, and the second only populates Findings.
//
// The second pass is all but free — measured at 0.24s against the first pass's
// 1.76s — because cargo replays the diagnostics it cached rather than
// recompiling. --message-format is an output concern and does not invalidate
// the build cache.
// [lydite:exclude_from_coverage][the proving ground runs clippy over a real
// crate on every run; a unit test here would run the machine's own cargo
// rather than lydite's invocation, which clippyArgv states and
// TestClippyArgvIsTwoPassesOverTheSameLints asserts]
func runClippy(ctx context.Context, dir string, env []string) executil.Result {
	r := named("cargo clippy", executil.RunEnv(ctx, dir, env, "cargo", clippyArgv(false)...))

	// RunQuiet, because this pass is data: streaming it would print the whole
	// report a second time, as JSON, under the one the developer just read.
	data := executil.RunQuietEnv(ctx, dir, env, "cargo", clippyArgv(true)...)
	if data.Output == "" {
		// Nothing to parse: leave the first pass's verdict and output as-is
		// rather than inventing one.
		return r
	}
	r.Findings = clippyFindings(dir, decodeClippy(strings.NewReader(data.Output)))
	return r
}
