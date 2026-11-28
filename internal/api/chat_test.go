package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"svg-master/internal/config"
	"svg-master/internal/llm"
	"svg-master/internal/project"
)

func newTestAPI(t *testing.T, upstream string) (*Server, *httptest.Server) {
	t.Helper()
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := store.Settings()
	settings.Providers = []config.Provider{{ID: "test", Name: "test", Protocol: config.ProtoOpenAIResp, BaseURL: upstream, APIKey: "test"}}
	settings.ActiveProviderID = "test"
	settings.ActiveModel = "test"
	if err := store.Save(settings); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store, token: "test-token", engines: map[string]*engineHandle{}}
	if err := s.openQuick(true); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.routes())
	t.Cleanup(srv.Close)
	return s, srv
}

func apiPost(t *testing.T, srv *httptest.Server, path string, value any) *http.Response {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-App-Token", "test-token")
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAbortUsesExactRunAndCancelsPendingUpstream(t *testing.T) {
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
			close(canceled)
		case <-release:
		}
	}))
	defer upstream.Close()
	defer close(release)
	s, srv := newTestAPI(t, upstream.URL)
	chat := apiPost(t, srv, "/api/chat", map[string]any{"prompt": "画图", "session": "s1", "runId": "current"})
	defer chat.Body.Close()
	if chat.StatusCode != 200 {
		t.Fatalf("chat HTTP %d", chat.StatusCode)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("响应头返回后未开始调用")
	}
	s.mu.Lock()
	handle := s.engines["s1"]
	s.mu.Unlock()
	stale := apiPost(t, srv, "/api/chat/abort", map[string]string{"session": "s1", "runId": "old"})
	var result struct {
		OK bool `json:"ok"`
	}
	_ = json.NewDecoder(stale.Body).Decode(&result)
	_ = stale.Body.Close()
	if result.OK {
		t.Fatal("旧任务停止请求误杀了新任务")
	}
	busy := apiPost(t, srv, "/api/chat", map[string]string{"prompt": "画图", "session": "s1", "runId": "new"})
	_ = busy.Body.Close()
	if busy.StatusCode != http.StatusConflict {
		t.Fatalf("重复任务应被拒绝，HTTP %d", busy.StatusCode)
	}
	abort := apiPost(t, srv, "/api/chat/abort", map[string]string{"session": "s1", "runId": "current"})
	_ = abort.Body.Close()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("停止未取消上游请求")
	}
	select {
	case <-handle.Done:
	case <-time.After(3 * time.Second):
		t.Fatal("停止后任务未退出")
	}
	if calls.Load() != 1 {
		t.Fatalf("停止后仍重试：%d", calls.Load())
	}
}

func TestChatRejectsEmptyPrompt(t *testing.T) {
	_, srv := newTestAPI(t, "http://127.0.0.1:1")
	resp := apiPost(t, srv, "/api/chat", map[string]string{"prompt": "   "})
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "提示词不能为空") {
		t.Fatalf("空提示词应返回 400：HTTP %d %s", resp.StatusCode, body)
	}
}

// quick 模式没有图层工具，emit_svg 的交付物也必须写回活动图层；
// 否则工程快照与「另存为工程」拿到的永远是空图层，重启后画面丢失。
func TestQuickModePersistsDeliveredSVGIntoProject(t *testing.T) {
	const svgText = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 400 300" width="400" height="300"><circle cx="100" cy="100" r="40" fill="#f00"/></svg>`
	args, err := json.Marshal(map[string]any{"svg": svgText, "note": "red circle"})
	if err != nil {
		t.Fatal(err)
	}
	emit := func(w http.ResponseWriter, event string, value map[string]any) {
		value["type"] = event
		raw, _ := json.Marshal(value)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if !strings.Contains(string(raw), `"function_call_output"`) {
			calls.Add(1)
			emit(w, "response.output_item.added", map[string]any{
				"output_index": 0,
				"item":         map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "emit_svg", "arguments": ""},
			})
			emit(w, "response.function_call_arguments.delta", map[string]any{
				"output_index": 0, "item_id": "fc_1", "delta": string(args),
			})
			emit(w, "response.output_item.done", map[string]any{
				"output_index": 0,
				"item":         map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "emit_svg", "arguments": string(args)},
			})
			emit(w, "response.completed", map[string]any{"response": map[string]any{"id": "resp_1", "status": "completed"}})
			return
		}
		emit(w, "response.output_text.delta", map[string]any{"output_index": 0, "content_index": 0, "delta": "done"})
		emit(w, "response.completed", map[string]any{"response": map[string]any{"id": "resp_2", "status": "completed"}})
	}))
	defer upstream.Close()

	s, srv := newTestAPI(t, upstream.URL)
	st := s.store.Settings()
	st.Agent.GenMode = config.GenModeQuick
	st.Agent.AutoValidate = true
	st.Canvas = config.CanvasPreset{Name: "测试", Width: 400, Height: 300, ViewBox: "0 0 400 300"}
	if err := s.store.Save(st); err != nil {
		t.Fatal(err)
	}
	resp := apiPost(t, srv, "/api/chat", map[string]any{
		"prompt": "red circle", "session": "s1", "runId": "r1", "genMode": "quick",
		"canvas": config.CanvasPreset{Name: "测试", Width: 400, Height: 300, ViewBox: "0 0 400 300"},
	})
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("HTTP %d: %s", resp.StatusCode, body)
	}
	if calls.Load() == 0 {
		t.Fatal("模型未收到请求")
	}
	p := s.Project()
	if p == nil || !strings.Contains(p.Render(), `cx="100"`) {
		t.Fatalf("快速模式交付物未写入工程：%s", p.Render())
	}
	active := p.Meta().ActiveLayer
	content, err := p.LayerContent(active)
	if err != nil || !strings.Contains(content, `r="40"`) {
		t.Fatalf("活动图层未保存交付内容：%q %v", content, err)
	}
	reopened, err := project.Open(p.Dir(), project.KindQuick)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reopened.Render(), `fill="#f00"`) {
		t.Fatal("重开工程后快速模式的画面丢失")
	}
}

func TestClearAllWaitsForRunAndPersistsBlankProject(t *testing.T) {
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
			close(canceled)
		case <-release:
		}
	}))
	defer upstream.Close()
	defer close(release)
	s, srv := newTestAPI(t, upstream.URL)
	if err := s.openProject(filepath.Join(t.TempDir(), "project")); err != nil {
		t.Fatal(err)
	}
	p := s.Project()
	if _, err := p.AddLayer("old", `<circle r="30"/>`, 0); err != nil {
		t.Fatal(err)
	}
	if err := p.SetHistory([]llm.Message{llm.TextMsg(llm.RoleUser, "old")}); err != nil {
		t.Fatal(err)
	}
	chat := apiPost(t, srv, "/api/chat", map[string]string{"prompt": "new", "session": "s1", "runId": "r1"})
	defer chat.Body.Close()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("任务未启动")
	}
	clear := apiPost(t, srv, "/api/session/drop", map[string]string{})
	defer clear.Body.Close()
	if clear.StatusCode != 200 {
		body, _ := io.ReadAll(clear.Body)
		t.Fatalf("清空失败：HTTP %d %s", clear.StatusCode, body)
	}
	var result struct {
		OK      bool            `json:"ok"`
		Project projectSnapshot `json:"project"`
	}
	if err := json.NewDecoder(clear.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || !result.Project.Empty || len(result.Project.Layers) != 1 || result.Project.Layers[0].Content != "" {
		t.Fatalf("清空响应仍有内容：%+v", result)
	}
	select {
	case <-canceled:
	default:
		t.Fatal("清空前上游未取消")
	}
	reopened, err := project.Open(p.Dir(), project.KindUser)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.IsEmpty() || len(reopened.History()) != 0 || strings.Contains(reopened.Render(), "circle") {
		t.Fatal("旧任务回写了已清空的工程")
	}
	s.mu.Lock()
	running := len(s.engines)
	s.mu.Unlock()
	if running != 0 {
		t.Fatal("清空后仍存在任务")
	}
}
