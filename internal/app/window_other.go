//go:build !windows

package app

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// runWindow 非 Windows 平台直接走系统浏览器。
func runWindow(opts Options, url string, onClose func()) error {
	fmt.Println("界面地址:", url)
	return openExternal(url)
}

func openExternal(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

func execCommand(name string, args ...string) error {
	return exec.Command(name, args...).Start()
}

func waitForever() error {
	fmt.Fprintln(os.Stderr, "常驻模式：Ctrl+C 退出")
	select {}
}
