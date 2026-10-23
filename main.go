// svg-master —— SVG 生成器（Go + HTML，绿色版单文件）
//
// 启动流程：解析参数 → app.Run() → 装配配置/模型/Agent/HTTP → 打开窗口。
// -web 模式不创建窗口，打印 URL 并调用系统浏览器，便于调试。
package main

import (
	"flag"
	"fmt"
	"os"

	"svg-master/internal/app"
	"svg-master/internal/config"
)

var (
	flagWeb   = flag.Bool("web", false, "用系统浏览器打开界面（不创建内置窗口）")
	flagPort  = flag.Int("port", 0, "HTTP 监听端口，0 = 随机")
	flagData  = flag.String("data", "", "数据目录，默认 exe 同级 data/")
	flagDebug = flag.Bool("debug", false, "开启 WebView2 调试模式")
)

func main() {
	flag.Parse()

	opts := app.Options{
		Web:       *flagWeb,
		Port:      *flagPort,
		DataDir:   *flagData,
		Debug:     *flagDebug,
		AppName:   "SVG Master",
		AppVer:    config.AppVersion,
		SingleRun: true,
	}
	if err := app.Run(opts); err != nil {
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}
}
