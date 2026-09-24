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
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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

// pnpmTarball is a pnpm release whose entry point, run by the fake node below,
// reports version.
func pnpmTarball(t *testing.T, version string) []byte {
	t.Helper()
	return tarball(t, "package", map[string]string{
		"package.json": `{"name":"pnpm","bin":{"pnpm":"bin/pnpm.cjs","pnpx":"bin/pnpx.cjs"}}`,
		"bin/pnpm.cjs": "#!/usr/bin/env node\necho " + version + "\n",
		"bin/pnpx.cjs": "#!/usr/bin/env node\n",
	})
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

// cachedManager unpacks a release into the cache directory provisioning would
// download it into, so installOnce finds it finished and fetches nothing.
func cachedManager(t *testing.T, manager, version string, data []byte) string {
	t.Helper()
	dir, err := cacheRoot(manager + "-" + version)
	if err != nil {
		t.Fatal(err)
	}
	if err := unpackManager(data, dir, manager); err != nil {
		t.Fatalf("unpackManager: %v", err)
	}
	return dir
}

// The wrapper is the whole of how a provisioned package manager becomes a
// command: named for it, executable, and running the package's entry point
// through whichever node the PATH offers.
func TestUnpackManagerWritesACommandThatRunsTheEntryUnderNode(t *testing.T) {
	bin := fakeToolchainBin(t)
	fakeNode(t, bin, "v22.0.0")
	staging := t.TempDir()

	if err := unpackManager(pnpmTarball(t, "8.15.4"), staging, "pnpm"); err != nil {
		t.Fatalf("unpackManager: %v", err)
	}
	out, err := exec.Command(filepath.Join(staging, "bin", "pnpm"), "--version").Output() // #nosec G204 -- a wrapper this test just wrote
	if err != nil {
		t.Fatalf("running the wrapper: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "8.15.4" {
		t.Fatalf("pnpm --version = %q, want the entry point's own answer", got)
	}
}

// installOnce unpacks into a staging directory and renames it into place, so a
// wrapper that remembered where it was written would point at a directory that
// no longer exists.
func TestTheWrapperSurvivesItsDirectoryBeingMoved(t *testing.T) {
	bin := fakeToolchainBin(t)
	fakeNode(t, bin, "v22.0.0")
	staging := filepath.Join(t.TempDir(), "staging")

	if err := unpackManager(pnpmTarball(t, "8.15.4"), staging, "pnpm"); err != nil {
		t.Fatalf("unpackManager: %v", err)
	}
	moved := filepath.Join(t.TempDir(), "pnpm-8.15.4")
	if err := os.Rename(staging, moved); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(filepath.Join(moved, "bin", "pnpm"), "--version").Output() // #nosec G204 -- a wrapper this test just wrote
	if err != nil || strings.TrimSpace(string(out)) != "8.15.4" {
		t.Fatalf("the moved wrapper answered (%q, %v), want 8.15.4", out, err)
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
	none := managerCacheKey("pnpm", "8.15.4", declaredHash{})
	if none != "pnpm-8.15.4" {
		t.Errorf("key without a declared hash = %q, want pnpm-8.15.4", none)
	}
	keyA, keyB := managerCacheKey("pnpm", "8.15.4", a), managerCacheKey("pnpm", "8.15.4", b)
	if keyA == none || keyB == none || keyA == keyB {
		t.Errorf("keys = none %q, a %q, b %q; want three distinct directories", none, keyA, keyB)
	}
	if strings.ContainsAny(keyA, `/\`) {
		t.Errorf("key %q is not a single path element", keyA)
	}
}

// A declared hash lydite cannot check stops provisioning before anything is
// fetched or taken from the cache.
func TestProvisionRefusesAnUncheckableHash(t *testing.T) {
	isolatedCache(t)
	cachedManager(t, "pnpm", "8.15.4", pnpmTarball(t, "8.15.4"))
	req := Requirement{Lang: runner.TypeScript, Manager: "pnpm", Version: "v8.15.4", Raw: "8.15.4",
		Source: "package.json (packageManager)", Hash: "md5." + strings.Repeat("0", 32)}
	if st, err := provisionPackageManager(context.Background(), req, "", false); err == nil {
		t.Fatalf("provisionPackageManager = %+v, want a refusal of the md5 hash", st)
	}
}

func TestRegistryPackage(t *testing.T) {
	for _, tc := range []struct{ manager, version, want string }{
		{"pnpm", "8.15.4", "pnpm"},
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
	dir := pnpmWorkspace(t, "8.15.4")
	bin := fakeToolchainBin(t)
	fakeNode(t, bin, "v22.0.0")
	cache := cachedManager(t, "pnpm", "8.15.4", pnpmTarball(t, "8.15.4"))
	// An ambient pnpm newer than the pin: a floor would accept it.
	writeScript(t, filepath.Join(bin, "pnpm"), "#!/bin/sh\necho 8.16.0\n")

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
	if !strings.Contains(out, "installed pnpm 8.15.4") || !strings.Contains(out, "ambient 8.16.0 is not the pinned release") {
		t.Errorf("log should name the pinned install and why the ambient pnpm was passed over, got %q", out)
	}
	if strings.Contains(out, "could not confirm") {
		t.Errorf("the installed pnpm should confirm its own version under the component's Node, got %q", out)
	}
}

// A pin is satisfied by that release alone, so the one ambient answer that
// installs nothing is the exact version.
func TestEnsureUsesAnAmbientManagerOnlyAtTheExactPin(t *testing.T) {
	isolatedCache(t)
	dir := pnpmWorkspace(t, "8.15.4")
	bin := fakeToolchainBin(t)
	fakeNode(t, bin, "v22.0.0")
	writeScript(t, filepath.Join(bin, "pnpm"), "#!/bin/sh\necho 8.15.4\n")

	var log bytes.Buffer
	env := ensureOne(t, dir, runner.TypeScript, Overrides{}, &log)
	if env == nil || len(env.PathDirs) != 0 {
		t.Fatalf("an ambient pnpm at the pin must add nothing to PATH, got %+v; log was %q", env, log.String())
	}
	if !strings.Contains(log.String(), "using ambient pnpm 8.15.4 (matches 8.15.4 pinned in package.json (packageManager))") {
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
	write(t, root, "a/package.json", `{"name":"a","packageManager":"pnpm@8.15.4+`+hashA+`"}`)
	write(t, root, "a/pnpm-lock.yaml", "lockfileVersion: '6.0'\n")
	write(t, root, "b/package.json", `{"name":"b","packageManager":"pnpm@8.15.4+`+hashB+`"}`)
	write(t, root, "b/pnpm-lock.yaml", "lockfileVersion: '6.0'\n")
	fakeNode(t, fakeToolchainBin(t), "v22.0.0")

	declaredA, err := parseDeclaredHash(hashA)
	if err != nil {
		t.Fatal(err)
	}
	declaredB, err := parseDeclaredHash(hashB)
	if err != nil {
		t.Fatal(err)
	}
	dirA, err := cacheRoot(managerCacheKey("pnpm", "8.15.4", declaredA))
	if err != nil {
		t.Fatal(err)
	}
	if err := unpackManager(pnpmTarball(t, "8.15.4"), dirA, "pnpm"); err != nil {
		t.Fatal(err)
	}
	dirB, err := cacheRoot(managerCacheKey("pnpm", "8.15.4", declaredB))
	if err != nil {
		t.Fatal(err)
	}
	if err := unpackManager(pnpmTarball(t, "8.15.4"), dirB, "pnpm"); err != nil {
		t.Fatal(err)
	}

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
	dir := pnpmWorkspace(t, "8.15.4")
	bin := fakeToolchainBin(t)
	fakeNode(t, bin, "v22.0.0")
	writeScript(t, filepath.Join(bin, "pnpm"), "#!/bin/sh\necho 8.14.0\n")

	var log bytes.Buffer
	ensureOne(t, dir, runner.TypeScript, Overrides{Disabled: true}, &log)
	if want := "warning: pnpm toolchain is 8.14.0, not the pinned 8.15.4, and toolchain.enabled is false"; !strings.Contains(log.String(), want) {
		t.Errorf("log = %q, want it to contain %q", log.String(), want)
	}
}

// A resolution is shared between every component asking for the same
// toolchain, so one component's package manager joining its environment must
// not reach the other components holding the same Node.
func TestAlongsideLeavesTheSharedRuntimeUntouched(t *testing.T) {
	runtime := &Env{PathDirs: []string{"/node/bin"}, Vars: []string{"A=1"}, Resolved: "22.0.0"}
	got := alongside(runtime, &Env{PathDirs: []string{"/pnpm/bin"}, Resolved: "8.15.4"})

	if !slices.Equal(got.PathDirs, []string{"/node/bin", "/pnpm/bin"}) || !slices.Equal(got.Vars, []string{"A=1"}) {
		t.Fatalf("alongside = %+v, want the runtime's directories and variables followed by the manager's", got)
	}
	if got.Resolved != "22.0.0" {
		t.Errorf("Resolved = %q, want the runtime's", got.Resolved)
	}
	if !slices.Equal(runtime.PathDirs, []string{"/node/bin"}) {
		t.Errorf("the shared runtime environment was modified: %+v", runtime)
	}

	if alongside(runtime, nil) != runtime || alongside(runtime, &Env{Resolved: "8.15.4"}) != runtime {
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

// A provisioned package manager is run through the component's own resolved
// Node, which exists only on the directories that runtime contributed.
func TestConfirmRunsTheManagerUnderTheComponentsRuntime(t *testing.T) {
	fakeToolchainBin(t)
	nodeDir := t.TempDir()
	fakeNode(t, nodeDir, "v22.0.0")
	staging := t.TempDir()
	if err := unpackManager(pnpmTarball(t, "8.15.4"), staging, "pnpm"); err != nil {
		t.Fatal(err)
	}
	req := Requirement{Lang: runner.TypeScript, Manager: "pnpm", Version: "v8.15.4", Raw: "8.15.4", Unit: Unit{Dir: "."}}
	st := &step{pathDirs: []string{filepath.Join(staging, "bin")}}

	got, err := confirm(context.Background(), t.TempDir(), req, st, &Env{PathDirs: []string{nodeDir}})
	if err != nil || got != "8.15.4" {
		t.Fatalf("confirm = (%q, %v), want the installed pnpm's own version", got, err)
	}
	if _, err := confirm(context.Background(), t.TempDir(), req, st, nil); err == nil {
		t.Fatal("confirm found a node to run pnpm with although no runtime provided one")
	}
}
