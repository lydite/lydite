//go:build unix

package executil

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// ownGroup starts cmd as the leader of a process group of its own, and makes a
// cancelled context kill that whole group rather than the leader alone.
//
// A suite is rarely one process: `npx vitest` starts node, which starts
// workers, and every one of them inherits the stdout and stderr pipes. Killing
// only the process lydite started reparents the rest to init still holding
// those pipes open, and Wait then blocks on the output copy forever. SIGKILL
// to the negative pgid reaches every member at once, and WaitDelay bounds the
// wait for anything that left the group before the kill.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return killGroup(cmd.Process.Pid)
	}
	cmd.WaitDelay = groupWaitDelay
}

// killGroup sends SIGKILL to every process in the group pgid leads. A group
// with no members left answers os.ErrProcessDone, which os/exec reads as a
// cancellation that found nothing to kill rather than as a failure of its own.
func killGroup(pgid int) error {
	err := syscall.Kill(-pgid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
