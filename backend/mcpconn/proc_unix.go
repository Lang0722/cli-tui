//go:build unix

package mcpconn

import (
	"os/exec"
	"syscall"
)

// ownGroup puts the server in its own process group, so stopping it
// stops whatever it started too (npx, a shell wrapper).
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func terminate(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
func kill(cmd *exec.Cmd)      { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
