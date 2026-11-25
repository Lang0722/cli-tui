//go:build !unix

package mcpconn

import "os/exec"

func ownGroup(*exec.Cmd)      {}
func terminate(cmd *exec.Cmd) { _ = cmd.Process.Kill() }
func kill(cmd *exec.Cmd)      { _ = cmd.Process.Kill() }
