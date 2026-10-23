package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"svg-master/internal/config"
	"svg-master/internal/llm"
	"svg-master/internal/svg"
)

// ---- 测试替身 ----

// testSVG 一段合法、可缩放的最小 SVG（含 viewBox 与纯矢量图元）。
const testSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1024 1024">` +
	`<rect x="0" y="0" width="1024" height="1024" fill="#f7f7f5"/>` +
	`<circle cx="512" cy="512" r="220" fill="#3366cc"/>` +
	`</svg>`

// fakeStep 一次 Stream 调用的脚本。
type fakeStep struct {
	events  []llm.StreamEvent
	block   bool          // true 表示挂起直到 ctx 被取消（用于 Abort 测试）
	started chan struct{} // 可选：被调用时通知测试
}

// fakeClient 脚本化的 llm.Client：第 n 次 Stream 播放第 n 个脚本，超出后重复最后一个。
type fakeClient struct {
	mu    sync.Mutex
	calls int
	steps []fakeStep
}

func (f *fakeClient) Protocol() string { return "fake" }

func (f *fakeClient) Stream(ctx context.Context, _ llm.Request) (<-chan llm.StreamEvent, error) {
	f.mu.Lock()
	idx := f.calls
	f.calls++
	f.mu.Unlock()

	if len(f.steps) == 0 {
		return nil, errors.New("测试脚本为空")
	}
	if idx >= len(f.steps) {
		idx = len(f.steps) - 1
	}
	step := f.steps[idx]
	if step.started != nil {
		select {
		case step.started <- struct{}{}:
		default:
		}
	}

	ch := make(chan llm.StreamEvent, len(step.events)+1)
	if step.block {
		go func() {
			defer close(ch)
			<-ctx.Done()
		}()
		return ch, nil
	}
	for _, ev := range step.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

// callCount 已发起的请求次数。
func (f *fakeClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// sink 收集 Emit 事件。
type sink struct {
	mu  sync.Mutex
	evs []Event
}

func (s *sink) emit(ev Event) {
	s.mu.Lock()
	s.evs = append(s.evs, ev)
	s.mu.Unlock()
}

func (s *sink) all() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.evs...)
}

func (s *sink) count(kind string) int {
	n := 0
	for _, ev := range s.all() {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

func (s *sink) find(kind string) (Event, bool) {
	for _, ev := range s.all() {
		if ev.Kind == kind {
			return ev, true
		}
	}
	return Event{}, false
}

// ---- 辅助构造 ----

// testDeps 一份可用的依赖（默认不启用压缩）。
func testDeps(c *fakeClient, emit func(Event)) Deps {
	return Deps{
		Client:    c,
		Settings:  config.AgentSettings{MaxSteps: 8, AutoValidate: true, CompressOn: false},
		Canvas:    config.CanvasPreset{Name: "1:1 方形", Width: 1024, Height: 1024, ViewBox: "0 0 1024 1024"},
		Model:     "test-model",
		MaxTokens: 4096,
		Emit:      emit,
	}
}

// textStep 一轮纯文本回复。
func textStep(text string) fakeStep {
	return fakeStep{events: []llm.StreamEvent{
		{Kind: llm.EvText, Text: text},
		{Kind: llm.EvDone},
	}}
}

// toolStep 一轮工具调用（参数一次性给出）。
func toolStep(id, name string, args any) fakeStep {
	raw, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return fakeStep{events: []llm.StreamEvent{
		{Kind: llm.EvToolStart, Index: 0, ID: id, Name: name},
		{Kind: llm.EvToolDelta, Index: 0, Text: string(raw)},
		{Kind: llm.EvDone},
	}}
}

// ---- 用例 ----

// 单轮无工具：模型直接回文本，任务结束。
func TestRunSingleTurnNoTool(t *testing.T) {
	c := &fakeClient{steps: []fakeStep{textStep("这是一个圆。")}}
	s := &sink{}
	eng := New(testDeps(c, s.emit))

	stats, err := eng.Run(context.Background(), Task{Prompt: "画一个圆"})
	if err != nil {
		t.Fatalf("Run 返回错误：%v", err)
	}
	if stats.ModelSteps != 1 {
		t.Fatalf("ModelSteps = %d，期望 1", stats.ModelSteps)
	}
	if stats.ToolCalls != 0 || stats.ToolErrors != 0 {
		t.Fatalf("ToolCalls=%d ToolErrors=%d，期望都是 0", stats.ToolCalls, stats.ToolErrors)
	}
	if s.count(EvText) != 1 {
		t.Fatalf("EvText 事件数 = %d，期望 1", s.count(EvText))
	}
	if c.callCount() != 1 {
		t.Fatalf("Stream 调用次数 = %d，期望 1", c.callCount())
	}
	if stats.ElapsedMS < 0 {
		t.Fatalf("ElapsedMS 异常：%d", stats.ElapsedMS)
	}
}

// 两轮带工具：第一轮 emit_svg，第二轮收尾文本。
func TestRunTwoTurnsWithEmitSVG(t *testing.T) {
	c := &fakeClient{steps: []fakeStep{
		toolStep("call_1", ToolEmitSVG, map[string]string{"svg": testSVG, "note": "圆"}),
		textStep("已完成。"),
	}}
	s := &sink{}
	eng := New(testDeps(c, s.emit))

	stats, err := eng.Run(context.Background(), Task{Prompt: "画一个圆"})
	if err != nil {
		t.Fatalf("Run 返回错误：%v", err)
	}
	if stats.ModelSteps != 2 {
		t.Fatalf("ModelSteps = %d，期望 2", stats.ModelSteps)
	}
	if stats.ToolCalls != 1 || stats.ToolErrors != 0 {
		t.Fatalf("ToolCalls=%d ToolErrors=%d，期望 1/0", stats.ToolCalls, stats.ToolErrors)
	}
	if s.count(EvToolStart) != 1 || s.count(EvToolEnd) != 1 {
		t.Fatalf("工具事件数 start=%d end=%d，期望各 1", s.count(EvToolStart), s.count(EvToolEnd))
	}
	svgEv, ok := s.find(EvSVG)
	if !ok || strings.TrimSpace(svgEv.SVG) == "" {
		t.Fatal("未收到带内容的 EvSVG 事件")
	}
	if !strings.Contains(svgEv.SVG, "<svg") {
		t.Fatalf("EvSVG 内容不像 SVG：%q", svgEv.SVG)
	}
	if eng.impl.snapshotSVG() == "" {
		t.Fatal("emit_svg 后引擎内未保存 SVG")
	}
}

// 工具报错后重试成功：先给空 svg（报错），再给合法 svg。
func TestRunToolErrorThenRetry(t *testing.T) {
	c := &fakeClient{steps: []fakeStep{
		toolStep("call_1", ToolEmitSVG, map[string]string{"svg": ""}),
		toolStep("call_2", ToolEmitSVG, map[string]string{"svg": testSVG}),
		textStep("已修正。"),
	}}
	s := &sink{}
	eng := New(testDeps(c, s.emit))

	stats, err := eng.Run(context.Background(), Task{Prompt: "画一个圆"})
	if err != nil {
		t.Fatalf("Run 返回错误：%v", err)
	}
	if stats.ToolCalls != 2 {
		t.Fatalf("ToolCalls = %d，期望 2", stats.ToolCalls)
	}
	if stats.ToolErrors != 1 {
		t.Fatalf("ToolErrors = %d，期望 1", stats.ToolErrors)
	}
	if stats.ModelSteps != 3 {
		t.Fatalf("ModelSteps = %d，期望 3", stats.ModelSteps)
	}
	if eng.impl.snapshotSVG() == "" {
		t.Fatal("重试成功后引擎内未保存 SVG")
	}
	var errEnd bool
	for _, ev := range s.all() {
		if ev.Kind == EvToolEnd && ev.Error != "" {
			errEnd = true
		}
	}
	if !errEnd {
		t.Fatal("未收到带 Error 的 EvToolEnd 事件")
	}
}

// emit_svg 必须逐字交付：不净化、不补 xmlns/viewBox、不包 transform。
func TestRunEmitSVGPassthrough(t *testing.T) {
	raw := "```svg\n" +
		`<svg viewBox="0 0 1024 1024" onload="x()"><script>alert(1)</script>` +
		`<image href="https://e.example/a.png"/><rect width="4" height="4"/>` +
		`<animate attributeName="r" values="5;9;5" dur="1.2s" repeatCount="indefinite"/></svg>` +
		"\n```"
	want := `<svg viewBox="0 0 1024 1024" onload="x()"><script>alert(1)</script>` +
		`<image href="https://e.example/a.png"/><rect width="4" height="4"/>` +
		`<animate attributeName="r" values="5;9;5" dur="1.2s" repeatCount="indefinite"/></svg>`

	c := &fakeClient{steps: []fakeStep{
		toolStep("call_1", ToolEmitSVG, map[string]string{"svg": raw}),
		textStep("完成。"),
	}}
	s := &sink{}
	eng := New(testDeps(c, s.emit))

	if _, err := eng.Run(context.Background(), Task{Prompt: "画一个圆"}); err != nil {
		t.Fatalf("Run 返回错误：%v", err)
	}
	got := eng.impl.snapshotSVG()
	if got != want {
		t.Fatalf("emit_svg 改写了内容\n实际：%q\n期望：%q", got, want)
	}
	ev, ok := s.find(EvSVG)
	if !ok || ev.SVG != want {
		t.Fatalf("EvSVG 事件内容与交付内容不一致：%q", ev.SVG)
	}
	// SMIL 属性必须活着（旧 sanitize 会吃掉 dur/repeatCount/attributeName）
	for _, needle := range []string{`dur="1.2s"`, `repeatCount="indefinite"`, `attributeName="r"`, "<script", "onload"} {
		if !strings.Contains(got, needle) {
			t.Fatalf("内容应保留 %q：%s", needle, got)
		}
	}
}

// 尺寸不符必须报 error（这是唯一保留的硬约束），且不自动改写。
func TestRunEmitSVGSizeMismatchError(t *testing.T) {
	wrong := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512" width="512" height="512"><rect width="4" height="4"/></svg>`
	c := &fakeClient{steps: []fakeStep{
		toolStep("call_1", ToolEmitSVG, map[string]string{"svg": wrong}),
		toolStep("call_2", ToolEmitSVG, map[string]string{"svg": testSVG}),
		textStep("已按画布重画。"),
	}}
	s := &sink{}
	eng := New(testDeps(c, s.emit))

	stats, err := eng.Run(context.Background(), Task{Prompt: "画一个圆"})
	if err != nil {
		t.Fatalf("Run 返回错误：%v", err)
	}
	if stats.ToolErrors != 1 {
		t.Fatalf("ToolErrors = %d，期望 1（尺寸不符应报错逼修）", stats.ToolErrors)
	}
	var errText string
	for _, ev := range s.all() {
		if ev.Kind == EvToolEnd && ev.Error != "" {
			errText = ev.Error
		}
	}
	if !strings.Contains(errText, "canvasViewBox") && !strings.Contains(errText, "画布要求") {
		t.Fatalf("错误应指出画布尺寸要求：%q", errText)
	}
	// 第一次虽然报错，内容也照样原样存了（不改写）
	if eng.impl.snapshotSVG() != testSVG {
		t.Fatalf("最终应保存重画后的原文：%q", eng.impl.snapshotSVG())
	}
}

// MaxSteps 熔断：模型一直调工具，达到上限后停止。
func TestRunMaxStepsBreaker(t *testing.T) {
	c := &fakeClient{steps: []fakeStep{
		toolStep("call_1", ToolAskUser, map[string]any{"question": "继续吗？"}),
	}}
	s := &sink{}
	deps := testDeps(c, s.emit)
	deps.Settings.MaxSteps = 2
	eng := New(deps)

	stats, err := eng.Run(context.Background(), Task{Prompt: "一直问"})
	if err != nil {
		t.Fatalf("Run 返回错误：%v", err)
	}
	if stats.ModelSteps != 2 {
		t.Fatalf("ModelSteps = %d，期望 2（受 MaxSteps 限制）", stats.ModelSteps)
	}
	if c.callCount() != 2 {
		t.Fatalf("Stream 调用次数 = %d，期望 2", c.callCount())
	}
	if s.count(EvAsk) != 2 {
		t.Fatalf("EvAsk 事件数 = %d，期望 2", s.count(EvAsk))
	}
}

// Abort 生效：请求挂起时取消，Run 返回 context.Canceled。
func TestRunAbort(t *testing.T) {
	started := make(chan struct{}, 1)
	c := &fakeClient{steps: []fakeStep{{block: true, started: started}}}
	s := &sink{}
	eng := New(testDeps(c, s.emit))

	type outcome struct {
		stats Stats
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		st, err := eng.Run(context.Background(), Task{Prompt: "画一个圆"})
		done <- outcome{stats: st, err: err}
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("等待 Stream 被调用超时")
	}
	eng.Abort()

	select {
	case out := <-done:
		if !errors.Is(out.err, context.Canceled) {
			t.Fatalf("Run 返回错误 = %v，期望 context.Canceled", out.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Abort 后 Run 未返回")
	}
}

// Compress → Decompress 可逆：折叠后投影变短，展开后恢复。
func TestCompressorCompressDecompressReversible(t *testing.T) {
	comp := newCompressor(128000, 75)
	if comp == nil {
		t.Fatal("窗口足够时不应返回 nil")
	}
	msgs := sampleMessages(5)

	if _, nudge := comp.Project(msgs, 100); nudge != "" {
		t.Fatalf("低用量不应产生 nudge，实际：%q", nudge)
	}
	proj0, _ := comp.Project(msgs, 100)
	before := len(proj0)

	created, tokens, warnings, err := comp.Compress([]CompressRange{
		{StartRef: "m00001", EndRef: "m00002", Topic: "准备", Summary: "用户要求画一个圆，画布 1024×1024。"},
	})
	if err != nil {
		t.Fatalf("Compress 返回错误：%v", err)
	}
	if created != 1 || tokens <= 0 {
		t.Fatalf("Compress created=%d tokens=%d，期望 1/正数", created, tokens)
	}
	if len(warnings) != 0 {
		t.Fatalf("出现意外告警：%v", warnings)
	}

	proj, _ := comp.Project(msgs, 100)
	if len(proj) != len(msgs)-2+1 {
		t.Fatalf("折叠后投影长度 = %d，期望 %d", len(proj), len(msgs)-2+1)
	}
	if before != len(msgs) {
		t.Fatalf("折叠前投影长度 = %d，期望 %d", before, len(msgs))
	}
	found := false
	for _, m := range proj {
		if strings.Contains(m.Text(), "上下文压缩块 b001") {
			found = true
		}
	}
	if !found {
		t.Fatal("投影里找不到压缩块摘要消息")
	}

	restored, preview, err := comp.Decompress("b001")
	if err != nil {
		t.Fatalf("Decompress 返回错误：%v", err)
	}
	if restored != 2 || strings.TrimSpace(preview) == "" {
		t.Fatalf("Decompress restored=%d preview=%q，期望 2/非空", restored, preview)
	}

	proj2, _ := comp.Project(msgs, 100)
	if len(proj2) != len(msgs) {
		t.Fatalf("展开后投影长度 = %d，期望 %d", len(proj2), len(msgs))
	}
}

// Project 绝不修改引擎持有的原始 messages。
func TestProjectKeepsOriginalMessages(t *testing.T) {
	comp := newCompressor(128000, 75)
	msgs := sampleMessages(4)
	msgs = append(msgs, llm.Message{Role: llm.RoleTool, Blocks: []llm.Block{{
		Type: llm.BlockToolResult,
		ToolResult: &llm.ToolResult{
			CallID:  "call_1",
			Name:    ToolEmitSVG,
			Content: `{"ok":true}`,
		},
	}}})
	snapshot := make([]string, len(msgs))
	for i, m := range msgs {
		snapshot[i] = m.Text() + "|" + toolContent(m)
	}

	if _, _, _, err := comp.Compress([]CompressRange{
		{StartRef: "m00001", EndRef: "m00003", Topic: "方案", Summary: "已确定圆形构图。"},
	}); err != nil {
		t.Fatalf("Compress 返回错误：%v", err)
	}
	for round := 0; round < 3; round++ {
		comp.Project(msgs, 100)
	}

	for i, m := range msgs {
		got := m.Text() + "|" + toolContent(m)
		if got != snapshot[i] {
			t.Fatalf("第 %d 条消息被改写：%q → %q", i, snapshot[i], got)
		}
		if strings.Contains(got, "<acp") {
			t.Fatalf("第 %d 条原始消息混入了 acp 标记：%q", i, got)
		}
	}
}

// nudge 在阈值触发。
func TestCompressorNudgeAtThreshold(t *testing.T) {
	comp := newCompressor(16000, 75)
	if comp == nil {
		t.Fatal("16000 窗口应启用压缩")
	}
	msgs := sampleMessages(6)

	if _, nudge := comp.Project(msgs, 1000); nudge != "" {
		t.Fatalf("远低于阈值不应 nudge，实际：%q", nudge)
	}
	_, nudge := comp.Project(msgs, 12000) // 12000/16000 = 75%
	if nudge == "" {
		t.Fatal("达到 75% 阈值应产生 nudge")
	}
	if !strings.Contains(nudge, "compress") || !strings.Contains(nudge, "m0000") {
		t.Fatalf("nudge 内容缺少工具名或 ref：%q", nudge)
	}
}

// 窗口过小时压缩退化为 nil（只保留 nudge 语义）。
func TestNewCompressorSmallWindow(t *testing.T) {
	if comp := newCompressor(minCompressWindow-1, 75); comp != nil {
		t.Fatal("窗口小于下限时应返回 nil")
	}
	if comp := newCompressor(minCompressWindow, 0); comp == nil {
		t.Fatal("窗口达到下限时应返回实现")
	}
}

// ---- 测试辅助 ----

// sampleMessages 生成 n 条 user/assistant 交替的样本消息。
func sampleMessages(n int) []llm.Message {
	out := make([]llm.Message, 0, n)
	for i := 0; i < n; i++ {
		role := llm.RoleUser
		if i%2 == 1 {
			role = llm.RoleAssistant
		}
		out = append(out, llm.TextMsg(role, "第"+itoa(i+1)+"条消息：讨论圆形的构图与配色。"))
	}
	return out
}

// toolContent 取出消息里第一个 tool_result 的正文（没有则空串）。
func toolContent(m llm.Message) string {
	for _, b := range m.Blocks {
		if b.Type == llm.BlockToolResult && b.ToolResult != nil {
			return b.ToolResult.Content
		}
	}
	return ""
}

// itoa 极简整数转字符串（避免额外引入 strconv 依赖干扰断言文本）。
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// 系统提示词只允许注入两件事：必须是 SVG + 尺寸规范；不允许任何风格规范。
func TestSystemPromptHasOutputSpec(t *testing.T) {
	canvas := config.CanvasPreset{Name: "2:3 竖版照片", Width: 1000, Height: 1500, ViewBox: "0 0 1000 1500"}
	sys := buildSystemPrompt(canvas)
	if !strings.Contains(sys, "输出规范（写死，优先于任何用户提示词）") {
		t.Fatal("系统提示词缺少写死的输出规范段")
	}
	if !strings.Contains(sys, "emit_svg") {
		t.Fatal("输出规范必须点名 emit_svg 交付通道")
	}
	if !strings.Contains(sys, "0 0 1000 1500") || !strings.Contains(sys, "1000") || !strings.Contains(sys, "1500") {
		t.Fatalf("系统提示词必须给出画布尺寸规范：%s", sys)
	}
	// 风格类规范一律不得再注入
	for _, banned := range []string{
		"色彩克制", "主色不超过", "安全留白", "铺满", "视觉重心", "缩小到 64px",
		"元素数量适中", "SVG 技术事实", "硬性原则", "工作流程", "完成标准", "上下文压缩",
	} {
		if strings.Contains(sys, banned) {
			t.Fatalf("系统提示词不应再注入风格/规范条目 %q：\n%s", banned, sys)
		}
	}
	// 默认提示词本身不该再夹带规范
	if strings.Contains(svg.DefaultPrompt, "SVG code only") {
		t.Fatalf("默认提示词不应包含输出规范：%q", svg.DefaultPrompt)
	}
}

// 系统提示词体积必须收敛到极小（旧版 5036 字节 / ~1531 tokens）。
func TestSystemPromptIsMinimal(t *testing.T) {
	canvas := config.CanvasPreset{Name: "1:1 方形", Width: 1024, Height: 1024, ViewBox: "0 0 1024 1024"}
	sys := buildSystemPrompt(canvas)
	if len(sys) > 1200 {
		t.Fatalf("系统提示词仍偏大：%d 字节\n%s", len(sys), sys)
	}
}
