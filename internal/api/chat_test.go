package api

import (
	"bytes"
	"encoding/json"
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
