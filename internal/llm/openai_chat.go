package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"svg-master/internal/config"
)

// openaiChat 实现 /v1/chat/completions（OpenAI 兼容，覆盖绝大多数中转站）。
type openaiChat struct {
	base, key string
	t         Transport
}

func (c *openaiChat) Protocol() string { return config.ProtoOpenAIChat }

// ---------------- 请求体 ----------------

type chatToolCallOut struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatMessageOut struct {
	Role             string            `json:"role"`
	Content          any               `json:"content,omitempty"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
	ToolCalls        []chatToolCallOut `json:"tool_calls,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
}

type chatToolOut struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type chatRequestOut struct {
	Model             string           `json:"model"`
	Messages          []chatMessageOut `json:"messages"`
	Tools             []chatToolOut    `json:"tools,omitempty"`
	MaxTokens         int              `json:"max_tokens,omitempty"`
	MaxCompletionToks int              `json:"max_completion_tokens,omitempty"`
	ReasoningEffort   string           `json:"reasoning_effort,omitempty"`
	Stream            bool             `json:"stream"`
	StreamOptions     *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
}

// part 一个多模态内容片段（text 或 image_url）。
type chatPartOut struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL    string `json:"url"`
		Detail string `json:"detail,omitempty"`
	} `json:"image_url,omitempty"`
}

func (c *openaiChat) buildBody(req Request) ([]byte, error) {
	out := chatRequestOut{Model: req.Model, Stream: req.Stream}
	if req.Stream {
		out.StreamOptions = &struct {
			IncludeUsage bool `json:"include_usage"`
		}{IncludeUsage: true}
	}
	if req.System != "" {
		out.Messages = append(out.Messages, chatMessageOut{Role: RoleSystem, Content: req.System})
	}
	for _, m := range req.Messages {
		out.Messages = append(out.Messages, encodeChatMessage(m)...)
	}
	for _, t := range req.Tools {
		var tool chatToolOut
		tool.Type = "function"
		tool.Function.Name = t.Name
		tool.Function.Description = t.Description
		tool.Function.Parameters = normalizeSchema(t.Parameters)
		out.Tools = append(out.Tools, tool)
	}
	if req.MaxTokens > 0 {
		// o 系列 / gpt-5 只接受 max_completion_tokens。
		if usesMaxCompletionTokens(req.Model) {
			out.MaxCompletionToks = req.MaxTokens
		} else {
			out.MaxTokens = req.MaxTokens
		}
	}
	if e := effortOf(req.Thinking); e != "" && InferModelCaps(req.Model).Reasoning {
		out.ReasoningEffort = e
	}
	return json.Marshal(out)
}

// encodeChatMessage 把中间表示消息转成一条或多条 OpenAI chat 消息
// （tool_result 块必须拆成独立的 role=tool 消息）。
func encodeChatMessage(m Message) []chatMessageOut {
	switch m.Role {
	case RoleTool:
		var outs []chatMessageOut
		for _, b := range m.Blocks {
			if b.Type != BlockToolResult || b.ToolResult == nil {
				continue
			}
			outs = append(outs, chatMessageOut{
				Role:       RoleTool,
				Content:    toolResultText(b.ToolResult),
				ToolCallID: b.ToolResult.CallID,
			})
		}
		if len(outs) == 0 {
			return []chatMessageOut{{Role: RoleTool, Content: m.Text()}}
		}
		return outs
	}

	var texts []string
	var parts []chatPartOut
	var calls []chatToolCallOut
	var reasoning strings.Builder
	multimodal := false
	for _, b := range m.Blocks {
		switch b.Type {
		case BlockText:
			if b.Text == "" {
				continue
			}
			texts = append(texts, b.Text)
			parts = append(parts, chatPartOut{Type: "text", Text: b.Text})
		case BlockThinking:
			reasoning.WriteString(b.Thinking)
		case BlockImage:
			if b.Image == nil || b.Image.Data == "" {
				continue
			}
			multimodal = true
			var p chatPartOut
			p.Type = "image_url"
			p.ImageURL = &struct {
				URL    string `json:"url"`
				Detail string `json:"detail,omitempty"`
			}{URL: imageDataURL(*b.Image)}
			parts = append(parts, p)
		case BlockToolCall:
			if b.ToolCall == nil {
				continue
			}
			var tc chatToolCallOut
			tc.ID = b.ToolCall.ID
			tc.Type = "function"
			tc.Function.Name = b.ToolCall.Name
			tc.Function.Arguments = rawArgsString(b.ToolCall.Arguments)
			calls = append(calls, tc)
		}
	}

	msg := chatMessageOut{Role: m.Role, ToolCalls: calls}
	if multimodal && m.Role == RoleUser {
		msg.Content = parts
	} else if len(texts) > 0 {
		msg.Content = strings.Join(texts, "\n")
	}
	if m.Role == RoleAssistant {
		msg.ReasoningContent = reasoning.String()
	}
	return []chatMessageOut{msg}
}

func (c *openaiChat) url() string {
	return apiURL(c.base, "chat/completions", "/v1")
}

func (c *openaiChat) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	body, err := c.buildBody(req)
	if err != nil {
		return nil, noRetry(fmt.Errorf("编码 openai-chat 请求失败：%w", err))
	}
	headers := map[string]string{"Authorization": "Bearer " + c.key}
	for k, v := range sessionHeaders(req.SessionID) {
		headers[k] = v
	}
	d := newDoer(c.t)
	resp, err := d.postJSON(ctx, c.url(), headers, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpError("openai-chat 请求失败", resp)
	}

	// 非流式：网关不支持 SSE 时（StreamDisabled），一次性 JSON 解码后回放成事件。
	if !req.Stream || strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
		raw, err := nonStreamBody(resp.Body, d.t.IdleTimeout)
		if err != nil {
			return nil, err
		}
		return eventsChan(ctx, c.decodeNonStream(raw)), nil
	}

	ch := make(chan StreamEvent, 64)
	go func() {
		defer close(ch)
		p := newPump(ctx, ch, resp.Body, d.t.IdleTimeout)
		started := map[int]bool{}
		finished := false
		p.endCheck = func() error {
			if !finished && !p.terminated {
				return fmt.Errorf("openai-chat 流在 finish_reason 前结束，响应不完整")
			}
			return nil
		}
		p.run(func(ev sseEvent) (bool, error) {
			var chunk chatChunkIn
			if json.Unmarshal([]byte(ev.Data), &chunk) == nil && len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason != nil {
				finished = true
			}
			return c.handleFrame(p, ev, started)
		})
	}()
	return ch, nil
}

type chatUsageIn struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	PromptDetails    *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CacheReadInputTokens int `json:"cache_read_input_tokens"`
}

type chatChunkIn struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content          string `json:"content"`
			Refusal          string `json:"refusal"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsageIn `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

func (c *openaiChat) handleFrame(p *pump, ev sseEvent, started map[int]bool) (bool, error) {
	data := strings.TrimSpace(ev.Data)
	if data == "" || data == "[DONE]" {
		return true, nil
	}
	var chunk chatChunkIn
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return false, noRetry(fmt.Errorf("解析 openai-chat SSE 失败：%w", err))
	}
	if chunk.Error != nil && chunk.Error.Message != "" {
		code := chunk.Error.Type
		if chunk.Error.Code != nil {
			code = fmt.Sprint(chunk.Error.Code)
		}
		return false, remoteError("openai-chat", code, chunk.Error.Message)
	}
	if u := chunk.Usage; u != nil {
		p.send(StreamEvent{Kind: EvUsage, Usage: usageFromChat(u)})
	}
	for _, ch0 := range chunk.Choices {
		if ch0.Index != 0 {
			continue
		}
		if ch0.FinishReason != nil && (*ch0.FinishReason == "length" || *ch0.FinishReason == "content_filter") {
			return false, stopReasonError("openai-chat", *ch0.FinishReason)
		}
		if ch0.Delta.Refusal != "" {
			return false, remoteError("openai-chat", "refusal", ch0.Delta.Refusal)
		}
		if s := ch0.Delta.ReasoningContent; s != "" {
			if !p.send(StreamEvent{Kind: EvThinking, Text: s}) {
				return false, nil
			}
		} else if s := ch0.Delta.Reasoning; s != "" {
			if !p.send(StreamEvent{Kind: EvThinking, Text: s}) {
				return false, nil
			}
		}
		if s := ch0.Delta.Content; s != "" {
			if !p.send(StreamEvent{Kind: EvText, Text: s}) {
				return false, nil
			}
		}
		for _, tc := range ch0.Delta.ToolCalls {
			idx := tc.Index
			if !started[idx] || tc.ID != "" || tc.Function.Name != "" {
				started[idx] = true
				if !p.send(StreamEvent{Kind: EvToolStart, Index: idx, ID: tc.ID, Name: tc.Function.Name}) {
					return false, nil
				}
			}
			if tc.Function.Arguments != "" {
				if !p.send(StreamEvent{Kind: EvToolDelta, Index: idx, ID: tc.ID, Name: tc.Function.Name, Text: tc.Function.Arguments}) {
					return false, nil
				}
			}
		}
	}
	return true, nil
}

func usageFromChat(u *chatUsageIn) *Usage {
	cached := u.CacheReadInputTokens
	if cached == 0 && u.PromptDetails != nil {
		cached = u.PromptDetails.CachedTokens
	}
	total := u.TotalTokens
	if total == 0 {
		total = u.PromptTokens + u.CompletionTokens
	}
	return &Usage{
		InputTokens:     u.PromptTokens,
		OutputTokens:    u.CompletionTokens,
		CacheReadTokens: cached,
		TotalTokens:     total,
	}
}

// ---------------- 小工具 ----------------

// apiURL 拼接端点：base 已带版本前缀时直接追加，否则补 version 前缀。
func apiURL(base, suffix, version string) string {
	base = strings.TrimRight(base, "/")
	if hasVersionSuffix(base) {
		return base + "/" + suffix
	}
	return base + version + "/" + suffix
}

// hasVersionSuffix 判断 base 末段是否形如 v1 / v4 / v1beta。
func hasVersionSuffix(base string) bool {
	i := strings.LastIndexByte(base, '/')
	if i < 0 || i == len(base)-1 {
		return false
	}
	seg := base[i+1:]
	if len(seg) < 2 || seg[0] != 'v' {
		return false
	}
	for _, r := range seg[1:] {
		if (r < '0' || r > '9') && r != 'b' && r != 'e' && r != 't' && r != 'a' {
			return false
		}
	}
	return true
}

// usesMaxCompletionTokens 判断模型是否只接受 max_completion_tokens。
func usesMaxCompletionTokens(model string) bool {
	m := strings.ToLower(model)
	if strings.Contains(m, "gpt-5") || strings.Contains(m, "gpt-6") {
		return true
	}
	if len(m) > 1 && m[0] == 'o' && m[1] >= '1' && m[1] <= '4' {
		return len(m) == 2 || m[2] == '-' || m[2] == '_' || m[2] == '.' || m[2] == ':'
	}
	return false
}

// effortOf 把 thinking 档位映射到 reasoning_effort；off/空返回空串。
func effortOf(thinking string) string {
	switch strings.ToLower(strings.TrimSpace(thinking)) {
	case "low", "minimal":
		return "low"
	case "medium", "mid":
		return "medium"
	case "high":
		return "high"
	case "xhigh":
		return "xhigh"
	case "max":
		return "max"
	}
	return ""
}

// normalizeSchema 保证 parameters 是合法 JSON 对象，缺省给空 object schema。
func normalizeSchema(raw json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return raw
}

// rawArgsString 把 arguments 转成字符串形式；空则给 "{}"。
func rawArgsString(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return "{}"
	}
	return s
}

// imageDataURL 生成 data URL。
func imageDataURL(img ImageData) string {
	mime := img.MIME
	if mime == "" {
		mime = "image/png"
	}
	return "data:" + mime + ";base64," + img.Data
}

// toolResultText 工具结果文本，附图片占位说明（chat 协议 tool 消息不支持图片）。
func toolResultText(tr *ToolResult) string {
	if tr == nil {
		return ""
	}
	out := tr.Content
	if tr.IsError && !strings.HasPrefix(out, "错误") {
		out = "错误：" + out
	}
	if len(tr.Images) > 0 {
		out += fmt.Sprintf("\n[附 %d 张图片，当前协议不支持在工具结果中回传图片]", len(tr.Images))
	}
	return out
}
