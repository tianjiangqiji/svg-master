package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"svg-master/internal/config"
)

// anthropicClient 实现 /v1/messages（Claude 原生协议）。
type anthropicClient struct {
	base, key string
	t         Transport
}

func (c *anthropicClient) Protocol() string { return config.ProtoAnthropic }

const anthropicVersion = "2023-06-01"

// anthropicThinkingBeta 开启 thinking 时必须带的内测头。
const anthropicThinkingBeta = "interleaved-thinking-2025-05-14"

// ---------------- 请求体 ----------------

type anthropicBlockOut struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// image
	Source *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   any    `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

type anthropicMsgOut struct {
	Role    string              `json:"role"`
	Content []anthropicBlockOut `json:"content"`
}

type anthropicToolOut struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicRequestOut struct {
	Model     string              `json:"model"`
	MaxTokens int                 `json:"max_tokens"`
	System    []anthropicBlockOut `json:"system,omitempty"`
	Messages  []anthropicMsgOut   `json:"messages"`
	Tools     []anthropicToolOut  `json:"tools,omitempty"`
	Stream    bool                `json:"stream"`
	Thinking  *struct {
		Type         string `json:"type"`
		BudgetTokens int    `json:"budget_tokens"`
	} `json:"thinking,omitempty"`
}

func (c *anthropicClient) buildBody(req Request) ([]byte, error) {
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = 8192
	}
	out := anthropicRequestOut{Model: req.Model, MaxTokens: maxTok, Stream: req.Stream}
	if req.System != "" {
		out.System = []anthropicBlockOut{{Type: "text", Text: req.System}}
	}
	for _, m := range req.Messages {
		if msg, ok := encodeAnthropicMessage(m); ok {
			out.Messages = append(out.Messages, msg)
		}
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, anthropicToolOut{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: normalizeSchema(t.Parameters),
		})
	}

	budget := anthropicBudget(req.Thinking)
	if budget > 0 {
		// Anthropic 要求 budget_tokens < max_tokens；这里把预算压到 max_tokens 的一半，
		// 既保证合法，也不会因为预算过大而突破模型自身的输出上限。
		if half := maxTok / 2; budget > half {
			budget = half
		}
	}
	if budget >= 1024 {
		out.Thinking = &struct {
			Type         string `json:"type"`
			BudgetTokens int    `json:"budget_tokens"`
		}{Type: "enabled", BudgetTokens: budget}
	}
	// 不下发 temperature：Anthropic 默认即 temperature=1，开启 thinking 也只允许 1。
	return json.Marshal(out)
}

// anthropicBudget 把档位映射到 thinking 预算 token 数；off 返回 0。
func anthropicBudget(thinking string) int {
	switch strings.ToLower(strings.TrimSpace(thinking)) {
	case "low", "minimal":
		return 2048
	case "medium", "mid":
		return 8192
	case "high":
		return 16384
	case "xhigh":
		return 24576
	case "max":
		return 32768
	}
	return 0
}

// encodeAnthropicMessage 把中间表示消息转成 anthropic 消息。
// tool_result 必须放进 user 消息的 content 数组里，因此 role=tool 会映射成 role=user。
func encodeAnthropicMessage(m Message) (anthropicMsgOut, bool) {
	role := m.Role
	if role == RoleTool {
		role = RoleUser
	}
	if role != RoleUser && role != RoleAssistant {
		role = RoleUser
	}

	var blocks []anthropicBlockOut
	for _, b := range m.Blocks {
		switch b.Type {
		case BlockText:
			if b.Text == "" {
				continue
			}
			blocks = append(blocks, anthropicBlockOut{Type: "text", Text: b.Text})
		case BlockThinking:
			if b.Thinking == "" || role != RoleAssistant {
				continue
			}
			blocks = append(blocks, anthropicBlockOut{Type: "thinking", Text: b.Thinking})
		case BlockImage:
			if b.Image == nil || b.Image.Data == "" {
				continue
			}
			mime := b.Image.MIME
			if mime == "" {
				mime = "image/png"
			}
			blk := anthropicBlockOut{Type: "image"}
			blk.Source = &struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			}{Type: "base64", MediaType: mime, Data: b.Image.Data}
			blocks = append(blocks, blk)
		case BlockToolCall:
			if b.ToolCall == nil {
				continue
			}
			blocks = append(blocks, anthropicBlockOut{
				Type:  "tool_use",
				ID:    b.ToolCall.ID,
				Name:  b.ToolCall.Name,
				Input: normalizeArgs(b.ToolCall.Arguments),
			})
		case BlockToolResult:
			if b.ToolResult == nil {
				continue
			}
			blocks = append(blocks, anthropicBlockOut{
				Type:      "tool_result",
				ToolUseID: b.ToolResult.CallID,
				Content:   anthropicToolResultContent(b.ToolResult),
				IsError:   b.ToolResult.IsError,
			})
		}
	}
	if len(blocks) == 0 {
		return anthropicMsgOut{}, false
	}
	return anthropicMsgOut{Role: role, Content: blocks}, true
}

// anthropicToolResultContent 工具结果内容：纯文本用 string，带图片用 block 数组。
func anthropicToolResultContent(tr *ToolResult) any {
	text := tr.Content
	if tr.IsError && !strings.HasPrefix(text, "错误") {
		text = "错误：" + text
	}
	if len(tr.Images) == 0 {
		return text
	}
	blocks := []anthropicBlockOut{{Type: "text", Text: text}}
	for _, img := range tr.Images {
		if img.Data == "" {
			continue
		}
		mime := img.MIME
		if mime == "" {
			mime = "image/png"
		}
		blk := anthropicBlockOut{Type: "image"}
		blk.Source = &struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		}{Type: "base64", MediaType: mime, Data: img.Data}
		blocks = append(blocks, blk)
	}
	return blocks
}

func (c *anthropicClient) url() string {
	return apiURL(c.base, "messages", "/v1")
}

func (c *anthropicClient) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	body, err := c.buildBody(req)
	if err != nil {
		return nil, fmt.Errorf("编码 anthropic 请求失败：%w", err)
	}
	headers := map[string]string{
		"x-api-key":         c.key,
		"anthropic-version": anthropicVersion,
	}
	if anthropicBudget(req.Thinking) > 0 {
		headers["anthropic-beta"] = anthropicThinkingBeta
	}
	for k, v := range sessionHeaders(req.SessionID) {
		headers[k] = v
	}
	d := newDoer(c.t)
	resp, err := d.postJSON(ctx, c.url(), headers, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpError("anthropic 请求失败", resp)
	}

	if !req.Stream {
		raw, err := nonStreamBody(resp.Body)
		if err != nil {
			return nil, err
		}
		return eventsChan(ctx, c.decodeNonStream(raw)), nil
	}

	ch := make(chan StreamEvent, 64)
	go func() {
		defer close(ch)
		p := newPump(ctx, ch, resp.Body, d.t.IdleTimeout)
		us := &anthropicUsageState{}
		p.run(func(ev sseEvent) (bool, error) { return c.handleFrame(p, ev, us) })
	}()
	return ch, nil
}

type anthropicUsageIn struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type anthropicEventIn struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage *anthropicUsageIn `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
		Text string `json:"text"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *anthropicUsageIn `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// anthropicUsageState 累积 usage：message_start 给输入侧，message_delta 只给 output_tokens。
// 直接把 message_delta 的 usage 当完整值会丢掉输入侧数字，所以这里做合并。
type anthropicUsageState struct {
	input, output, cacheRead, cacheWrite int
}

func (s *anthropicUsageState) merge(u *anthropicUsageIn) *Usage {
	if u == nil {
		return nil
	}
	if u.InputTokens > 0 {
		s.input = u.InputTokens
	}
	if u.OutputTokens > 0 {
		s.output = u.OutputTokens
	}
	if u.CacheReadInputTokens > 0 {
		s.cacheRead = u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens > 0 {
		s.cacheWrite = u.CacheCreationInputTokens
	}
	in := s.input + s.cacheRead + s.cacheWrite
	return &Usage{
		InputTokens:      in,
		OutputTokens:     s.output,
		CacheReadTokens:  s.cacheRead,
		CacheWriteTokens: s.cacheWrite,
		TotalTokens:      in + s.output,
	}
}

func (c *anthropicClient) handleFrame(p *pump, ev sseEvent, us *anthropicUsageState) (bool, error) {
	data := strings.TrimSpace(ev.Data)
	if data == "" || data == "[DONE]" {
		return true, nil
	}
	var in anthropicEventIn
	if err := json.Unmarshal([]byte(data), &in); err != nil {
		return true, nil
	}
	kind := in.Type
	if kind == "" {
		kind = ev.Event
	}
	switch kind {
	case "message_start":
		if in.Message != nil {
			if u := us.merge(in.Message.Usage); u != nil {
				p.send(StreamEvent{Kind: EvUsage, Usage: u})
			}
		}
	case "content_block_start":
		if in.ContentBlock != nil {
			switch in.ContentBlock.Type {
			case "tool_use":
				if !p.send(StreamEvent{Kind: EvToolStart, Index: in.Index, ID: in.ContentBlock.ID, Name: in.ContentBlock.Name}) {
					return false, nil
				}
			case "text":
				// 少数网关会在 start 里就带一段初始文本。
				if in.ContentBlock.Text != "" && !p.send(StreamEvent{Kind: EvText, Text: in.ContentBlock.Text}) {
					return false, nil
				}
			}
		}
	case "content_block_delta":
		if in.Delta != nil {
			switch in.Delta.Type {
			case "text_delta":
				if in.Delta.Text != "" && !p.send(StreamEvent{Kind: EvText, Text: in.Delta.Text}) {
					return false, nil
				}
			case "thinking_delta":
				if in.Delta.Thinking != "" && !p.send(StreamEvent{Kind: EvThinking, Text: in.Delta.Thinking}) {
					return false, nil
				}
			case "input_json_delta":
				if in.Delta.PartialJSON != "" && !p.send(StreamEvent{Kind: EvToolDelta, Index: in.Index, Text: in.Delta.PartialJSON}) {
					return false, nil
				}
			}
		}
	case "message_delta":
		if u := us.merge(in.Usage); u != nil {
			p.send(StreamEvent{Kind: EvUsage, Usage: u})
		}
		if in.Delta != nil && in.Delta.StopReason != "" && in.Delta.StopReason != "end_turn" &&
			in.Delta.StopReason != "tool_use" && in.Delta.StopReason != "stop_sequence" &&
			in.Delta.StopReason != "max_tokens" && in.Delta.StopReason != "pause_turn" {
			return false, nil
		}
	case "message_stop":
		return false, nil
	case "error":
		msg := "未知错误"
		if in.Error != nil && in.Error.Message != "" {
			msg = in.Error.Message
		}
		return false, fmt.Errorf("anthropic 流式错误：%s", truncateRunes(msg, errCharLimit))
	}
	return true, nil
}

// normalizeArgs 保证工具参数是合法 JSON 对象。
func normalizeArgs(raw json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage(`{}`)
	}
	return raw
}
