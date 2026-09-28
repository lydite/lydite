//go:build !unix

package executil

import (
	"os"
	"os/exec"
)

// ownGroup leaves cmd as os/exec builds it: a process group is a unix notion,
// and a platform without one runs the command exactly as every other caller
// here does.
func ownGroup(*exec.Cmd) {}

// killGroup has no group to kill off unix.
func killGroup(int) error { return os.ErrProcessDone }
