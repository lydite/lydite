// Temporary measurement probe for the memory bound on a mutant's run. It
// asserts nothing: every finding is logged, and the numbers are read out of a
// CI log on a real Linux runner because no Linux and no container runtime is
// available where this is written. Delete once ADR 0027 records the decision.

package mutation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// probeMarker prefixes every line so a CI log can be grepped for the probe's
// output alone.
const probeMarker = "MEMPROBE"

// probeTargetMiB is what the helper allocates and touches.
const probeTargetMiB = 256

// helperSource allocates probeTargetMiB in 16MiB chunks, touching one byte per
// 4KiB page so the resident set follows the allocation, and holds every chunk
// live so the collector cannot return it. It waits before starting so a limit
// can be applied to an already-running pid.
const helperSource = `package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

func main() {
	target, _ := strconv.Atoi(os.Args[1])
	delay, _ := strconv.Atoi(os.Args[2])
	time.Sleep(time.Duration(delay) * time.Millisecond)
	const chunk = 16 << 20
	var held [][]byte
	for done := 0; done < target; done += chunk >> 20 {
		b := make([]byte, chunk)
		for i := 0; i < len(b); i += 4096 {
			b[i] = 1
		}
		held = append(held, b)
		fmt.Fprintf(os.Stderr, "allocated %dMiB\n", done+(chunk>>20))
	}
	fmt.Fprintf(os.Stderr, "held %d chunks\n", len(held))
}
`

// probeResult is one helper execution.
type probeResult struct {
	err      error
	exitCode int
	signal   string
	maxrss   int64
	elapsed  time.Duration
	tail     string
}

func (r probeResult) String() string {
	return fmt.Sprintf("exit=%d signal=%s maxrss=%d elapsed=%s err=%v tail=%q",
		r.exitCode, r.signal, r.maxrss, r.elapsed.Round(time.Millisecond), r.err, r.tail)
}

func TestProbeMemoryBound(t *testing.T) {
	logf := func(format string, args ...any) {
		line := probeMarker + " " + fmt.Sprintf(format, args...)
		t.Log(line)
		// Written to stdout as well: `go test` without -v discards t.Log for a
		// passing test, and this probe's whole output is a passing test's.
		fmt.Println(line)
	}

	logf("goos=%s goarch=%s numcpu=%d", runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
	logf("maxrss unit: kilobytes on linux, bytes on darwin")

	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(helperSource), 0o600); err != nil {
		logf("write helper: %v", err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module memprobe\n\ngo 1.26\n"), 0o600); err != nil {
		logf("write go.mod: %v", err)
		return
	}
	helper := filepath.Join(dir, "memprobe")
	build := exec.Command("go", "build", "-o", helper, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOFLAGS=")
	if out, err := build.CombinedOutput(); err != nil {
		logf("build helper failed: %v: %s", err, out)
		return
	}

	run := func(name string, cmd *exec.Cmd, after func(*exec.Cmd)) probeResult {
		var buf strings.Builder
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		start := time.Now()
		res := probeResult{}
		if err := cmd.Start(); err != nil {
			res.err = err
			logf("%s: start failed: %v", name, err)
			return res
		}
		if after != nil {
			after(cmd)
		}
		res.err = cmd.Wait()
		res.elapsed = time.Since(start)
		if st := cmd.ProcessState; st != nil {
			res.exitCode = st.ExitCode()
			if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				res.signal = ws.Signal().String()
			}
			if ru, ok := st.SysUsage().(*syscall.Rusage); ok {
				res.maxrss = int64(ru.Maxrss)
			}
		}
		res.tail = probeTail(buf.String(), 3)
		logf("%s: %s", name, res)
		return res
	}

	baseline := run("baseline", exec.Command(helper, fmt.Sprint(probeTargetMiB), "0"), nil)
	peakBytes := baseline.maxrss
	if runtime.GOOS == "linux" {
		peakBytes *= 1024
	}
	logf("baseline peak resident: %d bytes (%dMiB) for a %dMiB live set",
		peakBytes, peakBytes>>20, probeTargetMiB)
	if peakBytes <= 0 {
		logf("no usable baseline peak; the limit probes below are against a guess")
		peakBytes = int64(probeTargetMiB) << 20
	}

	halfX := peakBytes / 2
	twoX := peakBytes * 2
	fourX := peakBytes * 4
	logf("limits under test: 0.5x=%d bytes (%dMiB) 2x=%d bytes (%dMiB) 4x=%d bytes (%dMiB)",
		halfX, halfX>>20, twoX, twoX>>20, fourX, fourX>>20)

	// ulimit -v is RLIMIT_AS, ulimit -d is RLIMIT_DATA; both in kilobytes. The
	// shell applies them to itself before exec, which is what a SysProcAttr
	// would do.
	for _, c := range []struct {
		name  string
		flag  string
		limit int64
	}{
		{"rlimit_as@0.5x", "-v", halfX},
		{"rlimit_as@2x", "-v", twoX},
		{"rlimit_as@4x", "-v", fourX},
		{"rlimit_data@0.5x", "-d", halfX},
		{"rlimit_data@2x", "-d", twoX},
		{"rlimit_data@4x", "-d", fourX},
	} {
		script := fmt.Sprintf("ulimit %s %d && exec %q %d 0", c.flag, c.limit/1024, helper, probeTargetMiB)
		run(c.name, exec.Command("/bin/sh", "-c", script), nil)
	}

	// prlimit(1) on an already-running pid is what x/sys/unix.Prlimit would do
	// after Start. The helper waits 1500ms before its first allocation.
	if _, err := exec.LookPath("prlimit"); err != nil {
		logf("prlimit(1) not on PATH: %v", err)
	} else {
		for _, c := range []struct {
			name  string
			arg   string
			limit int64
		}{
			{"prlimit_as@0.5x_after_start", "--as", halfX},
			{"prlimit_as@2x_after_start", "--as", twoX},
			{"prlimit_data@0.5x_after_start", "--data", halfX},
			{"prlimit_data@2x_after_start", "--data", twoX},
		} {
			run(c.name, exec.Command(helper, fmt.Sprint(probeTargetMiB), "1500"), func(cmd *exec.Cmd) {
				set := exec.Command("prlimit", fmt.Sprintf("--pid=%d", cmd.Process.Pid),
					fmt.Sprintf("%s=%d", c.arg, c.limit))
				out, err := set.CombinedOutput()
				logf("%s: prlimit said err=%v out=%q", c.name, err, strings.TrimSpace(string(out)))
			})
		}
	}

	// GOMEMLIMIT is a soft target for the Go heap: the collector runs harder
	// against it and nothing kills the process.
	for _, c := range []struct {
		name  string
		limit int64
	}{
		{"gomemlimit@0.5x", halfX},
		{"gomemlimit@2x", twoX},
		{"gomemlimit@4x", fourX},
	} {
		// Bounded: a limit below the live set makes the collector run without
		// ever reclaiming anything, which is slow rather than fatal.
		ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
		cmd := exec.CommandContext(ctx, helper, fmt.Sprint(probeTargetMiB), "0")
		cmd.Env = append(os.Environ(), fmt.Sprintf("GOMEMLIMIT=%d", c.limit))
		run(c.name, cmd, nil)
		cancel()
	}

	// cgroup v2: is a delegated user scope available at all on this runner?
	logf("cgroup: /proc/self/cgroup=%q", probeRead("/proc/self/cgroup"))
	logf("cgroup: /sys/fs/cgroup/cgroup.controllers=%q", probeRead("/sys/fs/cgroup/cgroup.controllers"))
	logf("cgroup: /sys/fs/cgroup writable=%v", probeWritable("/sys/fs/cgroup"))
	// Delegation is what decides whether lydite could make a scope of its own:
	// creating a child cgroup under the one this process is already in, and
	// the memory controller being enabled there.
	if own := probeOwnCgroup(); own != "" {
		logf("cgroup: own path=%q", own)
		logf("cgroup: own subtree_control=%q", probeRead(filepath.Join(own, "cgroup.subtree_control")))
		logf("cgroup: own controllers=%q", probeRead(filepath.Join(own, "cgroup.controllers")))
		child := filepath.Join(own, "memprobe")
		err := os.Mkdir(child, 0o755)
		logf("cgroup: mkdir %q err=%v", child, err)
		if err == nil {
			logf("cgroup: memory.max write err=%v",
				os.WriteFile(filepath.Join(child, "memory.max"), []byte(fmt.Sprint(twoX)), 0o600))
			logf("cgroup: rmdir err=%v", os.Remove(child))
		}
	}
	for _, probe := range [][]string{
		{"stat", "-f", "-c", "%T", "/sys/fs/cgroup"},
		{"systemctl", "--user", "status"},
		{"systemd-run", "--user", "--scope", "-p", fmt.Sprintf("MemoryMax=%d", twoX), "--", helper, fmt.Sprint(probeTargetMiB), "0"},
		{"systemd-run", "--scope", "-p", fmt.Sprintf("MemoryMax=%d", twoX), "--", helper, fmt.Sprint(probeTargetMiB), "0"},
	} {
		if _, err := exec.LookPath(probe[0]); err != nil {
			logf("%s: not on PATH: %v", probe[0], err)
			continue
		}
		// Bounded: systemd-run against the system manager can block on an
		// authentication agent that will never answer.
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		cmd := exec.CommandContext(ctx, probe[0], probe[1:]...)
		out, err := cmd.CombinedOutput()
		cancel()
		logf("%s %v: err=%v out=%q", probe[0], probe[1:], err, probeTail(string(out), 4))
	}

	// Deliberate, and the only channel this probe has: `go test` without -v
	// discards a passing test's log and its direct writes to stdout alike, so
	// the numbers above reach the CI log only from a test that fails.
	t.Fatal(probeMarker + " end of probe — this failure is how the measurements above are printed")
}

// probeOwnCgroup is the v2 path of this process, as a filesystem path.
func probeOwnCgroup() string {
	for _, line := range strings.Split(probeRead("/proc/self/cgroup"), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "0::"); ok {
			return filepath.Join("/sys/fs/cgroup", rest)
		}
	}
	return ""
}

func probeRead(path string) string {
	b, err := os.ReadFile(path) //nolint:gosec // probe reads a fixed procfs path
	if err != nil {
		return "unreadable: " + err.Error()
	}
	return strings.TrimSpace(string(b))
}

// probeWritable reports whether a directory accepts a new entry.
func probeWritable(dir string) bool {
	f, err := os.CreateTemp(dir, "memprobe")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

func probeTail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
