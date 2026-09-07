package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeRelease serves the GitHub release URLs selfUpdate and
// latestReleaseVersion depend on: the releases/latest redirect, a raw lydite
// binary, and checksums.txt. checksum overrides the real digest when non-empty.
func fakeRelease(t *testing.T, ver string, binary []byte, checksum string) *httptest.Server {
	t.Helper()
	asset := fmt.Sprintf("lydite_%s_%s_%s", ver, runtime.GOOS, runtime.GOARCH)
	if checksum == "" {
		sum := sha256.Sum256(binary)
		checksum = hex.EncodeToString(sum[:])
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases/tag/v"+ver, http.StatusFound)
	})
	mux.HandleFunc("/releases/download/v"+ver+"/"+asset, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(binary)
	})
	mux.HandleFunc("/releases/download/v"+ver+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "%s  %s\n", checksum, asset)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	orig := releaseBaseURL
	releaseBaseURL = srv.URL
	t.Cleanup(func() { releaseBaseURL = orig })
	return srv
}

// TestUpdateAvailable guards the version-ordering of the update nudge and the
// `lydite update` guard: an update is offered only when the resolved latest is
// STRICTLY newer than the running version. Regression for the stale-cache bug
// where a cached older tag nudges a newer build to "update".
func TestUpdateAvailable(t *testing.T) {
	cases := []struct {
		name        string
		latest, cur string
		want        bool
	}{
		{"newer patch", "1.0.1", "1.0.0", true},
		{"newer minor", "1.1.0", "1.0.9", true},
		{"newer major", "2.0.0", "1.9.9", true},
		{"same version", "1.0.1", "1.0.1", false},
		{"older patch (stale cache)", "1.0.0", "1.0.1", false},
		{"older major", "0.9.9", "1.0.0", false},
		{"empty latest (check failed/never ran)", "", "1.0.1", false},
		{"unparseable current (non-release build)", "1.0.1", "garbage", false},
		{"unparseable latest", "garbage", "1.0.1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := updateAvailable(tc.latest, tc.cur); got != tc.want {
				t.Errorf("updateAvailable(%q, %q) = %v, want %v", tc.latest, tc.cur, got, tc.want)
			}
		})
	}
}

func TestLatestReleaseVersion(t *testing.T) {
	fakeRelease(t, "1.9.0", []byte("bin"), "")

	got, err := latestReleaseVersion(context.Background(), http.DefaultClient)
	if err != nil {
		t.Fatalf("latestReleaseVersion: %v", err)
	}
	if got != "1.9.0" {
		t.Fatalf("got %q, want %q", got, "1.9.0")
	}
}

func TestLatestReleaseVersionNoRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	orig := releaseBaseURL
	releaseBaseURL = srv.URL
	t.Cleanup(func() { releaseBaseURL = orig })

	if _, err := latestReleaseVersion(context.Background(), http.DefaultClient); err == nil {
		t.Fatal("expected error when releases/latest does not redirect")
	}
}

func TestSelfUpdateReplacesBinary(t *testing.T) {
	newBinary := []byte("new lydite binary")
	fakeRelease(t, "1.9.0", newBinary, "")

	exe := filepath.Join(t.TempDir(), "lydite")
	if err := os.WriteFile(exe, []byte("old lydite binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := selfUpdate(context.Background(), "1.9.0", exe); err != nil {
		t.Fatalf("selfUpdate: %v", err)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newBinary) {
		t.Fatalf("binary not replaced: got %q", got)
	}
	fi, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("binary mode = %v, want 0755", fi.Mode().Perm())
	}
	// The staging temp file must not be left behind.
	entries, err := os.ReadDir(filepath.Dir(exe))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".lydite-update-") {
			t.Fatalf("staging file left behind: %s", e.Name())
		}
	}
}

func TestSelfUpdateChecksumMismatch(t *testing.T) {
	fakeRelease(t, "1.9.0", []byte("new lydite binary"), strings.Repeat("0", 64))

	exe := filepath.Join(t.TempDir(), "lydite")
	original := []byte("old lydite binary")
	if err := os.WriteFile(exe, original, 0o755); err != nil {
		t.Fatal(err)
	}

	err := selfUpdate(context.Background(), "1.9.0", exe)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch error, got %v", err)
	}
	got, readErr := os.ReadFile(exe)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Fatal("binary was replaced despite checksum mismatch")
	}
}

func TestUpdateCmdRefusesDevBuild(t *testing.T) {
	// Test binaries always run with the default version; fail loudly if that
	// assumption ever breaks, since this is the refusal path's only coverage.
	if version != "dev" {
		t.Fatalf("test binary has version %q, expected dev", version)
	}
	cmd := newUpdateCmd()
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "built from source") {
		t.Fatalf("expected built-from-source refusal, got %v", err)
	}
}

// TestUpdateCmdRejectsUnrecognizedVersion: a non-"dev" build whose version is not
// valid semver (a hand-built binary) must fail `lydite update` with a clear
// message rather than silently reporting "already the latest release".
func TestUpdateCmdRejectsUnrecognizedVersion(t *testing.T) {
	orig := version
	version = "not-a-release"
	t.Cleanup(func() { version = orig })

	// A release must resolve so control reaches the version-validity check.
	fakeRelease(t, "9.9.9", []byte("bin"), "")

	cmd := newUpdateCmd()
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "not a recognized release version") {
		t.Fatalf("expected unrecognized-version error, got %v", err)
	}
}

// The nudge runs only where somebody is watching and could act on it. A
// from-source build has no release to compare against, CI has nobody reading
// stderr, and a redirected stream is being parsed by something that did not ask
// for a notice.
func TestTheNudgeOnlyRunsWhereSomebodyWouldSeeIt(t *testing.T) {
	t.Parallel()
	// /dev/null is a character device, which is what the terminal check asks
	// about — so it stands in for an attached terminal and lets each of the
	// other two guards be asserted on its own. Without a stream that passes
	// that check, every case here is false for the same reason and the version
	// and CI guards are never exercised at all.
	tty, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tty.Close() }()
	// A pipe is what a redirected stderr is, and it is never a terminal.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()

	// The one case that is nudged, which is what makes the three refusals
	// below mean anything.
	if !nudgeWanted("1.0.0", "", tty) {
		t.Fatal("a released build on an attached terminal outside CI was not nudged")
	}
	if nudgeWanted("dev", "", tty) {
		t.Error("a from-source build was nudged, and it has no release to compare against")
	}
	if nudgeWanted("1.0.0", "true", tty) {
		t.Error("a CI run was nudged, and nobody is reading its stderr")
	}
	if nudgeWanted("1.0.0", "", w) {
		t.Error("a redirected stderr was nudged, and something is parsing it")
	}
}

// A cache that is absent, unreadable or malformed is a check that has not
// happened, which is what the zero CheckedAt already means — so it re-checks
// rather than failing or reporting a version nobody has.
func TestAnUnreadableUpdateCheckIsACheckThatHasNotHappened(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if got := readUpdateCheck(filepath.Join(dir, "absent.json")); got != (updateCheckState{}) {
		t.Errorf("an absent cache = %+v, want the zero state", got)
	}
	path := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readUpdateCheck(path); got != (updateCheckState{}) {
		t.Errorf("a malformed cache = %+v, want the zero state", got)
	}
}

// A check inside the TTL asks nothing, and one past it records the attempt even
// when it fails — otherwise every invocation past the TTL re-pays the network
// timeout. A failed check keeps the version it already knew, since failing to
// ask is no evidence that the release has gone.
func TestARefreshedCheckRecordsTheAttemptEvenWhenItFails(t *testing.T) {
	t.Parallel()
	// A client pointed at a closed listener, so the request fails at once.
	failing := &http.Client{Transport: &http.Transport{}, Timeout: time.Millisecond}

	fresh := updateCheckState{CheckedAt: time.Now(), Latest: "9.9.9"}
	if got := refreshedUpdateCheck(fresh, failing); got != fresh {
		t.Errorf("a check inside the TTL = %+v, want it left alone", got)
	}

	stale := updateCheckState{CheckedAt: time.Now().Add(-2 * updateCheckTTL), Latest: "1.2.3"}
	got := refreshedUpdateCheck(stale, failing)
	if !got.CheckedAt.After(stale.CheckedAt) {
		t.Error("a failed check did not record the attempt, so every run past the TTL re-pays the timeout")
	}
	if got.Latest != "1.2.3" {
		t.Errorf("latest = %q, want the version it already knew kept", got.Latest)
	}
}

// A check that the remote answers records what it found. It is the case that
// says the deadline the request is given is long enough to make one at all —
// a check given no time fails before it is sent, and every assertion about a
// failing check passes on that too.
func TestACheckTheRemoteAnswersRecordsWhatItFound(t *testing.T) { //nolint:paralleltest // fakeRelease swaps the package's base URL
	fakeRelease(t, "3.4.5", []byte("binary"), "")
	stale := updateCheckState{CheckedAt: time.Now().Add(-2 * updateCheckTTL), Latest: "1.2.3"}
	got := refreshedUpdateCheck(stale, http.DefaultClient)
	if got.Latest != "3.4.5" {
		t.Errorf("latest = %q, want the version the remote answered with", got.Latest)
	}
}

// The cache is an optimisation, so a run that cannot write it re-checks next
// time rather than failing. What it does write, it reads back.
func TestTheUpdateCheckRoundTrips(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "update-check.json")
	want := updateCheckState{CheckedAt: time.Now().UTC().Truncate(time.Second), Latest: "2.0.0"}
	writeUpdateCheck(path, want)
	if got := readUpdateCheck(path); !got.CheckedAt.Equal(want.CheckedAt) || got.Latest != want.Latest {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	// A path that cannot be created is silent.
	writeUpdateCheck(filepath.Join(path, "under-a-file.json"), want)
}
