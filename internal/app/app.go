// Package app 装配全部子系统并打开窗口。
package app

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"svg-master/internal/api"
	"svg-master/internal/config"
	"svg-master/internal/llm"
)

// Options 启动参数。
type Options struct {
	Web       bool
	Port      int
	DataDir   string
	Debug     bool
	AppName   string
	AppVer    string
	SingleRun bool
}

// Run 启动应用，阻塞直到窗口关闭。
func Run(opts Options) error {
	store, err := config.NewStore(opts.DataDir)
	if err != nil {
		return err
	}
	if e := store.LastError(); e != nil {
		fmt.Fprintln(os.Stderr, "配置警告:", e)
	}
	llm.SetModelDBDir(store.Dir()) // models.dev 能力库缓存目录

	srv, err := api.New(store, opts.Port)
	if err != nil {
		return err
	}
	// 内置窗口模式才能调系统文件夹选择器；-web 调试模式走浏览器回落。
	srv.SetDesktop(!opts.Web)
	go func() {
		if err := srv.Serve(); err != nil {
			fmt.Fprintln(os.Stderr, "HTTP 服务退出:", err)
		}
	}()
	defer srv.Close()

	url := srv.URL()
	// 给 HTTP 服务一点启动时间，避免窗口打开时首请求被拒
	time.Sleep(60 * time.Millisecond)

	if opts.Web {
		fmt.Println("界面地址:", url)
		fmt.Println("数据目录:", store.Dir())
		_ = openExternal(url)
		// 前台常驻，Ctrl+C 退出
		select {}
	}

	// 关闭窗口前：快速模式下若工程有改动，请前端提示「保存为工程 / 直接丢弃」。
	onClose := func() {
		if srv.NeedsSavePrompt() {
			srv.RequestSaveChoice(8 * time.Second)
		}
	}
	return runWindow(opts, url, onClose)
}

// openExternal 用系统默认浏览器打开 URL（Windows）。
func openExternal(url string) error {
	return execCommand("rundll32", "url.dll,FileProtocolHandler", url)
}

// DataFile 返回数据目录下的文件路径（供其它子系统使用）。
func DataFile(store *config.Store, name string) string {
	return filepath.Join(store.Dir(), name)
}
