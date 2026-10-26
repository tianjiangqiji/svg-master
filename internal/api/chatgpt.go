package api

import (
	"net/http"
	"os/exec"
	"runtime"
)

func openLoginBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

func (s *Server) handleChatGPT(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("providerId")
	st := s.store.Settings()
	p := st.FindProvider(id)
	if p == nil || p.AuthMode != "chatgpt" {
		writeErr(w, 400, "请先保存 ChatGPT 订阅供应商")
		return
	}
	if r.URL.Path == "/api/chatgpt/status" && r.Method == "GET" {
		writeJSON(w, s.codex.Status(r.Context(), id))
		return
	}
	if r.URL.Path == "/api/chatgpt/quota" && r.Method == "GET" {
		writeJSON(w, s.codex.Read(r.Context(), id))
		return
	}
	if r.Method != "POST" {
		writeErr(w, 405, "需要 POST")
		return
	}
	switch r.URL.Path {
	case "/api/chatgpt/login":
		u, err := s.codex.Login(r.Context(), id)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if err = openLoginBrowser(u); err != nil {
			_ = s.codex.Cancel(r.Context(), id)
			writeErr(w, 500, "无法打开系统浏览器，请设置默认浏览器后重试")
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	case "/api/chatgpt/cancel":
		if err := s.codex.Cancel(r.Context(), id); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	case "/api/chatgpt/logout":
		if err := s.codex.Logout(r.Context(), id); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	case "/api/chatgpt/usage":
		if err := openLoginBrowser("https://chatgpt.com/settings/usage"); err != nil {
			writeErr(w, 500, "无法打开系统浏览器")
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		http.NotFound(w, r)
	}
}
