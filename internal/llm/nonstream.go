package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// 非流式兜底：网关不支持 SSE 时（Settings.Agent.StreamDisabled），
// 响应是一次性 JSON。这里按协议解码后，产出与流式一致的事件序列。

// nonStreamBody 读取并限制大小，避免异常网关把内存打爆。
func nonStreamBody(body io.ReadCloser) ([]byte, error) {
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应体失败：%w", err)
	}
	return raw, nil
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
	var out struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
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
		return []StreamEvent{{Kind: EvError, Err: "解析 openai-chat 响应失败：" + err.Error()}}
	}
	var evs []StreamEvent
	if len(out.Choices) == 0 {
		return []StreamEvent{{Kind: EvError, Err: "openai-chat 响应中没有 choices：" + decodeError(raw)}}
	}
	msg := out.Choices[0].Message
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
	if out.Usage != nil {
		evs = append(evs, StreamEvent{Kind: EvUsage, Usage: usageFromChat(out.Usage)})
	}
	return append(evs, StreamEvent{Kind: EvDone})
}

// ---------------- openai responses ----------------

func (c *openaiResponses) decodeNonStream(raw []byte) []StreamEvent {
	var out struct {
		Output []struct {
			Type    string `json:"type"`
			CallID  string `json:"call_id"`
			Name    string `json:"name"`
			ArgsStr string `json:"arguments"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Summary []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"summary"`
		} `json:"output"`
		Usage *respUsageIn `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return []StreamEvent{{Kind: EvError, Err: "解析 openai-responses 响应失败：" + err.Error()}}
	}
	var evs []StreamEvent
	idx := 0
	for _, item := range out.Output {
		switch item.Type {
		case "reasoning":
			for _, s := range item.Summary {
				if s.Text != "" {
					evs = append(evs, StreamEvent{Kind: EvThinking, Text: s.Text})
				}
			}
		case "message":
			for _, p := range item.Content {
				if p.Text != "" {
					evs = append(evs, StreamEvent{Kind: EvText, Text: p.Text})
				}
			}
		case "function_call":
			evs = append(evs, StreamEvent{Kind: EvToolStart, Index: idx, ID: item.CallID, Name: item.Name})
			if item.ArgsStr != "" {
				evs = append(evs, StreamEvent{Kind: EvToolDelta, Index: idx, ID: item.CallID, Text: item.ArgsStr})
			}
			idx++
		}
	}
	if out.Usage != nil {
		evs = append(evs, StreamEvent{Kind: EvUsage, Usage: usageFromResp(out.Usage)})
	}
	return append(evs, StreamEvent{Kind: EvDone})
}

// ---------------- anthropic ----------------

func (c *anthropicClient) decodeNonStream(raw []byte) []StreamEvent {
	var out struct {
		Content []struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			Thinking string          `json:"thinking"`
			ID       string          `json:"id"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
		} `json:"content"`
		Usage *anthropicUsageIn `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return []StreamEvent{{Kind: EvError, Err: "解析 anthropic 响应失败：" + err.Error()}}
	}
	var evs []StreamEvent
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
	if out.Usage != nil {
		us := &anthropicUsageState{}
		evs = append(evs, StreamEvent{Kind: EvUsage, Usage: us.merge(out.Usage)})
	}
	return append(evs, StreamEvent{Kind: EvDone})
}

// ---------------- gemini ----------------

func (c *geminiClient) decodeNonStream(raw []byte) []StreamEvent {
	var out geminiResponseIn
	if err := json.Unmarshal(raw, &out); err != nil {
		return []StreamEvent{{Kind: EvError, Err: "解析 gemini 响应失败：" + err.Error()}}
	}
	if out.Error != nil && out.Error.Message != "" {
		return []StreamEvent{{Kind: EvError, Err: fmt.Sprintf("gemini 错误（HTTP %d）：%s", out.Error.Code, truncateRunes(out.Error.Message, errCharLimit))}}
	}
	var evs []StreamEvent
	idx := 0
	for _, cand := range out.Candidates {
		for _, part := range cand.Content.Parts {
			switch {
			case part.FunctionCall != nil:
				id := part.FunctionCall.ID
				if id == "" {
					id = geminiToolID("", part.FunctionCall.Name)
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
	return append(evs, StreamEvent{Kind: EvDone})
}
