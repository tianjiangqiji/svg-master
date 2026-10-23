package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"svg-master/internal/config"
	"svg-master/internal/llm"
	"svg-master/internal/svg"
)

// 工具名（固定，不可改：模型侧提示词与前端都依赖）。
//
// 按需求移除了会改写 SVG 或注入风格偏好的工具：apply_canvas / list_prompts / search_prompts。
// emit_svg 现在原样交付，不做任何净化、补属性或 transform 包裹。
const (
	ToolEmitSVG       = "emit_svg"
	ToolValidateSVG   = "validate_svg"
	ToolCompress      = "compress"
	ToolDecompress    = "decompress"
	ToolSearchContext = "search_context"
	ToolACPStatus     = "acp_status"
	ToolAskUser       = "ask_user"
)

// toolSpecs 暴露给模型的工具清单。
func toolSpecs(deps Deps) []llm.ToolSpec {
	specs := []llm.ToolSpec{
		{
			Name:        ToolEmitSVG,
			Description: "交付最终 SVG。原样交付，不做任何改写：只在内部校验结构与画布尺寸。这是唯一的交付通道，必须调用。返回校验报告 JSON。",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "svg": {"type": "string", "description": "完整 SVG 源码，含 <svg> 根元素，不要代码块围栏"},
    "note": {"type": "string", "description": "一句话设计说明（可选）"}
  },
  "required": ["svg"]
}`),
		},
		{
			Name:        ToolValidateSVG,
			Description: "校验 SVG 结构并返回报告（不修改画布）。用于交付前自检。",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "svg": {"type": "string", "description": "待校验的 SVG 源码"}
  },
  "required": ["svg"]
}`),
		},
		{
			Name:        ToolCompress,
			Description: "把一段连续消息折叠成摘要块，释放上下文。summary 由你撰写，必须保留已确定的决定与关键参数。",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "ranges": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "startRef": {"type": "string", "description": "起始消息 ref，如 m00003"},
          "endRef": {"type": "string", "description": "结束消息 ref，如 m00008"},
          "topic": {"type": "string", "description": "该段主题，如 构图方案"},
          "summary": {"type": "string", "description": "摘要正文：保留决定、参数、未完成事项，丢弃试错过程"}
        },
        "required": ["startRef", "endRef", "summary"]
      }
    }
  },
  "required": ["ranges"]
}`),
		},
		{
			Name:        ToolDecompress,
			Description: "展开一个压缩块，让被折叠的消息重新可见。",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "blockId": {"type": "string", "description": "压缩块 id，如 b001"}
  },
  "required": ["blockId"]
}`),
		},
		{
			Name:        ToolSearchContext,
			Description: "在已折叠的压缩块里按关键词检索历史内容。",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "关键词"},
    "limit": {"type": "integer", "description": "最多返回条数，默认 5"}
  },
  "required": ["query"]
}`),
		},
		{
			Name:        ToolACPStatus,
			Description: "查看当前上下文用量与压缩块统计。",
			Parameters:  json.RawMessage(`{"type": "object", "properties": {}}`),
		},
		{
			Name:        ToolAskUser,
			Description: "向用户提问澄清需求（不阻塞，立即返回）。仅在关键信息缺失时使用。",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "question": {"type": "string", "description": "要问用户的问题"},
    "options": {"type": "array", "items": {"type": "string"}, "description": "可选项（可选）"}
  },
  "required": ["question"]
}`),
		},
	}

	// 压缩未启用时不下发压缩相关工具，避免模型无效调用。
	if compressorFor(deps) == nil {
		keep := specs[:0]
		for _, s := range specs {
			switch s.Name {
			case ToolCompress, ToolDecompress, ToolSearchContext, ToolACPStatus:
				continue
			}
			keep = append(keep, s)
		}
		specs = keep
	}

	// 图层工具：生成方式为多图层 + 图层总开关都打开、且工程已就绪时才下发。
	// 动图模式刻意不下发图层工具——它要的是「一个图层里的会动的画面」。
	if layerToolsEnabled(deps) {
		specs = append(specs, layerToolSpecs()...)
	}
	return specs
}

// layerToolsEnabled 图层读写工具是否可用：调用方开关 + 工程就绪 + 生成方式为多图层。
func layerToolsEnabled(deps Deps) bool {
	if !deps.LayerTools || deps.Layers == nil {
		return false
	}
	return config.NormalizeGenMode(deps.Settings).GenMode == config.GenModeLayers
}

// toolOutcome 一次工具执行结果。
type toolOutcome struct {
	Content string
	IsError bool
}

// runTool 按名字分发工具调用。
func (e *Engine) runTool(ctx context.Context, call llm.ToolCall) toolOutcome {
	args := call.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	switch call.Name {
	case ToolEmitSVG:
		return e.toolEmitSVG(args)
	case ToolValidateSVG:
		return e.toolValidateSVG(args)
	case ToolCompress:
		return e.toolCompress(args)
	case ToolDecompress:
		return e.toolDecompress(args)
	case ToolSearchContext:
		return e.toolSearchContext(args)
	case ToolACPStatus:
		return toolACPStatus(e)
	case ToolAskUser:
		return e.toolAskUser(args)
	}
	if isLayerTool(call.Name) {
		return e.runLayerTool(call)
	}
	return toolOutcome{Content: fmt.Sprintf("未知工具 %q，可用工具：%s", call.Name, strings.Join(toolNames(e.deps), ", ")), IsError: true}
}

// toolNames 可用工具名拼接（用于错误提示）。
func toolNames(deps Deps) []string {
	names := []string{
		ToolEmitSVG, ToolValidateSVG,
		ToolCompress, ToolDecompress, ToolSearchContext, ToolACPStatus, ToolAskUser,
	}
	if layerToolsEnabled(deps) {
		names = append(names, layerToolNames()...)
	}
	return names
}

// argErr 参数解析失败的中文错误。
func argErr(tool string, err error) toolOutcome {
	return toolOutcome{Content: fmt.Sprintf("%s 参数解析失败：%v", tool, err), IsError: true}
}

// ---- emit_svg ----

// toolEmitSVG 原样交付 SVG：不改写、不净化、不补属性、不包 transform。
// 只做两件事——校验它是 SVG，以及尺寸是否符合画布规范；
// 校验不通过时把问题回给模型，让它自己重画（而不是由代码替它改）。
func (e *Engine) toolEmitSVG(raw json.RawMessage) toolOutcome {
	var in struct {
		SVG  string `json:"svg"`
		Note string `json:"note"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return argErr(ToolEmitSVG, err)
	}
	if strings.TrimSpace(in.SVG) == "" {
		return toolOutcome{Content: "svg 不能为空", IsError: true}
	}

	// 只做外壳剥离（代码围栏 / 前后闲话），内容本身逐字保留。
	// Closed=false 说明模型给的是半截 SVG，此时连外壳都不动，交给校验判错。
	text := strings.TrimSpace(in.SVG)
	if r := svg.Extract(text); r.Found && r.Closed && strings.TrimSpace(r.SVG) != "" {
		text = strings.TrimSpace(r.SVG)
	}

	canvas := e.impl.snapshotCanvas()
	rep := svg.Validate(text)
	sizeIssues := svg.CheckCanvasSize(text, toSVGCanvas(canvas))
	rep.Issues = append(rep.Issues, sizeIssues...)
	rep.OK = true
	for _, it := range rep.Issues {
		if it.Level == "error" {
			rep.OK = false
			break
		}
	}

	e.impl.setSVG(text, rep)
	e.emit(Event{Kind: EvSVG, SVG: text, Text: strings.TrimSpace(in.Note)})

	out, err := json.Marshal(struct {
		OK   bool   `json:"ok"`
		Note string `json:"note,omitempty"`
		svg.Report
	}{OK: rep.OK, Note: strings.TrimSpace(in.Note), Report: rep})
	if err != nil {
		return toolOutcome{Content: fmt.Sprintf("报告序列化失败：%v", err), IsError: true}
	}
	// 自动校验：开启时把校验失败当错误回给模型，逼它按规范重画，而不是带着错误收工。
	if e.deps.Settings.AutoValidate && !rep.OK {
		return toolOutcome{
			Content: string(out) + "\n校验未通过，请修正下列问题后再次调用 emit_svg：" + issueSummary(rep),
			IsError: true,
		}
	}
	return toolOutcome{Content: string(out)}
}

// issueSummary 把 error/warn 级问题压成一行，供模型直接读。
func issueSummary(rep svg.Report) string {
	var b strings.Builder
	for _, it := range rep.Issues {
		if it.Level == "error" || it.Level == "warn" {
			b.WriteString("\n- [" + it.Level + "] " + it.Message)
		}
	}
	if b.Len() == 0 {
		return "\n- 校验报告未通过，但没有给出具体条目"
	}
	return b.String()
}

// ---- validate_svg ----

func (e *Engine) toolValidateSVG(raw json.RawMessage) toolOutcome {
	var in struct {
		SVG string `json:"svg"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return argErr(ToolValidateSVG, err)
	}
	text := strings.TrimSpace(in.SVG)
	if text == "" {
		// 省略 svg 时校验当前画布内容。
		text = e.impl.snapshotSVG()
		if text == "" {
			return toolOutcome{Content: "svg 不能为空，且当前画布还没有内容", IsError: true}
		}
	}
	rep := svg.Validate(text)
	rep.Issues = append(rep.Issues, svg.CheckCanvasSize(text, toSVGCanvas(e.impl.snapshotCanvas()))...)
	rep.OK = true
	for _, it := range rep.Issues {
		if it.Level == "error" {
			rep.OK = false
			break
		}
	}
	out, err := json.Marshal(rep)
	if err != nil {
		return toolOutcome{Content: fmt.Sprintf("报告序列化失败：%v", err), IsError: true}
	}
	return toolOutcome{Content: string(out)}
}

// ---- 压缩相关 ----

// compressArgRange 兼容 startRef/endRef 与 startId/endId 两种键名。
type compressArgRange struct {
	StartRef string `json:"startRef"`
	StartID  string `json:"startId"`
	EndRef   string `json:"endRef"`
	EndID    string `json:"endId"`
	Topic    string `json:"topic"`
	Summary  string `json:"summary"`
}

func (e *Engine) toolCompress(raw json.RawMessage) toolOutcome {
	var in struct {
		Ranges []compressArgRange `json:"ranges"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return argErr(ToolCompress, err)
	}
	comp := e.impl.compressor()
	if comp == nil {
		return toolOutcome{Content: "上下文压缩未启用", IsError: true}
	}
	ranges := make([]CompressRange, 0, len(in.Ranges))
	for _, r := range in.Ranges {
		start := firstNonEmpty(r.StartRef, r.StartID)
		end := firstNonEmpty(r.EndRef, r.EndID)
		ranges = append(ranges, CompressRange{StartRef: start, EndRef: end, Topic: r.Topic, Summary: r.Summary})
	}
	if len(ranges) == 0 {
		return toolOutcome{Content: "ranges 不能为空", IsError: true}
	}
	created, tokens, warnings, err := comp.Compress(ranges)
	if err != nil {
		return toolOutcome{Content: fmt.Sprintf("压缩失败：%v", err), IsError: true}
	}
	out, mErr := json.Marshal(struct {
		Created  int             `json:"created"`
		Tokens   int             `json:"tokens"`
		Warnings []string        `json:"warnings,omitempty"`
		Blocks   []CompressBlock `json:"blocks,omitempty"`
	}{Created: created, Tokens: tokens, Warnings: warnings, Blocks: comp.Blocks()})
	if mErr != nil {
		return toolOutcome{Content: fmt.Sprintf("结果序列化失败：%v", mErr), IsError: true}
	}
	return toolOutcome{Content: string(out)}
}

func (e *Engine) toolDecompress(raw json.RawMessage) toolOutcome {
	var in struct {
		BlockID string `json:"blockId"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return argErr(ToolDecompress, err)
	}
	comp := e.impl.compressor()
	if comp == nil {
		return toolOutcome{Content: "上下文压缩未启用", IsError: true}
	}
	restored, preview, err := comp.Decompress(in.BlockID)
	if err != nil {
		return toolOutcome{Content: fmt.Sprintf("展开失败：%v", err), IsError: true}
	}
	out, mErr := json.Marshal(struct {
		Restored int    `json:"restored"`
		Preview  string `json:"preview"`
	}{Restored: restored, Preview: preview})
	if mErr != nil {
		return toolOutcome{Content: fmt.Sprintf("结果序列化失败：%v", mErr), IsError: true}
	}
	return toolOutcome{Content: string(out)}
}

func (e *Engine) toolSearchContext(raw json.RawMessage) toolOutcome {
	var in struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return argErr(ToolSearchContext, err)
	}
	comp := e.impl.compressor()
	if comp == nil {
		return toolOutcome{Content: "上下文压缩未启用", IsError: true}
	}
	hits := comp.Search(in.Query, in.Limit)
	out, err := json.Marshal(struct {
		Total  int             `json:"total"`
		Blocks []CompressBlock `json:"blocks"`
	}{Total: len(hits), Blocks: hits})
	if err != nil {
		return toolOutcome{Content: fmt.Sprintf("检索结果序列化失败：%v", err), IsError: true}
	}
	return toolOutcome{Content: string(out)}
}

func toolACPStatus(e *Engine) toolOutcome {
	st := e.Status()
	out, err := json.Marshal(st)
	if err != nil {
		return toolOutcome{Content: fmt.Sprintf("状态序列化失败：%v", err), IsError: true}
	}
	return toolOutcome{Content: string(out)}
}

// ---- ask_user ----

func (e *Engine) toolAskUser(raw json.RawMessage) toolOutcome {
	var in struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return argErr(ToolAskUser, err)
	}
	q := strings.TrimSpace(in.Question)
	if q == "" {
		return toolOutcome{Content: "question 不能为空", IsError: true}
	}
	opts := make([]string, 0, len(in.Options))
	for _, o := range in.Options {
		if s := strings.TrimSpace(o); s != "" {
			opts = append(opts, s)
		}
	}
	e.emit(Event{Kind: EvAsk, Question: q, Options: opts})

	out, err := json.Marshal(struct {
		OK     bool   `json:"ok"`
		Status string `json:"status"`
	}{OK: true, Status: "已向用户提问，请继续推进；不要等待用户回复而空转。"})
	if err != nil {
		return toolOutcome{Content: "已向用户提问"}
	}
	return toolOutcome{Content: string(out)}
}

// ---- 辅助 ----

// toSVGCanvas config 预设转 svg 画布。
func toSVGCanvas(c config.CanvasPreset) svg.Canvas {
	return svg.Canvas{Name: c.Name, Width: c.Width, Height: c.Height, ViewBox: c.ViewBox}
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
