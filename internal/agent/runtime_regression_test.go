package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"svg-master/internal/config"
	"svg-master/internal/llm"
)

func TestEmptyAfterSuccessfulDeliveryFinishes(t *testing.T) {
	c := &fakeClient{steps: []fakeStep{
		toolStep("call_1", ToolEmitSVG, map[string]string{"svg": testSVG}),
		textStep(""),
	}}
	s := &sink{}
	engine := New(testDeps(c, s.emit))
	stats, err := engine.Run(context.Background(), Task{Prompt: "画图"})
	if err != nil {
		t.Fatal(err)
	}
	if c.callCount() != 2 || stats.ModelSteps != 2 || stats.StreamRetry != 0 {
		t.Fatalf("交付后空回复仍重试：%+v calls=%d", stats, c.callCount())
	}
	for _, ev := range s.all() {
		if strings.Contains(ev.Text, "空回复") {
			t.Fatal("正常结束被标为空回复")
		}
	}
}

func TestAbortBeforeRunDoesNotCallModel(t *testing.T) {
	c := &fakeClient{steps: []fakeStep{textStep("wrong")}}
	engine := New(testDeps(c, nil))
	engine.Abort()
	_, err := engine.Run(context.Background(), Task{Prompt: "画图"})
	if !errors.Is(err, context.Canceled) || c.callCount() != 0 {
		t.Fatalf("提前取消无效：%v calls=%d", err, c.callCount())
	}
}

func TestAbortPreservesToolResultPairs(t *testing.T) {
	first := toolStep("call_1", ToolValidateSVG, map[string]string{"svg": testSVG})
	second := toolStep("call_2", ToolValidateSVG, map[string]string{"svg": testSVG})
	for i := range second.events {
		second.events[i].Index = 1
	}
	c := &fakeClient{steps: []fakeStep{{events: append(first.events[:len(first.events)-1], second.events...)}}}
	var engine *Engine
	engine = New(testDeps(c, func(ev Event) {
		if ev.Kind == EvToolEnd {
			engine.Abort()
		}
	}))
	stats, err := engine.Run(context.Background(), Task{Prompt: "校验"})
	if !errors.Is(err, context.Canceled) || stats.ToolCalls != 1 {
		t.Fatalf("取消后仍执行工具：%v %+v", err, stats)
	}
	var results []*llm.ToolResult
	for _, msg := range engine.History() {
		for _, block := range msg.Blocks {
			if block.ToolResult != nil {
				results = append(results, block.ToolResult)
			}
		}
	}
	if len(results) != 2 || results[1].CallID != "call_2" || !results[1].IsError {
		t.Fatalf("续聊历史存在未配对调用：%+v", results)
	}
}

type recordingClient struct {
	inner    *fakeClient
	requests []llm.Request
}

func (c *recordingClient) Protocol() string { return c.inner.Protocol() }
func (c *recordingClient) Stream(ctx context.Context, request llm.Request) (<-chan llm.StreamEvent, error) {
	c.requests = append(c.requests, request)
	return c.inner.Stream(ctx, request)
}

func TestNativeOutputSurvivesAgentAndProjection(t *testing.T) {
	native := &llm.NativeOutput{Protocol: config.ProtoOpenAIResp, Items: []json.RawMessage{
		json.RawMessage(`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque"}`),
	}}
	step := toolStep("call_1", ToolEmitSVG, map[string]string{"svg": testSVG})
	step.events = append([]llm.StreamEvent{{Kind: llm.EvOutput, Output: native}}, step.events...)
	fake := &fakeClient{steps: []fakeStep{step, textStep("ok")}}
	recorder := &recordingClient{inner: fake}
	deps := testDeps(fake, nil)
	deps.Client = recorder
	deps.Settings.CompressOn = true
	engine := New(deps)
	if _, err := engine.Run(context.Background(), Task{Prompt: "画图"}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.requests) != 2 {
		t.Fatal("未发起工具续传")
	}
	var found bool
	for _, msg := range recorder.requests[1].Messages {
		if msg.NativeOutput != nil {
			found = true
			if string(msg.NativeOutput.Items[0]) != string(native.Items[0]) {
				t.Fatal("投影改写了原生输出")
			}
		}
	}
	if !found {
		t.Fatal("Agent 或压缩投影丢失原生输出")
	}
	var persisted bool
	for _, msg := range engine.History() {
		if msg.NativeOutput != nil {
			persisted = true
		}
	}
	if !persisted {
		t.Fatal("运行结束未保留原生输出")
	}
}

func TestHTTPRetryBudgetIsNotMultipliedByAgent(t *testing.T) {
	for _, status := range []int{400, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":{"message":"test"}}`)
			}))
			defer srv.Close()
			deps := testDeps(&fakeClient{}, nil)
			deps.Client = llm.NewClient(config.ProtoOpenAIResp, srv.URL, "test", llm.Transport{Timeout: time.Second, IdleTimeout: time.Second, MaxRetries: 3, RetryBase: time.Millisecond, MaxRetryDelay: time.Millisecond})
			_, err := New(deps).Run(context.Background(), Task{Prompt: "画图"})
			if err == nil {
				t.Fatal("HTTP 错误被吞掉")
			}
			want := int32(1)
			if status == 503 {
				want = 4
			}
			if calls.Load() != want {
				t.Fatalf("HTTP %d 请求次数=%d，期望=%d", status, calls.Load(), want)
			}
		})
	}
}

func TestPauseTurnContinuesWithHistory(t *testing.T) {
	c := &fakeClient{steps: []fakeStep{
		{events: []llm.StreamEvent{{Kind: llm.EvThinking, Text: "thinking"}, {Kind: llm.EvContinue}, {Kind: llm.EvDone}}},
		textStep("done"),
	}}
	stats, err := New(testDeps(c, nil)).Run(context.Background(), Task{Prompt: "continue"})
	if err != nil || stats.ModelSteps != 2 {
		t.Fatalf("pause_turn 被误当成结束：%v %+v", err, stats)
	}
}
