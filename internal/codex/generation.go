package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"svg-master/internal/config"
	"svg-master/internal/llm"
)

// ModelClient lets SVG Master's existing agent execute dynamic tools, while Codex
// owns the authenticated model connection and the in-flight turn.
type ModelClient struct {
	m        *Manager
	provider string
	c        *client
	thread   string
	turn     string
	events   chan rpcReply
	life     context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	pending  *rpcReply
	callID   string
	finished bool
	usage    llm.Usage
}

func (m *Manager) Client(id string) *ModelClient {
	ctx, cancel := context.WithCancel(context.Background())
	return &ModelClient{m: m, provider: id, life: ctx, cancel: cancel, events: make(chan rpcReply, 128)}
}
func (a *ModelClient) Protocol() string { return "codex" }
func (a *ModelClient) Close() error {
	a.cancel()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.c != nil && a.thread != "" {
		a.c.mu.Lock()
		delete(a.c.events, a.thread)
		a.c.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var out any
		if !a.finished && a.turn != "" {
			_ = a.c.call(ctx, "turn/interrupt", map[string]string{"threadId": a.thread, "turnId": a.turn}, &out)
		}
		_ = a.c.call(ctx, "thread/unsubscribe", map[string]string{"threadId": a.thread}, &out)
		a.thread = ""
	}
	return nil
}

func (a *ModelClient) Stream(ctx context.Context, req llm.Request) (stream <-chan llm.StreamEvent, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	defer func() {
		if err != nil {
			a.cancel()
			err = llm.NoRetry(err)
		}
	}()
	if a.life.Err() != nil {
		return nil, errors.New("Codex 生成已结束，请重新发送请求")
	}
	if a.c == nil {
		c, err := a.m.get(ctx, a.provider)
		if err != nil {
			return nil, err
		}
		a.c = c
		if err = a.begin(ctx, req); err != nil {
			return nil, err
		}
	} else if a.pending != nil {
		var result *llm.ToolResult
		for i := len(req.Messages) - 1; i >= 0 && result == nil; i-- {
			for _, b := range req.Messages[i].Blocks {
				if b.ToolResult != nil && b.ToolResult.CallID == a.callID {
					result = b.ToolResult
					break
				}
			}
		}
		if result == nil {
			return nil, errors.New("缺少 Codex 绘图工具结果")
		}
		items := []map[string]any{{"type": "inputText", "text": result.Content}}
		for _, img := range result.Images {
			items = append(items, map[string]any{"type": "inputImage", "imageUrl": "data:" + img.MIME + ";base64," + img.Data})
		}
		if err := a.c.write(map[string]any{"id": a.pending.ID, "result": map[string]any{"contentItems": items, "success": !result.IsError}}); err != nil {
			return nil, err
		}
		a.pending = nil
	} else if a.finished {
		return nil, errors.New("Codex 本轮已结束，请重新发送请求")
	}
	ch := make(chan llm.StreamEvent, 64)
	go a.pump(ctx, req, ch)
	return ch, nil
}

func (a *ModelClient) begin(ctx context.Context, req llm.Request) error {
	tools := make([]map[string]any, 0, len(req.Tools))
	for _, t := range req.Tools {
		tools = append(tools, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "inputSchema": t.Parameters})
	}
	params := map[string]any{
		"model": req.Model, "ephemeral": true,
		"sandbox": "read-only", "approvalPolicy": "never",
		"baseInstructions":      req.System,
		"developerInstructions": "Use only svg_master tools to create or edit SVG. All project operations are handled by these tools.",
		"environments":          []any{},
		"dynamicTools": []any{map[string]any{
			"type": "namespace", "name": "svg_master", "description": "SVG Master drawing tools", "tools": tools,
		}},
		"config": map[string]any{
			"features.shell_tool": false, "features.multi_agent": false,
			"web_search": "disabled", "apps._default.enabled": false,
		},
	}
	if req.ServiceTier != "" {
		params["serviceTier"] = req.ServiceTier
	}
	var out struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := a.c.call(ctx, "thread/start", params, &out); err != nil {
		return err
	}
	a.thread = out.Thread.ID
	a.c.mu.Lock()
	a.c.events[a.thread] = &subscription{events: a.events, done: a.life.Done()}
	a.c.mu.Unlock()
	last := len(req.Messages) - 1
	if last < 0 {
		return errors.New("缺少生成提示词")
	}
	if last > 0 {
		items, err := historyItems(req.Messages[:last])
		if err != nil {
			return err
		}
		var ignored any
		if len(items) > 0 {
			if err = a.c.call(ctx, "thread/inject_items", map[string]any{"threadId": a.thread, "items": items}, &ignored); err != nil {
				return err
			}
		}
	}
	input := []map[string]any{}
	for _, b := range req.Messages[last].Blocks {
		if b.Type == llm.BlockText {
			input = append(input, map[string]any{"type": "text", "text": b.Text})
		}
		if b.Image != nil {
			input = append(input, map[string]any{"type": "image", "url": "data:" + b.Image.MIME + ";base64," + b.Image.Data})
		}
	}
	turn := map[string]any{"threadId": a.thread, "input": input, "summary": "auto"}
	effort := req.Thinking
	if effort == "off" {
		effort = ""
	}
	if effort != "" {
		turn["effort"] = effort
	}
	var started struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := a.c.call(ctx, "turn/start", turn, &started); err != nil {
		return err
	}
	a.turn = started.Turn.ID
	return nil
}

func (a *ModelClient) pump(ctx context.Context, req llm.Request, ch chan<- llm.StreamEvent) {
	defer close(ch)
	baseline := a.usage
	send := func(e llm.StreamEvent) bool {
		if e.Kind == llm.EvError && e.Cause == nil {
			e.Cause = llm.NoRetry(errors.New(e.Err))
		}
		select {
		case ch <- e:
			return true
		case <-ctx.Done():
			return false
		case <-a.life.Done():
			return false
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.life.Done():
			return
		case <-a.c.done:
			send(llm.StreamEvent{Kind: llm.EvError, Err: "Codex 连接已断开"})
			return
		case ev := <-a.events:
			var p struct {
				Delta     string          `json:"delta"`
				CallID    string          `json:"callId"`
				Tool      string          `json:"tool"`
				Arguments json.RawMessage `json:"arguments"`
				Turn      struct {
					Status string `json:"status"`
					Error  *struct {
						Message string `json:"message"`
					} `json:"error"`
				} `json:"turn"`
				TokenUsage struct {
					Total struct {
						Input  int `json:"inputTokens"`
						Output int `json:"outputTokens"`
						Cached int `json:"cachedInputTokens"`
						Total  int `json:"totalTokens"`
					} `json:"total"`
				} `json:"tokenUsage"`
			}
			if json.Unmarshal(ev.Params, &p) != nil {
				continue
			}
			switch ev.Method {
			case "item/agentMessage/delta":
				if !send(llm.StreamEvent{Kind: llm.EvText, Text: p.Delta}) {
					return
				}
			case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
				if !send(llm.StreamEvent{Kind: llm.EvThinking, Text: p.Delta}) {
					return
				}
			case "thread/tokenUsage/updated":
				u := p.TokenUsage.Total
				delta := llm.Usage{InputTokens: u.Input - baseline.InputTokens, OutputTokens: u.Output - baseline.OutputTokens, CacheReadTokens: u.Cached - baseline.CacheReadTokens, TotalTokens: u.Total - baseline.TotalTokens}
				a.usage = llm.Usage{InputTokens: u.Input, OutputTokens: u.Output, CacheReadTokens: u.Cached, TotalTokens: u.Total}
				if !send(llm.StreamEvent{Kind: llm.EvUsage, Usage: &delta}) {
					return
				}
			case "item/tool/call":
				known := false
				for _, t := range req.Tools {
					if t.Name == p.Tool {
						known = true
						break
					}
				}
				if !known {
					_ = a.c.write(map[string]any{"id": ev.ID, "result": map[string]any{"contentItems": []any{map[string]string{"type": "inputText", "text": "Unknown SVG Master tool"}}, "success": false}})
					send(llm.StreamEvent{Kind: llm.EvError, Err: "Codex 调用了未注册的绘图工具"})
					return
				}
				a.mu.Lock()
				a.pending = &ev
				a.callID = p.CallID
				a.mu.Unlock()
				if !send(llm.StreamEvent{Kind: llm.EvToolStart, ID: p.CallID, Name: p.Tool, Index: 0}) {
					return
				}
				send(llm.StreamEvent{Kind: llm.EvToolDelta, ID: p.CallID, Text: string(p.Arguments), Index: 0})
				return
			case "turn/completed":
				a.mu.Lock()
				a.finished = true
				a.mu.Unlock()
				if p.Turn.Status != "completed" {
					msg := "Codex 生成未完成"
					if p.Turn.Error != nil {
						msg += "：" + p.Turn.Error.Message
					}
					send(llm.StreamEvent{Kind: llm.EvError, Err: msg})
					return
				}
				if req.ServiceTier != "" {
					send(llm.StreamEvent{Kind: llm.EvServiceTier})
				}
				send(llm.StreamEvent{Kind: llm.EvDone})
				return
			default:
				if len(ev.ID) > 0 {
					_ = a.c.write(map[string]any{"id": ev.ID, "error": map[string]any{"code": -32601, "message": "SVG Master only handles drawing tools"}})
				}
			}
		}
	}
}

func historyItems(messages []llm.Message) ([]map[string]any, error) {
	items := []map[string]any{}
	for _, m := range messages {
		role := m.Role
		if role == llm.RoleSystem {
			role = "developer"
		}
		parts := []map[string]any{}
		for _, b := range m.Blocks {
			switch b.Type {
			case llm.BlockText:
				if role == llm.RoleTool {
					continue
				}
				kind := "input_text"
				if role == llm.RoleAssistant {
					kind = "output_text"
				}
				parts = append(parts, map[string]any{"type": kind, "text": b.Text})
			case llm.BlockImage:
				if b.Image != nil {
					parts = append(parts, map[string]any{"type": "input_image", "image_url": "data:" + b.Image.MIME + ";base64," + b.Image.Data})
				}
			}
		}
		if len(parts) > 0 {
			items = append(items, map[string]any{"type": "message", "role": role, "content": parts})
		}
		for _, b := range m.Blocks {
			if b.ToolCall != nil {
				t := b.ToolCall
				if !json.Valid(t.Arguments) {
					return nil, fmt.Errorf("历史工具参数无效：%s", t.Name)
				}
				items = append(items, map[string]any{"type": "function_call", "namespace": "svg_master", "name": t.Name, "call_id": t.ID, "arguments": string(t.Arguments)})
			}
			if b.ToolResult != nil {
				t := b.ToolResult
				items = append(items, map[string]any{"type": "function_call_output", "call_id": t.CallID, "output": t.Content})
			}
		}
	}
	return items, nil
}

// Preserve exact model IDs advertised by the signed-in Codex account.
func (m *Manager) Models(ctx context.Context, id string) ([]config.ModelEntry, error) {
	c, err := m.get(ctx, id)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []struct {
			Model  string `json:"model"`
			Name   string `json:"displayName"`
			Hidden bool   `json:"hidden"`
		} `json:"data"`
	}
	if err = c.call(ctx, "model/list", map[string]any{"includeHidden": false}, &out); err != nil {
		return nil, err
	}
	models := []config.ModelEntry{}
	for _, v := range out.Data {
		if !v.Hidden && strings.TrimSpace(v.Model) != "" {
			models = append(models, config.ModelEntry{ID: v.Model, Name: v.Name})
		}
	}
	return models, nil
}
