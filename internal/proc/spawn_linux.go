package proc

import (
	"os/exec"
	"syscall"
)

// ConfigureCmd — P1: дитина дістає SIGKILL, щойно вмирає батько.
func ConfigureCmd(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}

// GuardStarted — no-op: Pdeathsig діє з моменту exec.
func GuardStarted(*exec.Cmd) {}

// TerminateProc — м'яке завершення, SIGKILL іде окремим кроком вище.
func TerminateProc(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
}
