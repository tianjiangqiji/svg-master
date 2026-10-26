//go:build !windows

package codex

import "os/exec"

func configureCommand(cmd *exec.Cmd, path string, args []string) {}
