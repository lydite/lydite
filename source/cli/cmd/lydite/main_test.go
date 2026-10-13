package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// gitTraceFile is where every git process the suite starts appends its trace2
// events. gitTraceLines reads it back when a temp directory refuses to be
// removed, to name the process that was still writing into it.
var gitTraceFile string

// TestMain stops git from doing work on its own schedule inside the repositories
// the suite creates, and records what git does in them.
//
// Auto-gc, auto-maintenance and a push's auto-gc can each leave a process
// writing into a repository (gc.pid, gc.log, packed-refs) after the test that
// made it has returned. t.TempDir then fails its cleanup with "directory not
// empty" although nothing the test asserted is wrong. The settings are appended
// to GIT_CONFIG_COUNT so any the environment already carries survive.
func TestMain(m *testing.M) {
	appendGitConfig(map[string]string{
		"gc.auto":          "0",
		"receive.autogc":   "false",
		"maintenance.auto": "false",
	})

	dir, err := os.MkdirTemp("", "lydite-git-trace-")
	if err == nil {
		gitTraceFile = dir + string(os.PathSeparator) + "trace2.json"
		_ = os.Setenv("GIT_TRACE2_EVENT", gitTraceFile)
	}

	code := m.Run()
	if err == nil {
		_ = os.RemoveAll(dir)
	}
	os.Exit(code)
}

// explainLeftoverDirs logs what was left behind and which git processes ran in
// the directories, when the test has failed. Register it before the
// directories' own t.TempDir calls: cleanups run last-in first-out, so it then
// runs after their removal, where a failed removal has already marked the test
// failed. The pointers are read at cleanup time, so they may be assigned after
// registration.
func explainLeftoverDirs(t *testing.T, dirs ...*string) {
	t.Helper()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, d := range dirs {
			if *d == "" {
				continue
			}
			entries, err := os.ReadDir(*d)
			if err != nil {
				t.Logf("%s: %v", *d, err)
				continue
			}
			for _, e := range entries {
				t.Logf("%s still holds %s", *d, e.Name())
			}
			for _, line := range gitTraceLines(*d, 40) {
				t.Logf("git trace: %s", line)
			}
		}
		if out, err := exec.Command("ps", "-eo", "pid,ppid,etime,args").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if strings.Contains(line, "git") {
					t.Logf("running: %s", line)
				}
			}
		}
	})
}

// gitTraceLines returns the last n trace events that mention dir.
func gitTraceLines(dir string, n int) []string {
	data, err := os.ReadFile(gitTraceFile)
	if err != nil {
		return nil
	}
	var hits []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, dir) {
			hits = append(hits, line)
		}
	}
	if len(hits) > n {
		hits = hits[len(hits)-n:]
	}
	return hits
}

// appendGitConfig adds key/value pairs to the process environment's
// GIT_CONFIG_COUNT/KEY_n/VALUE_n set, after any entries already present.
func appendGitConfig(settings map[string]string) {
	n, _ := strconv.Atoi(os.Getenv("GIT_CONFIG_COUNT"))
	for k, v := range settings {
		_ = os.Setenv(fmt.Sprintf("GIT_CONFIG_KEY_%d", n), k)
		_ = os.Setenv(fmt.Sprintf("GIT_CONFIG_VALUE_%d", n), v)
		n++
	}
	_ = os.Setenv("GIT_CONFIG_COUNT", strconv.Itoa(n))
}
