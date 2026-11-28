package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"svg-master/internal/agent"
	"svg-master/internal/config"
	"svg-master/internal/llm"
	"svg-master/internal/project"
	"svg-master/internal/svg"
)

// randomID 生成短随机 id。
func randomID() string {
	return fmt.Sprintf("%x", time.Now().UnixNano())[8:]
}

// handleChat 发起任务并以 SSE 下行事件。
//
// 与旧版的区别：不再是「一轮一对话」的内存 session，而是
//   - 对话历史存在当前工程（project.json）里，多轮、重启后都能接着聊；
//   - 图层工具通过 agent.LayerBridge 直接读写工程里的图层文件；
//   - 生成方式（agent.GenMode）决定是否下发图层工具、注入哪一套引导语：
//     quick 一次性交付、layers 分步改图层、anim 单图层但交付动图。
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "需要 POST")
		return
	}
	var in struct {
		pixelChatFields
		Prompt    string              `json:"prompt"`
		Images    []llm.ImageData     `json:"images"`
		Canvas    config.CanvasPreset `json:"canvas"`
		Model     string              `json:"model"`
		Provider  string              `json:"providerId"`
		Thinking  string              `json:"thinking"`
		FastMode  *bool               `json:"fastMode"`
		Session   string              `json:"session"`
		RunID     string              `json:"runId"`
		Layer     string              `json:"layer"`     // 前端当前选中的图层（可选，仅作提示）
		GenMode   string              `json:"genMode"`   // 生成方式：quick / layers / anim；空则回落到设置值
		MultiStep *bool               `json:"multiStep"` // 旧字段，genMode 缺失时用它推断
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	proj := s.Project()
	if proj == nil {
		writeErr(w, http.StatusBadRequest, "尚未打开工程：请先选择快速模式或打开一个工程文件夹")
		return
	}
	isPixel := proj.Engine() == project.EnginePixel
	if !projectEngineOK(in.Engine) || isPixel != (in.Engine == project.EnginePixel) {
		writeErr(w, http.StatusConflict, "请求工作区与当前工程类型不一致")
		return
	}
	// 非像素模式必须有主题或参考图；否则会上游回落到默认的鹈鹕演示主题。
	if !isPixel && strings.TrimSpace(in.Prompt) == "" && len(in.Images) == 0 {
		writeErr(w, http.StatusBadRequest, "提示词不能为空")
		return
	}
	st := s.store.Settings()
	prov := st.FindProvider(in.Provider)
	if prov == nil {
		prov = st.ActiveProvider()
	}
	if prov == nil {
		writeErr(w, http.StatusBadRequest, "没有可用供应商，请先在设置里添加")
		return
	}
	model := in.Model
	if model == "" {
		model = st.ActiveModel
	}
	if model == "" {
		writeErr(w, http.StatusBadRequest, "未选择模型")
		return
	}
	if prov.APIKey == "" && prov.AuthMode != "chatgpt" {
		writeErr(w, http.StatusBadRequest, "该供应商未填写 API Key")
		return
	}
	// 画布以工程为准（工程自带 canvas，避免「设置里改了但作品没变」）。
	canvas := s.projectCanvas()
	if !isPixel && in.Canvas.Width > 0 && in.Canvas.Height > 0 {
		canvas = in.Canvas
	}
	thinking := in.Thinking
	if thinking == "" {
		thinking = st.Thinking
	}
	var capsOverride *config.PartialCaps
	if prov.ModelCaps != nil {
		if pc, ok := prov.ModelCaps[model]; ok {
			capsOverride = &pc
		}
	}
	caps := llm.ResolveModelCaps(model, capsOverride)
	if len(in.Images) > 0 && !caps.Vision {
		writeErr(w, http.StatusBadRequest, "当前模型未标记支持图像输入，请在设置→能力覆盖里勾选「视觉」或换模型")
		return
	}

	// 超时按用户设置传入：它是「无数据容忍时长」，不是总时长，
	// 因此长 SVG 生成不会被总时长掐断。
	transport := llm.TransportForTimeout(st.Agent.TimeoutSec)
	client := llm.NewClient(prov.Protocol, llm.NormalizeBaseURL(prov.Protocol, prov.BaseURL), prov.APIKey, transport)
	fast := st.FastMode
	if in.FastMode != nil {
		fast = *in.FastMode
	}
	serviceTier := ""
	if fast && prov.Protocol == config.ProtoOpenAIResp {
		serviceTier = "priority"
	}
	if prov.AuthMode == "chatgpt" {
		if state := s.codex.Status(r.Context(), prov.ID); !state.Connected {
			writeErr(w, 400, state.Message)
			return
		}
		connection := s.codex.Client(prov.ID)
		defer connection.Close()
		client = connection
	}
	session := in.Session
	if session == "" {
		session = "s" + randomID()
	}

	// 生成方式：请求体显式携带时以请求为准（前端开关即时生效，不用等设置写完存盘）；
	// 只带旧字段 multiStep 时按它推断，保持对旧前端的兼容。
	agentSettings := st.Agent
	if in.GenMode != "" {
		agentSettings.GenMode = in.GenMode
	} else if in.MultiStep != nil {
		agentSettings.MultiStep = *in.MultiStep
	}
	agentSettings = config.NormalizeGenMode(agentSettings)
	// 只有多图层模式才下发图层工具；动图模式仍是单图层，刻意不给图层工具。
	layerTools := !isPixel && agentSettings.GenMode == config.GenModeLayers && st.Layers.Enabled
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "服务器不支持流式响应")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	handle := &engineHandle{Abort: cancel, RunID: in.RunID, Done: make(chan struct{})}
	s.mu.Lock()
	if s.resetting || len(s.engines) > 0 || s.Project() != proj {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "工程已切换或上一个任务仍在结束，请同步后重试")
		return
	}
	if isPixel {
		if time.Now().Before(s.pixelStopped[in.RunID]) {
			delete(s.pixelStopped, in.RunID)
			s.mu.Unlock()
			writeErr(w, http.StatusConflict, "该任务已停止")
			return
		}
		if err := proj.CheckPixelRevision(in.pixelVersion.ID, in.Revision); err != nil {
			s.mu.Unlock()
			pixelError(w, err)
			return
		}
	}
	s.engines[session] = handle
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.engines[session] == handle {
			delete(s.engines, session)
		}
		close(handle.Done)
		s.mu.Unlock()
	}()

	var writeMu sync.Mutex
	writeFrame := func(frame string) {
		writeMu.Lock()
		defer writeMu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if _, err := fmt.Fprint(w, frame); err != nil {
			cancel()
			return
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			cancel()
		}
	}
	send := func(v any) {
		raw, err := json.Marshal(v)
		if err != nil {
			return
		}
		writeFrame(fmt.Sprintf("data: %s\n\n", raw))
	}

	// 图层改动即时落盘：工程模式落到用户目录，快速模式落到内置临时目录，
	// 退出时再问用户是否另存为工程。
	emit := func(ev agent.Event) {
		if ctx.Err() != nil {
			return
		}
		switch ev.Kind {
		case agent.EvLayers:
			s.markDirty() // 内部会落盘
		case agent.EvSVG:
			if ev.SVG != "" {
				_ = s.persistSVG(session, ev.SVG)
				// 快速 / GIF 模式没有图层工具：交付物必须写回活动图层，
				// 否则工程快照与「另存为工程」拿到的仍是空图层。
				if !isPixel && !layerTools {
					layerBridge := bridge{p: proj}
					if active := layerBridge.Active(); active != "" {
						if err := layerBridge.Write(active, ev.SVG); err != nil {
							send(agent.Event{Kind: agent.EvLog, Level: "warn", Text: "交付内容未写入图层：" + err.Error()})
						} else {
							s.markDirty()
						}
					}
				}
			}
		}
		send(ev)
	}

	var pixelRun *pixelAgentBridge
	var pixelBridge agent.PixelBridge
	var pixelInput *agent.PixelInput
	if isPixel {
		var err error
		pixelRun, pixelInput, in.Prompt, err = s.preparePixelChat(ctx, proj, in.pixelChatFields, in.Prompt, caps.Vision, emit)
		if err != nil {
			pixelError(w, err)
			return
		}
		if err := ctx.Err(); err != nil {
			return
		}
		if err := proj.BeginPixelTurn(pixelInput.DraftMode); err != nil {
			pixelError(w, err)
			return
		}
		pixelBridge = pixelRun
	} else if in.Layer != "" {
		// An outdated active-layer hint does not prevent SVG generation.
		_ = proj.SetActive(in.Layer)
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	eng := agent.New(agent.Deps{
		Client:           client,
		Settings:         agentSettings,
		Canvas:           canvas,
		Model:            model,
		MaxTokens:        caps.MaxTokens,
		Thinking:         thinking,
		ServiceTier:      serviceTier,
		Transport:        transport,
		Emit:             emit,
		Layers:           s.projectBridge(),
		LayerTools:       layerTools,
		ContextClearedAt: proj.ContextClearedAt(),
		Pixel:            pixelBridge,
		Vision:           caps.Vision,
	})
	w.WriteHeader(http.StatusOK)
	flusher.Flush() // 注册后立即发送响应头，停止不必等模型产生首条事件。
	history := proj.History()

	// SSE 心跳：15s 一次注释行，防止中间层断连
	hb := time.NewTicker(15 * time.Second)
	defer hb.Stop()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		for {
			select {
			case <-ctx.Done():
				return
			case <-hb.C:
				writeFrame(": keepalive\n\n")
			}
		}
	}()
	defer func() { cancel(); <-heartbeatDone }()

	stats, err := eng.Run(ctx, agent.Task{
		Prompt:  in.Prompt,
		Images:  in.Images,
		Canvas:  canvas,
		Model:   model,
		Session: session,
		History: history,
		Pixel:   pixelInput,
	})
	if isPixel {
		if saveErr := proj.EndPixelTurn(eng.History(), err == nil); saveErr != nil && err == nil {
			err = saveErr
		}
		pixelRun.changed("", nil)
	} else if proj.SetHistory(eng.History()) == nil {
		s.markDirty()
	}
	if err != nil {
		send(agent.Event{Kind: agent.EvError, Error: err.Error(), Usage: &stats})
		return
	}
	send(agent.Event{Kind: agent.EvDone, Usage: &stats, Session: session})
}

// handleAbort 中止任务。
func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "需要 POST")
		return
	}
	var in struct {
		Session string `json:"session"`
		RunID   string `json:"runId"`
		Wait    bool   `json:"wait"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	h := s.engines[in.Session]
	matches := h != nil && h.Abort != nil && (in.RunID == "" || h.RunID == in.RunID)
	if in.Wait && !matches && len(s.engines) > 0 {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "当前有另一项生成任务，请重新同步画布")
		return
	}
	if in.Wait && !matches && in.RunID != "" {
		now := time.Now()
		for id, expiry := range s.pixelStopped {
			if !now.Before(expiry) {
				delete(s.pixelStopped, id)
			}
		}
		if s.pixelStopped == nil {
			s.pixelStopped = make(map[string]time.Time)
		}
		if len(s.pixelStopped) < 256 {
			s.pixelStopped[in.RunID] = now.Add(time.Minute)
		}
	}
	s.mu.Unlock()
	if matches {
		h.Abort()
	}
	if in.Wait {
		if matches {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			select {
			case <-h.Done:
			case <-ctx.Done():
				writeErr(w, http.StatusConflict, "任务尚未完全停止，请重新同步画布")
				return
			}
		}
		writeJSON(w, map[string]any{"ok": true, "project": s.snapshot()})
		return
	}
	if !matches {
		writeJSON(w, map[string]any{"ok": false, "msg": "没有运行中的任务"})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleSessionDrop 先停止任务，再清空当前作品和上下文，避免旧任务把内容写回来。
func (s *Server) handleSessionDrop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "需要 POST")
		return
	}
	s.mu.Lock()
	if s.resetting {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "正在清空，请稍候")
		return
	}
	s.resetting = true
	var running []*engineHandle
	for _, h := range s.engines {
		running = append(running, h)
	}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.resetting = false; s.mu.Unlock() }()
	for _, h := range running {
		h.Abort()
	}
	waitCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	for _, h := range running {
		select {
		case <-h.Done:
		case <-waitCtx.Done():
			writeErr(w, http.StatusConflict, "任务尚未停止，未清空内容，请稍后重试")
			return
		}
	}
	if p := s.Project(); p != nil {
		var err error
		if p.Engine() == project.EnginePixel {
			err = p.ResetPixel()
		} else {
			err = p.ResetContent()
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if p.Engine() == project.EnginePixel {
			s.markPixelDirty(p)
		} else {
			s.markDirty()
		}
	}
	writeJSON(w, map[string]any{"ok": true, "resetCanvas": true, "project": s.snapshot()})
}

// handleContext 返回上下文状态（前端用量环轮询）。
func (s *Server) handleContext(w http.ResponseWriter, r *http.Request) {
	session := r.URL.Query().Get("session")
	s.mu.Lock()
	_, ok := s.engines[session]
	s.mu.Unlock()
	writeJSON(w, map[string]any{
		"running":   ok,
		"messages":  len(s.sessionHistory()),
		"clearedAt": s.contextClearedAt(),
	})
}

// persistSVG 把一次交付的 SVG 作为独立文件写进工程的 out/ 目录。
func (s *Server) persistSVG(session, body string) error {
	p := s.Project()
	if p == nil || strings.TrimSpace(body) == "" {
		return nil
	}
	seq := time.Now().Format("150405")
	name := filepath.Join("out", fmt.Sprintf("%s-%s.svg", session, seq))
	if err := p.WriteDeliverable(name, body); err != nil {
		return err
	}
	s.markDirty()
	return nil
}

// handleRenderResult 前端回传栅格化 PNG（render_check 工具用）。
func (s *Server) handleRenderResult(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Session string `json:"session"`
		Data    string `json:"data"`
		MIME    string `json:"mime"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleExport 导出 SVG（返回文本，前端负责下载）。
//
// 不再改写内容：只做外壳剥离（围栏/闲话），SVG 本体与交付内容逐字一致。
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if p := s.Project(); p != nil && p.Engine() == project.EnginePixel {
		pixelError(w, project.ErrSVGProject)
		return
	}
	var in struct {
		SVG string `json:"svg"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out := strings.TrimSpace(in.SVG)
	if out == "" {
		out = strings.TrimSpace(s.renderCurrent())
	}
	if r2 := svg.Extract(out); r2.Found && r2.Closed && strings.TrimSpace(r2.SVG) != "" {
		out = strings.TrimSpace(r2.SVG)
	}
	if out == "" {
		writeErr(w, http.StatusBadRequest, "svg 不能为空")
		return
	}
	writeJSON(w, map[string]any{"svg": out})
}
