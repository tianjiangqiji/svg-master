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

// 窗口尺寸按 96 DPI 的设计值给出，运行时乘以显示器缩放系数，
// 这样在 125%/150% 缩放下视觉大小不变，而不是被系统整体拉伸。
const (
	winDesignW = 1560
	winDesignH = 900
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

	// 必须在创建任何窗口之前声明 DPI 感知。
	// 不声明时进程按 96 DPI 处理，高缩放下 Windows 会把 WebView2 渲染好的
	// 整幅位图做双线性拉伸，13px 的中文小字首当其冲，看起来发虚。
	enableDPIAwareness()
	winW, winH := windowSizeForDPI()

	w := webview.NewWithOptions(webview.WebViewOptions{
		Debug:     opts.Debug,
		AutoFocus: true,
		WindowOptions: webview.WindowOptions{
			Title:  opts.AppName + " · SVG 生成器",
			Width:  uint(winW),
			Height: uint(winH),
			Center: true,
		},
	})
	if w == nil {
		return fmt.Errorf("创建窗口失败")
	}
	defer w.Destroy()
	w.SetSize(winW, winH, webview.HintNone)

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

// enableDPIAwareness 让进程按 Per-Monitor V2 处理 DPI。
//
// 顺序：Win10 1703 的 SetProcessDpiAwarenessContext →
// Win8.1 的 shcore.SetProcessDpiAwareness → Vista 的 SetProcessDPIAware。
// 任一成功即可；全失败时退回系统默认（高缩放下会被整幅位图拉伸）。
func enableDPIAwareness() {
	user32 := syscall.NewLazyDLL("user32.dll")
	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 的值是 -4
	const perMonitorAwareV2 = ^uintptr(3)
	if p := user32.NewProc("SetProcessDpiAwarenessContext"); p.Find() == nil {
		if r, _, _ := p.Call(perMonitorAwareV2); r != 0 {
			return
		}
	}
	if shcore := syscall.NewLazyDLL("shcore.dll"); shcore.Load() == nil {
		if p := shcore.NewProc("SetProcessDpiAwareness"); p.Find() == nil {
			const processPerMonitorDPI = 2
			if r, _, _ := p.Call(processPerMonitorDPI); r == 0 {
				return
			}
		}
	}
	if p := user32.NewProc("SetProcessDPIAware"); p.Find() == nil {
		_, _, _ = p.Call()
	}
}

// windowSizeForDPI 把 96 DPI 下的设计尺寸换算成当前显示器的物理像素。
// 换算后视觉大小与低缩放一致，但 WebView2 按物理像素渲染，文字不再被插值放大。
func windowSizeForDPI() (int, int) {
	scale := systemDPI()
	return winDesignW * scale / 96, winDesignH * scale / 96
}

// systemDPI 取系统 DPI（96 = 100%，120 = 125%，144 = 150%，192 = 200%）。
func systemDPI() int {
	user32 := syscall.NewLazyDLL("user32.dll")
	if p := user32.NewProc("GetDpiForSystem"); p.Find() == nil {
		if r, _, _ := p.Call(); r >= 96 && r <= 480 {
			return int(r)
		}
	}
	// GDI 兜底：GetDC(NULL) + GetDeviceCaps(LOGPIXELSX=88)
	dc, _, _ := user32.NewProc("GetDC").Call(0)
	if dc == 0 {
		return 96
	}
	release := user32.NewProc("ReleaseDC")
	defer release.Call(0, dc)
	gdi32 := syscall.NewLazyDLL("gdi32.dll")
	if r, _, _ := gdi32.NewProc("GetDeviceCaps").Call(dc, 88); r >= 96 && r <= 480 {
		return int(r)
	}
	return 96
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
