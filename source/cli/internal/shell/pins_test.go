package shell

import "testing"

// TestPinnedVersionParses guards the manifest→runtime path: Dependabot edits
// shellcheck-pin/requirements.txt, and a bump whose shape this parser can't
// read must fail here rather than becoming `pipx install shellcheck-py==`.
func TestPinnedVersionParses(t *testing.T) {
	v, err := pinnedVersion(requirements)
	if err != nil {
		t.Fatalf("pinnedVersion: %v", err)
	}
	if v == "" {
		t.Fatal("pinnedVersion is empty")
	}
	if v != version {
		t.Errorf("pinnedVersion = %q but package-level version = %q", v, version)
	}
}

func TestPinnedVersionSkipsCommentsAndBlanks(t *testing.T) {
	if _, err := pinnedVersion([]byte("# shellcheck-py==9.9.9.9\n\n")); err == nil {
		t.Error("pinnedVersion read a version out of a comment")
	}
}

func TestPinnedVersionRequiresAPin(t *testing.T) {
	if _, err := pinnedVersion([]byte("shellcheck-py\n")); err == nil {
		t.Error("pinnedVersion accepted an unpinned requirement")
	}
}

// The binary's name is not the distribution's: a line pinning `shellcheck`
// names a different PyPI project, and must not be read as this pin.
func TestPinnedVersionIgnoresTheBinaryName(t *testing.T) {
	if v, err := pinnedVersion([]byte("shellcheck==0.11.0\n")); err == nil {
		t.Errorf("pinnedVersion read %q from a line pinning a different distribution", v)
	}
}

func TestPinnedVersionStripsAnInlineComment(t *testing.T) {
	v, err := pinnedVersion([]byte("shellcheck-py==0.11.0.1  # note\n"))
	if err != nil {
		t.Fatalf("pinnedVersion: %v", err)
	}
	if v != "0.11.0.1" {
		t.Errorf("pinnedVersion = %q; want 0.11.0.1", v)
	}
}

// A requirements line has no terminator, so an environment marker or a hash
// Dependabot appends would otherwise reach `pipx install shellcheck-py==`
// verbatim.
func TestPinnedVersionRejectsTrailingContent(t *testing.T) {
	for _, line := range []string{
		"shellcheck-py==0.11.0.1 ; python_version >= \"3.9\"\n",
		"shellcheck-py==0.11.0.1 --hash=sha256:abc123\n",
	} {
		if v, err := pinnedVersion([]byte(line)); err == nil {
			t.Errorf("pinnedVersion(%q) returned %q; want an error", line, v)
		}
	}
}
