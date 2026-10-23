package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"svg-master/internal/config"
)

// geminiClient 实现 {base}/v1beta/models/{model}:streamGenerateContent?alt=sse。
type geminiClient struct {
	base, key string
	t         Transport
}

func (c *geminiClient) Protocol() string { return config.ProtoGemini }

// ---------------- 请求体 ----------------

type geminiPartOut struct {
	Text             string            `json:"text,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
	InlineData       *geminiInlineData `json:"inlineData,omitempty"`
	FunctionCall     *geminiFuncCall   `json:"functionCall,omitempty"`
	FunctionResponse *geminiFuncResp   `json:"functionResponse,omitempty"`
}

type geminiInlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiFuncCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type geminiFuncResp struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type geminiContentOut struct {
	Role  string          `json:"role,omitempty"`
	Parts []geminiPartOut `json:"parts"`
}

type geminiToolOut struct {
	FunctionDeclarations []struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"functionDeclarations"`
}

type geminiRequestOut struct {
	SystemInstruction *geminiContentOut  `json:"systemInstruction,omitempty"`
	Contents          []geminiContentOut `json:"contents"`
	Tools             []geminiToolOut    `json:"tools,omitempty"`
	GenerationConfig  *struct {
		MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
		ThinkingConfig  *struct {
			ThinkingBudget int `json:"thinkingBudget"`
		} `json:"thinkingConfig,omitempty"`
	} `json:"generationConfig,omitempty"`
}

func (c *geminiClient) buildBody(req Request) ([]byte, error) {
	out := geminiRequestOut{}
	if s := sanitizeText(req.System); s != "" {
		out.SystemInstruction = &geminiContentOut{Parts: []geminiPartOut{{Text: s}}}
	}

	// 先建 callID -> 函数名 映射：Gemini 的 functionResponse 必须给函数名而非调用 id。
	names := map[string]string{}
	for _, m := range req.Messages {
		for _, b := range m.Blocks {
			if b.Type == BlockToolCall && b.ToolCall != nil && b.ToolCall.ID != "" {
				names[b.ToolCall.ID] = b.ToolCall.Name
			}
		}
	}
	withID := geminiNeedsToolID(req.Model)

	for _, m := range req.Messages {
		contents, err := encodeGeminiMessage(m, names, withID)
		if err != nil {
			return nil, err
		}
		out.Contents = append(out.Contents, contents...)
	}

	for _, t := range req.Tools {
		var tool geminiToolOut
		decl := struct {
			Name        string          `json:"name"`
			Description string          `json:"description,omitempty"`
			Parameters  json.RawMessage `json:"parameters,omitempty"`
		}{Name: t.Name, Description: t.Description, Parameters: normalizeSchema(t.Parameters)}
		tool.FunctionDeclarations = append(tool.FunctionDeclarations, decl)
		out.Tools = append(out.Tools, tool)
	}

	gc := &struct {
		MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
		ThinkingConfig  *struct {
			ThinkingBudget int `json:"thinkingBudget"`
		} `json:"thinkingConfig,omitempty"`
	}{}
	if req.MaxTokens > 0 {
		gc.MaxOutputTokens = req.MaxTokens
	}
	if budget, ok := geminiThinkingBudget(req.Thinking); ok {
		gc.ThinkingConfig = &struct {
			ThinkingBudget int `json:"thinkingBudget"`
		}{ThinkingBudget: budget}
	}
	out.GenerationConfig = gc

	return json.Marshal(out)
}

// encodeGeminiMessage 把一条中间表示消息编码成 gemini content 列表。
//
// 关键约束：
//   - role=assistant → "model"，user/tool → "user"；
//   - 同一轮里多个 tool_result 必须合并进同一个 user content 的 parts，
//     否则 Gemini 会按顺序把它们当多轮对话处理；
//   - functionResponse.name 必须是函数名，response 是对象（{output} 或 {error}）；
//   - 模型主版本 ≥3 时 functionCall / functionResponse 都要带 id。
func encodeGeminiMessage(m Message, names map[string]string, withID bool) ([]geminiContentOut, error) {
	role := "user"
	if m.Role == RoleAssistant {
		role = "model"
	}

	var parts []geminiPartOut
	var funcResponses []geminiPartOut

	for _, b := range m.Blocks {
		switch b.Type {
		case BlockText:
			if b.Text == "" {
				continue
			}
			parts = append(parts, geminiPartOut{Text: sanitizeText(b.Text)})
		case BlockThinking:
			// Gemini 的 thought part 只在响应里出现，回传历史时并入文本更稳。
			if b.Thinking == "" || m.Role != RoleAssistant {
				continue
			}
			parts = append(parts, geminiPartOut{Text: sanitizeText(b.Thinking), Thought: true})
		case BlockImage:
			if b.Image == nil || b.Image.Data == "" {
				continue
			}
			mime := b.Image.MIME
			if mime == "" {
				mime = "image/png"
			}
			parts = append(parts, geminiPartOut{InlineData: &geminiInlineData{MIMEType: mime, Data: b.Image.Data}})
		case BlockToolCall:
			if b.ToolCall == nil {
				continue
			}
			fc := &geminiFuncCall{Name: b.ToolCall.Name, Args: normalizeArgs(b.ToolCall.Arguments)}
			if withID {
				fc.ID = geminiToolID(b.ToolCall.ID, b.ToolCall.Name)
			}
			parts = append(parts, geminiPartOut{FunctionCall: fc})
		case BlockToolResult:
			if b.ToolResult == nil {
				continue
			}
			name := b.ToolResult.Name
			if name == "" {
				name = names[b.ToolResult.CallID]
			}
			if name == "" {
				name = "unknown_tool"
			}
			fr := &geminiFuncResp{Name: name, Response: geminiFuncResponsePayload(b.ToolResult)}
			if withID {
				fr.ID = geminiToolID(b.ToolResult.CallID, name)
			}
			funcResponses = append(funcResponses, geminiPartOut{FunctionResponse: fr})
		}
	}

	// 同一轮的工具结果合并成一个 user content。
	if len(funcResponses) > 0 {
		parts = append(parts, funcResponses...)
	}
	if len(parts) == 0 {
		return nil, nil
	}
	return []geminiContentOut{{Role: role, Parts: parts}}, nil
}

// geminiFuncResponsePayload 生成 functionResponse.response：{output:...} 或 {error:...}。
func geminiFuncResponsePayload(tr *ToolResult) json.RawMessage {
	key := "output"
	if tr.IsError {
		key = "error"
	}
	payload, err := json.Marshal(map[string]string{key: sanitizeText(tr.Content)})
	if err != nil {
		return json.RawMessage(`{"output":""}`)
	}
	return payload
}

// geminiToolID 把调用 id 清洗成 Gemini 允许的形态：仅 [a-zA-Z0-9_-]，最长 64。
// 清洗后为空时用函数名兜底，保证同一轮里 id 非空。
func geminiToolID(id, fallbackName string) string {
	clean := sanitizeToolID(id)
	if clean == "" {
		clean = sanitizeToolID(fallbackName)
	}
	if clean == "" {
		clean = "tool"
	}
	return clean
}

var toolIDUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func sanitizeToolID(s string) string {
	clean := toolIDUnsafe.ReplaceAllString(s, "")
	if len(clean) > 64 {
		clean = clean[:64]
	}
	return clean
}

var geminiMajorRe = regexp.MustCompile(`gemini-(\d+)`)

// geminiNeedsToolID 判断模型主版本是否 ≥3（Gemini 3 起 functionCall 需要 id）。
func geminiNeedsToolID(model string) bool {
	m := strings.ToLower(model)
	if i := strings.IndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	if sub := geminiMajorRe.FindStringSubmatch(m); sub != nil {
		if n, err := strconv.Atoi(sub[1]); err == nil {
			return n >= 3
		}
	}
	return false
}

// geminiThinkingBudget 把档位映射成 thinkingBudget；off 返回 ok=false。
func geminiThinkingBudget(thinking string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(thinking)) {
	case "low", "minimal":
		return 1024, true
	case "medium", "mid":
		return 8192, true
	case "high":
		return 24576, true
	case "xhigh":
		return 32768, true
	case "max":
		return 49152, true
	}
	return 0, false
}

// sanitizeText 把孤立 UTF-16 代理项替换为 U+FFFD，并清除非法 UTF-8 序列。
//
// 网关有时会把 `\ud83d` 这类孤立代理项塞进 JSON 字符串；Go 的 string 装不下它们，
// 但若上游用 []byte→string 硬转，仍可能得到 U+FFFD 之外的残留，这里统一规整。
func sanitizeText(s string) string {
	if s == "" {
		return ""
	}
	if utf8.ValidString(s) && !hasSurrogate(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= 0xD800 && r <= 0xDFFF {
			b.WriteRune('\uFFFD')
			continue
		}
		if r == utf8.RuneError {
			b.WriteRune('\uFFFD')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// hasSurrogate 检查是否存在代理项区 rune（含 U+FFFD 之外的情况）。
func hasSurrogate(s string) bool {
	for _, r := range s {
		if r >= 0xD800 && r <= 0xDFFF {
			return true
		}
	}
	return false
}

func (c *geminiClient) url(model string) string {
	base := strings.TrimRight(c.base, "/")
	if !strings.HasSuffix(base, "/v1beta") {
		base += "/v1beta"
	}
	return base + "/models/" + url.PathEscape(model) + ":streamGenerateContent?alt=sse&key=" + url.QueryEscape(c.key)
}

func (c *geminiClient) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	body, err := c.buildBody(req)
	if err != nil {
		return nil, fmt.Errorf("编码 gemini 请求失败：%w", err)
	}
	headers := map[string]string{"x-goog-api-key": c.key}
	for k, v := range sessionHeaders(req.SessionID) {
		headers[k] = v
	}
	d := newDoer(c.t)
	resp, err := d.postJSON(ctx, c.url(req.Model), headers, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpError("gemini 请求失败", resp)
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
		next := 0
		p.run(func(ev sseEvent) (bool, error) { return c.handleFrame(p, ev, &next) })
	}()
	return ch, nil
}

// ---------------- 响应 ----------------

type geminiResponseIn struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text         string `json:"text"`
				Thought      bool   `json:"thought"`
				FunctionCall *struct {
					ID   string          `json:"id"`
					Name string          `json:"name"`
					Args json.RawMessage `json:"args"`
				} `json:"functionCall"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		TotalTokenCount         int `json:"totalTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
		ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

func (c *geminiClient) handleFrame(p *pump, ev sseEvent, next *int) (bool, error) {
	data := strings.TrimSpace(ev.Data)
	if data == "" || data == "[DONE]" {
		return true, nil
	}
	var in geminiResponseIn
	if err := json.Unmarshal([]byte(data), &in); err != nil {
		return true, nil
	}
	if in.Error != nil && in.Error.Message != "" {
		return false, fmt.Errorf("gemini 流式错误（HTTP %d）：%s", in.Error.Code, truncateRunes(in.Error.Message, errCharLimit))
	}
	if in.PromptFeedback != nil && in.PromptFeedback.BlockReason != "" {
		return false, fmt.Errorf("gemini 拒绝生成：%s", in.PromptFeedback.BlockReason)
	}
	if u := in.UsageMetadata; u != nil {
		total := u.TotalTokenCount
		if total == 0 {
			total = u.PromptTokenCount + u.CandidatesTokenCount + u.ThoughtsTokenCount
		}
		p.send(StreamEvent{Kind: EvUsage, Usage: &Usage{
			InputTokens:     u.PromptTokenCount,
			OutputTokens:    u.CandidatesTokenCount + u.ThoughtsTokenCount,
			CacheReadTokens: u.CachedContentTokenCount,
			TotalTokens:     total,
		}})
	}
	for _, cand := range in.Candidates {
		for _, part := range cand.Content.Parts {
			switch {
			case part.FunctionCall != nil:
				idx := *next
				*next++
				id := part.FunctionCall.ID
				if id == "" {
					id = geminiToolID("", part.FunctionCall.Name)
				}
				if !p.send(StreamEvent{Kind: EvToolStart, Index: idx, ID: id, Name: part.FunctionCall.Name}) {
					return false, nil
				}
				if len(part.FunctionCall.Args) > 0 && !p.send(StreamEvent{Kind: EvToolDelta, Index: idx, ID: id, Text: string(part.FunctionCall.Args)}) {
					return false, nil
				}
			case part.Text != "":
				kind := EvText
				if part.Thought {
					kind = EvThinking
				}
				if !p.send(StreamEvent{Kind: kind, Text: sanitizeText(part.Text)}) {
					return false, nil
				}
			}
		}
	}
	return true, nil
}
