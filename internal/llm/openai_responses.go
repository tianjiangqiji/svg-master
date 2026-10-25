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
	Output *string `json:"output,omitempty"`
}

type respToolOut struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type respRequestOut struct {
	ServiceTier     string            `json:"service_tier,omitempty"`
	Model           string            `json:"model"`
	Instructions    string            `json:"instructions,omitempty"`
	Input           []json.RawMessage `json:"input"`
	Tools           []respToolOut     `json:"tools,omitempty"`
	MaxOutputTokens int               `json:"max_output_tokens,omitempty"`
	Reasoning       *struct {
		Effort  string `json:"effort,omitempty"`
		Summary string `json:"summary,omitempty"`
	} `json:"reasoning,omitempty"`
	Stream         bool     `json:"stream"`
	Store          bool     `json:"store"`
	Include        []string `json:"include,omitempty"`
	PromptCacheKey string   `json:"prompt_cache_key,omitempty"`
}

func (c *openaiResponses) buildBody(req Request) ([]byte, error) {
	out := respRequestOut{
		ServiceTier:    req.ServiceTier,
		Model:          req.Model,
		Instructions:   req.System,
		Stream:         req.Stream,
		Store:          false,
		Include:        []string{"reasoning.encrypted_content"},
		PromptCacheKey: req.SessionID,
	}
	for _, m := range req.Messages {
		if m.Role == RoleAssistant && m.NativeOutput != nil && m.NativeOutput.Protocol == c.Protocol() && len(m.NativeOutput.Items) > 0 {
			out.Input = append(out.Input, m.NativeOutput.Items...)
			continue
		}
		for _, item := range encodeRespMessage(m) {
			raw, err := json.Marshal(item)
			if err != nil {
				return nil, err
			}
			out.Input = append(out.Input, raw)
		}
	}
	for _, t := range req.Tools {
		strict := false // 工具包含可选参数；保留原 schema 的可选语义。
		out.Tools = append(out.Tools, respToolOut{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  normalizeSchema(t.Parameters),
			Strict:      &strict,
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
			output := toolResultText(b.ToolResult)
			items = append(items, respInputItem{
				Type:   "function_call_output",
				CallID: b.ToolResult.CallID,
				Output: &output,
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
		return nil, noRetry(fmt.Errorf("编码 openai-responses 请求失败：%w", err))
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
		st := &respState{byItem: map[string]*respToolRef{}}
		p.endCheck = func() error {
			if !st.completed {
				return fmt.Errorf("openai-responses 流在 response.completed 前结束，响应不完整")
			}
			return nil
		}
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
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
	Summary []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
	Arguments string `json:"arguments"`
}
type respUsageIn struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails *struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
}

type respEventIn struct {
	Type      string          `json:"type"`
	Delta     string          `json:"delta"`
	Text      string          `json:"text"`
	ItemID    string          `json:"item_id"`
	Item      json.RawMessage `json:"item"`
	Arguments string          `json:"arguments"`
	Response  *respResponseIn `json:"response"`
	Error     *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
	Message string `json:"message"`
}

// respState 流式工具调用状态：
// Responses 的 delta 事件只带 item_id（形如 fc_xxx），而工具结果回传要用 call_id（call_xxx），
// 所以这里维护 item_id -> {index, call_id} 映射，保证 start / delta 的 ID 一致。
type respState struct {
	byItem    map[string]*respToolRef
	next      int
	items     []json.RawMessage
	text      string
	thinking  string
	completed bool
}

type respToolRef struct {
	index  int
	callID string
	name   string
	args   string
}

func (s *respState) ensure(item *respItemIn) *respToolRef {
	key := item.ID
	if key == "" {
		key = item.CallID
	}
	if ref, ok := s.byItem[key]; ok {
		if item.CallID != "" {
			ref.callID = item.CallID
		}
		if item.Name != "" {
			ref.name = item.Name
		}
		return ref
	}
	ref := &respToolRef{index: s.next, callID: callIDOf(item), name: item.Name}
	s.next++
	s.byItem[key] = ref
	return ref
}

func (s *respState) lookup(itemID string) (*respToolRef, bool) {
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
		return false, noRetry(fmt.Errorf("openai-responses SSE 数据不是合法 JSON：%w", err))
	}
	kind := in.Type
	if kind == "" {
		kind = ev.Event
	}

	switch kind {
	case "response.output_text.delta":
		st.text += in.Delta
		if in.Delta != "" && !p.send(StreamEvent{Kind: EvText, Text: in.Delta}) {
			return false, nil
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		st.thinking += in.Delta
		if in.Delta != "" && !p.send(StreamEvent{Kind: EvThinking, Text: in.Delta}) {
			return false, nil
		}
	case "response.output_item.added":
		if err := st.toolSnapshot(p, in.Item); err != nil {
			return false, err
		}
	case "response.function_call_arguments.delta":
		if in.Delta != "" {
			ref, ok := st.lookup(in.ItemID)
			if !ok {
				// 没见过 added（部分网关省略），先补一个 start，name 只能留空由 done 补。
				ref = st.ensure(&respItemIn{ID: in.ItemID})
				if !p.send(StreamEvent{Kind: EvToolStart, Index: ref.index, ID: ref.callID}) {
					return false, nil
				}
			}
			ref.args += in.Delta
			if !p.send(StreamEvent{Kind: EvToolDelta, Index: ref.index, ID: ref.callID, Text: in.Delta}) {
				return false, nil
			}
		}
	case "response.function_call_arguments.done":
		if ref, ok := st.lookup(in.ItemID); ok && in.Arguments != ref.args {
			ref.args = in.Arguments
			p.send(StreamEvent{Kind: EvToolDelta, Index: ref.index, ID: ref.callID, Text: ref.args, Replace: true})
		}
	case "response.output_item.done":
		if len(in.Item) > 0 {
			st.items = append(st.items, in.Item)
		}
		if err := st.toolSnapshot(p, in.Item); err != nil {
			return false, err
		}
	case "response.completed", "response.incomplete", "response.failed":
		if in.Response == nil {
			return false, noRetry(fmt.Errorf("openai-responses %s 缺少 response", kind))
		}
		if in.Response.Usage != nil {
			p.send(StreamEvent{Kind: EvUsage, Usage: usageFromResp(in.Response.Usage)})
		}
		if err := in.Response.checkStatus(kind); err != nil {
			return false, err
		}
		p.send(StreamEvent{Kind: EvServiceTier, Text: in.Response.ServiceTier})
		if in.Response.Output != nil {
			st.items = in.Response.Output
		}
		if err := st.finishOutput(p); err != nil {
			return false, err
		}
		st.completed = true
		return false, nil
	case "error":
		msg := in.Message
		if msg == "" && in.Error != nil {
			msg = in.Error.Message
		}
		if msg == "" {
			msg = "未知错误"
		}
		code := ""
		if in.Error != nil {
			code = in.Error.Code
		}
		return false, remoteError("openai-responses", code, msg)
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
		CacheWriteTokens: func() int {
			if u.InputTokensDetails != nil {
				return u.InputTokensDetails.CacheWriteTokens
			}
			return 0
		}(),
		TotalTokens: total,
	}
}
