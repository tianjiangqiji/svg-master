// Package llm 是四种协议的统一客户端层。
//
// 可见内容使用 Block；协议原生输出作为不透明数据随历史保存，供对应客户端续传。
package llm

import (
	"context"
	"encoding/json"
	"time"
)

// 角色常量。
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
	RoleSystem    = "system"
)

// 块类型常量。
const (
	BlockText       = "text"
	BlockImage      = "image"
	BlockThinking   = "thinking"
	BlockToolCall   = "tool_call"
	BlockToolResult = "tool_result"
)

// ImageData 一张内联图片（base64，不含 data: 前缀）。
type ImageData struct {
	MIME string `json:"mime"`
	Data string `json:"data"`
}

// ToolCall 模型发起的一次工具调用。
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolResult 工具执行结果。
type ToolResult struct {
	CallID  string      `json:"callId"`
	Name    string      `json:"name"`
	Content string      `json:"content"`
	IsError bool        `json:"isError"`
	Images  []ImageData `json:"images,omitempty"`
}

// Block 消息内容块。
type Block struct {
	Type       string      `json:"type"`
	Text       string      `json:"text,omitempty"`
	Image      *ImageData  `json:"image,omitempty"`
	ToolCall   *ToolCall   `json:"toolCall,omitempty"`
	ToolResult *ToolResult `json:"toolResult,omitempty"`
	Thinking   string      `json:"thinking,omitempty"`
}

// Message 一条消息。
type Message struct {
	Role         string        `json:"role"`
	Blocks       []Block       `json:"blocks"`
	NativeOutput *NativeOutput `json:"nativeOutput,omitempty"`
}

// NativeOutput 必须按原顺序、原字段回传，尤其不能丢失加密推理、phase 与工具调用 ID。
type NativeOutput struct {
	Protocol string            `json:"protocol"`
	Items    []json.RawMessage `json:"items"`
}

// TextMsg 构造纯文本消息。
func TextMsg(role, text string) Message {
	return Message{Role: role, Blocks: []Block{{Type: BlockText, Text: text}}}
}

// Text 拼接消息里所有 text 块。
func (m Message) Text() string {
	out := ""
	for _, b := range m.Blocks {
		if b.Type == BlockText {
			if out != "" {
				out += "\n"
			}
			out += b.Text
		}
	}
	return out
}

// ToolSpec 暴露给模型的工具定义。
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema
}

// Usage token 用量。
type Usage struct {
	InputTokens      int `json:"inputTokens"`
	OutputTokens     int `json:"outputTokens"`
	CacheReadTokens  int `json:"cacheReadTokens"`
	CacheWriteTokens int `json:"cacheWriteTokens"`
	TotalTokens      int `json:"totalTokens"`
}

// StreamEventKind 流事件类型。
const (
	EvText        = "text"
	EvThinking    = "thinking"
	EvToolStart   = "tool_start"
	EvToolDelta   = "tool_delta"
	EvUsage       = "usage"
	EvServiceTier = "service_tier"
	EvDone        = "done"
	EvError       = "error"
	EvOutput      = "output"   // 不透明原生输出，仅供历史续传，不向 UI 展开
	EvContinue    = "continue" // 服务端暂停，携带原生输出继续下一轮
)

// StreamEvent 流式事件。
type StreamEvent struct {
	Kind    string        `json:"kind"`
	Text    string        `json:"text,omitempty"`
	Index   int           `json:"index,omitempty"` // 工具调用序号
	ID      string        `json:"id,omitempty"`    // 工具调用 id
	Name    string        `json:"name,omitempty"`  // 工具名
	Usage   *Usage        `json:"usage,omitempty"`
	Err     string        `json:"err,omitempty"`
	Cause   error         `json:"-"`
	Output  *NativeOutput `json:"-"`
	Replace bool          `json:"-"` // 工具最终参数覆盖增量，避免 done 快照被重复拼接
}

// Request 一次模型请求（已与协议无关）。
type Request struct {
	System      string
	Messages    []Message
	Tools       []ToolSpec
	Model       string
	MaxTokens   int
	Thinking    string // off|low|medium|high|xhigh|max
	ServiceTier string // "priority" requests Fast; empty preserves the provider default.
	Stream      bool
	// SessionID 用于网关前缀缓存亲和（session_id / x-client-request-id / x-session-affinity）
	SessionID string
}

// Client 一个已配置好的协议客户端。
type Client interface {
	// Stream 发起流式请求，事件从返回的 channel 读取，channel 关闭即结束。
	// 调用方必须消费完 channel 或取消 ctx，否则 goroutine 泄漏。
	Stream(ctx context.Context, req Request) (<-chan StreamEvent, error)
	// Protocol 返回协议标识。
	Protocol() string
}

// Transport 可选：测试时替换 HTTP 行为。
type Transport struct {
	Timeout       time.Duration // 单请求总超时（流式时为首字节超时）
	IdleTimeout   time.Duration // 流式读空闲超时
	MaxRetries    int
	RetryBase     time.Duration
	MaxRetryDelay time.Duration
}

// DefaultTransport 默认传输参数（对齐 pi-browser-agent 的 turn-loop 常量）。
func DefaultTransport() Transport {
	return Transport{
		Timeout:       120 * time.Second,
		IdleTimeout:   300 * time.Second,
		MaxRetries:    3,
		RetryBase:     2 * time.Second,
		MaxRetryDelay: 30 * time.Second,
	}
}

// NewClient 见 client.go（工厂 + 未知协议兜底）。
