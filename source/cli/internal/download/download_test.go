package download

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A symlink whose target is an ABSOLUTE path is the escape the obvious
// containment check does not catch: filepath.Join("bin", "/etc/passwd") is
// "bin/etc/passwd", so joining the target against the link's own directory
// silently reinterprets it as relative and every such entry passes. Combined
// with a later regular-file entry at the same name — which O_CREATE would
// open *through* the symlink — an archive could overwrite any file the user
// can write. Both halves are covered here.
func TestExtractTarGzRejectsAbsoluteSymlinkTarget(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, []byte("ORIGINAL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := tarGzEntries(t, []tar.Header{
		{Name: "pkg/bin/npx", Typeflag: tar.TypeSymlink, Linkname: outside, Mode: 0o777},
		{Name: "pkg/bin/npx", Typeflag: tar.TypeReg, Mode: 0o755},
	}, map[string]string{"pkg/bin/npx": "PWNED\n"})

	if err := ExtractTarGz(archive, t.TempDir(), 1); err == nil {
		t.Error("extractTarGz accepted a symlink to an absolute path outside the destination")
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ORIGINAL\n" {
		t.Fatalf("a file outside the destination was overwritten with %q", got)
	}
}

// The relative form of the same escape.
func TestExtractTarGzRejectsRelativeSymlinkEscape(t *testing.T) {
	archive := tarGzEntries(t, []tar.Header{
		{Name: "pkg/bin/npx", Typeflag: tar.TypeSymlink, Linkname: "../../../../etc/passwd", Mode: 0o777},
	}, nil)
	if err := ExtractTarGz(archive, t.TempDir(), 1); err == nil {
		t.Error("extractTarGz accepted a relative symlink escaping the destination")
	}
}

// Contained symlinks are the normal case — Node's tarball links npm and npx
// into lib/node_modules — so the guard above must not reject them.
func TestExtractTarGzKeepsContainedSymlinks(t *testing.T) {
	archive := tarGzEntries(t, []tar.Header{
		{Name: "pkg/lib/node_modules/npm/bin/npm-cli.js", Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: "pkg/bin/npm", Typeflag: tar.TypeSymlink, Linkname: "../lib/node_modules/npm/bin/npm-cli.js", Mode: 0o777},
	}, map[string]string{"pkg/lib/node_modules/npm/bin/npm-cli.js": "#!/usr/bin/env node\n"})

	dest := t.TempDir()
	if err := ExtractTarGz(archive, dest, 1); err != nil {
		t.Fatalf("extractTarGz rejected a legitimate contained symlink: %v", err)
	}
	info, err := os.Lstat(filepath.Join(dest, "bin", "npm"))
	if err != nil {
		t.Fatalf("bin/npm missing: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("bin/npm should have been created as a symlink")
	}
}

// tarGzEntries builds a gzipped tar from explicit headers, so a test can
// control entry type, order and link targets. bodies supplies content for
// regular entries, keyed by header name.
func tarGzEntries(t *testing.T, headers []tar.Header, bodies map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, h := range headers {
		body := bodies[h.Name]
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestStrip(t *testing.T) {
	for _, tc := range []struct {
		in         string
		components int
		want       string
	}{
		{"go/bin/go", 1, "bin/go"},
		{"node-v22.11.0-linux-x64/bin/node", 1, "bin/node"},
		// A bare top-level entry has nothing left once its wrapper is
		// dropped, and is skipped rather than written to the destination root.
		{"go", 1, ""},
		{"", 1, ""},
		// A single-binary release tarball has no wrapper, and stripping there
		// would discard the only entry in it.
		{"cargo-nextest", 0, "cargo-nextest"},
		{"./cargo-nextest", 0, "cargo-nextest"},
	} {
		if got := strip(tc.in, tc.components); got != tc.want {
			t.Errorf("strip(%q, %d) = %q, want %q", tc.in, tc.components, got, tc.want)
		}
	}
}

// A flat archive keeps its entries, which is the whole reason the strip count
// is a parameter.
func TestExtractTarGzWithoutStripping(t *testing.T) {
	dest := t.TempDir()
	if err := ExtractTarGz(tarGz(t, map[string]string{"cargo-nextest": "#!/bin/sh\n"}), dest, 0); err != nil {
		t.Fatalf("ExtractTarGz: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "cargo-nextest")); err != nil {
		t.Errorf("the archive's only entry was not written: %v", err)
	}
}

// The traversal guard has to hold whether or not a component is stripped: a
// stripped archive gets one component eaten before safeJoin sees the path.
func TestExtractTarGzRejectsEscapeWithoutStripping(t *testing.T) {
	if err := ExtractTarGz(tarGz(t, map[string]string{"../escaped": "x"}), t.TempDir(), 0); err == nil {
		t.Error("ExtractTarGz accepted an entry escaping the destination")
	}
}

func TestExtractTarGz(t *testing.T) {
	archive := tarGz(t, map[string]string{
		"go/bin/go":       "#!/bin/sh\necho go\n",
		"go/src/fmt/x.go": "package fmt\n",
	})
	dest := t.TempDir()
	if err := ExtractTarGz(archive, dest, 1); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}
	// The wrapping directory is dropped, so bin/ lands directly in dest.
	got, err := os.ReadFile(filepath.Join(dest, "bin", "go"))
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	if !strings.Contains(string(got), "echo go") {
		t.Errorf("extracted content = %q", got)
	}
	info, err := os.Stat(filepath.Join(dest, "bin", "go"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("extracted binary lost its execute bit: %v", info.Mode())
	}
}

func TestExtractTarGzRejectsEscapingEntry(t *testing.T) {
	archive := tarGz(t, map[string]string{"go/../../escaped": "owned\n"})
	dest := t.TempDir()
	if err := ExtractTarGz(archive, dest, 1); err == nil {
		t.Fatal("extractTarGz accepted an entry escaping the destination")
	}
}

// tarGz builds a gzipped tar in memory from path -> content.
func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o755,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The zip-slip guard. lydite unpacks these archives with the user's own
// privileges, so an entry that escapes the destination writes anywhere the
// user can.
func TestSafeJoinRejectsEscapes(t *testing.T) {
	dest := t.TempDir()
	for _, rel := range []string{
		"../escaped",
		"../../etc/passwd",
		"a/../../../etc/passwd",
	} {
		if _, err := safeJoin(dest, rel); err == nil {
			t.Errorf("safeJoin(%q) was allowed; it escapes the destination", rel)
		}
	}
	// A path that merely contains ".." but stays inside is fine.
	if _, err := safeJoin(dest, "a/b/../c"); err != nil {
		t.Errorf("safeJoin rejected a contained path: %v", err)
	}
}

// A destination that is a prefix of a sibling directory name must not be
// mistaken for containment: "/tmp/dest-evil" is not inside "/tmp/dest".
func TestSafeJoinIsNotFooledByAPrefixSibling(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "dest")
	if _, err := safeJoin(dest, "../dest-evil/x"); err == nil {
		t.Fatal("safeJoin allowed a sibling directory sharing the destination's prefix")
	}
}
