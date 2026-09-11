package rust

import "testing"

func TestTomlString(t *testing.T) {
	cases := []struct{ in, want string }{
		{`name = "time"`, "time"},
		{`version = "0.1.44"`, "0.1.44"},
		{`name = ""`, ""},
		// Anything that is not a quoted value records nothing, which keeps a
		// lockfile entry lydite does not understand from being filed under a
		// name it does not have.
		{`name = time`, ""},
		{`name = "unterminated`, ""},
		{`no separator here`, ""},
		{`name =`, ""},
	}
	for _, tc := range cases {
		if got := tomlString(tc.in); got != tc.want {
			t.Errorf("tomlString(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTomlStringAtTheShortestQuotedValue(t *testing.T) {
	// `""` is two characters and a legitimate empty value; anything shorter
	// cannot carry both quotes. The boundary is what separates a value lydite
	// reads from one it refuses.
	if got := tomlString(`name = ""`); got != "" {
		t.Errorf("tomlString(`name = \"\"`) = %q, want the empty value", got)
	}
	// One quote is not a quoted value, and must not be read as one.
	if got := tomlString(`name = "`); got != "" {
		t.Errorf("tomlString(`name = \"`) = %q, want nothing", got)
	}
}
