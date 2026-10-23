package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"svg-master/internal/config"
)

// testTransport 测试用传输参数：短超时、几乎不等待的重试。
func testTransport() Transport {
	return Transport{
		Timeout:       5 * time.Second,
		IdleTimeout:   5 * time.Second,
		MaxRetries:    1,
		RetryBase:     time.Millisecond,
		MaxRetryDelay: 2 * time.Millisecond,
	}
}

// sseServer 启动一个按给定帧序列回放的 SSE 假服务器，并记录收到的请求。
type capturedRequest struct {
	Path   string
	Header http.Header
	Body   []byte
}

func sseServer(t *testing.T, frames []string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	cap := &capturedRequest{}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		cap.Path = r.URL.Path
		cap.Header = r.Header.Clone()
		cap.Body = body
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for _, f := range frames {
			_, _ = io.WriteString(w, f)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

// collect 读完 channel，返回事件切片；超时视为失败。
func collect(t *testing.T, ch <-chan StreamEvent) []StreamEvent {
	t.Helper()
	var out []StreamEvent
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			t.Fatalf("读取事件超时，已收到 %d 个事件", len(out))
		}
	}
}

func kinds(evs []StreamEvent) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	return out
}

func textOf(evs []StreamEvent, kind string) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Kind == kind {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

func findEvent(evs []StreamEvent, kind string) *StreamEvent {
	for i := range evs {
		if evs[i].Kind == kind {
			return &evs[i]
		}
	}
	return nil
}

func hasKind(evs []StreamEvent, kind string) bool { return findEvent(evs, kind) != nil }

func assertNoErrorEvent(t *testing.T, evs []StreamEvent) {
	t.Helper()
	for _, e := range evs {
		if e.Kind == EvError {
			t.Fatalf("意外收到错误事件：%s", e.Err)
		}
	}
}

// ---------------- OpenAI Chat ----------------

func TestOpenAIChatStream(t *testing.T) {
	frames := []string{
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"先想\"}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"<svg\"}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"/>\"}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"emit_svg\",\"arguments\":\"{\\\"svg\\\":\"}}]}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"<svg/>\\\"}\"}}]}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n",
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15,\"prompt_tokens_details\":{\"cached_tokens\":4}}}\n\n",
		"data: [DONE]\n\n",
	}
	srv, cap := sseServer(t, frames)
	c := NewClient(config.ProtoOpenAIChat, srv.URL, "sk-test", testTransport())

	req := Request{
		Model:     "gpt-5",
		System:    "你是 SVG 助手",
		Thinking:  "medium",
		Stream:    true,
		MaxTokens: 4096,
		SessionID: "sess-1",
		Messages: []Message{
			{Role: RoleUser, Blocks: []Block{
				{Type: BlockText, Text: "画一个圆"},
				{Type: BlockImage, Image: &ImageData{MIME: "image/png", Data: "AAAA"}},
			}},
		},
		Tools: []ToolSpec{{Name: "emit_svg", Description: "推送 SVG", Parameters: json.RawMessage(`{"type":"object","properties":{"svg":{"type":"string"}}}`)}},
	}
	ch, err := c.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream 返回错误：%v", err)
	}
	evs := collect(t, ch)
	assertNoErrorEvent(t, evs)

	if got := textOf(evs, EvThinking); got != "先想" {
		t.Errorf("thinking 文本 = %q，期望 %q", got, "先想")
	}
	if got := textOf(evs, EvText); got != "<svg/>" {
		t.Errorf("text 文本 = %q，期望 %q", got, "<svg/>")
	}
	start := findEvent(evs, EvToolStart)
	if start == nil {
		t.Fatal("缺少 tool_start 事件")
	}
	if start.ID != "call_1" || start.Name != "emit_svg" || start.Index != 0 {
		t.Errorf("tool_start = %+v，期望 id=call_1 name=emit_svg index=0", *start)
	}
	if got := textOf(evs, EvToolDelta); got != `{"svg":"<svg/>"}` {
		t.Errorf("tool_delta 参数 = %q", got)
	}
	u := findEvent(evs, EvUsage)
	if u == nil || u.Usage == nil {
		t.Fatal("缺少 usage 事件")
	}
	if u.Usage.InputTokens != 10 || u.Usage.OutputTokens != 5 || u.Usage.CacheReadTokens != 4 || u.Usage.TotalTokens != 15 {
		t.Errorf("usage = %+v", *u.Usage)
	}
	if !hasKind(evs, EvDone) {
		t.Error("缺少 done 事件")
	}

	// 请求体契约检查。
	if cap.Path != "/v1/chat/completions" {
		t.Errorf("请求路径 = %q，期望 /v1/chat/completions", cap.Path)
	}
	if got := cap.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("Authorization = %q", got)
	}
	if got := cap.Header.Get("session_id"); got != "sess-1" {
		t.Errorf("session_id 头 = %q", got)
	}
	var body struct {
		Model           string `json:"model"`
		Stream          bool   `json:"stream"`
		Reasoning       string `json:"reasoning_effort"`
		MaxCompletion   int    `json:"max_completion_tokens"`
		MaxTokensLegacy int    `json:"max_tokens"`
		StreamOptions   *struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(cap.Body, &body); err != nil {
		t.Fatalf("请求体不是合法 JSON：%v", err)
	}
	if body.Model != "gpt-5" || !body.Stream || body.Reasoning != "medium" {
		t.Errorf("请求体字段异常：%+v", body)
	}
	// o 系列 / gpt-5 只能发 max_completion_tokens。
	if body.MaxCompletion != 4096 || body.MaxTokensLegacy != 0 {
		t.Errorf("max_completion_tokens 应为 4096 且不带 max_tokens，实际 %d / %d", body.MaxCompletion, body.MaxTokensLegacy)
	}
	if body.StreamOptions == nil || !body.StreamOptions.IncludeUsage {
		t.Error("stream_options.include_usage 应为 true")
	}
	if len(body.Messages) != 2 || body.Messages[0].Role != RoleSystem {
		t.Fatalf("消息结构异常：%+v", body.Messages)
	}
	if !strings.Contains(string(body.Messages[1].Content), "image_url") {
		t.Errorf("多模态 content 应包含 image_url，实际 %s", body.Messages[1].Content)
	}
	if len(body.Tools) != 1 || body.Tools[0].Type != "function" || body.Tools[0].Function.Name != "emit_svg" {
		t.Errorf("tools 结构异常：%+v", body.Tools)
	}
}

// ---------------- OpenAI Responses ----------------

func TestOpenAIResponsesStream(t *testing.T) {
	frames := []string{
		"event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"推理\"}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"好\"}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"的\"}\n\n",
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_9\",\"name\":\"emit_svg\",\"arguments\":\"\"}}\n\n",
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":\"{\\\"svg\\\":\"}\n\n",
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":\"\\\"<svg/>\\\"}\"}\n\n",
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_9\",\"name\":\"emit_svg\",\"arguments\":\"{\\\"svg\\\":\\\"<svg/>\\\"}\"}}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":20,\"output_tokens\":8,\"total_tokens\":28,\"input_tokens_details\":{\"cached_tokens\":6}}}}\n\n",
	}
	srv, cap := sseServer(t, frames)
	c := NewClient(config.ProtoOpenAIResp, srv.URL, "sk-test", testTransport())

	ch, err := c.Stream(context.Background(), Request{
		Model:    "gpt-5",
		System:   "系统提示",
		Thinking: "high",
		Stream:   true,
		Messages: []Message{
			TextMsg(RoleUser, "画个圆"),
			{Role: RoleAssistant, Blocks: []Block{{Type: BlockToolCall, ToolCall: &ToolCall{ID: "call_9", Name: "emit_svg", Arguments: json.RawMessage(`{"svg":"<svg/>"}`)}}}},
			{Role: RoleTool, Blocks: []Block{{Type: BlockToolResult, ToolResult: &ToolResult{CallID: "call_9", Name: "emit_svg", Content: "已推送"}}}},
		},
		Tools: []ToolSpec{{Name: "emit_svg", Description: "推送", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatalf("Stream 返回错误：%v", err)
	}
	evs := collect(t, ch)
	assertNoErrorEvent(t, evs)

	if got := textOf(evs, EvThinking); got != "推理" {
		t.Errorf("thinking = %q", got)
	}
	if got := textOf(evs, EvText); got != "好的" {
		t.Errorf("text = %q", got)
	}
	start := findEvent(evs, EvToolStart)
	if start == nil || start.ID != "call_9" || start.Name != "emit_svg" {
		t.Errorf("tool_start = %+v，期望 id=call_9（用 call_id 而非 item id）", start)
	}
	if got := textOf(evs, EvToolDelta); got != `{"svg":"<svg/>"}` {
		t.Errorf("tool_delta = %q", got)
	}
	for _, e := range evs {
		if e.Kind == EvToolDelta && e.ID != "call_9" {
			t.Errorf("tool_delta 的 ID = %q，应与 tool_start 一致", e.ID)
		}
	}
	u := findEvent(evs, EvUsage)
	if u == nil || u.Usage.InputTokens != 20 || u.Usage.OutputTokens != 8 || u.Usage.CacheReadTokens != 6 {
		t.Errorf("usage = %+v", u)
	}

	if cap.Path != "/v1/responses" {
		t.Errorf("请求路径 = %q，期望 /v1/responses", cap.Path)
	}
	var body struct {
		Model        string           `json:"model"`
		Instructions string           `json:"instructions"`
		Input        []map[string]any `json:"input"`
		Tools        []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tools"`
		MaxOutputTokens int `json:"max_output_tokens"`
		Reasoning       *struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(cap.Body, &body); err != nil {
		t.Fatalf("请求体不是合法 JSON：%v", err)
	}
	if body.Instructions != "系统提示" || body.Model != "gpt-5" || !body.Stream {
		t.Errorf("请求体顶层字段异常：%+v", body)
	}
	if body.Reasoning == nil || body.Reasoning.Effort != "high" {
		t.Errorf("reasoning.effort 应为 high，实际 %+v", body.Reasoning)
	}
	if len(body.Tools) != 1 || body.Tools[0].Type != "function" || body.Tools[0].Name != "emit_svg" {
		t.Errorf("tools 应为扁平 {type,name} 结构，实际 %+v", body.Tools)
	}
	// input 里必须有 function_call 与 function_call_output 两种 item。
	var sawCall, sawOutput bool
	for _, it := range body.Input {
		switch it["type"] {
		case "function_call":
			sawCall = true
			if it["call_id"] != "call_9" || it["name"] != "emit_svg" {
				t.Errorf("function_call item 异常：%+v", it)
			}
		case "function_call_output":
			sawOutput = true
			if it["call_id"] != "call_9" || it["output"] != "已推送" {
				t.Errorf("function_call_output item 异常：%+v", it)
			}
		}
	}
	if !sawCall || !sawOutput {
		t.Errorf("input 缺少 function_call/function_call_output：%+v", body.Input)
	}
}

// ---------------- Anthropic ----------------

func TestAnthropicStream(t *testing.T) {
	frames := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12,\"cache_read_input_tokens\":3,\"cache_creation_input_tokens\":1}}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"<svg\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"/>\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"想一想\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"emit_svg\",\"input\":{}}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"svg\\\":\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"<svg/>\\\"}\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":2}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":7}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	srv, cap := sseServer(t, frames)
	c := NewClient(config.ProtoAnthropic, srv.URL, "sk-ant", testTransport())

	ch, err := c.Stream(context.Background(), Request{
		Model:     "claude-sonnet-4-5",
		System:    "系统",
		Thinking:  "low",
		Stream:    true,
		MaxTokens: 8192,
		Messages: []Message{
			TextMsg(RoleUser, "画个圆"),
			{Role: RoleTool, Blocks: []Block{{Type: BlockToolResult, ToolResult: &ToolResult{CallID: "toolu_0", Name: "emit_svg", Content: "ok"}}}},
		},
		Tools: []ToolSpec{{Name: "emit_svg", Description: "推送", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatalf("Stream 返回错误：%v", err)
	}
	evs := collect(t, ch)
	assertNoErrorEvent(t, evs)

	if got := textOf(evs, EvText); got != "<svg/>" {
		t.Errorf("text = %q", got)
	}
	if got := textOf(evs, EvThinking); got != "想一想" {
		t.Errorf("thinking = %q", got)
	}
	start := findEvent(evs, EvToolStart)
	if start == nil || start.ID != "toolu_1" || start.Name != "emit_svg" || start.Index != 2 {
		t.Errorf("tool_start = %+v", start)
	}
	if got := textOf(evs, EvToolDelta); got != `{"svg":"<svg/>"}` {
		t.Errorf("tool_delta = %q", got)
	}
	var lastUsage *Usage
	for _, e := range evs {
		if e.Kind == EvUsage && e.Usage != nil {
			lastUsage = e.Usage
		}
	}
	if lastUsage == nil {
		t.Fatal("缺少 usage 事件")
	}
	// input_tokens 12 + cache_read 3 + cache_write 1 = 16；output 来自 message_delta = 7。
	if lastUsage.InputTokens != 16 || lastUsage.OutputTokens != 7 || lastUsage.CacheReadTokens != 3 || lastUsage.CacheWriteTokens != 1 {
		t.Errorf("usage = %+v", *lastUsage)
	}
	if lastUsage.TotalTokens != 23 {
		t.Errorf("total = %d，期望 23", lastUsage.TotalTokens)
	}

	if cap.Path != "/v1/messages" {
		t.Errorf("请求路径 = %q", cap.Path)
	}
	if cap.Header.Get("x-api-key") != "sk-ant" {
		t.Errorf("x-api-key = %q", cap.Header.Get("x-api-key"))
	}
	if cap.Header.Get("anthropic-version") != anthropicVersion {
		t.Errorf("anthropic-version = %q", cap.Header.Get("anthropic-version"))
	}
	if cap.Header.Get("anthropic-beta") != anthropicThinkingBeta {
		t.Errorf("thinking 开启时必须带 anthropic-beta，实际 %q", cap.Header.Get("anthropic-beta"))
	}
	var body struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		System    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string `json:"type"`
				ToolUseID string `json:"tool_use_id"`
				Text      string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
		Thinking *struct {
			Type         string `json:"type"`
			BudgetTokens int    `json:"budget_tokens"`
		} `json:"thinking"`
		// 温度已从协议层整体移除：这里解码出来只为断言它永远不会被下发。
		Temperature *float64 `json:"temperature"`
		Tools       []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"input_schema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(cap.Body, &body); err != nil {
		t.Fatalf("请求体不是合法 JSON：%v", err)
	}
	if len(body.System) != 1 || body.System[0].Text != "系统" {
		t.Errorf("system 应放顶层 block 数组，实际 %+v", body.System)
	}
	if body.Thinking == nil || body.Thinking.BudgetTokens <= 0 {
		t.Errorf("thinking 应开启，实际 %+v", body.Thinking)
	}
	if body.Temperature != nil {
		t.Errorf("温度已从协议层移除，任何请求都不应再下发 temperature，实际 %v", *body.Temperature)
	}
	if body.MaxTokens <= body.Thinking.BudgetTokens {
		t.Errorf("max_tokens(%d) 必须大于 budget_tokens(%d)", body.MaxTokens, body.Thinking.BudgetTokens)
	}
	if len(body.Messages) != 2 {
		t.Fatalf("消息数 = %d", len(body.Messages))
	}
	// role=tool 的消息必须变成 user + tool_result 块。
	if body.Messages[1].Role != RoleUser {
		t.Errorf("tool 消息应映射成 user，实际 %q", body.Messages[1].Role)
	}
	if len(body.Messages[1].Content) != 1 || body.Messages[1].Content[0].Type != "tool_result" ||
		body.Messages[1].Content[0].ToolUseID != "toolu_0" {
		t.Errorf("tool_result 块异常：%+v", body.Messages[1].Content)
	}
	if len(body.Tools) != 1 || body.Tools[0].Name != "emit_svg" {
		t.Errorf("tools 异常：%+v", body.Tools)
	}
}

// ---------------- Gemini ----------------

func TestGeminiStream(t *testing.T) {
	frames := []string{
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"思考中\",\"thought\":true}]}}]}\n\n",
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"<svg\"}]}}]}\n\n",
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"/>\"}]}}]}\n\n",
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"emit_svg\",\"args\":{\"svg\":\"<svg/>\"}}}]}}]}\n\n",
		"data: {\"candidates\":[{\"content\":{\"parts\":[]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":9,\"candidatesTokenCount\":4,\"cachedContentTokenCount\":2}}\n\n",
	}
	srv, cap := sseServer(t, frames)
	c := NewClient(config.ProtoGemini, srv.URL, "gm-key", testTransport())

	ch, err := c.Stream(context.Background(), Request{
		Model:    "gemini-2.5-pro",
		System:   "系统",
		Thinking: "high",
		Stream:   true,
		Messages: []Message{TextMsg(RoleUser, "画个圆")},
		Tools:    []ToolSpec{{Name: "emit_svg", Description: "推送", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatalf("Stream 返回错误：%v", err)
	}
	evs := collect(t, ch)
	assertNoErrorEvent(t, evs)

	if got := textOf(evs, EvThinking); got != "思考中" {
		t.Errorf("thinking = %q", got)
	}
	if got := textOf(evs, EvText); got != "<svg/>" {
		t.Errorf("text = %q", got)
	}
	start := findEvent(evs, EvToolStart)
	if start == nil || start.Name != "emit_svg" {
		t.Errorf("tool_start = %+v", start)
	}
	if got := textOf(evs, EvToolDelta); got != `{"svg":"<svg/>"}` {
		t.Errorf("tool_delta = %q", got)
	}
	u := findEvent(evs, EvUsage)
	if u == nil || u.Usage.InputTokens != 9 || u.Usage.OutputTokens != 4 || u.Usage.CacheReadTokens != 2 {
		t.Errorf("usage = %+v", u)
	}

	if !strings.HasPrefix(cap.Path, "/v1beta/models/gemini-2.5-pro:streamGenerateContent") {
		t.Errorf("请求路径 = %q", cap.Path)
	}
	if cap.Header.Get("x-goog-api-key") != "gm-key" {
		t.Errorf("x-goog-api-key = %q", cap.Header.Get("x-goog-api-key"))
	}
	var body struct {
		SystemInstruction *struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"systemInstruction"`
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
		Tools []struct {
			FunctionDeclarations []struct {
				Name string `json:"name"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
		GenerationConfig struct {
			ThinkingConfig *struct {
				ThinkingBudget int `json:"thinkingBudget"`
			} `json:"thinkingConfig"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(cap.Body, &body); err != nil {
		t.Fatalf("请求体不是合法 JSON：%v", err)
	}
	if body.SystemInstruction == nil || len(body.SystemInstruction.Parts) != 1 {
		t.Errorf("systemInstruction 异常：%+v", body.SystemInstruction)
	}
	if len(body.Contents) != 1 || body.Contents[0].Role != "user" {
		t.Errorf("contents 异常：%+v", body.Contents)
	}
	if len(body.Tools) != 1 || len(body.Tools[0].FunctionDeclarations) != 1 || body.Tools[0].FunctionDeclarations[0].Name != "emit_svg" {
		t.Errorf("tools 异常：%+v", body.Tools)
	}
	if body.GenerationConfig.ThinkingConfig == nil || body.GenerationConfig.ThinkingConfig.ThinkingBudget <= 0 {
		t.Errorf("thinkingConfig 异常：%+v", body.GenerationConfig)
	}
}

// TestGeminiBuildBodyFunctionResponses 覆盖 Gemini 三条硬约束：
// 多个 functionResponse 合并进同一 user content；name 用函数名；response 是 {output}/{error} 对象；
// 主版本 ≥3 时 functionCall/functionResponse 带清洗后的 id。
func TestGeminiBuildBodyFunctionResponses(t *testing.T) {
	c := &geminiClient{base: "https://example.com", key: "k", t: testTransport()}
	req := Request{
		Model: "gemini-3-pro-preview",
		Messages: []Message{
			TextMsg(RoleUser, "同时做两件事"),
			{Role: RoleAssistant, Blocks: []Block{
				{Type: BlockToolCall, ToolCall: &ToolCall{ID: "call_1.abc", Name: "emit_svg", Arguments: json.RawMessage(`{"svg":"<svg/>"}`)}},
				{Type: BlockToolCall, ToolCall: &ToolCall{ID: "call_2/def", Name: "validate_svg", Arguments: json.RawMessage(`{}`)}},
			}},
			{Role: RoleTool, Blocks: []Block{
				{Type: BlockToolResult, ToolResult: &ToolResult{CallID: "call_1.abc", Name: "emit_svg", Content: "已推送"}},
				{Type: BlockToolResult, ToolResult: &ToolResult{CallID: "call_2/def", Name: "validate_svg", Content: "有 2 个错误", IsError: true}},
			}},
		},
	}
	raw, err := c.buildBody(req)
	if err != nil {
		t.Fatalf("buildBody 失败：%v", err)
	}
	var body struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text         string `json:"text"`
				FunctionCall *struct {
					ID   string          `json:"id"`
					Name string          `json:"name"`
					Args json.RawMessage `json:"args"`
				} `json:"functionCall"`
				FunctionResponse *struct {
					ID       string          `json:"id"`
					Name     string          `json:"name"`
					Response json.RawMessage `json:"response"`
				} `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("请求体不是合法 JSON：%v", err)
	}
	if len(body.Contents) != 3 {
		t.Fatalf("contents 条数 = %d，期望 3（user / model / 合并后的 user）", len(body.Contents))
	}
	modelTurn := body.Contents[1]
	if modelTurn.Role != "model" {
		t.Errorf("第二条 content role = %q，期望 model", modelTurn.Role)
	}
	for _, p := range modelTurn.Parts {
		if p.FunctionCall == nil {
			continue
		}
		if p.FunctionCall.ID == "" {
			t.Errorf("gemini-3 的 functionCall 必须带 id：%+v", p.FunctionCall)
			continue
		}
		if strings.ContainsAny(p.FunctionCall.ID, "./") {
			t.Errorf("functionCall id 未清洗：%q", p.FunctionCall.ID)
		}
		if len(p.FunctionCall.ID) > 64 {
			t.Errorf("functionCall id 过长：%d", len(p.FunctionCall.ID))
		}
	}

	toolTurn := body.Contents[2]
	if toolTurn.Role != "user" {
		t.Errorf("工具结果 content role = %q，期望 user", toolTurn.Role)
	}
	if len(toolTurn.Parts) != 2 {
		t.Fatalf("多个 functionResponse 必须合并进同一 content，实际 parts=%d", len(toolTurn.Parts))
	}
	names := map[string]bool{}
	for _, p := range toolTurn.Parts {
		if p.FunctionResponse == nil {
			t.Fatalf("part 不是 functionResponse：%+v", p)
		}
		names[p.FunctionResponse.Name] = true
		if p.FunctionResponse.ID == "" {
			t.Errorf("gemini-3 的 functionResponse 必须带 id")
		}
		var payload map[string]string
		if err := json.Unmarshal(p.FunctionResponse.Response, &payload); err != nil {
			t.Fatalf("functionResponse.response 必须是对象：%s", p.FunctionResponse.Response)
		}
		if _, ok := payload["output"]; !ok {
			if _, ok2 := payload["error"]; !ok2 {
				t.Errorf("response 必须是 {output} 或 {error}：%s", p.FunctionResponse.Response)
			}
		}
	}
	if !names["emit_svg"] || !names["validate_svg"] {
		t.Errorf("functionResponse.name 必须是函数名，实际 %v", names)
	}
}

// TestGeminiToolIDAndSanitize 覆盖 id 清洗与孤立代理项替换。
func TestGeminiToolIDAndSanitize(t *testing.T) {
	if got := geminiToolID("call_1.abc/def", "emit_svg"); got != "call_1abcdef" {
		t.Errorf("geminiToolID = %q", got)
	}
	long := strings.Repeat("a", 100)
	if got := geminiToolID(long, "x"); len(got) != 64 {
		t.Errorf("超长 id 应截断到 64，实际 %d", len(got))
	}
	if got := geminiToolID("", "emit_svg"); got != "emit_svg" {
		t.Errorf("空 id 应回落到函数名，实际 %q", got)
	}
	// 非法 UTF-8 字节必须被替换为 U+FFFD，保证请求体是合法 JSON 字符串。
	bad := string([]byte{0xff, 0xfe, 'a'})
	if got := sanitizeText(bad); got != "\uFFFD\uFFFD"+"a" {
		t.Errorf("sanitizeText = %q", got)
	}
	if got := sanitizeText("正常文本"); got != "正常文本" {
		t.Errorf("合法文本不应被改动，实际 %q", got)
	}
}

// ---------------- 取消与错误 ----------------

// TestStreamContextCancel 保证 ctx 取消时 channel 关闭、不泄漏 goroutine。
func TestStreamContextCancel(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"部分\"}}]}\n\n")
		if fl != nil {
			fl.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)

	c := NewClient(config.ProtoOpenAIChat, srv.URL, "k", Transport{
		Timeout: 30 * time.Second, IdleTimeout: 30 * time.Second,
		MaxRetries: 1, RetryBase: time.Millisecond, MaxRetryDelay: 2 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := c.Stream(ctx, Request{Model: "m", Stream: true, Messages: []Message{TextMsg(RoleUser, "hi")}})
	if err != nil {
		t.Fatalf("Stream 返回错误：%v", err)
	}
	// 先收到一部分文本，再取消。
	select {
	case ev := <-ch:
		if ev.Kind != EvText || ev.Text != "部分" {
			t.Errorf("首个事件 = %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待首个事件超时")
	}
	cancel()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel 已关闭，正常
			}
		case <-deadline:
			t.Fatal("ctx 取消后 channel 未关闭（goroutine 泄漏）")
		}
	}
}

// TestHTTPErrorFormat 校验中文错误信息包含状态码与响应体前缀。
func TestHTTPErrorFormat(t *testing.T) {
	body := strings.Repeat("错误详情", 200) // 远超 300 字符
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	c := NewClient(config.ProtoOpenAIChat, srv.URL, "k", testTransport())
	_, err := c.Stream(context.Background(), Request{Model: "m", Stream: true, Messages: []Message{TextMsg(RoleUser, "hi")}})
	if err == nil {
		t.Fatal("期望返回错误")
	}
	msg := err.Error()
	if !strings.Contains(msg, "HTTP 500") {
		t.Errorf("错误信息应包含状态码：%s", msg)
	}
	if !strings.Contains(msg, "错误详情") {
		t.Errorf("错误信息应包含响应体前缀：%s", msg)
	}
	// 响应体部分被截断到 300 字符（加省略号）。
	prefix := strings.TrimPrefix(msg[strings.Index(msg, "响应体："):], "响应体：")
	if n := len([]rune(prefix)); n > 302 {
		t.Errorf("响应体前缀应截断到 300 字符，实际 %d", n)
	}
}

// TestUnknownProtocolClient 未知协议应返回 stub，Stream 直接报错。
func TestUnknownProtocolClient(t *testing.T) {
	c := NewClient("mystery-proto", "http://x", "k", testTransport())
	if c.Protocol() != "mystery-proto" {
		t.Errorf("Protocol() = %q", c.Protocol())
	}
	ch, err := c.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("stub 的 Stream 不应直接返回 error：%v", err)
	}
	evs := collect(t, ch)
	if len(evs) != 1 || evs[0].Kind != EvError || !strings.Contains(evs[0].Err, "mystery-proto") {
		t.Errorf("事件 = %+v", evs)
	}
}

// ---------------- 能力推断 / 命名 / URL ----------------

func TestInferModelCaps(t *testing.T) {
	cases := []struct {
		id     string
		window int
		max    int
		reason bool
		vision bool
	}{
		{"gemini-1.5-pro", 1048576, 8192, false, true},
		{"gemini-2.5-pro", 1048576, 65536, true, true},
		{"gemini-2.0-flash", 1048576, 65536, false, true},
		{"gemini-exp-1206", 1048576, 65536, false, true},
		{"claude-sonnet-4-5", 200000, 64000, true, true},
		{"claude-opus-4-1", 200000, 64000, true, true},
		{"claude-3-7-sonnet", 200000, 64000, true, true},
		{"claude-3.7-sonnet", 200000, 64000, true, true},
		{"claude-3-5-sonnet", 200000, 8192, false, true},
		{"gpt-4.1", 1047576, 32768, false, true},
		{"gpt-4o", 128000, 16384, false, true},
		{"gpt-4-turbo", 128000, 16384, false, true},
		{"gpt-4-vision-preview", 128000, 16384, false, true},
		{"gpt-5", 400000, 128000, true, true},
		{"o3-mini", 400000, 128000, true, true},
		{"o4-mini", 400000, 128000, true, true},
		{"o1", 400000, 128000, true, false},
		{"deepseek-reasoner", 131072, 65536, true, false},
		{"deepseek-r1", 131072, 65536, true, false},
		{"deepseek-chat", 131072, 8192, false, false},
		{"kimi-k2", 262144, 65536, false, false},
		{"moonshot-v1-128k", 262144, 65536, false, false},
		{"qwen3-max", 262144, 65536, false, false},
		{"qwen2.5-vl-72b", 131072, 16384, false, true},
		{"qwq-32b", 131072, 16384, true, false},
		{"glm-4.5", 131072, 65536, true, false},
		{"glm-4-plus", 131072, 4096, false, false},
		{"glm-z1-air", 131072, 4096, true, false},
		{"grok-4", 131072, 16384, false, true},
		{"grok-3-mini", 131072, 16384, false, true},
		{"mistral-large", 131072, 16384, false, false},
		{"magistral-medium", 131072, 16384, true, false},
		{"llama-3.3-70b", 131072, 8192, false, false},
		{"some-unknown-model", 128000, 16384, false, false},
		{"thinking-model-think", 128000, 16384, true, false},
		{"qwen-vl-max", 131072, 16384, false, true},
		{"internvl2", 128000, 16384, false, true},
	}
	for _, c := range cases {
		got := InferModelCaps(c.id)
		if got.ContextWindow != c.window || got.MaxTokens != c.max || got.Reasoning != c.reason || got.Vision != c.vision {
			t.Errorf("InferModelCaps(%q) = %+v，期望 {%d %d %v %v}",
				c.id, got, c.window, c.max, c.reason, c.vision)
		}
	}
}

func TestResolveModelCaps(t *testing.T) {
	window, maxTok := 32000, 4000
	reason, vision := false, false
	got := ResolveModelCaps("gpt-4o", &config.PartialCaps{
		ContextWindow: &window, MaxTokens: &maxTok, Reasoning: &reason, Vision: &vision,
	})
	want := config.ModelCaps{ContextWindow: 32000, MaxTokens: 4000, Reasoning: false, Vision: false}
	if got != want {
		t.Errorf("覆盖后 = %+v，期望 %+v", got, want)
	}
	// nil 覆盖表：保持推断值。
	if got := ResolveModelCaps("gpt-4o", nil); got != InferModelCaps("gpt-4o") {
		t.Errorf("nil 覆盖应等于推断值，实际 %+v", got)
	}
	// 部分覆盖：只改窗口。
	if got := ResolveModelCaps("gpt-4o", &config.PartialCaps{ContextWindow: &window}); got.ContextWindow != 32000 || got.MaxTokens != 16384 || !got.Vision {
		t.Errorf("部分覆盖结果异常：%+v", got)
	}
}

func TestAutoModelName(t *testing.T) {
	cases := map[string]string{
		"gpt-4o":                  "GPT 4o",
		"gpt-4.1-mini":            "GPT 4.1 Mini",
		"o3-mini":                 "O3 Mini",
		"claude-sonnet-4-5":       "Claude Sonnet 4 5",
		"gemini-2.5-pro":          "Gemini 2.5 Pro",
		"deepseek_reasoner":       "Deepseek Reasoner",
		"qwen2.5-vl-72b":          "Qwen2.5 Vl 72b",
		"models/gemini-2.5-flash": "Gemini 2.5 Flash",
		"":                        "",
		"GLM-4.5":                 "Glm 4.5",
		"gpt-5":                   "GPT 5",
	}
	for id, want := range cases {
		if got := AutoModelName(id); got != want {
			t.Errorf("AutoModelName(%q) = %q，期望 %q", id, got, want)
		}
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	cases := []struct{ protocol, in, want string }{
		{config.ProtoOpenAIChat, "https://api.openai.com/v1/", "https://api.openai.com/v1"},
		{config.ProtoOpenAIChat, "https://api.openai.com", "https://api.openai.com"},
		{config.ProtoOpenAIResp, " https://api.openai.com/v1// ", "https://api.openai.com/v1"},
		{config.ProtoAnthropic, "https://api.anthropic.com/", "https://api.anthropic.com"},
		{config.ProtoAnthropic, "https://api.anthropic.com/v1", "https://api.anthropic.com/v1"},
		{config.ProtoGemini, "https://generativelanguage.googleapis.com", "https://generativelanguage.googleapis.com/v1beta"},
		{config.ProtoGemini, "https://generativelanguage.googleapis.com/", "https://generativelanguage.googleapis.com/v1beta"},
		{config.ProtoGemini, "https://generativelanguage.googleapis.com/v1beta", "https://generativelanguage.googleapis.com/v1beta"},
		{config.ProtoGemini, "https://generativelanguage.googleapis.com/v1beta/", "https://generativelanguage.googleapis.com/v1beta"},
		{config.ProtoOpenAIChat, "", "https://api.openai.com/v1"},
		{config.ProtoGemini, "", "https://generativelanguage.googleapis.com/v1beta"},
	}
	for _, c := range cases {
		if got := NormalizeBaseURL(c.protocol, c.in); got != c.want {
			t.Errorf("NormalizeBaseURL(%q, %q) = %q，期望 %q", c.protocol, c.in, got, c.want)
		}
	}
}

// ---------------- FetchRemoteModels ----------------

func TestFetchRemoteModels(t *testing.T) {
	tests := []struct {
		name      string
		protocol  string
		path      string
		headerKey string
		headerVal string
		payload   string
		wantIDs   []string
		wantNames []string
	}{
		{
			name:      "openai-completions",
			protocol:  config.ProtoOpenAIChat,
			path:      "/models",
			headerKey: "Authorization",
			headerVal: "Bearer sk-x",
			payload:   `{"object":"list","data":[{"id":"gpt-4o"},{"id":"gpt-4.1-mini"}]}`,
			wantIDs:   []string{"gpt-4o", "gpt-4.1-mini"},
			wantNames: []string{"GPT 4o", "GPT 4.1 Mini"},
		},
		{
			name:      "anthropic-messages",
			protocol:  config.ProtoAnthropic,
			path:      "/v1/models",
			headerKey: "x-api-key",
			headerVal: "sk-ant",
			payload:   `{"data":[{"id":"claude-sonnet-4-5","display_name":"Claude Sonnet 4.5"}]}`,
			wantIDs:   []string{"claude-sonnet-4-5"},
			wantNames: []string{"Claude Sonnet 4.5"},
		},
		{
			name:      "google-generative-ai",
			protocol:  config.ProtoGemini,
			path:      "/v1beta/models",
			headerKey: "x-goog-api-key",
			headerVal: "gm",
			payload:   `{"models":[{"name":"models/gemini-2.5-pro","displayName":"Gemini 2.5 Pro"},{"name":"models/gemini-2.0-flash"}]}`,
			wantIDs:   []string{"gemini-2.5-pro", "gemini-2.0-flash"},
			wantNames: []string{"Gemini 2.5 Pro", "Gemini 2.0 Flash"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotHeader string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotHeader = r.Header.Get(tc.headerKey)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.payload)
			}))
			defer srv.Close()

			out, err := FetchRemoteModels(tc.protocol, srv.URL, strings.TrimPrefix(tc.headerVal, "Bearer "), testTransport())
			if err != nil {
				t.Fatalf("FetchRemoteModels 失败：%v", err)
			}
			if gotPath != tc.path {
				t.Errorf("请求路径 = %q，期望 %q", gotPath, tc.path)
			}
			if gotHeader != tc.headerVal {
				t.Errorf("%s = %q，期望 %q", tc.headerKey, gotHeader, tc.headerVal)
			}
			if len(out) != len(tc.wantIDs) {
				t.Fatalf("模型数 = %d，期望 %d：%+v", len(out), len(tc.wantIDs), out)
			}
			for i, id := range tc.wantIDs {
				if out[i].ID != id {
					t.Errorf("[%d] ID = %q，期望 %q", i, out[i].ID, id)
				}
				if out[i].Name != tc.wantNames[i] {
					t.Errorf("[%d] Name = %q，期望 %q", i, out[i].Name, tc.wantNames[i])
				}
			}
		})
	}
}

// TestFetchRemoteModelsError 校验失败时返回中文错误并带状态码。
func TestFetchRemoteModelsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
	}))
	defer srv.Close()
	_, err := FetchRemoteModels(config.ProtoOpenAIChat, srv.URL, "bad", testTransport())
	if err == nil {
		t.Fatal("期望返回错误")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("错误信息应含状态码与响应体：%s", err)
	}
}

// TestModelListEmpty 结果为空要报错，避免前端拿到空下拉。
func TestModelListEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer srv.Close()
	if _, err := FetchRemoteModels(config.ProtoOpenAIChat, srv.URL, "k", testTransport()); err == nil {
		t.Fatal("空列表应返回错误")
	}
}

// TestRetryOn5xx 校验 5xx 会重试（第一次失败、第二次成功）。
func TestRetryOn5xx(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "bad gateway")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := NewClient(config.ProtoOpenAIChat, srv.URL, "k", Transport{
		Timeout: 5 * time.Second, IdleTimeout: 5 * time.Second,
		MaxRetries: 2, RetryBase: time.Millisecond, MaxRetryDelay: 2 * time.Millisecond,
	})
	ch, err := c.Stream(context.Background(), Request{Model: "m", Stream: true, Messages: []Message{TextMsg(RoleUser, "hi")}})
	if err != nil {
		t.Fatalf("重试后应成功，实际错误：%v", err)
	}
	evs := collect(t, ch)
	assertNoErrorEvent(t, evs)
	if got := textOf(evs, EvText); got != "ok" {
		t.Errorf("text = %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts < 2 {
		t.Errorf("5xx 应触发重试，实际请求 %d 次", attempts)
	}
}

// TestSSEReaderFrames 直接校验帧解析：event 字段、注释行、多行 data、[DONE]。
func TestSSEReaderFrames(t *testing.T) {
	raw := ": keepalive\n\n" +
		"event: foo\ndata: line1\ndata: line2\n\n" +
		"data: [DONE]\n\n"
	rd := newSSEReader(io.NopCloser(strings.NewReader(raw)), time.Second)
	defer rd.stop()

	ev, ok, err := rd.Next()
	if err != nil || !ok {
		t.Fatalf("第一帧解析失败：ok=%v err=%v", ok, err)
	}
	if ev.Event != "foo" || ev.Data != "line1\nline2" {
		t.Errorf("第一帧 = %+v", ev)
	}
	if _, ok, err := rd.Next(); ok || err != nil {
		t.Errorf("[DONE] 应结束流：ok=%v err=%v", ok, err)
	}
}

// TestNonStreamFallback 覆盖 StreamDisabled 时的非流式解码（四协议各一例）。
func TestNonStreamFallback(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		payload  string
		wantText string
		wantTool string
		wantIn   int
		wantOut  int
	}{
		{
			name:     "openai-chat",
			protocol: config.ProtoOpenAIChat,
			payload: `{"choices":[{"message":{"content":"<svg/>","tool_calls":[
				{"id":"call_1","function":{"name":"emit_svg","arguments":"{\"svg\":\"<svg/>\"}"}}]}}],
				"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
			wantText: "<svg/>",
			wantTool: "emit_svg",
			wantIn:   3,
			wantOut:  2,
		},
		{
			name:     "openai-responses",
			protocol: config.ProtoOpenAIResp,
			payload: `{"output":[
				{"type":"message","content":[{"type":"output_text","text":"<svg/>"}]},
				{"type":"function_call","call_id":"call_2","name":"emit_svg","arguments":"{\"svg\":\"<svg/>\"}"}],
				"usage":{"input_tokens":4,"output_tokens":5}}`,
			wantText: "<svg/>",
			wantTool: "emit_svg",
			wantIn:   4,
			wantOut:  5,
		},
		{
			name:     "anthropic",
			protocol: config.ProtoAnthropic,
			payload: `{"content":[
				{"type":"text","text":"<svg/>"},
				{"type":"tool_use","id":"toolu_1","name":"emit_svg","input":{"svg":"<svg/>"}}],
				"usage":{"input_tokens":6,"output_tokens":7}}`,
			wantText: "<svg/>",
			wantTool: "emit_svg",
			wantIn:   6,
			wantOut:  7,
		},
		{
			name:     "gemini",
			protocol: config.ProtoGemini,
			payload: `{"candidates":[{"content":{"parts":[
				{"text":"<svg/>"},
				{"functionCall":{"name":"emit_svg","args":{"svg":"<svg/>"}}}]}}],
				"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":9}}`,
			wantText: "<svg/>",
			wantTool: "emit_svg",
			wantIn:   8,
			wantOut:  9,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.payload)
			}))
			defer srv.Close()

			c := NewClient(tc.protocol, srv.URL, "k", testTransport())
			ch, err := c.Stream(context.Background(), Request{
				Model:    "test-model",
				Stream:   false, // 关键：非流式
				Messages: []Message{TextMsg(RoleUser, "hi")},
			})
			if err != nil {
				t.Fatalf("Stream 返回错误：%v", err)
			}
			evs := collect(t, ch)
			assertNoErrorEvent(t, evs)
			if got := textOf(evs, EvText); got != tc.wantText {
				t.Errorf("text = %q，期望 %q", got, tc.wantText)
			}
			start := findEvent(evs, EvToolStart)
			if start == nil || start.Name != tc.wantTool {
				t.Errorf("tool_start = %+v，期望 name=%q", start, tc.wantTool)
			}
			if got := textOf(evs, EvToolDelta); got == "" {
				t.Error("非流式工具调用也应产出完整的 tool_delta 参数")
			}
			u := findEvent(evs, EvUsage)
			if u == nil || u.Usage == nil || u.Usage.InputTokens != tc.wantIn || u.Usage.OutputTokens != tc.wantOut {
				t.Errorf("usage = %+v，期望 in=%d out=%d", u, tc.wantIn, tc.wantOut)
			}
			if !hasKind(evs, EvDone) {
				t.Error("缺少 done 事件")
			}
		})
	}
}
