package cargotool

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPinnedVersionStripsCargosExactOperator(t *testing.T) {
	v, err := PinnedVersion([]byte("[dependencies]\ncargo-nextest = \"=0.9.1\"\n"), "cargo-nextest")
	if err != nil {
		t.Fatalf("PinnedVersion: %v", err)
	}
	if v != "0.9.1" {
		t.Errorf("PinnedVersion = %q, want the bare version", v)
	}
}

func TestPinnedVersionMissingCrateIsAnError(t *testing.T) {
	if _, err := PinnedVersion([]byte("[dependencies]\ncargo-audit = \"=1.2.3\"\n"), "cargo-nope"); err == nil {
		t.Error("PinnedVersion accepted a crate the manifest does not pin")
	}
}

// A pin manifest also carries `version = "0.0.0"` and `name = "..."` in its
// [package] table, and a section-blind parser answers a lookup with that
// unrelated metadata.
func TestPinnedVersionIgnoresPackageMetadata(t *testing.T) {
	manifest := []byte("[package]\nname = \"pin\"\nversion = \"0.0.0\"\n\n[dependencies]\ncargo-audit = \"=1.2.3\"\n")
	if v, err := PinnedVersion(manifest, "version"); err == nil {
		t.Errorf("PinnedVersion matched the [package] version field, returning %q", v)
	}
}

// An inline comment left attached survives into `cargo install --version` as a
// corrupt value, where the parser promises a loud failure instead. This repo
// appends `# nosemgrep:` comments to config lines elsewhere, so it is not a
// hypothetical shape.
func TestPinnedVersionStripsInlineComment(t *testing.T) {
	v, err := PinnedVersion([]byte("[dependencies]\ncargo-audit = \"=1.2.3\" # pinned deliberately\n"), "cargo-audit")
	if err != nil {
		t.Fatalf("PinnedVersion: %v", err)
	}
	if v != "1.2.3" {
		t.Errorf("PinnedVersion = %q, want %q", v, "1.2.3")
	}
}

// An unparseable manifest must fail loudly rather than yield "", which becomes
// `cargo install --version ”` — a confusing failure far from its cause, or an
// install of whatever is latest.
func TestPinnedVersionRejectsAValueItCannotRead(t *testing.T) {
	for _, manifest := range []string{
		"[dependencies]\ncargo-audit = 1.2.3\n",
		"[dependencies]\ncargo-audit = \"=1.2.3\n",
		"[dependencies]\ncargo-audit = \"=\"\n",
	} {
		if v, err := PinnedVersion([]byte(manifest), "cargo-audit"); err == nil {
			t.Errorf("PinnedVersion(%q) = %q, want an error", manifest, v)
		}
	}
}

// Keyed by version, so two lydite releases pinning different ones do not
// overwrite each other and a downgrade does not silently keep the newer binary.
func TestRootIsKeyedByNameAndVersion(t *testing.T) {
	a, err := Tool{Name: "cargo-nextest", Version: "0.9.1"}.Root()
	if err != nil {
		t.Fatal(err)
	}
	b, err := Tool{Name: "cargo-nextest", Version: "0.9.2"}.Root()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("two versions share a cache directory: %q", a)
	}
	if filepath.Base(a) != "cargo-nextest-0.9.1" {
		t.Errorf("Root = %q, want it keyed by name and version", a)
	}
}

// --locked, or cargo re-resolves and the tool lydite installs is built from a
// different graph than its authors tested — a pin in name only.
func TestInstallArgvIsLocked(t *testing.T) {
	tool := Tool{Name: "cargo-nextest", Version: "0.9.1"}
	argv, err := tool.InstallArgv()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(argv, "--locked") {
		t.Errorf("InstallArgv = %v, want --locked", argv)
	}
	if !slices.Contains(argv, "0.9.1") {
		t.Errorf("InstallArgv = %v, want the pinned version", argv)
	}
	if argv[len(argv)-1] != "cargo-nextest" {
		t.Errorf("InstallArgv = %v, want the crate last", argv)
	}
	if !strings.HasPrefix(strings.Join(argv, " "), "install ") {
		t.Errorf("InstallArgv = %v, want a cargo install invocation", argv)
	}
}

func TestNotInstalledWhenTheBinaryIsAbsent(t *testing.T) {
	if (Tool{Name: "cargo-does-not-exist", Version: "9.9.9"}).Installed() {
		t.Error("Installed reported a tool that was never installed")
	}
}

// installPrebuilt is the path that turns a seven-minute source build into a
// three-second download, so it is worth testing end to end rather than only
// asserting the URLs it would fetch.
func TestInstallPrebuiltVerifiesAndStages(t *testing.T) {
	archive := tarGzOneFile(t, "cargo-fake", "#!/bin/sh\necho fake\n")
	sum := sha256.Sum256(archive)
	srv := serve(t, archive, hex.EncodeToString(sum[:])+"  cargo-fake.tar.gz\n")

	root := filepath.Join(t.TempDir(), "cargo-fake-1.0.0")
	tool := Tool{Name: "cargo-fake", Version: "1.0.0", Prebuilt: assetAt(srv)}
	if err := tool.installPrebuilt(t.Context(), root); err != nil {
		t.Fatalf("installPrebuilt: %v", err)
	}
	bin := filepath.Join(root, "bin", "cargo-fake")
	info, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("binary not installed: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("mode = %v, want the binary executable", info.Mode())
	}
	// Staged into a sibling and renamed, so an interrupted download cannot
	// leave a directory the next run reads as a finished install.
	entries, err := os.ReadDir(filepath.Dir(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("staging directory left behind: %v", entries)
	}
}

// lydite is about to put this binary on PATH and execute it, so an archive
// that does not match its published digest must never be unpacked.
func TestInstallPrebuiltRefusesAChecksumMismatch(t *testing.T) {
	srv := serve(t, tarGzOneFile(t, "cargo-fake", "payload"), strings.Repeat("0", 64)+"  cargo-fake.tar.gz\n")
	root := filepath.Join(t.TempDir(), "cargo-fake-1.0.0")
	tool := Tool{Name: "cargo-fake", Version: "1.0.0", Prebuilt: assetAt(srv)}
	if err := tool.installPrebuilt(t.Context(), root); err == nil {
		t.Fatal("installPrebuilt accepted an archive whose digest did not match")
	}
	if _, err := os.Stat(root); err == nil {
		t.Error("a refused download left an install directory behind")
	}
}

// An archive that does not contain the binary is refused rather than left as
// an empty install directory the next run treats as complete.
func TestInstallPrebuiltRefusesAnArchiveWithoutTheBinary(t *testing.T) {
	archive := tarGzOneFile(t, "something-else", "x")
	sum := sha256.Sum256(archive)
	srv := serve(t, archive, hex.EncodeToString(sum[:])+"  a.tar.gz\n")
	root := filepath.Join(t.TempDir(), "cargo-fake-1.0.0")
	tool := Tool{Name: "cargo-fake", Version: "1.0.0", Prebuilt: assetAt(srv)}
	if err := tool.installPrebuilt(t.Context(), root); err == nil {
		t.Fatal("installPrebuilt accepted an archive holding no binary")
	}
}

// A platform the publisher ships nothing for is not an error: the source
// build still works, just slowly.
func TestNoPrebuiltForThisPlatformIsNotAFailure(t *testing.T) {
	tool := Tool{Name: "cargo-fake", Version: "1.0.0", Prebuilt: func(string) (Asset, bool) { return Asset{}, false }}
	err := tool.installPrebuilt(t.Context(), filepath.Join(t.TempDir(), "x"))
	if !errors.Is(err, errNoPrebuilt) {
		t.Fatalf("err = %v, want errNoPrebuilt so the caller builds from source", err)
	}
}

func serve(t *testing.T, archive []byte, checksum string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			_, _ = w.Write([]byte(checksum))
			return
		}
		_, _ = w.Write(archive)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func assetAt(srv *httptest.Server) func(string) (Asset, bool) {
	return func(string) (Asset, bool) {
		return Asset{URL: srv.URL + "/a.tar.gz", ChecksumURL: srv.URL + "/a.sha256"}, true
	}
}

func tarGzOneFile(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
