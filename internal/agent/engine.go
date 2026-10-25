package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"svg-master/internal/config"
	"svg-master/internal/llm"
	"svg-master/internal/svg"
)

// engineImpl Engine 的实现载体。Engine 的 impl 是值类型字段，
// 因此所有可变状态都挂在这个指针上，Abort 与 Status 才能在其它 goroutine 安全访问。
type engineImpl struct {
	st *engineState
}

// engineState 运行时状态。
type engineState struct {
	mu          sync.Mutex
	cancel      context.CancelFunc
	aborted     bool
	canvas      config.CanvasPreset
	svg         string
	report      svg.Report
	comp        Compressor
	stats       Stats
	used        int
	max         int
	msgs        int
	streamRetry int
	history     []llm.Message // 完整历史（历史延续 + 本轮新消息），run 结束时写满
}

// newEngine 构造引擎（stub.go 删除后由本文件提供）。
func newEngine(deps Deps) *Engine {
	e := &Engine{
		deps:    deps,
		started: time.Now(),
		impl:    engineImpl{st: newEngineState(deps)},
	}
	return e
}

// newEngineState 初始化运行时状态与压缩内核。
func newEngineState(deps Deps) *engineState {
	canvas := pickCanvas(config.CanvasPreset{}, deps.Canvas)
	window := contextWindowOf(deps.Model)
	return &engineState{
		canvas: canvas,
		comp:   compressorFor(deps),
		used:   0,
		max:    window,
	}
}

// compressorFor 按配置构造压缩内核；未启用或窗口过小返回 nil。
func compressorFor(deps Deps) Compressor {
	if !deps.Settings.CompressOn {
		return nil
	}
	window := contextWindowOf(deps.Model)
	if window < minCompressWindow {
		return nil
	}
	return newCompressor(window, deps.Settings.CompressAt)
}

// ---- engineImpl 访问器 ----

// snapshotCanvas 当前画布快照。
func (im engineImpl) snapshotCanvas() config.CanvasPreset {
	if im.st == nil {
		return config.CanvasPreset{}
	}
	im.st.mu.Lock()
	defer im.st.mu.Unlock()
	return im.st.canvas
}

// setCanvas 更新画布。
func (im engineImpl) setCanvas(c config.CanvasPreset) {
	if im.st == nil {
		return
	}
	im.st.mu.Lock()
	defer im.st.mu.Unlock()
	im.st.canvas = c
}

// snapshotSVG 当前画布上的 SVG。
func (im engineImpl) snapshotSVG() string {
	if im.st == nil {
		return ""
	}
	im.st.mu.Lock()
	defer im.st.mu.Unlock()
	return im.st.svg
}

// setSVG 记录最新 SVG 与校验报告（供导出与 render 回调使用）。
func (im engineImpl) setSVG(text string, rep svg.Report) {
	if im.st == nil {
		return
	}
	im.st.mu.Lock()
	defer im.st.mu.Unlock()
	im.st.svg = text
	im.st.report = rep
}

// compressor 压缩内核（可能为 nil）。
func (im engineImpl) compressor() Compressor {
	if im.st == nil {
		return nil
	}
	return im.st.comp
}

// ---- Engine 内部辅助 ----

// emit 推送事件（Emit 由 api 层提供，可为 nil）。
func (e *Engine) emit(ev Event) {
	if e.deps.Emit != nil {
		e.deps.Emit(ev)
	}
}

// logf 推一条日志事件。
func (e *Engine) logf(level, format string, args ...any) {
	e.emit(Event{Kind: EvLog, Level: level, Text: fmt.Sprintf(format, args...)})
}

// abort 取消当前任务（可跨 goroutine 调用）。
func (e *Engine) abort() {
	if e == nil || e.impl.st == nil {
		return
	}
	e.impl.st.mu.Lock()
	e.impl.st.aborted = true
	cancel := e.impl.st.cancel
	e.impl.st.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// history 返回完整消息历史副本（须在 Run 返回后调用）。
func (e *Engine) history() []llm.Message {
	if e == nil || e.impl.st == nil {
		return nil
	}
	e.impl.st.mu.Lock()
	defer e.impl.st.mu.Unlock()
	out := make([]llm.Message, len(e.impl.st.history))
	copy(out, e.impl.st.history)
	return out
}

// status 返回上下文与统计快照。
func (e *Engine) status() ContextStatus {
	if e == nil || e.impl.st == nil {
		return ContextStatus{}
	}
	st := e.impl.st
	st.mu.Lock()
	used, max := st.used, st.max
	msgCount := st.msgs
	stats := st.stats
	comp := st.comp
	st.mu.Unlock()

	cs := ContextStatus{
		Used:     used,
		Max:      max,
		Messages: msgCount,
		TPS:      stats.TPS,
	}
	if comp != nil {
		u, m, blocks, compressed := comp.Status(used)
		cs.Used, cs.Max = u, m
		cs.Blocks, cs.Compressed = blocks, compressed
	}
	if cs.Max > 0 {
		cs.Percent = float64(cs.Used) * 100 / float64(cs.Max)
	}
	return cs
}

// ---- Run 主循环 ----

// run 执行任务：组装消息 → 多轮模型调用 → 工具执行 → 直到模型不再调用工具。
//
// 注意：EvDone / EvError 由 api 层在 Run 返回后统一发送，引擎内不重复发送，
// 避免前端收到两个终止事件。
func (e *Engine) run(ctx context.Context, t Task) (Stats, error) {
	if e == nil {
		return Stats{}, errors.New("引擎未初始化")
	}
	if e.impl.st == nil {
		e.impl = engineImpl{st: newEngineState(e.deps)}
	}
	if e.deps.Client == nil {
		return Stats{}, errors.New("缺少模型客户端，无法开始任务")
	}

	st := e.impl.st
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	st.mu.Lock()
	st.cancel = cancel
	if st.aborted {
		cancel()
	}
	st.used = 0
	st.mu.Unlock()
	defer func() {
		st.mu.Lock()
		st.cancel = nil
		st.mu.Unlock()
	}()

	e.started = time.Now()
	start := e.started

	canvas := pickCanvas(t.Canvas, e.deps.Canvas)
	st.mu.Lock()
	st.canvas = canvas
	st.mu.Unlock()

	model := strings.TrimSpace(t.Model)
	if model == "" {
		model = e.deps.Model
	}

	system := buildSystemPrompt(canvas)
	if extra := strings.TrimSpace(t.System); extra != "" {
		system += "\n\n" + extra
	}

	// 生成方式：三档开关的唯一真源是 Settings.GenMode（旧配置会按 MultiStep 推断）。
	//   quick  → 一次性交付整幅 SVG，不注入分步引导；
	//   layers → 注入分步引导 + 图层工作区约定（下发图层工具时）；
	//   anim   → 仍是单图层，但要求交付可播放的动图。
	mode := config.NormalizeGenMode(e.deps.Settings).GenMode
	switch mode {
	case config.GenModeLayers:
		system += "\n\n" + multiStepNote
		if e.deps.LayerTools {
			system += "\n\n" + layerToolsNote()
		}
	case config.GenModeAnim:
		system += "\n\n" + animatedNote()
	}
	// 用户清空过上下文：提醒它先读图层再动手，而不是假设画布是空的。
	// 动图模式没有图层工具，这条提醒会误导模型，因此跳过。
	if e.deps.ContextClearedAt > 0 && e.deps.LayerTools {
		system += "\n\n" + contextClearedNote()
	}

	msgs := buildInitialMessages(t)
	// 连续对话：把上一轮结束时的完整历史拼在本次输入之前（浅拷贝，运行期只 append）。
	if len(t.History) > 0 {
		msgs = append(append([]llm.Message{}, t.History...), msgs...)
	}

	maxSteps := e.deps.Settings.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultMaxModelSteps
	}
	specs := toolSpecs(e.deps)
	comp := e.impl.compressor()

	var out Stats
	modelSteps, emptyRetry := 0, 0
	deliveryOK := false
	completed := false
	breaker := newBreaker()

	for modelSteps < maxSteps {
		if err := runCtx.Err(); err != nil {
			e.saveHistory(msgs)
			e.finishStats(&out, start)
			return out, err
		}

		// 每次请求前投影上下文（只折叠投影，不动原始 msgs）。
		used := estTokensAll(msgs)
		projected, nudge := msgs, ""
		if comp != nil {
			projected, nudge = comp.Project(msgs, used)
			if nudge != "" {
				e.emit(Event{Kind: EvNudge, Text: nudge})
			}
		}
		st.mu.Lock()
		st.used = used
		st.msgs = len(msgs)
		st.mu.Unlock()

		req := llm.Request{
			System:      system,
			Messages:    projected,
			Tools:       specs,
			Model:       model,
			MaxTokens:   e.deps.MaxTokens,
			Thinking:    e.deps.Thinking,
			ServiceTier: e.deps.ServiceTier,
			Stream:      !e.deps.Settings.StreamDisabled,
			SessionID:   t.Session,
		}

		res, err := e.streamOnce(runCtx, req)
		if res != nil {
			applyUsage(&out, res.usage)
		}
		if err != nil {
			e.saveHistory(msgs)
			e.finishStats(&out, start)
			if errors.Is(err, context.Canceled) || runCtx.Err() != nil {
				return out, context.Canceled
			}
			return out, err
		}

		modelSteps++
		out.ModelSteps = modelSteps
		e.fillContextStats(&out)
		if res.usage != nil {
			e.emit(Event{Kind: EvUsage, Usage: statsCopy(out)})
		}

		assistant := buildAssistantMessage(res)
		if res.continueTurn {
			msgs = append(msgs, assistant)
			continue
		}

		// 空回复：无正文、无思考、无工具调用 —— 重试有限次。
		if strings.TrimSpace(res.text) == "" && len(res.toolCalls) == 0 {
			if deliveryOK {
				if assistant.NativeOutput != nil {
					msgs = append(msgs, assistant)
				}
				e.logf("info", "SVG 已交付，模型结束回复")
				completed = true
				break
			}
			emptyRetry++
			if emptyRetry <= maxEmptyRetries {
				e.logf("warn", "模型返回空回复，第 %d 次重试", emptyRetry)
				e.noteStreamRetry()
				if err := sleepCtx(runCtx, retryDelay(emptyRetry-1)); err != nil {
					e.saveHistory(msgs)
					e.finishStats(&out, start)
					return out, context.Canceled
				}
				continue
			}
			e.saveHistory(msgs)
			e.finishStats(&out, start)
			return out, errors.New("模型连续返回空回复，未完成交付；请检查渠道响应或输出 token 上限")
		}
		emptyRetry = 0

		msgs = append(msgs, assistant)

		if len(res.toolCalls) == 0 {
			// 无工具调用 → 任务完成。
			completed = true
			break
		}

		// 串行执行工具，保证事件顺序稳定。
		toolMsg := llm.Message{Role: llm.RoleTool}
		for i, call := range res.toolCalls {
			if err := runCtx.Err(); err != nil {
				// 保持调用/结果配对，防止停止后继续会话触发缺少工具结果的协议错误。
				for _, pending := range res.toolCalls[i:] {
					toolMsg.Blocks = append(toolMsg.Blocks, toolResultBlock(pending, "用户已中止，此工具未执行", true))
				}
				msgs = append(msgs, toolMsg)
				e.saveHistory(msgs)
				e.finishStats(&out, start)
				return out, err
			}
			e.emit(Event{Kind: EvToolStart, CallID: call.ID, Tool: call.Name, Args: call.Arguments})
			out.ToolCalls++

			// 熔断：同一工具连续重复同一错误时不再执行，改为注入提示。
			if stop, hint := breaker.hit(call, runCtx); stop {
				deliveryOK = false
				out.ToolErrors++
				content := fmt.Sprintf("工具 %s 已连续 %d 次报相同错误，本次调用被熔断。请换一种方式，或直接给出可用的结果。", call.Name, repeatedErrorCallThreshold)
				e.emit(Event{Kind: EvToolEnd, CallID: call.ID, Tool: call.Name, Error: content})
				e.logf("warn", "%s", content)
				toolMsg.Blocks = append(toolMsg.Blocks, toolResultBlock(call, content, true))
				if hint != "" {
					toolMsg.Blocks = append(toolMsg.Blocks, llm.Block{
						Type: llm.BlockText,
						Text: "系统提示：" + hint,
					})
				}
				continue
			}

			oc := e.runTool(runCtx, call)
			if oc.IsError {
				deliveryOK = false
				out.ToolErrors++
				e.emit(Event{Kind: EvToolEnd, CallID: call.ID, Tool: call.Name, Error: oc.Content})
			} else {
				if call.Name == ToolEmitSVG {
					deliveryOK = true
				}
				e.emit(Event{Kind: EvToolEnd, CallID: call.ID, Tool: call.Name, Result: oc.Content})
			}
			breaker.record(call, oc)
			toolMsg.Blocks = append(toolMsg.Blocks, toolResultBlock(call, oc.Content, oc.IsError))
		}
		if len(toolMsg.Blocks) > 0 {
			msgs = append(msgs, toolMsg)
		}
	}

	st.mu.Lock()
	st.used = estTokensAll(msgs)
	st.msgs = len(msgs)
	st.mu.Unlock()
	e.saveHistory(msgs)

	e.finishStats(&out, start)
	if !completed && modelSteps >= maxSteps {
		return out, fmt.Errorf("已达模型轮数上限（%d），任务尚未完成，当前作品已保留", maxSteps)
	}
	return out, nil
}

// saveHistory 把当前完整消息历史（含历史延续）写入状态（加锁）。
func (e *Engine) saveHistory(msgs []llm.Message) {
	if e == nil || e.impl.st == nil {
		return
	}
	snapshot := make([]llm.Message, len(msgs))
	copy(snapshot, msgs)
	e.impl.st.mu.Lock()
	e.impl.st.history = snapshot
	e.impl.st.mu.Unlock()
}

// finishStats 计算耗时与 TPS，并落盘到状态。
func (e *Engine) finishStats(out *Stats, start time.Time) {
	elapsed := time.Since(start)
	out.ElapsedMS = elapsed.Milliseconds()
	if elapsed > 0 && out.OutputTokens > 0 {
		out.TPS = float64(out.OutputTokens) / elapsed.Seconds()
	}
	out.StreamRetry = e.streamRetries()
	e.fillContextStats(out)
	if e.impl.st != nil {
		e.impl.st.mu.Lock()
		e.impl.st.stats = *out
		e.impl.st.mu.Unlock()
	}
}

// fillContextStats 把上下文用量与压缩块统计写进 Stats。
func (e *Engine) fillContextStats(out *Stats) {
	if e.impl.st == nil {
		return
	}
	st := e.impl.st
	st.mu.Lock()
	used, max, comp := st.used, st.max, st.comp
	st.mu.Unlock()

	out.ContextUsed, out.ContextMax = used, max
	if comp == nil {
		return
	}
	u, m, blocks, compressed := comp.Status(used)
	out.ContextUsed, out.ContextMax = u, m
	out.Blocks, out.Compressed = blocks, compressed
}

// applyUsage 累积 token 用量。
func applyUsage(out *Stats, u *llm.Usage) {
	if u == nil {
		return
	}
	out.InputTokens += u.InputTokens
	out.OutputTokens += u.OutputTokens
	out.CacheRead += u.CacheReadTokens
	total := u.TotalTokens
	if total <= 0 {
		total = u.InputTokens + u.OutputTokens
	}
	out.Tokens += total
}

// statsCopy 返回统计副本（事件里用指针，避免调用方修改内部值）。
func statsCopy(s Stats) *Stats {
	c := s
	return &c
}

// buildInitialMessages 组装首条 user 消息（正文 + 参考图）。
func buildInitialMessages(t Task) []llm.Message {
	msg := llm.Message{Role: llm.RoleUser}
	if p := strings.TrimSpace(t.Prompt); p != "" {
		msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockText, Text: p})
	}
	for i := range t.Images {
		img := t.Images[i]
		msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockImage, Image: &img})
	}
	if len(msg.Blocks) == 0 {
		msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockText, Text: svg.DefaultPrompt})
	}
	return []llm.Message{msg}
}

// buildAssistantMessage 把一次流的累积结果转成 assistant 消息。
func buildAssistantMessage(res *streamResult) llm.Message {
	msg := llm.Message{Role: llm.RoleAssistant, NativeOutput: res.output}
	if strings.TrimSpace(res.thinking) != "" {
		msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockThinking, Thinking: res.thinking})
	}
	if strings.TrimSpace(res.text) != "" {
		msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockText, Text: res.text})
	}
	for i := range res.toolCalls {
		call := res.toolCalls[i]
		msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockToolCall, ToolCall: &call})
	}
	return msg
}

// toolResultBlock 构造工具结果块。
func toolResultBlock(call llm.ToolCall, content string, isErr bool) llm.Block {
	return llm.Block{
		Type: llm.BlockToolResult,
		ToolResult: &llm.ToolResult{
			CallID:  call.ID,
			Name:    call.Name,
			Content: content,
			IsError: isErr,
		},
	}
}

// pickCanvas 决定本次任务使用的画布：任务 > 依赖 > 出厂默认。
func pickCanvas(task, dep config.CanvasPreset) config.CanvasPreset {
	c := task
	if c.Width <= 0 || c.Height <= 0 {
		c = dep
	}
	if c.Width <= 0 || c.Height <= 0 {
		c = config.DefaultSettings().Canvas
	}
	if strings.TrimSpace(c.Name) == "" {
		c.Name = fmt.Sprintf("%d×%d", c.Width, c.Height)
	}
	if strings.TrimSpace(c.ViewBox) == "" {
		c.ViewBox = fmt.Sprintf("0 0 %d %d", c.Width, c.Height)
	}
	return c
}

// marshalJSON 稳定的 JSON 序列化（失败时退回错误文本）。
func marshalJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("序列化失败：%v", err)
	}
	return string(raw)
}
