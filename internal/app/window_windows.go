//go:build windows

package app

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	webview "github.com/jchv/go-webview2"
)

// runWindow 创建 WebView2 窗口并加载本地 URL。
// onClose 在窗口关闭前执行（用于询问是否保存快速模式工程）。
func runWindow(opts Options, url string, onClose func()) error {
	if !webView2Available() {
		fmt.Fprintln(os.Stderr, "未检测到 WebView2 运行时，改用系统浏览器打开。")
		fmt.Println("界面地址:", url)
		if err := openExternal(url); err != nil {
			return err
		}
		return waitForever()
	}

	w := webview.NewWithOptions(webview.WebViewOptions{
		Debug:     opts.Debug,
		AutoFocus: true,
		WindowOptions: webview.WindowOptions{
			Title:  opts.AppName + " · SVG 生成器",
			Width:  1560,
			Height: 900,
			Center: true,
		},
	})
	if w == nil {
		return fmt.Errorf("创建窗口失败")
	}
	defer w.Destroy()
	w.SetSize(1560, 900, webview.HintNone)

	// 用户点窗口关闭时：先让前端落盘并询问快速模式是否另存为工程。
	// 前端没实现/报错也继续关窗，不能把人锁在窗子里。
	var once sync.Once
	if onClose != nil {
		if err := w.Bind("appClosing", func() {
			w.Dispatch(func() {
				w.Eval(`(function(){ try { if (window.dshSaveChoice) window.dshSaveChoice(); } catch (e) {} })();`)
			})
			once.Do(onClose)
		}); err != nil {
			w.Dispatch(func() {
				w.Eval(`(function(){ try { if (window.dshSaveChoice) window.dshSaveChoice(); } catch (e) {} })();`)
			})
			once.Do(onClose)
		}
	}
	w.Navigate(url)
	w.Run()
	return nil
}

// webView2Available 检查注册表里的 WebView2 运行时版本。
func webView2Available() bool {
	keys := []string{
		`SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`,
		`SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`,
	}
	for _, k := range keys {
		ver, err := regQueryString(`HKLM\`+k, "pv")
		if err == nil && ver != "" && ver != "0.0.0.0" {
			return true
		}
	}
	// 目录兜底
	for _, p := range []string{
		`C:\Program Files (x86)\Microsoft\EdgeWebView\Application`,
		`C:\Program Files\Microsoft\EdgeWebView\Application`,
	} {
		if entries, err := os.ReadDir(p); err == nil && len(entries) > 0 {
			return true
		}
	}
	return false
}

func regQueryString(path, name string) (string, error) {
	advapi := syscall.NewLazyDLL("advapi32.dll")
	regOpen := advapi.NewProc("RegOpenKeyExW")
	regQuery := advapi.NewProc("RegQueryValueExW")
	regClose := advapi.NewProc("RegCloseKey")

	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	var hkey syscall.Handle
	const keyRead = 0x20019
	r, _, _ := regOpen.Call(uintptr(unsafe.Pointer(p)), 0, keyRead, uintptr(unsafe.Pointer(&hkey)))
	if r != 0 {
		return "", fmt.Errorf("RegOpenKeyEx 失败: %d", r)
	}
	defer regClose.Call(uintptr(hkey))

	n, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, 512)
	size := uint32(len(buf) * 2)
	var typ uint32
	r, _, _ = regQuery.Call(uintptr(hkey), uintptr(unsafe.Pointer(n)), 0, uintptr(unsafe.Pointer(&typ)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r != 0 {
		return "", fmt.Errorf("RegQueryValueEx 失败: %d", r)
	}
	return syscall.UTF16ToString(buf), nil
}

func execCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Start()
}

func waitForever() error {
	select {}
}
