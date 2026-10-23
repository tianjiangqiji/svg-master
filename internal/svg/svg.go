// Package svg 负责 SVG 的提取、只读校验与提示词库。
//
// 本包不改写任何 SVG 内容：交付的 SVG 与模型给出的文本逐字一致。
// 唯一允许的“规范”是尺寸（viewBox / width / height 与画布对齐），
// 且只以校验报告的形式呈现，不做自动修补。
//
// 本文件声明「契约」（api / agent 两层都依赖它），函数签名不得随意更改。
package svg

// Canvas 画布预设：比例 + 尺寸 + viewBox。
type Canvas struct {
	Name    string `json:"name"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	ViewBox string `json:"viewBox"`
}

// Issue 校验发现的一条问题。
// Level: "error"（无法渲染或尺寸不符）| "warn"（能渲染但有风险）
type Issue struct {
	Level   string `json:"level"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Report 校验报告。
type Report struct {
	OK        bool    `json:"ok"`
	Root      string  `json:"root"`
	ViewBox   string  `json:"viewBox"`
	Width     string  `json:"width"`
	Height    string  `json:"height"`
	Elements  int     `json:"elements"`
	Paths     int     `json:"paths"`
	Texts     int     `json:"texts"`
	Gradients int     `json:"gradients"`
	Bytes     int     `json:"bytes"`
	Issues    []Issue `json:"issues"`
}

// ExtractResult 从模型输出里抽 SVG 的结果。
type ExtractResult struct {
	SVG    string `json:"svg"`
	Found  bool   `json:"found"`
	Closed bool   `json:"closed"` // 是否见到 </svg>（流式中间态为 false）
	Source string `json:"source"` // fence-svg | fence-xml | raw | none
}

// Extract 从任意文本中抽出 SVG。优先 ```svg / ```xml 代码块，其次裸 <svg>…</svg>。
// 允许多个候选时取最长的一个（模型常先给一个草稿再给终版）。
// 只做切片定位，不改写内容。
func Extract(text string) ExtractResult { return extract(text) }

// Validate 只读解析并统计结构，返回报告（解析失败时 OK=false 并带 error 级 Issue）。
// 判定范围：是不是 SVG、XML 是否合法、尺寸声明是否自洽。不含任何风格判断。
func Validate(svgText string) Report { return validate(svgText) }

// CheckCanvasSize 把交付内容与画布尺寸规范对齐，只返回尺寸类 Issue，绝不改写内容。
// canvas 的 Width/Height 非正数时视为没有尺寸规范，返回 nil。
func CheckCanvasSize(svgText string, canvas Canvas) []Issue { return checkCanvasSize(svgText, canvas) }

// Presets 返回内置比例预设（前端下拉直接消费）。
func Presets() []Canvas { return presets() }

// Prompt 一条提示词模板。Template 里用 {{subject}} 占位。
type Prompt struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Title    string `json:"title"`
	Template string `json:"template"`
	Note     string `json:"note"`
	Ratio    string `json:"ratio"`  // 建议比例，如 "1:1"
	Source   string `json:"source"` // 来源 URL，或 "builtin"
}

// Prompts 返回全部内置提示词。
func Prompts() []Prompt { return prompts() }

// Categories 返回分类清单（有序）。
func Categories() []string { return categories() }

// SearchPrompts 按关键词（标题/分类/模板/备注）过滤；q 为空返回全部。
func SearchPrompts(q string) []Prompt { return searchPrompts(q) }

// DefaultPrompt 是自带的演示主题（只描述主题，不含任何输出规范）。
//
// 输出规范（只回 SVG、只走 emit_svg 通道）已经写死在系统提示词里，
// 用户提示词只负责说清「画什么」，不再重复「怎么交」。
const DefaultPrompt = "A pelican riding a bicycle."

// OutputSpec 是写死的输出规范，由 agent 拼进系统提示词。
// 除此之外不注入任何风格、构图或色彩要求。
const OutputSpec = "## 输出规范（写死，优先于任何用户提示词）\n" +
	"1. 交付物必须是 SVG：根元素 <svg>，XML 合法、可直接渲染。\n" +
	"2. 尺寸必须等于画布规范：根元素的 viewBox 与 width/height 按上面给出的画布数值写。\n" +
	"3. 唯一交付通道是 emit_svg 工具，一次给全，不要代码围栏、不要引导语、不要「此处省略」。\n" +
	"4. 除以上三条外没有任何风格要求：配色、构图、元素数量、细节密度完全由你按主题决定。\n"

// SubjectPlaceholder 提示词模板里的主题占位符。
const SubjectPlaceholder = "{{subject}}"
