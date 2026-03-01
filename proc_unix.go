//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// setProcGroup sets up process group isolation so child processes
// can be killed together on timeout.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcGroup kills an entire process group by PID.
func killProcGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
