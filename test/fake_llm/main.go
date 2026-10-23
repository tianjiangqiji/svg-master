// 假 LLM 服务器：模拟四种协议的 SSE 流，用于端到端联调（不需要真实 API Key）。
//
//	go run ./test/fake_llm -port 18999
//
// 行为：首轮返回 emit_svg 工具调用（附一张简笔 SVG），次轮（检测到已有 tool 结果）返回纯文本收尾。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// pelicanSVG 是假模型交付的示例图形（纯手写矢量，无外部引用）。
const pelicanSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 400 300" width="400" height="300">
  <rect width="400" height="300" fill="#f7f3e8"/>
  <circle cx="330" cy="60" r="30" fill="#ffd166"/>
  <g stroke="#2b2b2b" stroke-width="4" fill="none">
    <circle cx="130" cy="220" r="34"/>
    <circle cx="230" cy="220" r="34"/>
    <circle cx="130" cy="220" r="8" fill="#2b2b2b"/>
    <circle cx="230" cy="220" r="8" fill="#2b2b2b"/>
    <path d="M130 220 L170 190 L230 220"/>
    <path d="M170 190 L160 150"/>
    <path d="M160 150 L200 145"/>
  </g>
  <g fill="#ffffff" stroke="#2b2b2b" stroke-width="4">
    <ellipse cx="185" cy="130" rx="58" ry="40"/>
    <path d="M145 118 Q120 96 132 78 Q150 92 160 108 Z"/>
    <circle cx="228" cy="104" r="20"/>
    <path d="M246 104 L286 112 L246 120 Z" fill="#f4a261"/>
  </g>
  <circle cx="234" cy="100" r="3" fill="#2b2b2b"/>
  <text x="200" y="285" font-family="system-ui, sans-serif" font-size="16" text-anchor="middle" fill="#5b5b5b">pelican riding a bicycle</text>
</svg>`

// slowMS 每片工具参数之间的额外延时（-slow 参数），默认 0。
var slowMS time.Duration

func main() {
	port := flag.Int("port", 18999, "监听端口")
	slow := flag.Int("slow", 0, "工具参数分片之间的额外延时（毫秒），用于观察实时预览")
	flag.Parse()
	slowMS = time.Duration(*slow) * time.Millisecond

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", openAIChat)
	mux.HandleFunc("/v1/responses", openAIResponses)
	mux.HandleFunc("/v1/messages", anthropic)
	mux.HandleFunc("/v1beta/models/", gemini)
	mux.HandleFunc("/models", modelList)
	mux.HandleFunc("/v1/models", modelList)
	mux.HandleFunc("/v1beta/models", modelList)

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("假 LLM 服务器启动: http://%s", addr)
	log.Printf("  OpenAI 兼容  : http://%s/v1", addr)
	log.Printf("  OpenAI Resp  : http://%s/v1", addr)
	log.Printf("  Anthropic    : http://%s", addr)
	log.Printf("  Gemini       : http://%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

// needsTool 判断当前是否首轮（还没有 tool 结果 → 该发工具调用）。
func needsTool(body map[string]any) bool {
	raw, _ := json.Marshal(body)
	s := strings.ToLower(string(raw))
	// 出现 tool / functionResponse / function_call_output 等结果标记，说明已执行过工具
	for _, marker := range []string{`"role":"tool"`, `"tool_result"`, `"functionresponse"`, `"function_call_output"`, `"function_call_output"`} {
		if strings.Contains(s, marker) {
			return false
		}
	}
	return true
}

// historyRounds 数一数请求里带了几轮历史 user 消息（连续对话验证用）。
// 请求 JSON 里 "role":"user" 出现次数减去本轮 1 次，就是历史轮数。
func historyRounds(body map[string]any) int {
	raw, _ := json.Marshal(body)
	return strings.Count(string(raw), `"role":"user"`) - 1
}

func sse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
}

func emit(w http.ResponseWriter, event string, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	if event != "" {
		fmt.Fprintf(w, "event: %s\n", event)
	}
	fmt.Fprintf(w, "data: %s\n\n", raw)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	time.Sleep(12 * time.Millisecond) // 让流式效果可见
}

// ---------- OpenAI Chat Completions ----------

func openAIChat(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	sse(w)

	if needsTool(body) {
		// 先流一段长思维链，验证前端「思考」分段 + 5 行限高滚动 + 完成后自动折叠
		for _, part := range chunkString("先确认主题：鹈鹕骑自行车。\n\n**第一步，确定构图**：主体居左偏上，自行车斜向右下，留出右上角给太阳。\n\n第二步，拆解形状：\n- 车轮用 `circle` 描边\n- 车身与车架用 `path` 折线\n- 鹈鹕身体用 `ellipse`，翅膀用两条二次贝塞尔\n\n第三步，配色方案：背景米白 #f7f3e8，车轮与线条炭黑 #2b2b2b，鹈鹕羽毛纯白，喙与太阳用暖黄 #ffd166，地面阴影用半透明黑。\n\n> 检查约束：viewBox 400×300，所有坐标取整，避免小于 1px 的细节，保证缩小到 48px 仍可辨识。\n\n第五步，规划绘制顺序：背景 → 太阳 → 阴影 → 车轮 → 车架 → 鹈鹕身体 → 翅膀 → 喙 → 眼睛。", 14) {
			emit(w, "", map[string]any{"id": "chatcmpl-1", "object": "chat.completion.chunk",
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"reasoning_content": part}}}})
		}
		args := mustJSON(map[string]any{"svg": pelicanSVG, "note": "pelican on a bicycle"})
		emit(w, "", map[string]any{"id": "chatcmpl-1", "object": "chat.completion.chunk",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": ""}}}})
		emit(w, "", map[string]any{"id": "chatcmpl-1", "object": "chat.completion.chunk",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{
				map[string]any{"index": 0, "id": "call_fake_1", "type": "function",
					"function": map[string]any{"name": "emit_svg", "arguments": ""}}}}}}})
		for _, part := range chunkString(args, 96) {
			emit(w, "", map[string]any{"id": "chatcmpl-1", "object": "chat.completion.chunk",
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{
					map[string]any{"index": 0, "function": map[string]any{"arguments": part}}}}}}})
			if slowMS > 0 {
				time.Sleep(slowMS)
			}
		}
		emit(w, "", map[string]any{"id": "chatcmpl-1", "object": "chat.completion.chunk",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}})
		emit(w, "", map[string]any{"id": "chatcmpl-1", "object": "chat.completion.chunk",
			"choices": []any{}, "usage": map[string]any{"prompt_tokens": 420, "completion_tokens": 96, "total_tokens": 516}})
		fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}

	rounds := historyRounds(body)
	text := fmt.Sprintf("## 交付说明\n\n**SVG 已交付**。画面包含白色鹈鹕、黑色自行车与暖色背景，`viewBox 400×300`，纯矢量无外部引用。\n\n- 车轮：`circle` 描边\n- 身体：`ellipse` + 两条贝塞尔\n- 配色：米白背景 + 暖黄 *太阳*\n\n1. 先铺背景\n2. 再画主体\n3. 最后点缀\n\n> 提示：可直接保存或继续迭代。\n\n```svg\n<circle cx=\"200\" cy=\"150\" r=\"40\"/>\n```\n\n（本次携带 %d 轮历史对话）", rounds)
	if rounds <= 0 {
		text = "## 交付说明\n\n**SVG 已交付**。画面包含白色鹈鹕、黑色自行车与暖色背景，`viewBox 400×300`，纯矢量无外部引用。\n\n- 车轮：`circle` 描边\n- 身体：`ellipse` + 两条贝塞尔\n- 配色：米白背景 + 暖黄 *太阳*\n\n1. 先铺背景\n2. 再画主体\n3. 最后点缀\n\n> 提示：可直接保存或继续迭代。\n\n```svg\n<circle cx=\"200\" cy=\"150\" r=\"40\"/>\n```"
	}
	for _, part := range chunkString(text, 12) {
		emit(w, "", map[string]any{"id": "chatcmpl-2", "object": "chat.completion.chunk",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": part}}}})
	}
	emit(w, "", map[string]any{"id": "chatcmpl-2", "object": "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
	emit(w, "", map[string]any{"id": "chatcmpl-2", "object": "chat.completion.chunk",
		"choices": []any{}, "usage": map[string]any{"prompt_tokens": 620, "completion_tokens": 42, "total_tokens": 662}})
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// ---------- OpenAI Responses ----------

func openAIResponses(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	sse(w)

	if needsTool(body) {
		args := mustJSON(map[string]any{"svg": pelicanSVG, "note": "pelican on a bicycle"})
		emit(w, "response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_1"}})
		emit(w, "response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0,
			"item": map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_fake_1", "name": "emit_svg", "arguments": ""}})
		for _, part := range chunkString(args, 96) {
			emit(w, "response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta",
				"output_index": 0, "item_id": "fc_1", "delta": part})
		}
		emit(w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0,
			"item": map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_fake_1", "name": "emit_svg", "arguments": args}})
		emit(w, "response.completed", map[string]any{"type": "response.completed", "response": map[string]any{
			"id": "resp_1", "status": "completed",
			"usage": map[string]any{"input_tokens": 420, "output_tokens": 96, "total_tokens": 516}}})
		return
	}

	emit(w, "response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_2"}})
	for _, part := range chunkString("SVG 已交付，比例 4:3，可直接用于 logo 或插图。", 10) {
		emit(w, "response.output_text.delta", map[string]any{"type": "response.output_text.delta",
			"output_index": 0, "content_index": 0, "delta": part})
	}
	emit(w, "response.completed", map[string]any{"type": "response.completed", "response": map[string]any{
		"id": "resp_2", "status": "completed",
		"usage": map[string]any{"input_tokens": 620, "output_tokens": 42, "total_tokens": 662}}})
}

// ---------- Anthropic Messages ----------

func anthropic(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	sse(w)

	if needsTool(body) {
		args := mustJSON(map[string]any{"svg": pelicanSVG, "note": "pelican on a bicycle"})
		emit(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": "msg_1", "role": "assistant", "usage": map[string]any{"input_tokens": 420, "output_tokens": 0}}})
		emit(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "tool_use", "id": "toolu_fake_1", "name": "emit_svg", "input": map[string]any{}}})
		for _, part := range chunkString(args, 96) {
			emit(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": part}})
		}
		emit(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		emit(w, "message_delta", map[string]any{"type": "message_delta",
			"delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]any{"output_tokens": 96}})
		emit(w, "message_stop", map[string]any{"type": "message_stop"})
		return
	}

	emit(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": "msg_2", "role": "assistant", "usage": map[string]any{"input_tokens": 620, "output_tokens": 0}}})
	emit(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""}})
	for _, part := range chunkString("SVG 已交付，纯矢量，无脚本与外链。", 8) {
		emit(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": part}})
	}
	emit(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	emit(w, "message_delta", map[string]any{"type": "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 42}})
	emit(w, "message_stop", map[string]any{"type": "message_stop"})
}

// ---------- Gemini ----------

func gemini(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	sse(w)

	if needsTool(body) {
		emit(w, "", map[string]any{
			"candidates": []any{map[string]any{
				"content": map[string]any{"role": "model", "parts": []any{map[string]any{
					"functionCall": map[string]any{"name": "emit_svg", "args": map[string]any{"svg": pelicanSVG, "note": "pelican on a bicycle"}}}}},
				"finishReason": "STOP",
			}},
			"usageMetadata": map[string]any{"promptTokenCount": 420, "candidatesTokenCount": 96, "totalTokenCount": 516},
		})
		return
	}

	emit(w, "", map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"role": "model", "parts": []any{map[string]any{
				"text": "SVG 已交付，viewBox 400×300。"}}},
			"finishReason": "STOP",
		}},
		"usageMetadata": map[string]any{"promptTokenCount": 620, "candidatesTokenCount": 42, "totalTokenCount": 662},
	})
}

// ---------- 模型列表 ----------

func modelList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	models := []map[string]any{
		{"id": "fake-vision-svg", "displayName": "Fake Vision SVG", "object": "model"},
		{"id": "fake-text-only", "displayName": "Fake Text Only", "object": "model"},
		{"id": "claude-sonnet-4-5", "displayName": "Claude Sonnet 4.5", "object": "model"},
		{"id": "gemini-2.5-pro", "displayName": "Gemini 2.5 Pro", "object": "model"},
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": models, "models": models})
}

// ---------- 工具函数 ----------

func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func chunkString(s string, size int) []string {
	runes := []rune(s)
	var out []string
	for i := 0; i < len(runes); i += size {
		end := i + size
		if end > len(runes) {
			end = len(runes)
		}
		out = append(out, string(runes[i:end]))
	}
	return out
}
