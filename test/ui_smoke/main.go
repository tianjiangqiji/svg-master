// UI 联调入口：只连本机 fake_llm，使用独立临时配置，不读取真实 API Key。
package main

import (
	"fmt"
	"os"

	"svg-master/internal/api"
	"svg-master/internal/config"
)

func main() {
	dir, err := os.MkdirTemp("", "svg-master-ui-smoke-")
	if err != nil {
		panic(err)
	}
	store, err := config.NewStore(dir)
	if err != nil {
		panic(err)
	}
	settings := store.Settings()
	settings.Providers = nil
	for i, protocol := range []string{config.ProtoOpenAIResp, config.ProtoOpenAIChat, config.ProtoAnthropic, config.ProtoGemini} {
		id := fmt.Sprintf("test%d", i)
		settings.Providers = append(settings.Providers, config.Provider{ID: id, Name: protocol, Protocol: protocol, BaseURL: "http://127.0.0.1:18999", APIKey: "fake-local-only", Models: []config.ModelEntry{{ID: "test-model", Name: "test-model"}}})
	}
	settings.ActiveProviderID = "test0"
	settings.ActiveModel = "test-model"
	settings.Canvas = config.CanvasPreset{Name: "测试", Width: 400, Height: 300, ViewBox: "0 0 400 300"}
	settings.Agent.GenMode = config.GenModeQuick
	settings.Agent.AutoValidate = true
	if err := store.Save(settings); err != nil {
		panic(err)
	}
	server, err := api.New(store, 0)
	if err != nil {
		panic(err)
	}
	defer server.Close()
	fmt.Println("UI_SMOKE_URL=" + server.URL())
	fmt.Println("UI_SMOKE_DATA=" + dir)
	if err := server.Serve(); err != nil {
		panic(err)
	}
}
