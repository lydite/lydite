package toolchain

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1" // #nosec G505 -- building the legacy registry digest a fixture is checked against
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"lydite/lydite/internal/runner"
)

// tarball builds a registry-shaped .tar.gz: every entry under one top-level
// directory, the way `npm pack` writes it.
func tarball(t *testing.T, top string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		body := files[name]
		if err := tw.WriteHeader(&tar.Header{
			Name: top + "/" + name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
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
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// pnpmVersion is the pnpm release the fixtures below publish.
const pnpmVersion = "12.4.1"

// glibcExes are the `@pnpm/exe` packages lydite provisions pnpm from, one per
// platform it is built for.
var glibcExes = []string{"@pnpm/exe.linux-x64", "@pnpm/exe.linux-arm64", "@pnpm/exe.darwin-x64", "@pnpm/exe.darwin-arm64"}

// exeDeps pins every glibc exe package at version, as a pnpm release does.
func exeDeps(version string) map[string]string {
	deps := map[string]string{}
	for _, exe := range glibcExes {
		deps[exe] = version
	}
	return deps
}

// pnpmTarball is a pnpm release in its 12.x shape: its `bin` entry is a shell
// placeholder at the package root, and its native binary is one of the exe
// packages it names among optionalDependencies at its own version.
func pnpmTarball(t *testing.T, version string) []byte {
	t.Helper()
	return pnpmTarballDepending(t, version, exeDeps(version))
}

// pnpmTarballDepending is a pnpm release naming deps as its optional
// dependencies.
func pnpmTarballDepending(t *testing.T, version string, deps map[string]string) []byte {
	t.Helper()
	manifest, err := json.Marshal(map[string]any{
		"name":                 "pnpm",
		"version":              version,
		"bin":                  map[string]string{"pn": "pnpm", "pnx": "pnx", "pnpm": "pnpm", "pnpx": "pnpx"},
		"optionalDependencies": deps,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tarball(t, "package", map[string]string{
		"package.json": string(manifest),
		"pnpm":         "This file intentionally left blank; pnpm's preinstall replaces it.\n",
	})
}

// pnpmExeScript is the fixture native binary: a script that answers
// --version with version and needs nothing from PATH.
func pnpmExeScript(version string) string {
	return "#!/bin/sh\necho " + version + "\n"
}

// pnpmExeTarball is an `@pnpm/exe` package whose binary reports version.
func pnpmExeTarball(t *testing.T, version string) []byte {
	t.Helper()
	return tarball(t, "package", map[string]string{
		"package.json": `{"name":"@pnpm/exe.linux-x64","version":"` + version + `"}`,
		"pnpm":         pnpmExeScript(version),
		"LICENSE":      "MIT\n",
	})
}

// pnpmExeTarballMissingBinary is an `@pnpm/exe` package with no `pnpm` entry
// at all — the shape a truncated or malformed publish could produce.
func pnpmExeTarballMissingBinary(t *testing.T, version string) []byte {
	t.Helper()
	return tarball(t, "package", map[string]string{
		"package.json": `{"name":"@pnpm/exe.linux-x64","version":"` + version + `"}`,
		"LICENSE":      "MIT\n",
	})
}

// yarnTarball is a yarn release whose entry point, run by the fake node below,
// reports version.
func yarnTarball(t *testing.T, version string) []byte {
	t.Helper()
	return tarball(t, "package", map[string]string{
		"package.json": `{"name":"@yarnpkg/cli-dist","bin":{"yarn":"bin/yarn.js","yarnpkg":"bin/yarn.js"}}`,
		"bin/yarn.js":  "#!/usr/bin/env node\necho " + version + "\n",
	})
}

// hostExe is the exe package this machine provisions pnpm from; a test of
// that chain has nothing to fetch on a platform with none.
func hostExe(t *testing.T) string {
	t.Helper()
	exe, err := pnpmExePackage(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("no pnpm exe package for this platform: %v", err)
	}
	return exe
}

// fakeRegistry is an npm registry serving published documents and tarballs,
// counting what was asked of it. It stands in for npmRegistry for the length
// of the test.
type fakeRegistry struct {
	srv   *httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	hits  map[string]int
}

func newFakeRegistry(t *testing.T) *fakeRegistry {
	t.Helper()
	r := &fakeRegistry{files: map[string][]byte{}, hits: map[string]int{}}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.EscapedPath()
		r.mu.Lock()
		r.hits[p]++
		body, ok := r.files[p]
		r.mu.Unlock()
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(r.srv.Close)
	previous := npmRegistry
	npmRegistry = r.srv.URL + "/"
	t.Cleanup(func() { npmRegistry = previous })
	return r
}

// publish serves data as pkg@version's tarball, under a document whose dist
// the registry would publish for it, rewritten by edit when edit is non-nil.
func (r *fakeRegistry) publish(t *testing.T, pkg, version string, data []byte, edit func(*registryDist)) {
	t.Helper()
	base := pkg[strings.LastIndex(pkg, "/")+1:]
	tarPath := "/" + pkg + "/-/" + base + "-" + version + ".tgz"
	s512 := sha512.Sum512(data)
	s1 := sha1.Sum(data) // #nosec G401 -- the fixture's own legacy digest
	dist := registryDist{
		Tarball:   r.srv.URL + tarPath,
		Integrity: "sha512-" + base64.StdEncoding.EncodeToString(s512[:]),
		Shasum:    hex.EncodeToString(s1[:]),
	}
	if edit != nil {
		edit(&dist)
	}
	doc, err := json.Marshal(map[string]any{"name": pkg, "version": version, "dist": dist})
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files[tarPath] = data
	r.files["/"+strings.Replace(pkg, "/", "%2F", 1)+"/"+version] = doc
}

// requested is how many times anything under pkg — its document or its
// tarball — was asked for.
func (r *fakeRegistry) requested(pkg string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for p, hits := range r.hits {
		if strings.HasPrefix(p, "/"+pkg+"/") || strings.HasPrefix(p, "/"+strings.Replace(pkg, "/", "%2F", 1)+"/") {
			n += hits
		}
	}
	return n
}

// publishPnpm serves a pnpm release and this platform's exe package for it.
func publishPnpm(t *testing.T, r *fakeRegistry, release []byte, editExe func(*registryDist)) {
	t.Helper()
	r.publish(t, "pnpm", pnpmVersion, release, nil)
	r.publish(t, hostExe(t), pnpmVersion, pnpmExeTarball(t, pnpmVersion), editExe)
}

// sha512Pin is the `+sha512.<hex>` a packageManager field pins data with.
func sha512Pin(data []byte) string {
	s := sha512.Sum512(data)
	return "sha512." + hex.EncodeToString(s[:])
}

// fakeNode puts a node on PATH that identifies itself as version and otherwise
// runs the script it is handed with sh — enough to execute the fixture entry
// points above the way the real node executes pnpm's.
func fakeNode(t *testing.T, bin, version string) {
	t.Helper()
	writeScript(t, filepath.Join(bin, "node"), strings.Join([]string{
		"#!/bin/sh",
		`if [ "$1" = --version ]; then echo ` + quote(version) + `; exit 0; fi`,
		`exec /bin/sh "$@"`,
		"",
	}, "\n"))
}

// isolatedCache points os.UserCacheDir at a fresh directory on every platform
// lydite builds for.
func isolatedCache(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// cachedPnpm lays a pnpm install out in the cache directory provisioning
// would install it into under hash, so installOnce finds it finished and
// fetches nothing.
func cachedPnpm(t *testing.T, version, hash string) string {
	t.Helper()
	declared, err := parseDeclaredHash(hash)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := cacheRoot(managerCacheKey("pnpm", version, declared))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(dir, "bin", "pnpm"), pnpmExeScript(version))
	return dir
}

// The wrapper is the whole of how a provisioned yarn becomes a command: named
// for it, executable, and running the package's entry point through whichever
// node the PATH offers.
func TestUnpackManagerWritesACommandThatRunsTheEntryUnderNode(t *testing.T) {
	bin := fakeToolchainBin(t)
	fakeNode(t, bin, "v22.0.0")
	staging := t.TempDir()

	if err := unpackManager(yarnTarball(t, "4.1.0"), staging, "yarn"); err != nil {
		t.Fatalf("unpackManager: %v", err)
	}
	out, err := exec.Command(filepath.Join(staging, "bin", "yarn"), "--version").Output() // #nosec G204 -- a wrapper this test just wrote
	if err != nil {
		t.Fatalf("running the wrapper: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "4.1.0" {
		t.Fatalf("yarn --version = %q, want the entry point's own answer", got)
	}
}

// installOnce unpacks into a staging directory and renames it into place, so a
// wrapper that remembered where it was written would point at a directory that
// no longer exists.
func TestTheWrapperSurvivesItsDirectoryBeingMoved(t *testing.T) {
	bin := fakeToolchainBin(t)
	fakeNode(t, bin, "v22.0.0")
	staging := filepath.Join(t.TempDir(), "staging")

	if err := unpackManager(yarnTarball(t, "4.1.0"), staging, "yarn"); err != nil {
		t.Fatalf("unpackManager: %v", err)
	}
	moved := filepath.Join(t.TempDir(), "yarn-4.1.0")
	if err := os.Rename(staging, moved); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(filepath.Join(moved, "bin", "yarn"), "--version").Output() // #nosec G204 -- a wrapper this test just wrote
	if err != nil || strings.TrimSpace(string(out)) != "4.1.0" {
		t.Fatalf("the moved wrapper answered (%q, %v), want 4.1.0", out, err)
	}
}

func TestBinEntry(t *testing.T) {
	for _, tc := range []struct {
		name     string
		manifest string
		files    []string
		want     string
		wantErr  bool
	}{
		{"an object names each command", `{"bin":{"pnpm":"bin/pnpm.cjs","pnpx":"bin/pnpx.cjs"}}`,
			[]string{"bin/pnpm.cjs"}, "bin/pnpm.cjs", false},
		{"a leading ./ is the same file", `{"bin":{"pnpm":"./bin/pnpm.cjs"}}`,
			[]string{"bin/pnpm.cjs"}, "bin/pnpm.cjs", false},
		{"a bare string is the package's one command", `{"bin":"bin/pnpm.cjs"}`,
			[]string{"bin/pnpm.cjs"}, "bin/pnpm.cjs", false},
		{"an object without the command has none for it", `{"bin":{"pnpx":"bin/pnpx.cjs"}}`,
			[]string{"bin/pnpx.cjs"}, "", true},
		{"no bin at all is refused", `{"name":"pnpm"}`, nil, "", true},
		{"a bin escaping the package is refused", `{"bin":{"pnpm":"../../evil.js"}}`, nil, "", true},
		{"an absolute bin is refused", `{"bin":{"pnpm":"/usr/bin/evil"}}`, nil, "", true},
		{"a bin the package does not contain is refused", `{"bin":{"pnpm":"bin/pnpm.cjs"}}`, nil, "", true},
		{"a directory is not an entry point", `{"bin":{"pnpm":"bin"}}`, []string{"bin/pnpm.cjs"}, "", true},
		{"an unreadable manifest is refused", `{`, nil, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, "package.json", tc.manifest)
			for _, f := range tc.files {
				write(t, dir, f, "")
			}
			got, err := binEntry(dir, "pnpm")
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("binEntry = (%q, %v), want (%q, error %v)", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestBinEntryNeedsAManifest(t *testing.T) {
	got, err := binEntry(t.TempDir(), "pnpm")
	if err == nil {
		t.Fatal("binEntry found an entry in a package with no package.json")
	}
	if got != "" {
		t.Errorf("binEntry returned %q alongside its error, want none", got)
	}
}

// What a tarball is checked against comes from the registry's document for the
// version, and the strongest digest it carries is the one used.
func TestVerifyDist(t *testing.T) {
	data := []byte("tarball")
	s512 := sha512.Sum512(data)
	good512 := "sha512-" + base64.StdEncoding.EncodeToString(s512[:])
	bad512 := "sha512-" + base64.StdEncoding.EncodeToString(make([]byte, 64))
	s1 := sha1.Sum(data) // #nosec G401 -- the fixture's own legacy digest
	good1 := hex.EncodeToString(s1[:])

	for _, tc := range []struct {
		name    string
		dist    registryDist
		wantErr bool
	}{
		{"a matching sha512 passes", registryDist{Integrity: good512}, false},
		{"a mismatching sha512 fails", registryDist{Integrity: bad512}, true},
		// The shasum is not consulted once an integrity is published, so a
		// matching SHA-1 cannot vouch for a tarball SHA-512 rejects.
		{"sha512 is preferred over a matching sha1", registryDist{Integrity: bad512, Shasum: good1}, true},
		{"sha512 is found among several digests", registryDist{Integrity: "sha1-abc " + good512}, false},
		{"an undecodable sha512 fails", registryDist{Integrity: "sha512-!!!"}, true},
		{"sha1 is the fallback without an integrity", registryDist{Shasum: good1}, false},
		{"sha1 compares case-insensitively", registryDist{Shasum: strings.ToUpper(good1)}, false},
		{"integrity without a sha512 falls back to sha1", registryDist{Integrity: "sha1-abc", Shasum: good1}, false},
		{"a mismatching sha1 fails", registryDist{Shasum: strings.Repeat("0", 40)}, true},
		{"no digest at all is refused", registryDist{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := verifyDist(data, tc.dist); (err != nil) != tc.wantErr {
				t.Fatalf("verifyDist = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}

// A declared hash is checked beside the registry's digest, never in place of
// it: a tarball the registry's own document agrees with is still refused when
// it is not the one the repository pinned.
func TestVerifyTarball(t *testing.T) {
	data := []byte("tarball")
	s512 := sha512.Sum512(data)
	registry := registryDist{Integrity: "sha512-" + base64.StdEncoding.EncodeToString(s512[:])}
	badRegistry := registryDist{Integrity: "sha512-" + base64.StdEncoding.EncodeToString(make([]byte, 64))}
	s256 := sha256.Sum256(data)

	for _, tc := range []struct {
		name     string
		dist     registryDist
		declared string
		wantErr  bool
	}{
		{"no declared hash checks the registry's digest alone", registry, "", false},
		{"no declared hash still fails the registry's digest", badRegistry, "", true},
		{"a matching declared hash passes", registry, "sha512." + hex.EncodeToString(s512[:]), false},
		{"a declared hash in upper case matches", registry, "sha512." + strings.ToUpper(hex.EncodeToString(s512[:])), false},
		{"a matching sha256 passes", registry, "sha256." + hex.EncodeToString(s256[:]), false},
		{"a tarball the registry vouches for but the pin does not is refused",
			registry, "sha512." + strings.Repeat("0", 128), true},
		{"a matching declared hash does not excuse the registry's digest",
			badRegistry, "sha512." + hex.EncodeToString(s512[:]), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			declared, err := parseDeclaredHash(tc.declared)
			if err != nil {
				t.Fatalf("parseDeclaredHash(%q): %v", tc.declared, err)
			}
			if err := verifyTarball(data, tc.dist, declared); (err != nil) != tc.wantErr {
				t.Fatalf("verifyTarball = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}

// A hash lydite cannot check is refused, not skipped: skipping it installs
// whatever the registry serves as though nothing had been pinned.
func TestParseDeclaredHash(t *testing.T) {
	for _, tc := range []struct {
		name    string
		hash    string
		want    string
		wantErr bool
	}{
		{"none declared", "", "", false},
		{"sha512", "sha512." + strings.Repeat("ab", 64), "sha512." + strings.Repeat("ab", 64), false},
		{"sha1", "sha1." + strings.Repeat("0", 40), "sha1." + strings.Repeat("0", 40), false},
		{"sha224", "sha224." + strings.Repeat("0", 56), "sha224." + strings.Repeat("0", 56), false},
		{"sha384", "sha384." + strings.Repeat("0", 96), "sha384." + strings.Repeat("0", 96), false},
		{"upper-case hex is read as lower", "sha256." + strings.Repeat("AB", 32), "sha256." + strings.Repeat("ab", 32), false},
		{"an unknown algorithm is refused", "md5." + strings.Repeat("0", 32), "", true},
		{"no separator is refused", "sha512", "", true},
		{"the SRI spelling is refused", "sha512-" + strings.Repeat("0", 128), "", true},
		{"a digest that is not hex is refused", "sha256." + strings.Repeat("zz", 32), "", true},
		{"a digest of the wrong length is refused", "sha512." + strings.Repeat("0", 64), "", true},
		{"a path is refused", "sha512../../../etc", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDeclaredHash(tc.hash)
			if (err != nil) != tc.wantErr || got.String() != tc.want {
				t.Fatalf("parseDeclaredHash(%q) = (%q, %v), want (%q, error %v)", tc.hash, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

// An unpack is only verified against the hash it was installed under, so the
// same version pinned to different bytes is a different cache directory, and a
// pin with no hash keeps the manager-and-version key.
func TestManagerCacheKey(t *testing.T) {
	a, _ := parseDeclaredHash("sha512." + strings.Repeat("0", 128))
	b, _ := parseDeclaredHash("sha512." + strings.Repeat("1", 128))
	none := managerCacheKey("yarn", "4.1.0", declaredHash{})
	if none != "yarn-4.1.0" {
		t.Errorf("key without a declared hash = %q, want yarn-4.1.0", none)
	}
	keyA, keyB := managerCacheKey("yarn", "4.1.0", a), managerCacheKey("yarn", "4.1.0", b)
	if keyA == none || keyB == none || keyA == keyB {
		t.Errorf("keys = none %q, a %q, b %q; want three distinct directories", none, keyA, keyB)
	}
	if strings.ContainsAny(keyA, `/\`) {
		t.Errorf("key %q is not a single path element", keyA)
	}
}

// pnpm's cache holds its native binary for one platform, so its key names
// both, and never matches the directory a pnpm release's placeholder `bin`
// was unpacked into under the manager-and-version key.
func TestManagerCacheKeyForPnpmNamesTheNativeBinaryAndPlatform(t *testing.T) {
	a, _ := parseDeclaredHash("sha512." + strings.Repeat("0", 128))
	none := managerCacheKey("pnpm", pnpmVersion, declaredHash{})
	want := "pnpm-exe-" + pnpmVersion + "-" + runtime.GOOS + "-" + runtime.GOARCH
	if none != want {
		t.Errorf("key without a declared hash = %q, want %q", none, want)
	}
	if none == "pnpm-"+pnpmVersion {
		t.Errorf("key %q is the placeholder release's directory", none)
	}
	hashed := managerCacheKey("pnpm", pnpmVersion, a)
	if hashed != want+"+"+a.String() {
		t.Errorf("key with a declared hash = %q, want %q", hashed, want+"+"+a.String())
	}
	if hashed == "pnpm-"+pnpmVersion+"+"+a.String() {
		t.Errorf("key %q is the placeholder release's directory", hashed)
	}
}

// A declared hash lydite cannot check stops provisioning before anything is
// fetched or taken from the cache.
func TestProvisionRefusesAnUncheckableHash(t *testing.T) {
	isolatedCache(t)
	cachedPnpm(t, pnpmVersion, "")
	req := Requirement{Lang: runner.TypeScript, Manager: "pnpm", Version: "v" + pnpmVersion, Raw: pnpmVersion,
		Source: "package.json (packageManager)", Hash: "md5." + strings.Repeat("0", 32)}
	if st, err := provisionPackageManager(context.Background(), req, "", false); err == nil {
		t.Fatalf("provisionPackageManager = %+v, want a refusal of the md5 hash", st)
	}
}

func TestRegistryPackage(t *testing.T) {
	for _, tc := range []struct{ manager, version, want string }{
		{"pnpm", "12.4.1", "pnpm"},
		{"yarn", "1.22.19", "yarn"},
		{"yarn", "2.0.0", "@yarnpkg/cli-dist"},
		{"yarn", "4.1.0", "@yarnpkg/cli-dist"},
		{"yarn", "2.0.0-rc.1", "yarn"},
	} {
		if got := registryPackage(tc.manager, tc.version); got != tc.want {
			t.Errorf("registryPackage(%q, %q) = %q, want %q", tc.manager, tc.version, got, tc.want)
		}
	}
}

// pnpmWorkspace is a TypeScript component pinning pnpm in packageManager.
func pnpmWorkspace(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "package.json", `{"name":"x","packageManager":"pnpm@`+version+`"}`)
	write(t, dir, "pnpm-lock.yaml", "lockfileVersion: '6.0'\n")
	return dir
}

// The package manager joins the component's environment beside its Node
// rather than replacing it: both directories, and the Node as what the
// component resolved to, because a package manager measures nothing a
// baseline records.
func TestEnsureProvisionsThePinnedManagerAlongsideNode(t *testing.T) {
	isolatedCache(t)
	dir := pnpmWorkspace(t, pnpmVersion)
	bin := fakeToolchainBin(t)
	fakeNode(t, bin, "v22.0.0")
	cache := cachedPnpm(t, pnpmVersion, "")
	// An ambient pnpm newer than the pin: a floor would accept it.
	writeScript(t, filepath.Join(bin, "pnpm"), "#!/bin/sh\necho 12.5.0\n")

	var log bytes.Buffer
	env := ensureOne(t, dir, runner.TypeScript, Overrides{}, &log)
	if env == nil {
		t.Fatalf("Ensure returned no environment; log was %q", log.String())
	}
	if want := filepath.Join(cache, "bin"); !slices.Contains(env.PathDirs, want) {
		t.Fatalf("PathDirs = %q, want the pinned pnpm's %q; log was %q", env.PathDirs, want, log.String())
	}
	if got := env.Version(); got != "22.0.0" {
		t.Errorf("Version = %q, want the Node the component runs under", got)
	}
	out := log.String()
	if !strings.Contains(out, "installed pnpm "+pnpmVersion) || !strings.Contains(out, "ambient 12.5.0 is not the pinned release") {
		t.Errorf("log should name the pinned install and why the ambient pnpm was passed over, got %q", out)
	}
	if strings.Contains(out, unconfirmed) {
		t.Errorf("the installed pnpm should confirm its own version, got %q", out)
	}
}

// A package manager whose installed version cannot be confirmed is a failed
// provision of the manager alone. The component keeps the Node it resolved,
// directories and identity both, and gains nothing from the manager — not a
// directory holding a release nothing confirmed is the one pinned.
func TestAnUnconfirmedManagerLeavesTheComponentsRuntimeIntact(t *testing.T) {
	isolatedCache(t)
	dir := pnpmWorkspace(t, pnpmVersion)
	write(t, dir, ".nvmrc", "22.0.0\n")
	fakeToolchainBin(t)
	// The component's Node is itself provisioned, so its environment carries a
	// directory that has to survive the manager's failure.
	node, err := cacheRoot("node-22.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(node, "bin"), 0o750); err != nil {
		t.Fatal(err)
	}
	fakeNode(t, filepath.Join(node, "bin"), "v22.0.0")
	// A cached pnpm whose binary fails, so `pnpm --version` answers with an error.
	manager := cachedPnpm(t, pnpmVersion, "")
	writeScript(t, filepath.Join(manager, "bin", "pnpm"), "#!/bin/sh\necho 'pnpm: bad install' >&2\nexit 1\n")

	var log bytes.Buffer
	env := ensureOne(t, dir, runner.TypeScript, Overrides{}, &log)
	out := log.String()
	if !strings.Contains(out, "installed Node v22.0.0") {
		t.Fatalf("the cached Node was never provisioned, so there is no runtime to keep; log was %q", out)
	}
	if env == nil {
		t.Fatalf("Ensure returned no environment, want the component's Node; log was %q", out)
	}
	if want := []string{filepath.Join(node, "bin")}; !slices.Equal(env.PathDirs, want) {
		t.Errorf("PathDirs = %q, want the Node's %q alone and not the unconfirmed pnpm's %q",
			env.PathDirs, want, filepath.Join(manager, "bin"))
	}
	if got := env.Version(); got != "22.0.0" {
		t.Errorf("Version = %q, want the Node the component runs under", got)
	}
	if !strings.Contains(out, unconfirmed) || !strings.Contains(out, "exit status 1") {
		t.Errorf("log should warn that pnpm's installed version went unconfirmed and name why, got %q", out)
	}
	if !strings.Contains(out, "continuing with what is on PATH") {
		t.Errorf("log should say the component continues with what is on PATH, got %q", out)
	}
}

// A pin is satisfied by that release alone, so the one ambient answer that
// installs nothing is the exact version.
func TestEnsureUsesAnAmbientManagerOnlyAtTheExactPin(t *testing.T) {
	isolatedCache(t)
	dir := pnpmWorkspace(t, pnpmVersion)
	bin := fakeToolchainBin(t)
	fakeNode(t, bin, "v22.0.0")
	writeScript(t, filepath.Join(bin, "pnpm"), pnpmExeScript(pnpmVersion))

	var log bytes.Buffer
	env := ensureOne(t, dir, runner.TypeScript, Overrides{}, &log)
	if env == nil || len(env.PathDirs) != 0 {
		t.Fatalf("an ambient pnpm at the pin must add nothing to PATH, got %+v; log was %q", env, log.String())
	}
	if !strings.Contains(log.String(), "using ambient pnpm 12.4.1 (matches 12.4.1 pinned in package.json (packageManager))") {
		t.Errorf("log should say the ambient pnpm matched the pin, got %q", log.String())
	}
}

// Two workspaces pinning the same version under different hashes must not
// share Ensure's cached resolution: each has its own hash checked and its own
// cache directory, not whichever one happened to resolve first.
func TestEnsureVerifiesEachWorkspacesOwnDeclaredHash(t *testing.T) {
	isolatedCache(t)
	root := t.TempDir()
	hashA := "sha512." + strings.Repeat("a", 128)
	hashB := "sha512." + strings.Repeat("b", 128)
	write(t, root, "a/package.json", `{"name":"a","packageManager":"pnpm@`+pnpmVersion+`+`+hashA+`"}`)
	write(t, root, "a/pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	write(t, root, "b/package.json", `{"name":"b","packageManager":"pnpm@`+pnpmVersion+`+`+hashB+`"}`)
	write(t, root, "b/pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	fakeNode(t, fakeToolchainBin(t), "v22.0.0")

	dirA := cachedPnpm(t, pnpmVersion, hashA)
	dirB := cachedPnpm(t, pnpmVersion, hashB)

	units := []Unit{
		{Name: "a", Lang: runner.TypeScript, Dir: "a"},
		{Name: "b", Lang: runner.TypeScript, Dir: "b"},
	}
	envs, err := Ensure(context.Background(), root, units, Overrides{}, nil)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	envA, envB := envs.For("a"), envs.For("b")
	if envA == nil || envB == nil {
		t.Fatalf("Ensure returned no environment for one workspace: a=%+v b=%+v", envA, envB)
	}
	wantA, wantB := filepath.Join(dirA, "bin"), filepath.Join(dirB, "bin")
	if !slices.Contains(envA.PathDirs, wantA) {
		t.Errorf("workspace a's PathDirs = %q, want its own hash's cache %q", envA.PathDirs, wantA)
	}
	if !slices.Contains(envB.PathDirs, wantB) {
		t.Errorf("workspace b's PathDirs = %q, want its own hash's cache %q — sharing a's resolution means b's hash was never checked", envB.PathDirs, wantB)
	}
}

// A package manager that cannot be provisioned degrades exactly as a runtime
// does: a warning naming it, and the run continues with what is on PATH.
func TestEnsureDisabledNamesTheUnpinnedManager(t *testing.T) {
	isolatedCache(t)
	dir := pnpmWorkspace(t, pnpmVersion)
	bin := fakeToolchainBin(t)
	fakeNode(t, bin, "v22.0.0")
	writeScript(t, filepath.Join(bin, "pnpm"), "#!/bin/sh\necho 12.3.0\n")

	var log bytes.Buffer
	ensureOne(t, dir, runner.TypeScript, Overrides{Disabled: true}, &log)
	if want := "warning: pnpm toolchain is 12.3.0, not the pinned 12.4.1, and toolchain.enabled is false"; !strings.Contains(log.String(), want) {
		t.Errorf("log = %q, want it to contain %q", log.String(), want)
	}
}

// A resolution is shared between every component asking for the same
// toolchain, so one component's package manager joining its environment must
// not reach the other components holding the same Node.
func TestAlongsideLeavesTheSharedRuntimeUntouched(t *testing.T) {
	runtime := &Env{PathDirs: []string{"/node/bin"}, Vars: []string{"A=1"}, Resolved: "22.0.0"}
	got := alongside(runtime, &Env{PathDirs: []string{"/pnpm/bin"}, Resolved: "12.4.1"})

	if !slices.Equal(got.PathDirs, []string{"/node/bin", "/pnpm/bin"}) || !slices.Equal(got.Vars, []string{"A=1"}) {
		t.Fatalf("alongside = %+v, want the runtime's directories and variables followed by the manager's", got)
	}
	if got.Resolved != "22.0.0" {
		t.Errorf("Resolved = %q, want the runtime's", got.Resolved)
	}
	if !slices.Equal(runtime.PathDirs, []string{"/node/bin"}) {
		t.Errorf("the shared runtime environment was modified: %+v", runtime)
	}

	if alongside(runtime, nil) != runtime || alongside(runtime, &Env{Resolved: "12.4.1"}) != runtime {
		t.Error("a manager contributing nothing should leave the runtime's environment as it is")
	}
	if got := alongside(nil, &Env{PathDirs: []string{"/pnpm/bin"}}); got.Resolved != "" || !slices.Equal(got.PathDirs, []string{"/pnpm/bin"}) {
		t.Errorf("alongside(nil, manager) = %+v, want the manager's directories and no resolved runtime", got)
	}
}

func TestPinnedKeepsThePreRelease(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"8.15.4", "v8.15.4"},
		{"9.0.0-rc.1\n", "v9.0.0-rc.1"},
		{"v1.22.19", "v1.22.19"},
		{"8.15", ""},
		{"", ""},
	} {
		if got := pinned(tc.in); got != tc.want {
			t.Errorf("pinned(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A provisioned yarn is run through the component's own resolved Node, which
// exists only on the directories that runtime contributed.
func TestConfirmRunsTheManagerUnderTheComponentsRuntime(t *testing.T) {
	fakeToolchainBin(t)
	nodeDir := t.TempDir()
	fakeNode(t, nodeDir, "v22.0.0")
	staging := t.TempDir()
	if err := unpackManager(yarnTarball(t, "4.1.0"), staging, "yarn"); err != nil {
		t.Fatal(err)
	}
	req := Requirement{Lang: runner.TypeScript, Manager: "yarn", Version: "v4.1.0", Raw: "4.1.0", Unit: Unit{Dir: "."}}
	st := &step{pathDirs: []string{filepath.Join(staging, "bin")}}

	got, err := confirm(context.Background(), t.TempDir(), req, st, &Env{PathDirs: []string{nodeDir}})
	if err != nil || got != "4.1.0" {
		t.Fatalf("confirm = (%q, %v), want the installed yarn's own version", got, err)
	}
	if _, err := confirm(context.Background(), t.TempDir(), req, st, nil); err == nil {
		t.Fatal("confirm found a node to run yarn with although no runtime provided one")
	}
}

// provisionPnpm provisions pnpmVersion pinned under hash from whatever the
// fake registry serves.
func provisionPnpm(t *testing.T, hash string) (*step, error) {
	t.Helper()
	req := Requirement{Lang: runner.TypeScript, Manager: "pnpm", Version: "v" + pnpmVersion, Raw: pnpmVersion,
		Source: "package.json (packageManager)", Hash: hash}
	return provisionPackageManager(context.Background(), req, "", false)
}

// pnpm is provisioned as its native binary, taken from the exe package its
// verified release names, and put on PATH as it is: no wrapper, and nothing
// asked of node.
func TestEnsureProvisionsPnpmAsItsNativeBinary(t *testing.T) {
	isolatedCache(t)
	reg := newFakeRegistry(t)
	release := pnpmTarball(t, pnpmVersion)
	publishPnpm(t, reg, release, nil)
	hash := sha512Pin(release)
	dir := t.TempDir()
	write(t, dir, "package.json", `{"name":"x","packageManager":"pnpm@`+pnpmVersion+`+`+hash+`"}`)
	write(t, dir, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	// Nothing on PATH at all — no node for a wrapper to run under.
	fakeToolchainBin(t)

	var log bytes.Buffer
	env := ensureOne(t, dir, runner.TypeScript, Overrides{}, &log)
	declared, err := parseDeclaredHash(hash)
	if err != nil {
		t.Fatal(err)
	}
	cache, err := cacheRoot(managerCacheKey("pnpm", pnpmVersion, declared))
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(cache, "bin")
	if env == nil || !slices.Contains(env.PathDirs, binDir) {
		t.Fatalf("Ensure = %+v, want %q on PATH; log was %q", env, binDir, log.String())
	}
	pnpm := filepath.Join(binDir, "pnpm")
	body, err := os.ReadFile(pnpm) // #nosec G304 -- a file this test's provisioning just installed
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != pnpmExeScript(pnpmVersion) {
		t.Errorf("bin/pnpm = %q, want the exe package's own binary", body)
	}
	// The fixture archive carries it 0644; the install still has to run.
	if info, err := os.Stat(pnpm); err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("bin/pnpm is not executable: %v, %v", info, err)
	}
	out, err := exec.Command(pnpm, "--version").Output() // #nosec G204 -- a binary this test's provisioning just installed
	if err != nil || strings.TrimSpace(string(out)) != pnpmVersion {
		t.Fatalf("pnpm --version with no node on PATH = (%q, %v), want %s", out, err, pnpmVersion)
	}
	if got := log.String(); !strings.Contains(got, "installed pnpm "+pnpmVersion) || strings.Contains(got, "could not confirm") {
		t.Errorf("log should name the install and confirm its version, got %q", got)
	}
}

// The exe package is trusted only because the release naming it matched the
// repository's pin, so a release that does not match is refused before the
// exe is ever asked for.
func TestProvisionPnpmRefusesADeclaredHashMismatchBeforeFetchingTheExe(t *testing.T) {
	isolatedCache(t)
	reg := newFakeRegistry(t)
	publishPnpm(t, reg, pnpmTarball(t, pnpmVersion), nil)

	_, err := provisionPnpm(t, "sha512."+strings.Repeat("0", 128))
	if err == nil || !strings.Contains(err.Error(), "packageManager pins") {
		t.Fatalf("provisionPackageManager = %v, want the declared hash's mismatch", err)
	}
	if n := reg.requested("pnpm"); n != 2 {
		t.Errorf("the release was asked for %d times, want its document and its tarball", n)
	}
	if n := reg.requested(hostExe(t)); n != 0 {
		t.Errorf("the exe package was asked for %d times after the release failed its pin, want none", n)
	}
}

// Every refusal along the chain from the release to its exe, each with the
// reason it gives.
func TestProvisionPnpmRefusesAnUnverifiableExe(t *testing.T) {
	for _, tc := range []struct {
		name    string
		release func(t *testing.T) []byte
		exe     func(t *testing.T, version string) []byte
		editExe func(*registryDist)
		want    string
	}{
		{"an exe tarball not matching its integrity",
			func(t *testing.T) []byte { return pnpmTarball(t, pnpmVersion) },
			nil,
			func(d *registryDist) {
				d.Integrity = "sha512-" + base64.StdEncoding.EncodeToString(make([]byte, 64))
			},
			"checksum mismatch"},
		{"an exe with only a sha1 shasum",
			func(t *testing.T) []byte { return pnpmTarball(t, pnpmVersion) },
			nil,
			func(d *registryDist) { d.Integrity = "" },
			"publishes no sha512 integrity"},
		{"an exe whose integrity names sha1 alone",
			func(t *testing.T) []byte { return pnpmTarball(t, pnpmVersion) },
			nil,
			func(d *registryDist) { d.Integrity = "sha1-abc" },
			"publishes no sha512 integrity"},
		{"an exe tarball outside the registry",
			func(t *testing.T) []byte { return pnpmTarball(t, pnpmVersion) },
			nil,
			func(d *registryDist) { d.Tarball = "https://example.com/pnpm.tgz" },
			"outside"},
		{"a release naming no exe for this platform",
			func(t *testing.T) []byte {
				deps := exeDeps(pnpmVersion)
				delete(deps, hostExe(t))
				return pnpmTarballDepending(t, pnpmVersion, deps)
			},
			nil, nil, "names no @pnpm/exe."},
		{"a release pinning its exe at another version",
			func(t *testing.T) []byte {
				deps := exeDeps(pnpmVersion)
				deps[hostExe(t)] = "12.4.0"
				return pnpmTarballDepending(t, pnpmVersion, deps)
			},
			nil, nil, "not the same version"},
		{"a release naming its exe by a range",
			func(t *testing.T) []byte {
				deps := exeDeps(pnpmVersion)
				deps[hostExe(t)] = "^" + pnpmVersion
				return pnpmTarballDepending(t, pnpmVersion, deps)
			},
			nil, nil, "not an exact version"},
		{"a release naming its exe with a v-prefixed version",
			func(t *testing.T) []byte {
				deps := exeDeps(pnpmVersion)
				deps[hostExe(t)] = "v" + pnpmVersion
				return pnpmTarballDepending(t, pnpmVersion, deps)
			},
			nil, nil, "not the same version"},
		{"an exe tarball carrying no pnpm binary",
			func(t *testing.T) []byte { return pnpmTarball(t, pnpmVersion) },
			pnpmExeTarballMissingBinary,
			nil, "carries no pnpm binary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedCache(t)
			reg := newFakeRegistry(t)
			release := tc.release(t)
			exeTarball := pnpmExeTarball
			if tc.exe != nil {
				exeTarball = tc.exe
			}
			reg.publish(t, "pnpm", pnpmVersion, release, nil)
			reg.publish(t, hostExe(t), pnpmVersion, exeTarball(t, pnpmVersion), tc.editExe)

			st, err := provisionPnpm(t, sha512Pin(release))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("provisionPackageManager = (%+v, %v), want a refusal containing %q", st, err, tc.want)
			}
		})
	}
}

// Every point downloadPnpm reads or unpacks something can itself fail — a
// document or tarball the registry does not serve, bytes that are not a
// valid archive, an unpacked release missing the manifest pinnedExe reads —
// and each one is reported as a refusal, not left to panic or hang.
func TestDownloadPnpmRefusesAFetchOrUnpackFailure(t *testing.T) {
	garbage := []byte("not a tar.gz")

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, reg *fakeRegistry) []byte // publishes what the case needs; returns the bytes provisionPnpm pins against
		want  string
	}{
		{"the pnpm release itself is not published",
			func(t *testing.T, reg *fakeRegistry) []byte {
				return pnpmTarball(t, pnpmVersion) // never given to reg.publish
			},
			"404 Not Found"},
		{"the pnpm document is not valid JSON",
			func(t *testing.T, reg *fakeRegistry) []byte {
				release := pnpmTarball(t, pnpmVersion)
				reg.publish(t, "pnpm", pnpmVersion, release, nil)
				reg.mu.Lock()
				reg.files["/pnpm/"+pnpmVersion] = []byte("{not json")
				reg.mu.Unlock()
				return release
			},
			"pnpm@" + pnpmVersion},
		{"the pnpm document names a tarball nothing serves",
			func(t *testing.T, reg *fakeRegistry) []byte {
				release := pnpmTarball(t, pnpmVersion)
				reg.publish(t, "pnpm", pnpmVersion, release, func(d *registryDist) {
					d.Tarball = reg.srv.URL + "/pnpm/-/does-not-exist.tgz"
				})
				return release
			},
			"404 Not Found"},
		{"the pnpm tarball is not a valid archive",
			func(t *testing.T, reg *fakeRegistry) []byte {
				reg.publish(t, "pnpm", pnpmVersion, garbage, nil)
				return garbage
			},
			"gzip"},
		{"the pnpm tarball carries no package.json",
			func(t *testing.T, reg *fakeRegistry) []byte {
				release := tarball(t, "package", map[string]string{"pnpm": "placeholder\n"})
				reg.publish(t, "pnpm", pnpmVersion, release, nil)
				return release
			},
			"package.json"},
		{"the pnpm tarball's package.json is not valid JSON",
			func(t *testing.T, reg *fakeRegistry) []byte {
				release := tarball(t, "package", map[string]string{
					"package.json": "{not json",
					"pnpm":         "placeholder\n",
				})
				reg.publish(t, "pnpm", pnpmVersion, release, nil)
				return release
			},
			"pnpm@" + pnpmVersion + "'s package.json"},
		{"the exe tarball is not a valid archive",
			func(t *testing.T, reg *fakeRegistry) []byte {
				release := pnpmTarball(t, pnpmVersion)
				reg.publish(t, "pnpm", pnpmVersion, release, nil)
				reg.publish(t, hostExe(t), pnpmVersion, garbage, nil)
				return release
			},
			"gzip"},
		{"the exe document names a tarball nothing serves",
			func(t *testing.T, reg *fakeRegistry) []byte {
				release := pnpmTarball(t, pnpmVersion)
				reg.publish(t, "pnpm", pnpmVersion, release, nil)
				reg.publish(t, hostExe(t), pnpmVersion, pnpmExeTarball(t, pnpmVersion), func(d *registryDist) {
					d.Tarball = reg.srv.URL + "/exe/-/does-not-exist.tgz"
				})
				return release
			},
			"404 Not Found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedCache(t)
			reg := newFakeRegistry(t)
			release := tc.setup(t, reg)

			st, err := provisionPnpm(t, sha512Pin(release))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("provisionPackageManager = (%+v, %v), want a refusal containing %q", st, err, tc.want)
			}
		})
	}
}

// A refusal is the provisioning warning every other toolchain gives, the
// reason included, and the run carries on.
func TestEnsureWarnsWhenThePnpmExeIsRefused(t *testing.T) {
	isolatedCache(t)
	reg := newFakeRegistry(t)
	release := pnpmTarball(t, pnpmVersion)
	publishPnpm(t, reg, release, func(d *registryDist) { d.Integrity = "" })
	dir := pnpmWorkspace(t, pnpmVersion+"+"+sha512Pin(release))
	fakeNode(t, fakeToolchainBin(t), "v22.0.0")

	var log bytes.Buffer
	env := ensureOne(t, dir, runner.TypeScript, Overrides{}, &log)
	got := log.String()
	if !strings.Contains(got, "warning: could not provision the pnpm toolchain") ||
		!strings.Contains(got, "publishes no sha512 integrity") {
		t.Errorf("log = %q, want a provisioning warning naming the refused exe", got)
	}
	if env != nil && len(env.PathDirs) != 0 {
		t.Errorf("a refused pnpm must add nothing to PATH, got %q", env.PathDirs)
	}
}

// Only a platform an exe package is published for is provisioned, and a host
// with none is refused before anything is fetched.
func TestPnpmExePackage(t *testing.T) {
	for _, tc := range []struct{ goos, goarch, want string }{
		{"linux", "amd64", "@pnpm/exe.linux-x64"},
		{"linux", "arm64", "@pnpm/exe.linux-arm64"},
		{"darwin", "amd64", "@pnpm/exe.darwin-x64"},
		{"darwin", "arm64", "@pnpm/exe.darwin-arm64"},
		{"windows", "amd64", ""},
		{"linux", "386", ""},
		{"freebsd", "arm64", ""},
	} {
		got, err := pnpmExePackage(tc.goos, tc.goarch)
		if got != tc.want || (err != nil) != (tc.want == "") {
			t.Errorf("pnpmExePackage(%s, %s) = (%q, %v), want %q", tc.goos, tc.goarch, got, err, tc.want)
		}
	}
}

func TestDownloadPnpmRefusesAnUnsupportedPlatformBeforeFetching(t *testing.T) {
	reg := newFakeRegistry(t)
	release := pnpmTarball(t, pnpmVersion)
	reg.publish(t, "pnpm", pnpmVersion, release, nil)

	err := downloadPnpm(context.Background(), pnpmVersion, declaredHash{}, "windows", "amd64", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "windows/amd64") {
		t.Fatalf("downloadPnpm = %v, want a refusal naming the platform", err)
	}
	if n := reg.requested("pnpm"); n != 0 {
		t.Errorf("the registry was asked %d times for a platform with no exe package, want none", n)
	}
}

// yarn keeps its node-run wrapper when fetched from the registry, verified the
// same way as ever.
func TestDownloadPackageManagerUnpacksYarnBehindAWrapper(t *testing.T) {
	reg := newFakeRegistry(t)
	release := yarnTarball(t, "4.1.0")
	reg.publish(t, "@yarnpkg/cli-dist", "4.1.0", release, nil)
	// The yarn document is fetched with its scope's slash as written.
	reg.mu.Lock()
	reg.files["/@yarnpkg/cli-dist/4.1.0"] = reg.files["/@yarnpkg%2Fcli-dist/4.1.0"]
	reg.mu.Unlock()
	declared, err := parseDeclaredHash(sha512Pin(release))
	if err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	if err := downloadPackageManager(context.Background(), "yarn", "4.1.0", declared, staging); err != nil {
		t.Fatalf("downloadPackageManager: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(staging, "bin", "yarn")) // #nosec G304 -- a wrapper this test's download just wrote
	if err != nil || !strings.Contains(string(body), "exec node") {
		t.Fatalf("bin/yarn = (%q, %v), want the node wrapper", body, err)
	}
	if err := downloadPackageManager(context.Background(), "yarn", "4.1.0", declaredHash{algo: "sha512", sum: make([]byte, 64)}, t.TempDir()); err == nil {
		t.Fatal("a yarn release not matching its pin was installed")
	}
}
