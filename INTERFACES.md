# SVG Master — 接口契约（并行开发必读）

> 本文件是四个并行任务的**唯一契约**。改动签名前必须先改这里，并通知 Lead。

## 0. 目录与归属（写作用域互不重叠，严禁越界）

| 归属 | 目录 / 文件 |
|---|---|
| **Lead**（已完成，勿改） | `main.go`、`go.mod`、`internal/config/**`、`internal/api/api.go`、`internal/api/handlers.go`、`internal/api/chat.go`、`internal/app/**` |
| **llm-proto** | `internal/llm/**`（`types.go` 只可新增，不可改既有签名；**必须删除 `stub.go`**） |
| **agent-core** | `internal/agent/**`（`agent.go` 只可新增，不可改既有签名；**必须删除 `stub.go`**） |
| **svg-core** | `internal/svg/**`（`svg.go` 只可新增，不可改既有签名；**必须删除 `stub.go`**）、`internal/svg/prompts.json` |
| **web-ui** | `internal/api/webdist/**`（`index.html` / `app.js` / `style.css`） |

前端资源目录是 `internal/api/webdist/`（`go:embed all:webdist` 直接内嵌，**无构建步骤**）。
`SVG_WEB_DIR` 环境变量可让服务读磁盘目录调试，但正式资源必须写进 `webdist/`。

## 1. 后端已有接口（Lead 已实现，可直接调用）

### `internal/config`
```go
const (
    ProtoOpenAIChat = "openai-completions"
    ProtoOpenAIResp = "openai-responses"
    ProtoAnthropic  = "anthropic-messages"
    ProtoGemini     = "google-generative-ai"
)

type ProtocolMeta struct { Key, Label, Sub, BaseURL, Hint string }
var ProtocolMetas []ProtocolMeta

type ModelCaps struct { ContextWindow, MaxTokens int; Reasoning, Vision bool }
type PartialCaps struct {
    ContextWindow *int  `json:"contextWindow,omitempty"`
    MaxTokens     *int  `json:"maxTokens,omitempty"`
    Reasoning     *bool `json:"reasoning,omitempty"`
    Vision        *bool `json:"vision,omitempty"`
}
type ModelEntry struct { ID, Name string }
type Provider struct {
    ID, Name, Protocol, BaseURL, APIKey string
    Models    []ModelEntry
    ModelCaps map[string]PartialCaps
}
type AgentSettings struct {
    MaxSteps       int  `json:"maxSteps"`
    AutoValidate   bool `json:"autoValidate"`
    CompressOn     bool `json:"compressOn"`
    CompressAt     int  `json:"compressAt"`
    VisionCheck    bool `json:"visionCheck"`
    StreamDisabled bool `json:"streamDisabled"`
}
type CanvasPreset struct { Name string; Width, Height int; ViewBox string }
type Settings struct {
    Version          int
    Providers        []Provider
    ActiveProviderID string
    ActiveModel      string
    Thinking         string   // off|low|medium|high
    Agent            AgentSettings
    Canvas           CanvasPreset
    Export           ExportSettings
}
func (s *Settings) ActiveProvider() *Provider
func (s *Settings) FindProvider(id string) *Provider
func DefaultSettings() Settings
func MetaOf(protocol string) (ProtocolMeta, bool)
```

### `internal/llm`（**llm-proto 补全**，已有类型）
```go
const (RoleUser="user"; RoleAssistant="assistant"; RoleTool="tool"; RoleSystem="system")
const (BlockText="text"; BlockImage="image"; BlockThinking="thinking"; BlockToolCall="tool_call"; BlockToolResult="tool_result")

type ImageData struct { MIME, Data string }            // Data 为裸 base64，不含 data: 前缀
type ToolCall struct { ID, Name string; Arguments json.RawMessage }
type ToolResult struct { CallID, Name, Content string; IsError bool; Images []ImageData }
type Block struct {
    Type string
    Text string
    Image *ImageData
    ToolCall *ToolCall
    ToolResult *ToolResult
    Thinking string
}
type Message struct { Role string; Blocks []Block }
func TextMsg(role, text string) Message
func (m Message) Text() string

type ToolSpec struct { Name, Description string; Parameters json.RawMessage }
type Usage struct { InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens, TotalTokens int }
const (EvText="text"; EvThinking="thinking"; EvToolStart="tool_start"; EvToolDelta="tool_delta"; EvUsage="usage"; EvDone="done"; EvError="error")
type StreamEvent struct { Kind, Text string; Index int; ID, Name string; Usage *Usage; Err string }

type Request struct {
    System string; Messages []Message; Tools []ToolSpec
    Model string; MaxTokens int
    Thinking string; Stream bool; SessionID string
}
type Client interface { Stream(ctx, req) (<-chan StreamEvent, error); Protocol() string }
type Transport struct { Timeout, IdleTimeout, RetryBase, MaxRetryDelay time.Duration; MaxRetries int }
func DefaultTransport() Transport
func NewClient(protocol, baseURL, apiKey string, t Transport) Client
```
**llm-proto 必须额外实现（api 层已调用，当前在 `stub.go` 里）**：
```go
func FetchRemoteModels(protocol, baseURL, apiKey string, t Transport) ([]config.ModelEntry, error)
func InferModelCaps(modelID string) config.ModelCaps
func ResolveModelCaps(modelID string, o *config.PartialCaps) config.ModelCaps
func AutoModelName(id string) string
func NormalizeBaseURL(protocol, base string) string
```

### `internal/svg`（**svg-core 补全**）
```go
type Canvas struct { Name string; Width, Height int; ViewBox string }
type Issue struct { Level, Code, Message string }   // Level: error|warn|info
type Report struct {
    OK bool; Root, ViewBox, Width, Height string
    Elements, Paths, Texts, Gradients, Bytes int
    Issues []Issue
}
type ExtractResult struct { SVG string; Found, Closed bool; Source string } // Source: fence-svg|fence-xml|raw|none
type Prompt struct { ID, Category, Title, Template, Note, Ratio string }

func Extract(text string) ExtractResult
func Validate(svgText string) Report
func CheckCanvasSize(svgText string, canvas Canvas) []Issue   // 只读比对画布尺寸，不改写
func Presets() []Canvas
func Prompts() []Prompt
func Categories() []string
func SearchPrompts(q string) []Prompt

// 已按需求移除：Normalize / Sanitize / ApplyCanvas（三者都会改写 SVG）。
// internal/svg 现在只做提取 + 只读校验，任何路径都不回写模型交付的内容。

const DefaultPrompt = "A pelican riding a bicycle."  // 只描述主题
const OutputSpec = "## 输出规范（写死，优先于任何用户提示词）
..."  // agent 拼进系统提示词
const SubjectPlaceholder = "{{subject}}"
```

### `internal/agent`（**agent-core 补全**）
```go
const (EvLog="log"; EvText="text"; EvThinking="thinking"; EvToolStart="tool_start"; EvToolEnd="tool_end";
       EvSVG="svg"; EvCanvas="canvas"; EvUsage="usage"; EvNudge="nudge"; EvAsk="ask"; EvDone="done"; EvError="error")

type Event struct {
    Kind, Level, Text string
    CallID, Tool string; Args json.RawMessage; Result, Error string
    SVG string; Canvas *config.CanvasPreset
    Usage *Stats
    Question string; Options []string
}
type Stats struct {
    ModelSteps, ToolCalls, ToolErrors, Tokens, InputTokens, OutputTokens, CacheRead int
    Compressed, Blocks, ContextUsed, ContextMax, StreamRetry int
    ElapsedMS int64; TPS float64
}
type Task struct { Prompt string; Images []llm.ImageData; Canvas config.CanvasPreset; Model, System, Session string }
type Deps struct {
    Client llm.Client; Settings config.AgentSettings; Canvas config.CanvasPreset
    Model string; MaxTokens int; Thinking string; Transport llm.Transport
    Emit func(Event)
}
type Engine struct { /* 内部字段自由 */ }
func New(deps Deps) *Engine
func (e *Engine) Run(ctx context.Context, t Task) (Stats, error)
func (e *Engine) Abort()
type ContextStatus struct { Used, Max int; Percent float64; Messages, Blocks, Compressed int; TPS float64 }
func (e *Engine) Status() ContextStatus

type CompressBlock struct { BlockID string; Tier int; Topic, Summary, StartRef, EndRef string; Active bool; Tokens int }
type CompressRange struct { StartRef, EndRef, Topic, Summary string }
type Compressor interface {
    Project(msgs []llm.Message, tokenCount int) (out []llm.Message, nudge string)
    Compress(ranges []CompressRange) (created int, tokens int, warnings []string, err error)
    Decompress(blockID string) (restored int, preview string, err error)
    Search(query string, limit int) []CompressBlock
    Blocks() []CompressBlock
    Status(tokenCount int) (used, max, blocks, compressed int)
}
```
`Engine` 内部可自由加字段与方法，但 `New/Run/Abort/Status` 签名固定。

## 2. HTTP 接口（Lead 已实现路由；前端按此消费）

所有请求需带令牌：页面 URL 上的 `?k=<token>`，或请求头 `X-App-Token`。
前端从 `location.search` 取 `k`，后续 `fetch` 都带 `X-App-Token` 头。

| 方法 路径 | 请求体 | 响应 |
|---|---|---|
| `GET /api/state` | — | `{settings, protocols, canvases, prompts, categories, defaultPrompt, version, dataDir, warn}` |
| `PUT /api/settings` | 完整 `Settings` | `{ok, settings}`（Key 回传打码值表示不变） |
| `GET /api/providers` | — | `{providers}` |
| `POST /api/providers` | `Provider` | `{ok, id, settings}` |
| `DELETE /api/providers?id=` | — | `{ok, settings}` |
| `GET /api/protocols` | — | `{protocols}` |
| `GET /api/prompts?q=&category=` | — | `{prompts, categories}` |
| `GET /api/canvas` | — | `{presets, current}` |
| `POST /api/canvas` | `CanvasPreset` | `{ok, canvas}` |
| `POST /api/models` | `{providerId?, protocol?, baseUrl?, apiKey?}` | `{models:[{id,name,caps}]}`；失败 `{error}` |
| `POST /api/validate` | `{svg}` | `Report` |
| `POST /api/export` | `{svg}` | `{svg}`（只剥外壳，内容原样） |
| `POST /api/chat` | `{prompt, images:[{mime,data}], canvas, model, providerId, thinking, session, layer, genMode}` | **SSE**，见下 |
| `POST /api/chat/abort` | `{session}` | `{ok}` |
| `POST /api/render-result` | `{session, data, mime}` | `{ok}` |

### SSE 事件（`data: <json>\n\n`，15s 一次 `: keepalive` 注释）
即 `agent.Event` 的 JSON：`{kind, level?, text?, callId?, tool?, args?, result?, error?, svg?, canvas?, usage?, question?, options?}`。
`kind` 取值见 `internal/agent` 常量。结束一定收到 `done`（带 `usage`）或 `error`。

## 3. 前端要点（web-ui）

- 三段式：左栏配置 / 中栏棋盘格画布 / 右栏日志+工具卡片+上下文用量。底部抽屉「原始返回/错误详情」。
- 中栏把前端 `sanitizeSVG()` 处理过的副本塞进容器 `innerHTML`（只影响渲染，不改存储/导出的原文）；流式期间每收到 `svg` 事件就重绘。
- 图片输入：拖拽/粘贴/选择 → 前端缩放到长边 ≤1568px → `canvas.toDataURL('image/png')` → 取 `,` 后 base64 作为 `images[].data`，`mime` 填 `image/png`。
- 生成方式三级开关（`quick` / `layers` / `anim`）：`genMode` 随 `/api/chat` 下发，是唯一真源；`multiStep` 只是旧字段回落。**切换规则**：未点「开始生成」前三档可自由来回切；点过之后仅当仍停在 `quick` 时留最后一次机会，切走即锁死；在 `anim` 下开始生成后为单向门，只能靠工程弹窗「快速生成」重置。动图模式单图层交付 SMIL 动图，前端提供播放/暂停/帧率与「导出 GIF」（canvas 逐帧 + 自写 GIF89a 编码，纯前端，不经后端）。工程快照 `GET /api/project` 新增 `empty` 字段：所有图层内容为空且无对话历史时为 true，前端据此跳过「快速生成」的清空确认框。
- 比例预设下拉来自 `/api/canvas`；切换后把 `canvas` 随 `/api/chat` 一起发（只更新尺寸规范）。已生成的 SVG 保持原样，不做重排；下载走前端本地 Blob，不经 `/api/export`。
- 模型管理面板：供应商列表 + 四协议宫格 + Base URL + Key（密码框）+「获取模型列表」（调 `/api/models`）+ 手动加 id + 能力覆盖表（contextWindow/maxTokens/reasoning/vision 可手改）。
- 提示词库：分类标签 + 搜索框 + 卡片列表，点击把 `template` 里的 `{{subject}}` 替换为当前主题后填入输入框。
- 视觉风格：深色、紧凑、无圆角浮夸；日志带 `[HH:MM:SS]` 时间戳与彩色标签（对齐截图）。
