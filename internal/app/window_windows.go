//go:build windows

package app

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
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

	// 用户点窗口关闭时：先问前端要不要保存，再决定关不关。
	// go-webview2 的 wndproc 收到 WM_CLOSE 直接 DestroyWindow，前端来不及弹窗，
	// 所以这里子类化窗口过程，把 WM_CLOSE 截下来。
	guard := newCloseGuard(w, onClose)
	guard.install() // 必须在 Navigate 之前：回执函数靠文档创建期脚本注入
	w.Navigate(url)
	w.Run()
	return nil
}

// ── 关窗询问 ────────────────────────────────────────────
// 窗口过程被换成 closeGuard.wndProc：第一次 WM_CLOSE 拦下并问前端，
// 前端回 true 才放行（重新投递一次 WM_CLOSE），回 false 就什么都不做。

const (
	wmClose    = 0x0010
	gwlWndProc = ^uintptr(3) // GWLP_WNDPROC = -4

	// closeAskTimeout 前端回执的最长等待时间。
	// 给足思考时间（10 分钟），超时才按同意退出处理：
	// 页面脚本坏掉不该让人关不掉窗口，而退出前 onClose 还会留一份快照。
	closeAskTimeout = 10 * time.Minute
)

var (
	procSetWindowLongPtr = user32DLL.NewProc("SetWindowLongPtrW")
	procCallWindowProc   = user32DLL.NewProc("CallWindowProcW")
	procPostMessage      = user32DLL.NewProc("PostMessageW")
	procDefWindowProc    = user32DLL.NewProc("DefWindowProcW")
)

type closeGuard struct {
	w       webview.WebView
	onClose func()

	hwnd    uintptr
	oldProc uintptr

	mu      sync.Mutex
	asking  bool
	allowed bool
	once    sync.Once
	answer  chan bool
}

func newCloseGuard(w webview.WebView, onClose func()) *closeGuard {
	return &closeGuard{w: w, onClose: onClose, answer: make(chan bool, 1)}
}

// install 把窗口过程换成自己的，并注册前端回执入口。
func (g *closeGuard) install() {
	hwnd := uintptr(g.w.Window())
	if hwnd == 0 {
		return
	}
	g.hwnd = hwnd

	// 前端问完后调 window.dshSaveResult(true/false) 回执；没实现就当同意退出。
	_ = g.w.Bind("dshSaveResult", func(allow bool) {
		select {
		case g.answer <- allow:
		default: // 已有回执在排队，忽略重复
		}
	})

	cb := syscall.NewCallback(g.wndProc)
	prev, _, _ := procSetWindowLongPtr.Call(hwnd, gwlWndProc, cb)
	if prev == 0 {
		// 32 位系统没有 SetWindowLongPtrW，退回 SetWindowLongW
		if p := user32DLL.NewProc("SetWindowLongW"); p.Find() == nil {
			prev, _, _ = p.Call(hwnd, gwlWndProc, cb)
		}
	}
	g.oldProc = prev
}

func (g *closeGuard) wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	if msg == wmClose && hwnd == g.hwnd {
		g.mu.Lock()
		if g.allowed {
			g.mu.Unlock()
			return g.callOld(hwnd, msg, wp, lp)
		}
		busy := g.asking
		g.asking = true
		g.mu.Unlock()
		if !busy {
			go g.ask()
		}
		return 0 // 吞掉这次关闭，等前端答复
	}
	return g.callOld(hwnd, msg, wp, lp)
}

// callOld 把消息交回原窗口过程；子类化没成功时退回 DefWindowProc，
// 保证窗口还能正常拖动、缩放、关闭。
func (g *closeGuard) callOld(hwnd, msg, wp, lp uintptr) uintptr {
	if g.oldProc != 0 {
		r, _, _ := procCallWindowProc.Call(g.oldProc, hwnd, msg, wp, lp)
		return r
	}
	r, _, _ := procDefWindowProc.Call(hwnd, msg, wp, lp)
	return r
}

// ask 让前端弹「另存 / 丢弃 / 取消」。
// 前端没实现、抛异常或迟迟不回，都按同意退出处理——不能把人锁死在窗子里。
func (g *closeGuard) ask() {
	g.w.Dispatch(func() {
		g.w.Eval(`(function(){ try {
			if (!window.dshSaveChoice) { if (window.dshSaveResult) window.dshSaveResult(true); return; }
			Promise.resolve(window.dshSaveChoice()).then(function(ok){
				if (window.dshSaveResult) window.dshSaveResult(ok !== false);
			}, function(){ if (window.dshSaveResult) window.dshSaveResult(true); });
		} catch (e) { try { if (window.dshSaveResult) window.dshSaveResult(true); } catch (e2) {} } })();`)
	})

	var allow bool
	select {
	case allow = <-g.answer:
	case <-time.After(closeAskTimeout):
		// 前端一直不回（脚本出错、页面卡死）：不能把人锁死在窗子里，
		// 按同意退出处理；onClose 里还会再留一份快照兜底。
		allow = true
	}

	if !allow {
		g.mu.Lock()
		g.asking = false
		g.mu.Unlock()
		return
	}
	// 真正退出前再落一次盘（前端已处理时这里是空操作）
	if g.onClose != nil {
		g.once.Do(g.onClose)
	}
	g.mu.Lock()
	g.allowed = true
	g.mu.Unlock()
	procPostMessage.Call(g.hwnd, wmClose, 0, 0)
}

// user32DLL 是 user32.dll 的惰性句柄，窗口子类化与 DPI 设置共用。
var user32DLL = syscall.NewLazyDLL("user32.dll")

// enableDPIAwareness 让进程按 Per-Monitor V2 处理 DPI。
//
// 顺序：Win10 1703 的 SetProcessDpiAwarenessContext →
// Win8.1 的 shcore.SetProcessDpiAwareness → Vista 的 SetProcessDPIAware。
// 任一成功即可；全失败时退回系统默认（高缩放下会被整幅位图拉伸）。
func enableDPIAwareness() {
	user32 := user32DLL
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
