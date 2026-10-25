package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"svg-master/internal/llm"
)

// A fake app-server checks the actual stdio request/response lifecycle, including
// the server's string request IDs, tool failures, history and interruption.
func TestGenerationRPCProcess(t *testing.T) {
	if os.Getenv("SVG_MASTER_GENERATION_TEST_HELPER") != "1" {
		return
	}
	enc := json.NewEncoder(os.Stdout)
	send := func(v any) { _ = enc.Encode(v) }
	notify := func(method string, p map[string]any) {
		p["threadId"] = "thread-1"
		send(map[string]any{"method": method, "params": p})
	}
	scan := bufio.NewScanner(os.Stdin)
	history, interrupted, unsubscribed := false, false, false
	for scan.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
			Result struct {
				Success bool             `json:"success"`
				Items   []map[string]any `json:"contentItems"`
			} `json:"result"`
		}
		if json.Unmarshal(scan.Bytes(), &req) != nil {
			os.Exit(1)
		}
		var result any = map[string]any{}
		switch req.Method {
		case "thread/start":
			cfg, _ := req.Params["config"].(map[string]any)
			if req.Params["serviceTier"] != "priority" || cfg["features.shell_tool"] != false || cfg["features.multi_agent"] != false {
				send(map[string]any{"id": req.ID, "error": map[string]any{"code": -32602, "message": "bad thread configuration"}})
				continue
			}
			result = map[string]any{"thread": map[string]string{"id": "thread-1"}}
		case "thread/inject_items":
			items, _ := req.Params["items"].([]any)
			history = len(items) == 3
		case "turn/start":
			send(map[string]any{"id": req.ID, "result": map[string]any{"turn": map[string]string{"id": "turn-1"}}})
			notify("item/reasoning/summaryTextDelta", map[string]any{"delta": "Thinking"})
			notify("thread/tokenUsage/updated", map[string]any{"tokenUsage": map[string]any{"total": map[string]int{"inputTokens": 100, "outputTokens": 10, "totalTokens": 110}}})
			send(map[string]any{"id": "tool-request-1", "method": "item/tool/call", "params": map[string]any{"threadId": "thread-1", "callId": "call-1", "tool": "emit_svg", "arguments": map[string]string{"svg": "<svg/>"}}})
			continue
		case "":
			if string(req.ID) != `"tool-request-1"` || req.Result.Success || len(req.Result.Items) == 0 || req.Result.Items[0]["text"] != "Fix invalid SVG" {
				os.Exit(2)
			}
			notify("thread/tokenUsage/updated", map[string]any{"tokenUsage": map[string]any{"total": map[string]int{"inputTokens": 180, "outputTokens": 20, "totalTokens": 200}}})
			notify("thread/tokenUsage/updated", map[string]any{"tokenUsage": map[string]any{"total": map[string]int{"inputTokens": 180, "outputTokens": 25, "totalTokens": 205}}})
			notify("item/agentMessage/delta", map[string]any{"delta": "Done"})
			notify("turn/completed", map[string]any{"turn": map[string]string{"status": "completed"}})
			continue
		case "turn/interrupt":
			interrupted = true
		case "thread/unsubscribe":
			unsubscribed = true
		case "test/state":
			result = map[string]bool{"history": history, "interrupted": interrupted, "unsubscribed": unsubscribed}
		}
		send(map[string]any{"id": req.ID, "result": result})
	}
	os.Exit(0)
}

func generationFixture(t *testing.T) (*Manager, *ModelClient, llm.Request) {
	t.Helper()
	m := New(t.TempDir())
	m.clients["one"] = testProcessClient(t, "TestGenerationRPCProcess", "SVG_MASTER_GENERATION_TEST_HELPER=1")
	t.Cleanup(m.Close)
	c := m.Client("one")
	t.Cleanup(func() { _ = c.Close() })
	req := llm.Request{Model: "test", ServiceTier: "priority", Tools: []llm.ToolSpec{{Name: "emit_svg", Parameters: json.RawMessage(`{"type":"object"}`)}}, Messages: []llm.Message{
		llm.TextMsg(llm.RoleUser, "Draw a circle"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolCall, ToolCall: &llm.ToolCall{ID: "old-call", Name: "emit_svg", Arguments: json.RawMessage(`{"svg":"<svg/>"}`)}}}},
		{Role: llm.RoleTool, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolResult: &llm.ToolResult{CallID: "old-call", Content: "OK"}}}},
		llm.TextMsg(llm.RoleUser, "Make it green"),
	}}
	return m, c, req
}

func collect(t *testing.T, c *ModelClient, req llm.Request) []llm.StreamEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := c.Stream(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var events []llm.StreamEvent
	for e := range ch {
		if e.Kind == llm.EvError {
			t.Fatal(e.Err)
		}
		events = append(events, e)
	}
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	return events
}

func TestGenerationToolRoundTripAndHistory(t *testing.T) {
	m, c, req := generationFixture(t)
	events := collect(t, c, req)
	var callID string
	for _, e := range events {
		if e.Kind == llm.EvToolStart {
			callID = e.ID
			if e.Name != "emit_svg" {
				t.Fatal(e)
			}
		}
	}
	if callID != "call-1" {
		t.Fatalf("missing tool call: %+v", events)
	}
	req.Messages = append(req.Messages, llm.Message{Role: llm.RoleTool, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolResult: &llm.ToolResult{CallID: callID, Content: "Fix invalid SVG", IsError: true}}}})
	events = collect(t, c, req)
	var lastUsage int
	done, tier := false, false
	for _, e := range events {
		switch e.Kind {
		case llm.EvUsage:
			lastUsage = e.Usage.TotalTokens
		case llm.EvDone:
			done = true
		case llm.EvServiceTier:
			tier = true
			if e.Text != "" {
				t.Fatal("unconfirmed Fast reported as applied")
			}
		}
	}
	if !done || !tier || lastUsage != 95 {
		t.Fatalf("completion/usage mismatch: %+v", events)
	}
	_ = c.Close()
	var state map[string]bool
	if err := m.clients["one"].call(context.Background(), "test/state", nil, &state); err != nil {
		t.Fatal(err)
	}
	if !state["history"] || !state["unsubscribed"] || state["interrupted"] {
		t.Fatal(state)
	}
}

func TestGenerationStopInterruptsPendingTool(t *testing.T) {
	m, c, req := generationFixture(t)
	collect(t, c, req)
	_ = c.Close()
	var state map[string]bool
	if err := m.clients["one"].call(context.Background(), "test/state", nil, &state); err != nil {
		t.Fatal(err)
	}
	if !state["interrupted"] || !state["unsubscribed"] {
		t.Fatal(state)
	}
	if _, err := c.Stream(context.Background(), req); err == nil || llm.Retryable(err) {
		t.Fatal("closed stateful request must not replay")
	}
}
