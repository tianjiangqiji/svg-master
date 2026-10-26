// Package api 提供本地 HTTP 接口与内嵌前端资源。
package api

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"

	"svg-master/internal/codex"
	"svg-master/internal/config"
)

//go:embed all:webdist
var webFS embed.FS

// Server 本地 HTTP 服务。
type Server struct {
	codex     *codex.Manager
	store     *config.Store
	token     string
	ln        net.Listener
	srv       *http.Server
	mu        sync.Mutex
	engines   map[string]*engineHandle // session -> 正在运行的任务
	resetting bool
	proj      projectState // 当前打开的工程（单例）
	desktop   bool         // 内置窗口模式：可以调系统文件夹选择器
}

// engineHandle 一个运行中的任务句柄（具体类型由 api 内部使用）。
type engineHandle struct {
	Abort func()
	RunID string
	Done  chan struct{}
}

// New 创建服务并绑定随机端口。
func New(store *config.Store, port int) (*Server, error) {
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	s := &Server{
		codex:   codex.New(store.Dir()),
		store:   store,
		token:   hex.EncodeToString(tok),
		engines: map[string]*engineHandle{},
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("监听端口: %w", err)
	}
	s.ln = ln
	s.srv = &http.Server{Handler: s.routes()}
	return s, nil
}

// URL 返回带令牌的访问地址。
func (s *Server) URL() string {
	return fmt.Sprintf("http://%s/?k=%s", s.ln.Addr().String(), s.token)
}

// SetDesktop 标记运行在内置窗口里（可以使用系统文件夹选择器）。
func (s *Server) SetDesktop(v bool) { s.desktop = v }

// Addr 返回监听地址（不含令牌）。
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Token 返回一次性访问令牌。
func (s *Server) Token() string { return s.token }

// Serve 阻塞提供服务。
func (s *Server) Serve() error {
	err := s.srv.Serve(s.ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Close 关闭服务。
func (s *Server) Close() error { s.codex.Close(); return s.srv.Close() }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	sub, err := fs.Sub(webFS, "webdist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			data, rerr := fs.ReadFile(sub, "index.html")
			if rerr != nil {
				http.Error(w, "missing index.html", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(data)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/")
		fileServer.ServeHTTP(w, r2)
	})

	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/providers", s.handleProviders)
	mux.HandleFunc("/api/chatgpt/", s.handleChatGPT)
	mux.HandleFunc("/api/models", s.handleModels)
	mux.HandleFunc("/api/protocols", s.handleProtocols)
	mux.HandleFunc("/api/prompts", s.handlePrompts)
	mux.HandleFunc("/api/canvas", s.handleCanvas)
	mux.HandleFunc("/api/chat", s.handleChat)
	mux.HandleFunc("/api/chat/abort", s.handleAbort)
	mux.HandleFunc("/api/session/drop", s.handleSessionDrop)
	mux.HandleFunc("/api/context", s.handleContext)
	mux.HandleFunc("/api/validate", s.handleValidate)
	mux.HandleFunc("/api/export", s.handleExport)
	mux.HandleFunc("/api/render-result", s.handleRenderResult)
	mux.HandleFunc("/api/project", s.handleProject)
	mux.HandleFunc("/api/project/browse", s.handleProjectBrowse)
	mux.HandleFunc("/api/project/open", s.handleProjectOpen)
	mux.HandleFunc("/api/project/create", s.handleProjectCreate)
	mux.HandleFunc("/api/project/save", s.handleProjectSave)
	mux.HandleFunc("/api/project/saveas", s.handleProjectSaveAs)
	mux.HandleFunc("/api/project/close", s.handleProjectClose)
	mux.HandleFunc("/api/layers", s.handleLayers)
	mux.HandleFunc("/api/layers/ops", s.handleLayerOps)

	return withAuth(s, mux)
}

// authed 校验令牌：?k= 或 X-App-Token 头。
func (s *Server) authed(r *http.Request) bool {
	if r.URL.Path == "/health" {
		return true
	}
	if r.URL.Query().Get("k") == s.token {
		return true
	}
	if r.Header.Get("X-App-Token") == s.token {
		return true
	}
	return false
}

// withAuth 全局鉴权中间件（静态资源同样要求令牌，防止本机其它程序探测）。
func withAuth(s *Server, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// 只允许本机来源，防 CSRF
		if o := r.Header.Get("Origin"); o != "" {
			if !strings.HasPrefix(o, "http://127.0.0.1") && !strings.HasPrefix(o, "http://localhost") {
				http.Error(w, "bad origin", http.StatusForbidden)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// readJSON 绑定请求体（上限 32MB）。
func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 32<<20)).Decode(v)
}
