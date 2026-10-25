package agent

// 图层读写工具：让模型在多步模式下逐个图层地创建、读取、编辑，而不是一次性吐出整幅 SVG。
//
// 设计取舍：
//   - create / read / edit 三个核心工具覆盖 90% 的迭代场景；
//   - set_layer / delete_layer 负责「改名字、改可见性、改层级、删层」这类结构化操作；
//   - 每个工具都只碰一个图层，失败范围可控，模型看得到具体是哪一层出了问题；
//   - 所有返回值都是 JSON，字段名与前端图层面板一致，模型和用户看到的是同一套术语。

import (
	"encoding/json"
	"fmt"
	"strings"

	"svg-master/internal/llm"
)

// 图层工具名。
const (
	ToolCreateLayer = "create_layer"
	ToolReadLayer   = "read_layer"
	ToolEditLayer   = "edit_layer"
	ToolListLayers  = "list_layers"
	ToolSetLayer    = "set_layer"
	ToolDeleteLayer = "delete_layer"
)

// layerToolSpecs 图层工具清单（仅在 LayerTools 打开时下发）。
func layerToolSpecs() []llm.ToolSpec {
	return []llm.ToolSpec{
		{
			Name: ToolListLayers,
			Description: "列出工程里全部图层的元信息（id / 名字 / 可见性 / 锁定 / 不透明度 / 混合模式 / 层级）。" +
				"动手前先调它，确认从哪里改、当前作用在哪一层。",
			Parameters: json.RawMessage(`{"type": "object", "properties": {}}`),
		},
		{
			Name: ToolReadLayer,
			Description: "读取一个图层的完整 SVG 内容。省略 layerId 时读当前选中图层。" +
				"改之前必须读，不要凭记忆改。",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "layerId": {"type": "string", "description": "图层 id；省略则读当前图层"}
  }
}`),
		},
		{
			Name: ToolCreateLayer,
			Description: "新建一个图层并写入 SVG 内容，返回新图层 id。新图层默认在最上层、可见。" +
				"content 可以是完整 <svg>，也可以是裸片段（<g>/<path>/<rect> 等），两种都行。",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "图层名，如「背景」「太阳」「文字标题」"},
    "content": {"type": "string", "description": "SVG 内容，可留空之后再 edit"},
    "index": {"type": "integer", "description": "层级序号，越大越靠上；省略则排到最上层"},
    "blend": {"type": "string", "description": "混合模式，默认 normal"}
  },
  "required": ["name"]
}`),
		},
		{
			Name: ToolEditLayer,
			Description: "对图层内容做局部字符串替换（不是整层重写）。" +
				"oldText 必须在图层里唯一出现，否则带上更多上下文重试，或把 replaceAll 设为 true。" +
				"这是日常微调的主要手段：改颜色、改坐标、改文字都走它。",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "layerId": {"type": "string", "description": "图层 id；省略则改当前图层"},
    "oldText": {"type": "string", "description": "要被替换的原文（必须与图层内容逐字一致）"},
    "newText": {"type": "string", "description": "替换成的新文本"},
    "replaceAll": {"type": "boolean", "description": "为 true 时替换全部出现处"}
  },
  "required": ["oldText", "newText"]
}`),
		},
		{
			Name:        ToolSetLayer,
			Description: "调整图层属性：改名、显示/隐藏、锁定、不透明度、混合模式、层级。只传要改的字段。",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "layerId": {"type": "string", "description": "图层 id；省略则改当前图层"},
    "name": {"type": "string"},
    "visible": {"type": "boolean"},
    "locked": {"type": "boolean"},
    "opacity": {"type": "number", "description": "0..1"},
    "blend": {"type": "string"},
    "index": {"type": "integer", "description": "层级序号，越大越靠上"}
  }
}`),
		},
		{
			Name:        ToolDeleteLayer,
			Description: "删除一个图层（连同它的文件）。仅在用户明确要求删掉某层内容时使用。",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "layerId": {"type": "string", "description": "图层 id"}
  },
  "required": ["layerId"]
}`),
		},
	}
}

// layerToolNames 图层工具名（错误提示用）。
func layerToolNames() []string {
	return []string{ToolCreateLayer, ToolReadLayer, ToolEditLayer, ToolListLayers, ToolSetLayer, ToolDeleteLayer}
}

// isLayerTool 判断是否为图层工具。
func isLayerTool(name string) bool {
	switch name {
	case ToolCreateLayer, ToolReadLayer, ToolEditLayer, ToolListLayers, ToolSetLayer, ToolDeleteLayer:
		return true
	}
	return false
}

// runLayerTool 分发并执行图层工具。
func (e *Engine) runLayerTool(call llm.ToolCall) toolOutcome {
	br := e.deps.Layers
	if br == nil {
		return toolOutcome{Content: "图层工作区未就绪（工程未打开）", IsError: true}
	}
	args := call.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}

	switch call.Name {
	case ToolListLayers:
		return layerList(br, e)

	case ToolReadLayer:
		var in struct {
			LayerID string `json:"layerId"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return argErr(call.Name, err)
		}
		info, content, err := br.Read(strings.TrimSpace(in.LayerID))
		if err != nil {
			return layerErr(call.Name, err)
		}
		return toolOutcome{Content: marshalJSON(struct {
			Layer   LayerInfo `json:"layer"`
			Content string    `json:"content"`
			Bytes   int       `json:"bytes"`
		}{Layer: info, Content: content, Bytes: len(content)})}

	case ToolCreateLayer:
		var in struct {
			Name    string `json:"name"`
			Content string `json:"content"`
			Index   int    `json:"index"`
			Blend   string `json:"blend"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return argErr(call.Name, err)
		}
		info, err := br.Add(strings.TrimSpace(in.Name), in.Content, strings.TrimSpace(in.Blend), in.Index)
		if err != nil {
			return layerErr(call.Name, err)
		}
		e.emit(Event{Kind: EvLayers, Layers: layerListOf(br)})
		e.emit(Event{Kind: EvSVG, SVG: br.Render()})
		return toolOutcome{Content: marshalJSON(struct {
			OK    bool      `json:"ok"`
			Layer LayerInfo `json:"layer"`
			Note  string    `json:"note"`
		}{OK: true, Layer: info, Note: "已新建图层。想改内容用 edit_layer，想改属性用 set_layer。"})}

	case ToolEditLayer:
		var in struct {
			LayerID    string `json:"layerId"`
			OldText    string `json:"oldText"`
			NewText    string `json:"newText"`
			ReplaceAll bool   `json:"replaceAll"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return argErr(call.Name, err)
		}
		n, err := br.Patch(strings.TrimSpace(in.LayerID), in.OldText, in.NewText, in.ReplaceAll)
		if err != nil {
			return layerErr(call.Name, err)
		}
		e.emit(Event{Kind: EvLayers, Layers: layerListOf(br)})
		e.emit(Event{Kind: EvSVG, SVG: br.Render()})
		return toolOutcome{Content: marshalJSON(struct {
			OK           bool `json:"ok"`
			Replacements int  `json:"replacements"`
		}{OK: true, Replacements: n})}

	case ToolSetLayer:
		var in struct {
			LayerID string   `json:"layerId"`
			Name    *string  `json:"name"`
			Visible *bool    `json:"visible"`
			Locked  *bool    `json:"locked"`
			Opacity *float64 `json:"opacity"`
			Blend   *string  `json:"blend"`
			Index   *int     `json:"index"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return argErr(call.Name, err)
		}
		info, err := br.Props(strings.TrimSpace(in.LayerID), LayerPatch{
			Name: in.Name, Visible: in.Visible, Locked: in.Locked,
			Opacity: in.Opacity, Blend: in.Blend, Index: in.Index,
		})
		if err != nil {
			return layerErr(call.Name, err)
		}
		e.emit(Event{Kind: EvLayers, Layers: layerListOf(br)})
		e.emit(Event{Kind: EvSVG, SVG: br.Render()})
		return toolOutcome{Content: marshalJSON(struct {
			OK    bool      `json:"ok"`
			Layer LayerInfo `json:"layer"`
		}{OK: true, Layer: info})}

	case ToolDeleteLayer:
		var in struct {
			LayerID string `json:"layerId"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return argErr(call.Name, err)
		}
		id := strings.TrimSpace(in.LayerID)
		if id == "" {
			return toolOutcome{Content: "layerId 不能为空", IsError: true}
		}
		if err := br.Delete(id); err != nil {
			return layerErr(call.Name, err)
		}
		e.emit(Event{Kind: EvLayers, Layers: layerListOf(br)})
		e.emit(Event{Kind: EvSVG, SVG: br.Render()})
		return toolOutcome{Content: marshalJSON(map[string]any{"ok": true, "deleted": id})}
	}
	return toolOutcome{Content: fmt.Sprintf("未知图层工具 %q", call.Name), IsError: true}
}

// layerList 返回图层列表 + 当前图层 + 画布。
func layerList(br LayerBridge, e *Engine) toolOutcome {
	w, h, vb := br.Canvas()
	return toolOutcome{Content: marshalJSON(struct {
		Canvas struct {
			Width   int    `json:"width"`
			Height  int    `json:"height"`
			ViewBox string `json:"viewBox"`
		} `json:"canvas"`
		Active string      `json:"active"`
		Layers []LayerInfo `json:"layers"`
		Count  int         `json:"count"`
	}{
		Canvas: struct {
			Width   int    `json:"width"`
			Height  int    `json:"height"`
			ViewBox string `json:"viewBox"`
		}{Width: w, Height: h, ViewBox: vb},
		Active: br.Active(),
		Layers: layerListOf(br),
		Count:  len(br.List()),
	})}
}

// layerListOf 取图层列表（永不返回 nil，便于前端直接迭代）。
func layerListOf(br LayerBridge) []LayerInfo {
	ls := br.List()
	if ls == nil {
		return []LayerInfo{}
	}
	return ls
}

// layerErr 把图层操作错误压成可读中文。
func layerErr(tool string, err error) toolOutcome {
	return toolOutcome{Content: fmt.Sprintf("%s 执行失败：%v", tool, err), IsError: true}
}
