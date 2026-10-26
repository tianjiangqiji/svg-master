// Package agent 是 SVG 生成任务的 Agent 引擎：多轮模型循环 + 工具执行 + 上下文压缩。
//
// 本文件由 Lead 声明「契约」，agent-core 负责补齐实现；签名不得随意更改。
package agent

import (
	"context"
	"encoding/json"
	"time"

	"svg-master/internal/config"
	"svg-master/internal/llm"
)

// EventKind Agent 下行事件类型（api 层直接转 SSE）。
const (
	EvLog         = "log"      // 普通日志（带 Level）
	EvText        = "text"     // 模型正文增量
	EvThinking    = "thinking" // 思考增量
	EvToolStart   = "tool_start"
	EvToolDelta   = "tool_delta" // 工具参数增量（当前用于 emit_svg 的实时预览）
	EvToolEnd     = "tool_end"
	EvSVG         = "svg" // 画布更新（完整 SVG）
	EvCanvas      = "canvas"
	EvLayers      = "layers" // 图层树更新（数组为 []LayerInfo）
	EvUsage       = "usage"
	EvServiceTier = "service_tier"
	EvNudge       = "nudge" // 上下文压缩提醒
	EvAsk         = "ask"   // 需要用户澄清
	EvDone        = "done"
	EvError       = "error"
)

// Event Agent 推给上层的统一事件。
type Event struct {
	Kind  string `json:"kind"`
	Level string `json:"level,omitempty"` // log 用：info|warn|error|tool
	Text  string `json:"text,omitempty"`
	// 工具相关
	CallID string          `json:"callId,omitempty"`
	Tool   string          `json:"tool,omitempty"`
	Args   json.RawMessage `json:"args,omitempty"`
	Result string          `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
	// 画布相关
	SVG    string               `json:"svg,omitempty"`
	Canvas *config.CanvasPreset `json:"canvas,omitempty"`
	Layers []LayerInfo          `json:"layers,omitempty"`
	// 会话（连续对话标识，done 事件回传给前端）
	Session string `json:"session,omitempty"`
	// 用量/统计
	Usage *Stats `json:"usage,omitempty"`
	// ask
	Question string   `json:"question,omitempty"`
	Options  []string `json:"options,omitempty"`
}

// Stats 一次任务的运行统计。
type Stats struct {
	ModelSteps   int     `json:"modelSteps"`
	ToolCalls    int     `json:"toolCalls"`
	ToolErrors   int     `json:"toolErrors"`
	Tokens       int     `json:"tokens"`
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	CacheRead    int     `json:"cacheRead"`
	Compressed   int     `json:"compressed"`  // 被折叠的消息条数
	Blocks       int     `json:"blocks"`      // 活跃压缩块数
	ContextUsed  int     `json:"contextUsed"` // 估算的上下文 token
	ContextMax   int     `json:"contextMax"`
	StreamRetry  int     `json:"streamRetry"`
	ElapsedMS    int64   `json:"elapsedMs"`
	TPS          float64 `json:"tps"`
}

// Task 一次任务输入。
type Task struct {
	Prompt  string              `json:"prompt"`
	Images  []llm.ImageData     `json:"images,omitempty"` // 图转 SVG 的输入
	Canvas  config.CanvasPreset `json:"canvas"`
	Model   string              `json:"model"`
	System  string              `json:"system,omitempty"` // 额外系统提示（可选）
	Session string              `json:"session,omitempty"`
	// History 跨请求延续的历史消息（连续对话模式）；引擎把它拼在本次输入之前，
	// 运行结束后可通过 Engine.History() 取回完整历史（含本轮）。
	History []llm.Message `json:"-"`
}

// Deps Agent 引擎的外部依赖。
type Deps struct {
	Client      llm.Client
	Settings    config.AgentSettings
	Canvas      config.CanvasPreset
	Model       string
	MaxTokens   int
	Thinking    string
	ServiceTier string
	Transport   llm.Transport

	// Layers 图层工作区（工程文件夹）。为 nil 时一切退回「整幅 SVG」老行为：
	// 不注入多步引导，也不下发图层读写工具。
	Layers LayerBridge
	// LayerTools 是否下发图层读写工具（由多步开关 + 图层总开关共同决定）。
	LayerTools bool
	// ContextClearedAt 用户最近一次清空上下文的时间（UnixNano），非 0 时注入提醒，
	// 要求模型先读图层再动手。
	ContextClearedAt int64
	// Emit 由 api 层提供，Agent 每产生一个事件就调用它（实现内部自行保证串行）
	Emit func(Event)
}

// Engine Agent 引擎。一个 Engine 服务一个任务，任务结束即丢弃。
type Engine struct {
	deps    Deps
	stats   Stats
	started time.Time
	// 内部字段由实现补充
	impl engineImpl
}

// New 创建引擎。
func New(deps Deps) *Engine { return newEngine(deps) }

// Run 阻塞执行任务直到完成/取消；事件通过 deps.Emit 推送。
// 返回最终统计与错误（ctx 取消时 err = context.Canceled）。
func (e *Engine) Run(ctx context.Context, t Task) (Stats, error) { return e.run(ctx, t) }

// Abort 请求中止当前任务（可被 api 层从其它 goroutine 调用）。
func (e *Engine) Abort() { e.abort() }

// History 返回本任务结束后的完整消息历史（含历史延续 + 本次运行的新消息）。
// 必须在 Run 返回后调用；Run 执行期间返回值为空切片。
func (e *Engine) History() []llm.Message { return e.history() }

// ContextStatus 当前上下文占用情况（前端用量环轮询用）。
type ContextStatus struct {
	Used       int     `json:"used"`
	Max        int     `json:"max"`
	Percent    float64 `json:"percent"`
	Messages   int     `json:"messages"`
	Blocks     int     `json:"blocks"`
	Compressed int     `json:"compressed"`
	TPS        float64 `json:"tps"`
}

// Status 返回当前统计快照。
func (e *Engine) Status() ContextStatus { return e.status() }

// CompressBlock 一个压缩块（前端上下文面板展示）。
type CompressBlock struct {
	BlockID  string `json:"blockId"`
	Tier     int    `json:"tier"`
	Topic    string `json:"topic"`
	Summary  string `json:"summary"`
	StartRef string `json:"startRef"`
	EndRef   string `json:"endRef"`
	Active   bool   `json:"active"`
	Tokens   int    `json:"tokens"`
}

// Compressor 上下文压缩内核（agent-core 实现于 context.go）。
type Compressor interface {
	// Project 在每次模型请求前调用：返回投影后的消息与可选的 nudge 文本。
	Project(msgs []llm.Message, tokenCount int) (out []llm.Message, nudge string)
	// Compress 由 compress 工具调用。
	Compress(ranges []CompressRange) (created int, tokens int, warnings []string, err error)
	// Decompress 由 decompress 工具调用。
	Decompress(blockID string) (restored int, preview string, err error)
	// Search 由 search_context 工具调用。
	Search(query string, limit int) []CompressBlock
	// Blocks 返回全部块。
	Blocks() []CompressBlock
	// Status 返回用量。
	Status(tokenCount int) (used int, max int, blocks int, compressed int)
}

// CompressRange 一次压缩请求覆盖的区间。
type CompressRange struct {
	StartRef string `json:"startId"`
	EndRef   string `json:"endId"`
	Topic    string `json:"topic"`
	Summary  string `json:"summary"`
}

// ---- 图层桥接 ----
//
// agent 包不认识 project 包（避免反向依赖），它只依赖这层最小接口；
// 具体实现由 api 层用 project.Project 适配注入。

// LayerBridge 图层工作区：AI 工具与 api 层共用。
type LayerBridge interface {
	// Canvas 画布尺寸（与 Deps.Canvas 同义，工程里也存一份）。
	Canvas() (width, height int, viewBox string)
	// List 图层列表（按层级升序）。
	List() []LayerInfo
	// Active 当前图层 id。
	Active() string
	// Read 读图层内容；id 为空时读当前图层。
	Read(id string) (LayerInfo, string, error)
	// Add 新建图层；content 为空时创建空图层。
	Add(name, content, blend string, index int) (LayerInfo, error)
	// Write 整层覆盖。
	Write(id, content string) error
	// Patch 局部替换；返回替换处数。
	Patch(id, oldText, newText string, replaceAll bool) (int, error)
	// Delete 删图层。
	Delete(id string) error
	// Props 改属性（名字/可见/锁定/不透明度/混合模式/层级）；nil 字段不改。
	Props(id string, p LayerPatch) (LayerInfo, error)
	// SetActive 设置当前图层。
	SetActive(id string) error
	// Render 合成整幅 SVG（预览与导出用）。
	Render() string
}

// LayerInfo 图层元信息的传输结构。
type LayerInfo struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Visible  bool    `json:"visible"`
	Locked   bool    `json:"locked"`
	Opacity  float64 `json:"opacity"`
	Blend    string  `json:"blend"`
	Index    int     `json:"index"`
	Bytes    int     `json:"bytes,omitempty"`
	Elements int     `json:"elements,omitempty"`
}

// LayerPatch 图层属性增量（nil = 不改）。
type LayerPatch struct {
	Name    *string  `json:"name"`
	Visible *bool    `json:"visible"`
	Locked  *bool    `json:"locked"`
	Opacity *float64 `json:"opacity"`
	Blend   *string  `json:"blend"`
	Index   *int     `json:"index"`
}
