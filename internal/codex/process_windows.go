//go:build windows

package codex

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func configureCommand(cmd *exec.Cmd, path string, args []string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if strings.HasSuffix(strings.ToLower(path), ".cmd") || strings.HasSuffix(strings.ToLower(path), ".bat") {
		// npm exposes codex.cmd on Windows. The path is quoted; arguments are fixed literals.
		cmd.Path = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
		cmd.Args = []string{cmd.Path}
		cmd.SysProcAttr.CmdLine = `cmd.exe /d /s /c ""` + path + `" ` + strings.Join(args, " ") + `"`
	}
}
