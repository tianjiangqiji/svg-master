// Package config 定义设置结构并负责持久化（绿色版：exe 同级 data/settings.json）。
package config

import (
	"regexp"
	"strings"
)

// AppVersion 是程序版本号，前端徽标与 /api/state 共用。
const AppVersion = "1.4.1"

// SettingsVersion 是 settings.json 的格式版本。
// v4 引入生成方式三级开关（agent.genMode）。
const SettingsVersion = 4

// 协议枚举。与前端下拉、llm 层实现一一对应。
const (
	ProtoOpenAIChat = "openai-completions"
	ProtoOpenAIResp = "openai-responses"
	ProtoAnthropic  = "anthropic-messages"
	ProtoGemini     = "google-generative-ai"
)

// 运行模式：快速模式（内置临时工程，退出自动清空）与工程模式（用户目录，自动保存）。
const (
	ModeQuick   = "quick"
	ModeProject = "project"
)

// 生成方式（左栏三级开关）：决定模型拿到什么提示与工具。
//
//   - GenModeQuick  一次性交付整幅 SVG，不下发图层工具，也不注入分步引导；
//   - GenModeLayers 分多步、按图层改造画面，下发图层读写工具；
//   - GenModeAnim   仍是单个图层，但要求交付可播放的动图（帧分组，兼容 SMIL）。
const (
	GenModeQuick  = "quick"
	GenModeLayers = "layers"
	GenModeAnim   = "anim"
)

// ValidGenMode 判断生成方式是否合法；空串视为未设置。
func ValidGenMode(m string) bool {
	switch m {
	case GenModeQuick, GenModeLayers, GenModeAnim:
		return true
	}
	return false
}

// NormalizeGenMode 把生成方式归一到合法值，并回填派生的 MultiStep。
//
// 规则：合法值原样保留；空值按 MultiStep 推断（旧配置没有 genMode 字段）；
// 非法值回落到 layers（历史默认行为）。返回值是归一后的副本，不修改入参。
func NormalizeGenMode(a AgentSettings) AgentSettings {
	mode := a.GenMode
	if mode == "" {
		if a.MultiStep {
			mode = GenModeLayers
		} else {
			mode = GenModeQuick
		}
	}
	if !ValidGenMode(mode) {
		mode = GenModeLayers
	}
	a.GenMode = mode
	a.MultiStep = mode == GenModeLayers
	return a
}

// ProtocolMeta 描述每种协议的展示信息与默认 Base URL。
type ProtocolMeta struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Sub     string `json:"sub"`
	BaseURL string `json:"baseUrl"`
	Hint    string `json:"hint"`
}

// ProtocolMetas 顺序即前端宫格顺序。
var ProtocolMetas = []ProtocolMeta{
	{ProtoOpenAIChat, "OpenAI 兼容", "/v1/chat/completions", "https://api.openai.com/v1", "最通用的中转站协议"},
	{ProtoOpenAIResp, "OpenAI Responses", "/v1/responses", "https://api.openai.com/v1", "新版 Responses API"},
	{ProtoAnthropic, "Anthropic Messages", "/v1/messages", "https://api.anthropic.com", "Claude 原生协议"},
	{ProtoGemini, "Gemini V1beta", "/v1beta/models", "https://generativelanguage.googleapis.com", "Google Gemini 原生协议"},
}

// MetaOf 返回协议元信息；未知协议返回零值。
func MetaOf(protocol string) (ProtocolMeta, bool) {
	for _, m := range ProtocolMetas {
		if m.Key == protocol {
			return m, true
		}
	}
	return ProtocolMeta{}, false
}

// versionTailRe 匹配路径末尾的版本段：/v1、/v1beta、/v4、/v1alpha…
var versionTailRe = regexp.MustCompile(`/v\d+[a-z0-9]*$`)

// CanonicalBaseURL 把用户输入的 Base URL 归一成可持久化的稳定形态。
//
// 这是「Base URL 不再必填 /v1」的落点：
//   - 去空白；尾部斜杠一律削掉（含多重斜杠），保留 scheme 的 //
//   - 没写协议时补 https://（用户常直接粘 api.xxx.com）
//   - 已有版本段（/v1、/v1beta、/v4…）就原样尊重
//   - 没有版本段时按协议补：gemini → /v1beta，其余 → /v1
//
// 空输入回落到协议默认值；未知协议原样返回。
func CanonicalBaseURL(protocol, base string) string {
	b := strings.TrimSpace(base)
	meta, ok := MetaOf(protocol)
	if !ok && b == "" {
		return ""
	}
	if b == "" {
		b = strings.TrimSpace(meta.BaseURL)
	}
	if b == "" {
		return ""
	}
	// 补协议：只认 http/https，其它 scheme 一律视为裸主机名处理
	lower := strings.ToLower(b)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		b = "https://" + strings.TrimLeft(b, "/")
		lower = strings.ToLower(b)
	}
	// 削尾部斜杠 + 折叠路径里的重复斜杠（scheme 的 // 必须保留）
	b = strings.TrimRight(b, "/")
	if i := strings.Index(b, "://"); i >= 0 {
		head, tail := b[:i+3], b[i+3:]
		for strings.Contains(tail, "//") {
			tail = strings.ReplaceAll(tail, "//", "/")
		}
		b = head + tail
	}
	if b == "" {
		return ""
	}
	if versionTailRe.MatchString(b) {
		return b
	}
	if protocol == ProtoGemini {
		return b + "/v1beta"
	}
	return b + "/v1"
}

// ModelCaps 模型能力：上下文窗口、最大输出、是否推理模型、是否支持图像输入。
type ModelCaps struct {
	ContextWindow int  `json:"contextWindow"`
	MaxTokens     int  `json:"maxTokens"`
	Reasoning     bool `json:"reasoning"`
	Vision        bool `json:"vision"`
}

// PartialCaps 用户按模型 id 覆盖的能力（nil 字段表示沿用推断值）。
type PartialCaps struct {
	ContextWindow *int  `json:"contextWindow,omitempty"`
	MaxTokens     *int  `json:"maxTokens,omitempty"`
	Reasoning     *bool `json:"reasoning,omitempty"`
	Vision        *bool `json:"vision,omitempty"`
}

// ModelEntry 一条模型。
type ModelEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Provider 一个供应商（协议 + 地址 + 密钥 + 模型清单）。
type Provider struct {
	AuthMode  string                 `json:"authMode,omitempty"` // chatgpt uses the provider's Codex login for generation and quota.
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Protocol  string                 `json:"protocol"`
	BaseURL   string                 `json:"baseUrl"`
	APIKey    string                 `json:"apiKey"`
	Models    []ModelEntry           `json:"models"`
	ModelCaps map[string]PartialCaps `json:"modelCaps,omitempty"`
}

// AgentSettings Agent 行为开关。
type AgentSettings struct {
	MaxSteps       int  `json:"maxSteps"`       // 单任务模型轮数上限
	AutoValidate   bool `json:"autoValidate"`   // emit_svg 校验不过时把问题回灌给模型继续修
	CompressOn     bool `json:"compressOn"`     // 是否启用上下文压缩
	CompressAt     int  `json:"compressAt"`     // 触发 nudge 的窗口占比（百分比）
	StreamDisabled bool `json:"streamDisabled"` // 网关不支持流式时的兜底
	// GenMode 生成方式，左栏三级开关的唯一真源：quick / layers / anim。
	// 由它派生 MultiStep 与图层工具下发；见 GenMode* 常量。
	GenMode string `json:"genMode"`
	// MultiStep 是 GenMode 的派生值（layers 时为 true），保留给旧配置与旧代码读取。
	// 写入时由 NormalizeGenMode 统一回填，不要单独设置它。
	MultiStep bool `json:"multiStep"`
}

// LayerSettings 图层面板行为。
type LayerSettings struct {
	// Enabled 关闭后不下发图层工具，图层树只用于显示与手动管理。
	Enabled bool `json:"enabled"`
	// PanelHeight 图层面板高度（px），前端拖拽后回存。
	PanelHeight int `json:"panelHeight"`
}

// CanvasPreset 画布比例/尺寸预设。
type CanvasPreset struct {
	Name    string `json:"name"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	ViewBox string `json:"viewBox"`
}

// ExportSettings 导出偏好。
type ExportSettings struct {
	LastDir string `json:"lastDir"`
	Scale   int    `json:"scale"` // PNG 导出倍率
}

// ProjectSettings 工程模式配置。
type ProjectSettings struct {
	// Mode 上次使用的运行模式（quick|project）。启动时以此预选欢迎页选项。
	Mode string `json:"mode"`
	// Dir 工程根目录（存放项目文件夹）。
	Dir string `json:"dir"`
	// AutoSave 工程模式下改动即写盘（关闭时仍会在退出前询问保存）。
	AutoSave bool `json:"autoSave"`
	// LastProject 最近打开的工程文件夹绝对路径。
	LastProject string `json:"lastProject"`
	// Recent 最近工程列表（绝对路径，最多 12 条）。
	Recent []string `json:"recent,omitempty"`
	// AskRepeat 发送内容与上次完全一致时弹窗确认。
	AskRepeat bool `json:"askRepeat"`
}

// Settings 全部持久化设置。
type Settings struct {
	Version          int             `json:"version"`
	Providers        []Provider      `json:"providers"`
	ActiveProviderID string          `json:"activeProviderId"`
	ActiveModel      string          `json:"activeModel"`
	Thinking         string          `json:"thinking"`
	FastMode         bool            `json:"fastMode"`         // Request Fast processing for OpenAI Responses; defaults to off.
	ExtendedThinking bool            `json:"extendedThinking"` // 解锁 xhigh / max 两档
	Agent            AgentSettings   `json:"agent"`
	Layers           LayerSettings   `json:"layers"`
	Project          ProjectSettings `json:"project"`
	Canvas           CanvasPreset    `json:"canvas"`
	Export           ExportSettings  `json:"export"`
}

// DefaultSettings 出厂默认：一个指向 OpenAI 官方地址的空供应商，方便用户直接填 Key。
func DefaultSettings() Settings {
	return Settings{
		Version: 4,
		Providers: []Provider{{
			ID:       "p1",
			Name:     "OpenAI",
			Protocol: ProtoOpenAIChat,
			BaseURL:  "https://api.openai.com/v1",
			APIKey:   "",
			Models:   []ModelEntry{},
		}},
		ActiveProviderID: "p1",
		ActiveModel:      "",
		Thinking:         "off",
		Agent: AgentSettings{
			MaxSteps:   40,
			CompressOn: true,
			CompressAt: 75,
			GenMode:    GenModeLayers,
			MultiStep:  true,
		},
		Layers: LayerSettings{Enabled: true, PanelHeight: 220},
		Project: ProjectSettings{
			Mode:      ModeQuick,
			AutoSave:  true,
			AskRepeat: true,
		},
		Canvas: CanvasPreset{Name: "1:1 方形", Width: 1024, Height: 1024, ViewBox: "0 0 1024 1024"},
		Export: ExportSettings{Scale: 2},
	}
}

// SettingsPatch 增量更新：所有字段为 nil 表示「本次不改」。
// PUT /api/settings 走这个语义，避免前端只传几个字段时把整个配置覆盖掉。
type SettingsPatch struct {
	Version          *int             `json:"version,omitempty"`
	Providers        *[]Provider      `json:"providers,omitempty"`
	ActiveProviderID *string          `json:"activeProviderId,omitempty"`
	ActiveModel      *string          `json:"activeModel,omitempty"`
	Thinking         *string          `json:"thinking,omitempty"`
	FastMode         *bool            `json:"fastMode,omitempty"`
	ExtendedThinking *bool            `json:"extendedThinking,omitempty"`
	Agent            *AgentSettings   `json:"agent,omitempty"`
	Layers           *LayerSettings   `json:"layers,omitempty"`
	Project          *ProjectSettings `json:"project,omitempty"`
	Canvas           *CanvasPreset    `json:"canvas,omitempty"`
	Export           *ExportSettings  `json:"export,omitempty"`
}

// Apply 把增量合并到基线设置上并返回新副本。
func (p SettingsPatch) Apply(base Settings) Settings {
	out := base
	if p.Version != nil {
		out.Version = *p.Version
	}
	if p.Providers != nil {
		out.Providers = *p.Providers
	}
	if p.ActiveProviderID != nil {
		out.ActiveProviderID = *p.ActiveProviderID
	}
	if p.ActiveModel != nil {
		out.ActiveModel = *p.ActiveModel
	}
	if p.Thinking != nil {
		out.Thinking = *p.Thinking
	}
	if p.FastMode != nil {
		out.FastMode = *p.FastMode
	}
	if p.ExtendedThinking != nil {
		out.ExtendedThinking = *p.ExtendedThinking
	}
	if p.Agent != nil {
		out.Agent = NormalizeGenMode(*p.Agent)
	}
	if p.Layers != nil {
		out.Layers = *p.Layers
	}
	if p.Project != nil {
		out.Project = *p.Project
	}
	if p.Canvas != nil {
		out.Canvas = *p.Canvas
	}
	if p.Export != nil {
		out.Export = *p.Export
	}
	return out
}

// Empty 判断增量是否什么都没带（避免空请求也触发一次落盘）。
func (p SettingsPatch) Empty() bool {
	return p.Version == nil && p.Providers == nil && p.ActiveProviderID == nil &&
		p.ActiveModel == nil && p.Thinking == nil && p.FastMode == nil && p.ExtendedThinking == nil && p.Agent == nil &&
		p.Layers == nil && p.Project == nil && p.Canvas == nil && p.Export == nil
}

// ActiveProvider 返回当前选中的供应商（不存在返回 nil）。
func (s *Settings) ActiveProvider() *Provider {
	for i := range s.Providers {
		if s.Providers[i].ID == s.ActiveProviderID {
			return &s.Providers[i]
		}
	}
	if len(s.Providers) > 0 {
		return &s.Providers[0]
	}
	return nil
}

// FindProvider 按 id 查找供应商。
func (s *Settings) FindProvider(id string) *Provider {
	for i := range s.Providers {
		if s.Providers[i].ID == id {
			return &s.Providers[i]
		}
	}
	return nil
}
