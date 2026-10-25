package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"svg-master/internal/config"
)

func responseFrame(value any) string {
	raw, _ := json.Marshal(value)
	return "data: " + string(raw) + "\n\n"
}

func TestResponsesPreservesNativeOutput(t *testing.T) {
	items := []json.RawMessage{
		json.RawMessage(`{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"opaque-reasoning"}`),
		json.RawMessage(`{"id":"msg_1","type":"message","role":"assistant","phase":"commentary","status":"completed","content":[{"type":"output_text","text":"开始","annotations":[]}]}`),
		json.RawMessage(`{"id":"fc_1","type":"function_call","call_id":"call_1","name":"emit_svg","arguments":"{\"svg\":\"<svg/>\"}","status":"completed"}`),
	}
	srv, _ := sseServer(t, []string{responseFrame(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": items}})})
	client := &openaiResponses{base: srv.URL, t: testTransport()}
	ch, err := client.Stream(context.Background(), Request{Stream: true, Model: "gpt-6.1-sol"})
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, ch)
	assertNoErrorEvent(t, events)
	if textOf(events, EvText) != "开始" {
		t.Fatal("完成快照里的正文丢失")
	}
	output := findEvent(events, EvOutput)
	if output == nil || output.Output == nil {
		t.Fatal("原生输出未保存")
	}
	body, err := client.buildBody(Request{Model: "gpt-6.1-sol", SessionID: "session_1", Messages: []Message{
		TextMsg(RoleUser, "画图"),
		{Role: RoleAssistant, Blocks: []Block{{Type: BlockText, Text: "不应重复重建"}}, NativeOutput: output.Output},
		{Role: RoleTool, Blocks: []Block{{Type: BlockToolResult, ToolResult: &ToolResult{CallID: "call_1", Content: "ok"}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Input []json.RawMessage `json:"input"`
		Store bool              `json:"store"`
		Key   string            `json:"prompt_cache_key"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Input) != 5 || request.Store || request.Key != "session_1" {
		t.Fatalf("请求结构异常：%s", body)
	}
	for i, item := range items {
		var before, after any
		_ = json.Unmarshal(item, &before)
		_ = json.Unmarshal(request.Input[i+1], &after)
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("第 %d 项续传发生变化：%s", i, request.Input[i+1])
		}
	}
}

func TestResponsesRecoversLateToolMetadata(t *testing.T) {
	srv, _ := sseServer(t, []string{
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"svg\":"}` + "\n\n",
		`data: {"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"real_call","name":"emit_svg","arguments":"{\"svg\":\"<svg/>\"}"}}` + "\n\n",
		`data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n",
	})
	client := NewClient(config.ProtoOpenAIResp, srv.URL, "test", testTransport())
	ch, err := client.Stream(context.Background(), Request{Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, ch)
	assertNoErrorEvent(t, events)
	var id, name, args string
	for _, ev := range events {
		if ev.Kind == EvToolStart {
			if ev.ID != "" {
				id = ev.ID
			}
			if ev.Name != "" {
				name = ev.Name
			}
		}
		if ev.Kind == EvToolDelta {
			if ev.Replace {
				args = ""
			}
			args += ev.Text
		}
	}
	if id != "real_call" || name != "emit_svg" || args != `{"svg":"<svg/>"}` {
		t.Fatalf("恢复失败：%s %s %s", id, name, args)
	}
}

func TestProtocolPermanentErrors(t *testing.T) {
	cases := []struct{ name, protocol, frame, want string }{
		{"responses-incomplete", config.ProtoOpenAIResp, `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`, "max_output_tokens"},
		{"responses-failed", config.ProtoOpenAIResp, `{"type":"response.failed","response":{"error":{"code":"invalid_request_error","message":"missing reasoning"}}}`, "missing reasoning"},
		{"chat-length", config.ProtoOpenAIChat, `{"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":"length"}]}`, "length"},
		{"chat-filter", config.ProtoOpenAIChat, `{"choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`, "content_filter"},
		{"anthropic-max", config.ProtoAnthropic, `{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`, "max_tokens"},
		{"anthropic-auth", config.ProtoAnthropic, `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`, "bad key"},
		{"gemini-max", config.ProtoGemini, `{"candidates":[{"finishReason":"MAX_TOKENS"}]}`, "MAX_TOKENS"},
		{"gemini-blocked", config.ProtoGemini, `{"promptFeedback":{"blockReason":"SAFETY"}}`, "SAFETY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := sseServer(t, []string{"data: " + tc.frame + "\n\n"})
			client := NewClient(tc.protocol, srv.URL, "test", testTransport())
			ch, err := client.Stream(context.Background(), Request{Stream: true, Model: "test"})
			if err != nil {
				t.Fatal(err)
			}
			events := collect(t, ch)
			ev := findEvent(events, EvError)
			if ev == nil || !strings.Contains(ev.Err, tc.want) || ev.Cause == nil || Retryable(ev.Cause) {
				t.Fatalf("错误未正确分类：%+v", events)
			}
			if findEvent(events, EvDone) != nil {
				t.Fatal("错误被标成正常完成")
			}
		})
	}
}

func TestProtocolJSONFallbackAndErrors(t *testing.T) {
	cases := []struct{ protocol, body string }{
		{config.ProtoOpenAIResp, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`},
		{config.ProtoOpenAIChat, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`},
		{config.ProtoAnthropic, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`},
		{config.ProtoGemini, `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.protocol, func(t *testing.T) {
			for _, bad := range []bool{false, true} {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if bad {
						_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","code":400,"message":"bad request"}}`)
					} else {
						_, _ = io.WriteString(w, tc.body)
					}
				}))
				client := NewClient(tc.protocol, srv.URL, "test", testTransport())
				ch, err := client.Stream(context.Background(), Request{Stream: true, Model: "test"})
				if err != nil {
					srv.Close()
					t.Fatal(err)
				}
				events := collect(t, ch)
				srv.Close()
				if bad {
					ev := findEvent(events, EvError)
					if ev == nil || ev.Cause == nil || Retryable(ev.Cause) {
						t.Fatalf("200 JSON 错误被吞掉：%+v", events)
					}
				} else {
					assertNoErrorEvent(t, events)
					if textOf(events, EvText) != "ok" {
						t.Fatalf("JSON 兜底丢失：%+v", events)
					}
				}
			}
		})
	}
}

func TestProtocolCancelBeforeAndAfterHeaders(t *testing.T) {
	for _, protocol := range []string{config.ProtoOpenAIResp, config.ProtoOpenAIChat, config.ProtoAnthropic, config.ProtoGemini} {
		for _, headers := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/headers=%t", protocol, headers), func(t *testing.T) {
				started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var calls atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					_, _ = io.Copy(io.Discard, r.Body)
					if headers {
						w.Header().Set("Content-Type", "text/event-stream")
						w.WriteHeader(200)
						w.(http.Flusher).Flush()
					}
					close(started)
					select {
					case <-r.Context().Done():
						close(canceled)
					case <-release:
					}
				}))
				defer srv.Close()
				defer close(release)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan struct{})
				go func() {
					defer close(done)
					ch, err := NewClient(protocol, srv.URL, "test", testTransport()).Stream(ctx, Request{Stream: true, Model: "test"})
					if err == nil {
						for range ch {
						}
					}
				}()
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					t.Fatal("未发起请求")
				}
				cancel()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("取消后客户端未退出")
				}
				select {
				case <-canceled:
				case <-time.After(3 * time.Second):
					t.Fatal("上游连接未收到取消")
				}
				if calls.Load() != 1 {
					t.Fatalf("取消后产生重试：%d", calls.Load())
				}
			})
		}
	}
}

func TestSignedNativeHistory(t *testing.T) {
	t.Run("anthropic", func(t *testing.T) {
		srv, _ := sseServer(t, []string{
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}` + "\n\n",
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think"}}` + "\n\n",
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}` + "\n\n",
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"emit_svg","input":{}}}` + "\n\n",
			`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"svg\":\"<svg/>\"}"}}` + "\n\n",
			`data: {"type":"message_stop"}` + "\n\n",
		})
		client := &anthropicClient{base: srv.URL, t: testTransport()}
		ch, err := client.Stream(context.Background(), Request{Stream: true})
		if err != nil {
			t.Fatal(err)
		}
		events := collect(t, ch)
		assertNoErrorEvent(t, events)
		native := findEvent(events, EvOutput)
		if native == nil || len(native.Output.Items) != 2 {
			t.Fatalf("未保留 thinking + tool_use：%+v", events)
		}
		body, err := client.buildBody(Request{Messages: []Message{{Role: RoleAssistant, NativeOutput: native.Output}}})
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			Messages []struct {
				Content []struct {
					Signature string
					Thinking  string
					Input     map[string]string
				}
			}
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatal(err)
		}
		if len(decoded.Messages) != 1 || len(decoded.Messages[0].Content) != 2 || decoded.Messages[0].Content[0].Signature != "sig" || decoded.Messages[0].Content[0].Thinking != "think" || decoded.Messages[0].Content[1].Input["svg"] != "<svg/>" {
			t.Fatalf("签名或参数丢失：%s", body)
		}
	})
	t.Run("gemini", func(t *testing.T) {
		srv, _ := sseServer(t, []string{`data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"emit_svg","args":{}},"thoughtSignature":"signed"},{"functionCall":{"name":"emit_svg","args":{}},"thoughtSignature":"signed2"}]},"finishReason":"STOP"}]}` + "\n\n"})
		client := &geminiClient{base: srv.URL, t: testTransport()}
		ch, err := client.Stream(context.Background(), Request{Stream: true, Model: "gemini-3-pro"})
		if err != nil {
			t.Fatal(err)
		}
		events := collect(t, ch)
		assertNoErrorEvent(t, events)
		var ids []string
		for _, ev := range events {
			if ev.Kind == EvToolStart {
				ids = append(ids, ev.ID)
			}
		}
		if len(ids) != 2 || ids[0] == ids[1] {
			t.Fatalf("同名工具 ID 冲突：%v", ids)
		}
		native := findEvent(events, EvOutput)
		if native == nil {
			t.Fatal("签名未保存")
		}
		body, err := client.buildBody(Request{Model: "gemini-3-pro", Messages: []Message{{Role: RoleAssistant, NativeOutput: native.Output}}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"thoughtSignature":"signed"`) || !strings.Contains(string(body), ids[1]) {
			t.Fatalf("签名/工具 ID 丢失：%s", body)
		}
	})
}

func TestTruncatedStreamsFail(t *testing.T) {
	for _, protocol := range []string{config.ProtoOpenAIResp, config.ProtoOpenAIChat, config.ProtoAnthropic, config.ProtoGemini} {
		t.Run(protocol, func(t *testing.T) {
			srv, _ := sseServer(t, []string{": keepalive\n\n"})
			ch, err := NewClient(protocol, srv.URL, "test", testTransport()).Stream(context.Background(), Request{Stream: true, Model: "test"})
			if err != nil {
				t.Fatal(err)
			}
			events := collect(t, ch)
			if findEvent(events, EvError) == nil || findEvent(events, EvDone) != nil {
				t.Fatalf("截断流被误判成功：%+v", events)
			}
		})
	}
}

func TestGeminiSelectsGenerationEndpoint(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			method := "generateContent"
			if stream {
				method = "streamGenerateContent"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1beta/models/gemini-3-pro:"+method || (r.URL.Query().Get("alt") == "sse") != stream {
					t.Errorf("调用了错误的端点：%s", r.URL.RequestURI())
					http.Error(w, "wrong endpoint", http.StatusBadRequest)
					return
				}
				payload := `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: "+payload+"\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, payload)
				}
			}))
			defer srv.Close()
			client := NewClient(config.ProtoGemini, srv.URL, "test", testTransport())
			ch, err := client.Stream(context.Background(), Request{Model: "models/gemini-3-pro", Stream: stream})
			if err != nil {
				t.Fatal(err)
			}
			events := collect(t, ch)
			assertNoErrorEvent(t, events)
			if textOf(events, EvText) != "ok" || findEvent(events, EvDone) == nil {
				t.Fatalf("生成结果丢失：%+v", events)
			}
		})
	}
}

func TestNonStreamFailureKeepsUsage(t *testing.T) {
	cases := []struct{ protocol, body string }{
		{config.ProtoOpenAIResp, `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":100,"output_tokens":50,"total_tokens":150}}`},
		{config.ProtoOpenAIChat, `{"choices":[{"message":{"content":"partial"},"finish_reason":"length"}],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}`},
		{config.ProtoAnthropic, `{"content":[{"type":"text","text":"partial"}],"stop_reason":"max_tokens","usage":{"input_tokens":100,"output_tokens":50}}`},
		{config.ProtoGemini, `{"candidates":[{"content":{"parts":[{"text":"partial"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":50,"totalTokenCount":150}}`},
	}
	for _, tc := range cases {
		t.Run(tc.protocol, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			client := NewClient(tc.protocol, srv.URL, "test", testTransport())
			ch, err := client.Stream(context.Background(), Request{Model: "test", Stream: false})
			if err != nil {
				t.Fatal(err)
			}
			events := collect(t, ch)
			usage, failure := findEvent(events, EvUsage), findEvent(events, EvError)
			if usage == nil || usage.Usage == nil || usage.Usage.InputTokens != 100 || usage.Usage.OutputTokens != 50 || usage.Usage.TotalTokens != 150 {
				t.Fatalf("失败响应的用量被丢弃：%+v", events)
			}
			if failure == nil || failure.Cause == nil || Retryable(failure.Cause) || findEvent(events, EvDone) != nil {
				t.Fatalf("截断响应的错误未正确保留：%+v", events)
			}
		})
	}
}
