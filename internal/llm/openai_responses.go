package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"svg-master/internal/config"
)

// openaiResponses 实现 /v1/responses（OpenAI 新版 Responses API）。
type openaiResponses struct {
	base, key string
	t         Transport
}

func (c *openaiResponses) Protocol() string { return config.ProtoOpenAIResp }

// ---------------- 请求体 ----------------

type respContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type respInputItem struct {
	Type    string            `json:"type,omitempty"`
	Role    string            `json:"role,omitempty"`
	Content []respContentPart `json:"content,omitempty"`
	// function_call（历史 assistant 的工具调用）
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	// function_call_output
	Output string `json:"output,omitempty"`
}

type respToolOut struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type respRequestOut struct {
	Model           string          `json:"model"`
	Instructions    string          `json:"instructions,omitempty"`
	Input           []respInputItem `json:"input"`
	Tools           []respToolOut   `json:"tools,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
	Reasoning       *struct {
		Effort  string `json:"effort,omitempty"`
		Summary string `json:"summary,omitempty"`
	} `json:"reasoning,omitempty"`
	Stream bool `json:"stream"`
	Store  bool `json:"store"`
}

func (c *openaiResponses) buildBody(req Request) ([]byte, error) {
	out := respRequestOut{
		Model:        req.Model,
		Instructions: req.System,
		Stream:       req.Stream,
		Store:        false,
	}
	for _, m := range req.Messages {
		out.Input = append(out.Input, encodeRespMessage(m)...)
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, respToolOut{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  normalizeSchema(t.Parameters),
		})
	}
	if req.MaxTokens > 0 {
		out.MaxOutputTokens = req.MaxTokens
	}
	if e := effortOf(req.Thinking); e != "" {
		out.Reasoning = &struct {
			Effort  string `json:"effort,omitempty"`
			Summary string `json:"summary,omitempty"`
		}{Effort: e, Summary: "auto"}
	}
	return json.Marshal(out)
}

// encodeRespMessage 把中间表示消息转成 Responses 的 input item 列表。
func encodeRespMessage(m Message) []respInputItem {
	var items []respInputItem
	var parts []respContentPart
	var calls []respInputItem

	for _, b := range m.Blocks {
		switch b.Type {
		case BlockText:
			if b.Text == "" {
				continue
			}
			typ := "input_text"
			if m.Role == RoleAssistant {
				typ = "output_text"
			}
			parts = append(parts, respContentPart{Type: typ, Text: b.Text})
		case BlockImage:
			if b.Image == nil || b.Image.Data == "" {
				continue
			}
			parts = append(parts, respContentPart{
				Type:     "input_image",
				ImageURL: imageDataURL(*b.Image),
				Detail:   "auto",
			})
		case BlockToolCall:
			if b.ToolCall == nil {
				continue
			}
			calls = append(calls, respInputItem{
				Type:      "function_call",
				CallID:    b.ToolCall.ID,
				Name:      b.ToolCall.Name,
				Arguments: rawArgsString(b.ToolCall.Arguments),
			})
		case BlockToolResult:
			if b.ToolResult == nil {
				continue
			}
			items = append(items, respInputItem{
				Type:   "function_call_output",
				CallID: b.ToolResult.CallID,
				Output: toolResultText(b.ToolResult),
			})
		}
	}

	// tool 角色消息只有 function_call_output，已在上面处理完。
	if m.Role != RoleTool && len(parts) > 0 {
		items = append([]respInputItem{{Role: m.Role, Content: parts}}, items...)
	}
	items = append(items, calls...)
	return items
}

func (c *openaiResponses) url() string {
	return apiURL(c.base, "responses", "/v1")
}

func (c *openaiResponses) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	body, err := c.buildBody(req)
	if err != nil {
		return nil, fmt.Errorf("编码 openai-responses 请求失败：%w", err)
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
		return nil, httpError("openai-responses 请求失败", resp)
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
		st := &respState{byItem: map[string]respToolRef{}}
		p.run(func(ev sseEvent) (bool, error) { return c.handleFrame(p, ev, st) })
	}()
	return ch, nil
}

type respItemIn struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	CallID  string `json:"call_id"`
	Name    string `json:"name"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Arguments string `json:"arguments"`
}
type respUsageIn struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

type respEventIn struct {
	Type     string      `json:"type"`
	Delta    string      `json:"delta"`
	Text     string      `json:"text"`
	ItemID   string      `json:"item_id"`
	Item     *respItemIn `json:"item"`
	Response *struct {
		Usage *respUsageIn `json:"usage"`
		Error *struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	} `json:"response"`
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
	Message string `json:"message"`
}

// respState 流式工具调用状态：
// Responses 的 delta 事件只带 item_id（形如 fc_xxx），而工具结果回传要用 call_id（call_xxx），
// 所以这里维护 item_id -> {index, call_id} 映射，保证 start / delta 的 ID 一致。
type respState struct {
	byItem map[string]respToolRef
	next   int
}

type respToolRef struct {
	index  int
	callID string
}

func (s *respState) ensure(item *respItemIn) (respToolRef, bool) {
	key := item.ID
	if key == "" {
		key = item.CallID
	}
	if ref, ok := s.byItem[key]; ok {
		if ref.callID == "" {
			ref.callID = callIDOf(item)
			s.byItem[key] = ref
		}
		return ref, false
	}
	ref := respToolRef{index: s.next, callID: callIDOf(item)}
	s.next++
	s.byItem[key] = ref
	return ref, true
}

func (s *respState) lookup(itemID string) (respToolRef, bool) {
	ref, ok := s.byItem[itemID]
	return ref, ok
}

func (c *openaiResponses) handleFrame(p *pump, ev sseEvent, st *respState) (bool, error) {
	data := strings.TrimSpace(ev.Data)
	if data == "" || data == "[DONE]" {
		return true, nil
	}
	var in respEventIn
	if err := json.Unmarshal([]byte(data), &in); err != nil {
		return true, nil
	}
	kind := in.Type
	if kind == "" {
		kind = ev.Event
	}

	switch kind {
	case "response.output_text.delta":
		if in.Delta != "" && !p.send(StreamEvent{Kind: EvText, Text: in.Delta}) {
			return false, nil
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if in.Delta != "" && !p.send(StreamEvent{Kind: EvThinking, Text: in.Delta}) {
			return false, nil
		}
	case "response.output_item.added":
		if in.Item != nil && in.Item.Type == "function_call" {
			ref, isNew := st.ensure(in.Item)
			if isNew && !p.send(StreamEvent{Kind: EvToolStart, Index: ref.index, ID: ref.callID, Name: in.Item.Name}) {
				return false, nil
			}
			if in.Item.Arguments != "" && !p.send(StreamEvent{Kind: EvToolDelta, Index: ref.index, ID: ref.callID, Text: in.Item.Arguments}) {
				return false, nil
			}
		}
	case "response.function_call_arguments.delta":
		if in.Delta != "" {
			ref, ok := st.lookup(in.ItemID)
			if !ok {
				// 没见过 added（部分网关省略），先补一个 start，name 只能留空由 done 补。
				ref = respToolRef{index: st.next, callID: in.ItemID}
				st.next++
				st.byItem[in.ItemID] = ref
				if !p.send(StreamEvent{Kind: EvToolStart, Index: ref.index, ID: ref.callID}) {
					return false, nil
				}
			}
			if !p.send(StreamEvent{Kind: EvToolDelta, Index: ref.index, ID: ref.callID, Text: in.Delta}) {
				return false, nil
			}
		}
	case "response.output_item.done":
		// done 事件带完整 arguments；若之前没有 added（某些网关省略），这里补发 start + 全量参数。
		if in.Item != nil && in.Item.Type == "function_call" {
			ref, isNew := st.ensure(in.Item)
			if isNew {
				if !p.send(StreamEvent{Kind: EvToolStart, Index: ref.index, ID: ref.callID, Name: in.Item.Name}) {
					return false, nil
				}
				if in.Item.Arguments != "" && !p.send(StreamEvent{Kind: EvToolDelta, Index: ref.index, ID: ref.callID, Text: in.Item.Arguments}) {
					return false, nil
				}
			}
		}
	case "response.completed", "response.incomplete":
		if in.Response != nil {
			if in.Response.Usage != nil {
				p.send(StreamEvent{Kind: EvUsage, Usage: usageFromResp(in.Response.Usage)})
			}
			if in.Response.Error != nil && in.Response.Error.Message != "" {
				return false, fmt.Errorf("openai-responses 流式错误：%s", truncateRunes(in.Response.Error.Message, errCharLimit))
			}
		}
		return false, nil
	case "response.failed":
		msg := "响应失败"
		if in.Response != nil && in.Response.Error != nil && in.Response.Error.Message != "" {
			msg = in.Response.Error.Message
		}
		return false, fmt.Errorf("openai-responses 流式错误：%s", truncateRunes(msg, errCharLimit))
	case "error":
		msg := in.Message
		if msg == "" && in.Error != nil {
			msg = in.Error.Message
		}
		if msg == "" {
			msg = "未知错误"
		}
		return false, fmt.Errorf("openai-responses 流式错误：%s", truncateRunes(msg, errCharLimit))
	}
	return true, nil
}

func callIDOf(item *respItemIn) string {
	if item.CallID != "" {
		return item.CallID
	}
	return item.ID
}

func usageFromResp(u *respUsageIn) *Usage {
	cached := 0
	if u.InputTokensDetails != nil {
		cached = u.InputTokensDetails.CachedTokens
	}
	total := u.TotalTokens
	if total == 0 {
		total = u.InputTokens + u.OutputTokens
	}
	return &Usage{
		InputTokens:     u.InputTokens,
		OutputTokens:    u.OutputTokens,
		CacheReadTokens: cached,
		TotalTokens:     total,
	}
}
