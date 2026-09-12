package rust

import (
	"strings"
	"testing"
)

func TestDecodeNDJSONSkipsABlankLine(t *testing.T) {
	// A blank line is not a record. Parsing one would add a zero value to
	// every stream that ends with a newline, which both of this package's
	// tools' streams do.
	stream := "\n" +
		`{"type":"diagnostic","fields":{"code":"first","severity":"error","message":"a"}}` + "\n" +
		"\n"
	got := decodeNDJSON[denyMessage](strings.NewReader(stream))
	if len(got) != 1 {
		t.Fatalf("got %d messages, want the one real record", len(got))
	}
}

func TestDecodeNDJSONReadsALastLineWithNoNewline(t *testing.T) {
	// A stream whose last line carries no newline still ends with a record,
	// which is why readLine hands back the line alongside the error rather
	// than instead of it.
	stream := `{"type":"diagnostic","fields":{"code":"only","severity":"error","message":"a"}}`
	got := decodeNDJSON[denyMessage](strings.NewReader(stream))
	if len(got) != 1 || got[0].Fields.Code != "only" {
		t.Fatalf("got %d messages, want the unterminated last record", len(got))
	}
}

func TestDecodeNDJSONKeepsALineAtExactlyTheCap(t *testing.T) {
	// The cap bounds the line together with the newline that terminates it,
	// which is what the reader actually holds. A line filling the cap exactly
	// is kept; one byte more is abandoned. The boundary is what the cap means,
	// so it is stated rather than approached.
	pad := func(n int) string {
		prefix := `{"type":"diagnostic","fields":{"code":"x","severity":"e","message":"`
		suffix := `"}}`
		return prefix + strings.Repeat("y", n-len(prefix)-len(suffix)) + suffix
	}
	atCap := pad(maxNDJSONLine-1) + "\n"
	if len(atCap) != maxNDJSONLine {
		t.Fatalf("the fixture is %d bytes, want exactly the cap", len(atCap))
	}
	if got := decodeNDJSON[denyMessage](strings.NewReader(atCap)); len(got) != 1 {
		t.Errorf("got %d messages for a line filling the cap, want 1", len(got))
	}
	overCap := pad(maxNDJSONLine) + "\n"
	if got := decodeNDJSON[denyMessage](strings.NewReader(overCap)); len(got) != 0 {
		t.Errorf("got %d messages for a line one byte over the cap, want none", len(got))
	}
}
