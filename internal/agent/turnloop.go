package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"svg-master/internal/llm"
)

// turn-loop 容错常量（对齐 pi-browser-agent 的 turn-loop 语义）。
const (
	maxStreamRetries           = 3                // 单轮模型请求的最大重试次数
	maxEmptyRetries            = 2                // 空回复重试次数
	defaultMaxModelSteps       = 40               // 未配置时的模型轮数上限
	repeatedErrorCallThreshold = 3                // 同一工具连续同错熔断阈值
	streamWatchdog             = 5 * time.Minute  // 无任何事件的看门狗
	retryBaseDelay             = 2 * time.Second  // 重试基础退避
	retryMaxDelay              = 30 * time.Second // 重试最大退避
)

// streamResult 一次流式调用的累积结果。
type streamResult struct {
	text         string
	thinking     string
	toolCalls    []llm.ToolCall
	usage        *llm.Usage
	output       *llm.NativeOutput
	continueTurn bool
}

// toolCallAcc 单个工具调用的累积器（按 Index 归并）。
type toolCallAcc struct {
	id      string
	name    string
	args    strings.Builder
	hasArgs bool
	// emit_svg 实时预览节流用
	prevLen    int
	prevRawLen int
	prevAt     time.Time
	prevPeek   bool
}

// noteStreamRetry 记录一次流式重试（计数落盘到运行时状态）。
func (e *Engine) noteStreamRetry() {
	if e.impl.st == nil {
		return
	}
	e.impl.st.mu.Lock()
	e.impl.st.streamRetry++
	e.impl.st.mu.Unlock()
}

// streamRetries 当前累计的流式重试次数。
func (e *Engine) streamRetries() int {
	if e.impl.st == nil {
		return 0
	}
	e.impl.st.mu.Lock()
	defer e.impl.st.mu.Unlock()
	return e.impl.st.streamRetry
}

// streamOnce 发起一次模型请求并消费流，带重试与看门狗。
func (e *Engine) streamOnce(ctx context.Context, req llm.Request) (*streamResult, error) {
	var lastErr error
	var total *llm.Usage
	failure := func(err error) (*streamResult, error) { return &streamResult{usage: total}, err }
	for attempt := 0; attempt <= maxStreamRetries; attempt++ {
		if attempt > 0 {
			e.noteStreamRetry()
			delay := retryDelay(attempt - 1)
			e.logf("warn", "模型请求失败（%v），%s 后重试（第 %d/%d 次）", lastErr, delay, attempt, maxStreamRetries)
			if err := sleepCtx(ctx, delay); err != nil {
				return failure(context.Canceled)
			}
		}

		res, err := e.streamAttempt(ctx, req)
		if res != nil && res.usage != nil {
			if total == nil {
				total = &llm.Usage{}
			}
			total.InputTokens += res.usage.InputTokens
			total.OutputTokens += res.usage.OutputTokens
			total.CacheReadTokens += res.usage.CacheReadTokens
			total.CacheWriteTokens += res.usage.CacheWriteTokens
			total.TotalTokens += res.usage.TotalTokens
		}
		if err == nil {
			if ctx.Err() != nil {
				return failure(context.Canceled)
			}
			res.usage = total
			return res, nil
		}
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return failure(context.Canceled)
		}
		if !llm.Retryable(err) {
			return failure(err)
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("模型请求失败")
	}
	return failure(fmt.Errorf("模型请求连续 %d 次失败：%w", maxStreamRetries+1, lastErr))
}

// streamAttempt 单次流式请求：带 5 分钟无事件看门狗。
func (e *Engine) streamAttempt(ctx context.Context, req llm.Request) (*streamResult, error) {
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch, err := e.deps.Client.Stream(callCtx, req)
	if err != nil {
		return nil, err
	}
	if ch == nil {
		return nil, errors.New("模型客户端返回空事件流")
	}

	res := &streamResult{}
	accs := map[int]*toolCallAcc{}
	var order []int

	watchdog := time.NewTimer(streamWatchdog)
	defer watchdog.Stop()
	reset := func() {
		if !watchdog.Stop() {
			select {
			case <-watchdog.C:
			default:
			}
		}
		watchdog.Reset(streamWatchdog)
	}

	for {
		select {
		case <-ctx.Done():
			// 必须排空 channel，否则客户端 goroutine 泄漏。
			go drain(ch)
			return res, context.Canceled

		case <-watchdog.C:
			cancel()
			go drain(ch)
			return res, fmt.Errorf("模型响应超过 %s 没有任何事件，已中止本次请求", streamWatchdog)

		case ev, ok := <-ch:
			if !ok {
				if ctx.Err() != nil {
					return res, context.Canceled
				}
				return finishStream(res, accs, order)
			}
			reset()
			if done, err := e.handleStreamEvent(ev, res, accs, &order); err != nil {
				go drain(ch)
				return res, err
			} else if done {
				go drain(ch)
				return finishStream(res, accs, order)
			}
		}
	}
}

// handleStreamEvent 处理单个流事件；done=true 表示流已正常结束。
func (e *Engine) handleStreamEvent(ev llm.StreamEvent, res *streamResult, accs map[int]*toolCallAcc, order *[]int) (bool, error) {
	switch ev.Kind {
	case llm.EvText:
		if ev.Text == "" {
			return false, nil
		}
		res.text += ev.Text
		e.emit(Event{Kind: EvText, Text: ev.Text})

	case llm.EvThinking:
		if ev.Text == "" {
			return false, nil
		}
		res.thinking += ev.Text
		e.emit(Event{Kind: EvThinking, Text: ev.Text})

	case llm.EvToolStart:
		acc := accs[ev.Index]
		if acc == nil {
			acc = &toolCallAcc{}
			accs[ev.Index] = acc
			*order = append(*order, ev.Index)
		}
		if ev.ID != "" {
			acc.id = ev.ID
		}
		if ev.Name != "" {
			acc.name = ev.Name
		}

	case llm.EvToolDelta:
		acc := accs[ev.Index]
		if acc == nil {
			acc = &toolCallAcc{}
			accs[ev.Index] = acc
			*order = append(*order, ev.Index)
		}
		if ev.ID != "" && acc.id == "" {
			acc.id = ev.ID
		}
		if ev.Name != "" && acc.name == "" {
			acc.name = ev.Name
		}
		if ev.Text != "" {
			if ev.Replace {
				acc.args.Reset()
			}
			acc.args.WriteString(ev.Text)
			acc.hasArgs = true
			// emit_svg 的参数里就是整段 SVG：边收边抽出已到达的部分推给上层，
			// 前端据此做「补全渲染」，实现逐笔长出来的实时预览。
			if acc.name == ToolEmitSVG {
				e.emitSVGPreview(acc)
			}
		}

	case llm.EvUsage:
		if ev.Usage != nil {
			u := *ev.Usage
			res.usage = &u
		}
	case llm.EvServiceTier:
		e.emit(Event{Kind: EvServiceTier, Text: ev.Text})
		if e.deps.ServiceTier == "priority" {
			switch ev.Text {
			case "priority", "fast":
				e.logf("info", "本轮已使用 Fast 加速")
			case "default":
				e.logf("warn", "已请求 Fast，但服务端本轮按标准档位处理")
			default:
				e.logf("info", "已请求 Fast，服务端未确认使用 Fast 档位")
			}
		}

	case llm.EvError:
		if ev.Cause != nil {
			return false, ev.Cause
		}
		msg := strings.TrimSpace(ev.Err)
		if msg == "" {
			msg = "模型流返回未知错误"
		}
		return false, errors.New(msg)

	case llm.EvOutput:
		res.output = ev.Output
	case llm.EvContinue:
		res.continueTurn = true

	case llm.EvDone:
		return true, nil

	default:
		// 未知事件忽略，保持向前兼容。
	}
	return false, nil
}

// maxSVGPreview 实时预览最多推这么长的 SVG 片段（超出后不再推，等最终结果）。
const maxSVGPreview = 96 * 1024

// svgPreviewMinGrow 片段至少再长这么多才推一次，避免逐字符刷屏。
const svgPreviewMinGrow = 32

// svgPreviewMinLen 片段短于这个长度时先不推（此时多半只有 <svg 开头，没意义）。
const svgPreviewMinLen = 48

// svgPreviewInterval 距上次推送超过这个时长时，即使增长很小也补推一次。
const svgPreviewInterval = 150 * time.Millisecond

// emitSVGPreview 从累积中的工具参数里抽出 svg 字段的当前片段，节流后推给上层。
func (e *Engine) emitSVGPreview(acc *toolCallAcc) {
	raw := acc.args.String()
	if len(raw) > maxSVGPreview {
		return
	}
	if len(raw) == acc.prevRawLen {
		return
	}
	acc.prevRawLen = len(raw)
	part, ok := extractSVGArg(raw)
	if !ok || len(part) < svgPreviewMinLen {
		return
	}
	now := time.Now()
	grow := len(part) - acc.prevLen
	if grow < svgPreviewMinGrow && now.Sub(acc.prevAt) < svgPreviewInterval {
		return
	}
	if acc.prevPeek && grow <= 0 {
		return
	}
	acc.prevLen = len(part)
	acc.prevAt = now
	acc.prevPeek = true
	e.emit(Event{Kind: EvToolDelta, CallID: acc.id, Tool: acc.name, Text: part})
}

// extractSVGArg 从「尚未闭合」的 JSON 参数串里取出 svg 键对应的字符串值。
// 允许 JSON 不完整：只解析到值字符串当前为止的部分，做反转义后返回。
func extractSVGArg(raw string) (string, bool) {
	key := `"svg"`
	idx := strings.Index(raw, key)
	if idx < 0 {
		return "", false
	}
	i := idx + len(key)
	// 跳过空白与冒号
	for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t' || raw[i] == '\n' || raw[i] == '\r' || raw[i] == ':') {
		i++
	}
	if i >= len(raw) || raw[i] != '"' {
		return "", false
	}
	i++
	var b strings.Builder
	b.Grow(len(raw) - i)
	for i < len(raw) {
		c := raw[i]
		if c == '"' {
			// 值结束
			return b.String(), true
		}
		if c != '\\' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 >= len(raw) {
			// 转义序列还没到齐，停在这里
			break
		}
		esc := raw[i+1]
		switch esc {
		case 'n':
			b.WriteByte('\n')
			i += 2
		case 't':
			b.WriteByte('\t')
			i += 2
		case 'r':
			b.WriteByte('\r')
			i += 2
		case '"':
			b.WriteByte('"')
			i += 2
		case '\\':
			b.WriteByte('\\')
			i += 2
		case '/':
			b.WriteByte('/')
			i += 2
		case 'u':
			if i+5 >= len(raw) {
				return b.String(), true
			}
			var r rune
			if _, err := fmt.Sscanf(raw[i+2:i+6], "%04x", &r); err != nil {
				return b.String(), true
			}
			b.WriteRune(r)
			i += 6
		default:
			b.WriteByte(esc)
			i += 2
		}
	}
	return b.String(), true
}

// finishStream 把累积器整理成工具调用列表。
func finishStream(res *streamResult, accs map[int]*toolCallAcc, order []int) (*streamResult, error) {
	for _, idx := range order {
		acc := accs[idx]
		if acc == nil || strings.TrimSpace(acc.name) == "" {
			continue
		}
		args := strings.TrimSpace(acc.args.String())
		if args == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			// 参数不合法时保留原文，交给工具层报中文错误。
			args = fmt.Sprintf("%q", args)
			args = "{\"_raw\":" + args + "}"
		}
		call := llm.ToolCall{
			ID:        acc.id,
			Name:      acc.name,
			Arguments: json.RawMessage(args),
		}
		if call.ID == "" {
			call.ID = fmt.Sprintf("call_%d_%s", idx, acc.name)
		}
		res.toolCalls = append(res.toolCalls, call)
	}
	return res, nil
}

// drain 排空 channel，防止客户端 goroutine 泄漏。
func drain(ch <-chan llm.StreamEvent) {
	if ch == nil {
		return
	}
	for range ch {
	}
}

// retryDelay 指数退避：2s, 4s, 8s… 上限 30s。
func retryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	d := retryBaseDelay
	for i := 0; i < attempt; i++ {
		d *= 2
		if d >= retryMaxDelay {
			return retryMaxDelay
		}
	}
	if d > retryMaxDelay {
		d = retryMaxDelay
	}
	return d
}

// sleepCtx 可取消的等待。
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// breaker 同一工具连续同错熔断器。
type breaker struct {
	lastName string
	lastErr  string
	repeat   int
}

// newBreaker 构造熔断器。
func newBreaker() *breaker { return &breaker{} }

// hit 判断本次调用是否应熔断；返回 (是否熔断, 注入提示)。
func (b *breaker) hit(call llm.ToolCall, ctx context.Context) (bool, string) {
	if b.repeat < repeatedErrorCallThreshold {
		return false, ""
	}
	if call.Name != b.lastName {
		return false, ""
	}
	hint := fmt.Sprintf("工具 %s 最近 %d 次调用都以相同错误结束（%s）。请改变策略：检查参数、换用其它工具，或直接基于已有信息作答。",
		call.Name, b.repeat, truncate(b.lastErr, 160))
	return true, hint
}

// record 记录本次调用的结果，维护连续同错计数。
func (b *breaker) record(call llm.ToolCall, oc toolOutcome) {
	if !oc.IsError {
		b.lastName, b.lastErr, b.repeat = "", "", 0
		return
	}
	key := truncate(oc.Content, 240)
	if call.Name == b.lastName && key == b.lastErr {
		b.repeat++
		return
	}
	b.lastName, b.lastErr, b.repeat = call.Name, key, 1
}

// truncate 按 rune 截断字符串。
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
