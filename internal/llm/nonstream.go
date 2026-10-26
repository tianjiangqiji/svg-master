package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// 非流式兜底：网关不支持 SSE 时（Settings.Agent.StreamDisabled），
// 响应是一次性 JSON。这里按协议解码后，产出与流式一致的事件序列。

// nonStreamBody 读取并限制大小，避免异常网关把内存打爆。
func nonStreamBody(body io.ReadCloser, timeout ...time.Duration) ([]byte, error) {
	defer body.Close()
	idle := DefaultTransport().IdleTimeout
	if len(timeout) > 0 {
		idle = timeout[0]
	}
	wd := newIdleWatchdog(idle, body)
	defer wd.stop()
	raw, err := io.ReadAll(io.LimitReader(&activityReader{reader: body, wd: wd}, (16<<20)+1))
	if err != nil {
		if wd.fired() {
			return nil, fmt.Errorf("读取非流式响应超时：%s 内无数据", idle)
		}
		return nil, fmt.Errorf("读取响应体失败：%w", err)
	}
	if len(raw) > 16<<20 {
		return nil, noRetry(fmt.Errorf("非流式响应超出 16 MiB 上限"))
	}
	return raw, nil
}

type activityReader struct {
	reader io.Reader
	wd     *idleWatchdog
}

func (r *activityReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.wd.reset()
	}
	return n, err
}

// eventsChan 把已解码的事件切片包成 channel；ctx 取消时提前关闭，保持接口一致。
func eventsChan(ctx context.Context, evs []StreamEvent) <-chan StreamEvent {
	ch := make(chan StreamEvent, len(evs)+1)
	go func() {
		defer close(ch)
		for _, ev := range evs {
			select {
			case <-ctx.Done():
				return
			case ch <- ev:
			}
		}
	}()
	return ch
}

// decodeError 从常见错误结构里提取信息。
func decodeError(raw []byte) string {
	var probe struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &probe); err == nil {
		if probe.Error != nil && probe.Error.Message != "" {
			return probe.Error.Message
		}
		if probe.Message != "" {
			return probe.Message
		}
	}
	return truncateRunes(strings.TrimSpace(string(raw)), errCharLimit)
}

// ---------------- openai chat ----------------

func (c *openaiChat) decodeNonStream(raw []byte) []StreamEvent {
	if err := envelopeError(raw, "openai-chat"); err != nil {
		return errorEvents(err)
	}
	var out struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content          string `json:"content"`
				Refusal          string `json:"refusal"`
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage *chatUsageIn `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return errorEvents(noRetry(fmt.Errorf("解析 openai-chat 响应失败：%w", err)))
	}
	var evs []StreamEvent
	if out.Usage != nil {
		evs = append(evs, StreamEvent{Kind: EvUsage, Usage: usageFromChat(out.Usage)})
	}
	if len(out.Choices) == 0 {
		return append(evs, errorEvents(noRetry(fmt.Errorf("openai-chat 响应中没有 choices：%s", decodeError(raw))))...)
	}
	if reason := out.Choices[0].FinishReason; reason == "length" || reason == "content_filter" {
		return append(evs, errorEvents(stopReasonError("openai-chat", reason))...)
	}
	msg := out.Choices[0].Message
	if msg.Refusal != "" {
		return append(evs, errorEvents(remoteError("openai-chat", "refusal", msg.Refusal))...)
	}
	if s := msg.ReasoningContent; s != "" {
		evs = append(evs, StreamEvent{Kind: EvThinking, Text: s})
	} else if s := msg.Reasoning; s != "" {
		evs = append(evs, StreamEvent{Kind: EvThinking, Text: s})
	}
	if msg.Content != "" {
		evs = append(evs, StreamEvent{Kind: EvText, Text: msg.Content})
	}
	for i, tc := range msg.ToolCalls {
		evs = append(evs, StreamEvent{Kind: EvToolStart, Index: i, ID: tc.ID, Name: tc.Function.Name})
		if tc.Function.Arguments != "" {
			evs = append(evs, StreamEvent{Kind: EvToolDelta, Index: i, ID: tc.ID, Text: tc.Function.Arguments})
		}
	}
	return append(evs, StreamEvent{Kind: EvDone})
}

// ---------------- openai responses ----------------

func (c *openaiResponses) decodeNonStream(raw []byte) []StreamEvent {
	var out respResponseIn
	if err := json.Unmarshal(raw, &out); err != nil {
		return errorEvents(noRetry(fmt.Errorf("解析 openai-responses 响应失败：%w", err)))
	}
	var evs []StreamEvent
	if out.Usage != nil {
		evs = append(evs, StreamEvent{Kind: EvUsage, Usage: usageFromResp(out.Usage)})
	}
	if err := out.checkStatus(""); err != nil {
		return append(evs, errorEvents(err)...)
	}
	evs = append(evs, StreamEvent{Kind: EvServiceTier, Text: out.ServiceTier})
	if out.Output == nil {
		return append(evs, errorEvents(noRetry(fmt.Errorf("openai-responses 响应缺少 output：%s", decodeError(raw))))...)
	}
	ch := make(chan StreamEvent, 2*len(out.Output)+4)
	p := newPump(context.Background(), ch, nil, 0)
	st := &respState{byItem: map[string]*respToolRef{}, items: out.Output}
	err := st.finishOutput(p)
	close(ch)
	for ev := range ch {
		evs = append(evs, ev)
	}
	if err != nil {
		return append(evs, errorEvents(err)...)
	}
	return append(evs, StreamEvent{Kind: EvDone})
}

// ---------------- anthropic ----------------

func (c *anthropicClient) decodeNonStream(raw []byte) []StreamEvent {
	if err := envelopeError(raw, "anthropic"); err != nil {
		return errorEvents(err)
	}
	var native struct {
		Content    []json.RawMessage `json:"content"`
		StopReason string            `json:"stop_reason"`
		Usage      *anthropicUsageIn `json:"usage"`
	}
	if err := json.Unmarshal(raw, &native); err != nil {
		return errorEvents(noRetry(fmt.Errorf("解析 anthropic 响应失败：%w", err)))
	}
	var evs []StreamEvent
	if native.Usage != nil {
		us := &anthropicUsageState{}
		evs = append(evs, StreamEvent{Kind: EvUsage, Usage: us.merge(native.Usage)})
	}
	if native.Content == nil {
		return append(evs, errorEvents(noRetry(fmt.Errorf("anthropic 响应缺少 content：%s", decodeError(raw))))...)
	}
	switch native.StopReason {
	case "max_tokens", "model_context_window_exceeded", "refusal":
		return append(evs, errorEvents(stopReasonError("anthropic", native.StopReason))...)
	}
	var out struct {
		Content []struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			Thinking string          `json:"thinking"`
			ID       string          `json:"id"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return errorEvents(noRetry(fmt.Errorf("解析 anthropic 响应失败：%w", err)))
	}
	idx := 0
	for _, b := range out.Content {
		switch b.Type {
		case "text":
			if b.Text != "" {
				evs = append(evs, StreamEvent{Kind: EvText, Text: b.Text})
			}
		case "thinking":
			if b.Thinking != "" {
				evs = append(evs, StreamEvent{Kind: EvThinking, Text: b.Thinking})
			}
		case "tool_use":
			evs = append(evs, StreamEvent{Kind: EvToolStart, Index: idx, ID: b.ID, Name: b.Name})
			if len(b.Input) > 0 {
				evs = append(evs, StreamEvent{Kind: EvToolDelta, Index: idx, ID: b.ID, Text: rawArgsString(b.Input)})
			}
			idx++
		}
	}
	if len(native.Content) > 0 {
		evs = append(evs, StreamEvent{Kind: EvOutput, Output: &NativeOutput{Protocol: c.Protocol(), Items: native.Content}})
	}
	if native.StopReason == "pause_turn" {
		evs = append(evs, StreamEvent{Kind: EvContinue})
	}
	return append(evs, StreamEvent{Kind: EvDone})
}

// ---------------- gemini ----------------

func (c *geminiClient) decodeNonStream(raw []byte, model ...string) []StreamEvent {
	var out geminiResponseIn
	if err := json.Unmarshal(raw, &out); err != nil {
		return errorEvents(noRetry(fmt.Errorf("解析 gemini 响应失败：%w", err)))
	}
	var evs []StreamEvent
	if u := out.UsageMetadata; u != nil {
		total := u.TotalTokenCount
		if total == 0 {
			total = u.PromptTokenCount + u.CandidatesTokenCount + u.ThoughtsTokenCount
		}
		evs = append(evs, StreamEvent{Kind: EvUsage, Usage: &Usage{
			InputTokens:     u.PromptTokenCount,
			OutputTokens:    u.CandidatesTokenCount + u.ThoughtsTokenCount,
			CacheReadTokens: u.CachedContentTokenCount,
			TotalTokens:     total,
		}})
	}
	if out.Error != nil && out.Error.Message != "" {
		return append(evs, errorEvents(remoteError("gemini", fmt.Sprint(out.Error.Code), out.Error.Message))...)
	}
	if out.PromptFeedback != nil && out.PromptFeedback.BlockReason != "" {
		return append(evs, errorEvents(stopReasonError("gemini", out.PromptFeedback.BlockReason))...)
	}
	if len(out.Candidates) == 0 {
		return append(evs, errorEvents(noRetry(fmt.Errorf("gemini 响应中没有 candidates：%s", decodeError(raw))))...)
	}
	idx := 0
	for i, cand := range out.Candidates {
		if i > 0 {
			break
		}
		if cand.FinishReason != "" && cand.FinishReason != "STOP" {
			return append(evs, errorEvents(stopReasonError("gemini", cand.FinishReason))...)
		}
		for _, part := range cand.Content.Parts {
			switch {
			case part.FunctionCall != nil:
				id := part.FunctionCall.ID
				if id == "" {
					id = geminiGeneratedID(idx, part.FunctionCall.Name)
				}
				evs = append(evs, StreamEvent{Kind: EvToolStart, Index: idx, ID: id, Name: part.FunctionCall.Name})
				if len(part.FunctionCall.Args) > 0 {
					evs = append(evs, StreamEvent{Kind: EvToolDelta, Index: idx, ID: id, Text: string(part.FunctionCall.Args)})
				}
				idx++
			case part.Text != "":
				kind := EvText
				if part.Thought {
					kind = EvThinking
				}
				evs = append(evs, StreamEvent{Kind: kind, Text: sanitizeText(part.Text)})
			}
		}
	}
	native := &geminiOutputState{}
	if len(model) > 0 {
		native.withID = geminiNeedsToolID(model[0])
	}
	if err := native.capture(sseEvent{Data: string(raw)}); err != nil {
		return errorEvents(err)
	}
	if len(native.items) > 0 {
		evs = append(evs, StreamEvent{Kind: EvOutput, Output: &NativeOutput{Protocol: c.Protocol(), Items: native.items}})
	}
	return append(evs, StreamEvent{Kind: EvDone})
}
