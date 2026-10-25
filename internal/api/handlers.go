package api

import (
	"net/http"
	"os"

	"svg-master/internal/config"
	"svg-master/internal/llm"
	"svg-master/internal/svg"
)

// webSource 返回前端资源来源：开发时可用 SVG_WEB_DIR 指向磁盘目录，打包后走内嵌。
func webSource() (readFile func(string) ([]byte, error), hasDirFS bool, dir string) {
	if d := os.Getenv("SVG_WEB_DIR"); d != "" {
		return func(name string) ([]byte, error) { return os.ReadFile(d + "/" + name) }, true, d
	}
	return nil, false, ""
}

// handleState 返回前端启动所需的一切：设置（Key 打码）、协议元信息、预设、提示词、版本。
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"settings":      s.store.SettingsMasked(),
		"protocols":     config.ProtocolMetas,
		"canvases":      svg.Presets(),
		"prompts":       svg.Prompts(),
		"categories":    svg.Categories(),
		"defaultPrompt": svg.DefaultPrompt,
		"version":       config.AppVersion,
		"dataDir":       s.store.Dir(),
		"project":       s.snapshot(),
		"warn":          errText(s.store.LastError()),
	})
}

func errText(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}

// handleSettings 保存设置。
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "需要 PUT")
		return
	}
	var patch config.SettingsPatch
	if err := readJSON(r, &patch); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if patch.Empty() {
		writeJSON(w, map[string]any{"ok": true, "settings": s.store.SettingsMasked()})
		return
	}
	merged := patch.Apply(s.store.Settings())
	if err := s.store.Save(merged); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "settings": s.store.SettingsMasked()})
}

// handleProviders 供应商增删改（前端也可整体 PUT /api/settings，这里提供细粒度入口）。
func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	cur := s.store.Settings()
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"providers": s.store.SettingsMasked().Providers})
	case http.MethodPost:
		var p config.Provider
		if err := readJSON(r, &p); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		meta, _ := config.MetaOf(p.Protocol)
		if p.ID == "" {
			p.ID = "p" + randomID()
		}
		if p.BaseURL == "" {
			p.BaseURL = meta.BaseURL
		}
		if p.Name == "" {
			p.Name = meta.Label
		}
		cur.Providers = append(cur.Providers, p)
		if err := s.store.Save(cur); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true, "id": p.ID, "settings": s.store.SettingsMasked()})
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			writeErr(w, http.StatusBadRequest, "缺少 id")
			return
		}
		out := cur.Providers[:0]
		for _, p := range cur.Providers {
			if p.ID != id {
				out = append(out, p)
			}
		}
		cur.Providers = out
		if err := s.store.Save(cur); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true, "settings": s.store.SettingsMasked()})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "不支持的方法")
	}
}

// handleProtocols 协议元信息（前端宫格）。
func (s *Server) handleProtocols(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"protocols": config.ProtocolMetas})
}

// handlePrompts 提示词库（支持 ?q= 搜索、?category= 过滤）。
func (s *Server) handlePrompts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	cat := r.URL.Query().Get("category")
	list := svg.SearchPrompts(q)
	if cat != "" {
		filtered := make([]svg.Prompt, 0, len(list))
		for _, p := range list {
			if p.Category == cat {
				filtered = append(filtered, p)
			}
		}
		list = filtered
	}
	writeJSON(w, map[string]any{"prompts": list, "categories": svg.Categories()})
}

// handleCanvas 返回/切换画布预设。
func (s *Server) handleCanvas(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, map[string]any{"presets": svg.Presets(), "current": s.store.Settings().Canvas})
		return
	}
	var c config.CanvasPreset
	if err := readJSON(r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cur := s.store.Settings()
	cur.Canvas = c
	if err := s.store.Save(cur); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "canvas": c})
}

// handleValidate 校验一段 SVG（前端手动触发）。
// 与 emit_svg 同一套规则：只判「是不是 SVG」与「尺寸是否等于当前画布」，不改写内容。
func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SVG string `json:"svg"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rep := svg.Validate(in.SVG)
	c := s.projectCanvas()
	rep.Issues = append(rep.Issues, svg.CheckCanvasSize(in.SVG, svg.Canvas{
		Name: c.Name, Width: c.Width, Height: c.Height, ViewBox: c.ViewBox,
	})...)
	rep.OK = true
	for _, it := range rep.Issues {
		if it.Level == "error" {
			rep.OK = false
			break
		}
	}
	writeJSON(w, rep)
}

// handleModels 抓取远端模型列表。
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProviderID string `json:"providerId"`
		Protocol   string `json:"protocol"`
		BaseURL    string `json:"baseUrl"`
		APIKey     string `json:"apiKey"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.ProviderID != "" {
		cur := s.store.Settings()
		if p := cur.FindProvider(in.ProviderID); p != nil {
			if in.Protocol == "" {
				in.Protocol = p.Protocol
			}
			if in.BaseURL == "" {
				in.BaseURL = p.BaseURL
			}
			if in.APIKey == "" || containsMask(in.APIKey) {
				in.APIKey = p.APIKey
			}
		}
	}
	if in.Protocol == "" {
		in.Protocol = config.ProtoOpenAIChat
	}
	var models []config.ModelEntry
	var err error
	cur := s.store.Settings()
	if p := cur.FindProvider(in.ProviderID); p != nil && p.AuthMode == "chatgpt" {
		models, err = s.codex.Models(r.Context(), p.ID)
	} else {
		models, err = llm.FetchRemoteModels(in.Protocol, in.BaseURL, in.APIKey, llm.DefaultTransport())
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	type row struct {
		ID   string           `json:"id"`
		Name string           `json:"name"`
		Caps config.ModelCaps `json:"caps"`
	}
	out := make([]row, 0, len(models))
	for _, m := range models {
		out = append(out, row{ID: m.ID, Name: m.Name, Caps: llm.InferModelCaps(m.ID)})
	}
	writeJSON(w, map[string]any{"models": out})
}

func containsMask(s string) bool {
	for i := 0; i+3 < len(s); i++ {
		if s[i] == '*' && s[i+1] == '*' && s[i+2] == '*' && s[i+3] == '*' {
			return true
		}
	}
	return false
}
