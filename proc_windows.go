//go:build windows

package main

import "os/exec"

// setProcGroup is a no-op on Windows (no Setpgid support).
func setProcGroup(cmd *exec.Cmd) {}

// killProcGroup kills the process on Windows.
func killProcGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}
