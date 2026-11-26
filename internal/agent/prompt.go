package agent

import (
	"fmt"
	"strings"

	"svg-master/internal/config"
	"svg-master/internal/svg"
)

// buildSystemPrompt 组装系统提示词。
//
// 按需求收敛到最小注入：只声明「交付物必须是 SVG」和「尺寸必须等于画布规范」两件事。
// 角色设定、硬性原则、SVG 技术事实、构图/留白/配色/元素数量等风格规范、
// 工作流程、完成标准、ACP 压缩说明段已全部移除，
// 风格与画法的决定权完全交给模型与用户提示词。
func buildSystemPrompt(canvas config.CanvasPreset) string {
	return buildPromptWithSpec(canvas, svg.OutputSpec)
}

// buildPromptWithSpec 用指定输出规范组装系统提示词（规范是唯一可换的一段）。
func buildPromptWithSpec(canvas config.CanvasPreset, spec string) string {
	var b strings.Builder

	b.WriteString("你是 SVG Master 的生成引擎。你的交付物是 SVG 源码。\n\n")

	// ---- 画布尺寸规范（唯一硬约束）----
	b.WriteString("## 画布尺寸规范\n")
	fmt.Fprintf(&b, "- 画布：%s，%d × %d px，宽高比 %s\n", canvas.Name, canvas.Width, canvas.Height, ratioString(canvas.Width, canvas.Height))
	if vb := strings.TrimSpace(canvas.ViewBox); vb != "" {
		fmt.Fprintf(&b, "- 根元素 viewBox 必须是：%s\n", vb)
	} else {
		fmt.Fprintf(&b, "- 根元素 viewBox 必须是：0 0 %d %d\n", canvas.Width, canvas.Height)
	}
	fmt.Fprintf(&b, "- 根元素 width / height 必须是：%d 与 %d\n", canvas.Width, canvas.Height)
	b.WriteString("- 除此之外没有任何风格要求：配色、构图、元素数量、细节密度由你按主题决定。\n\n")

	// ---- 输出规范（写死，与用户提示词解耦）----
	b.WriteString(spec)

	return strings.TrimRight(b.String(), "\n")
}

// buildLayersSystemPrompt 多图层模式的系统提示词。
//
// 与 quick 的关键差别：输出规范换成「分层交付」，并且必须与图层工作区说明
// 一起注入——只给工具不给规范时，模型经常仍一次性 emit_svg 整幅图，
// 结果多图层模式退化成单图层（用户反馈的「模型有些随机」正是这个原因）。
func buildLayersSystemPrompt(canvas config.CanvasPreset) string {
	return buildPromptWithSpec(canvas, svg.OutputSpecLayers)
}

// multiStepNote 多图层模式下注入的分步与分层约束。
// 它必须比 layerToolsNote 更强硬：模型一旦在这里「图省事」，
// 多图层模式就会退化成一次性整图。
const multiStepNote = "## 分层交付要求（多图层模式）\n" +
	"本次任务必须按图层分多步完成，禁止一次性交付整幅画面：\n" +
	"- 先把任务拆成若干步（例如：背景 → 主体轮廓 → 主体细节 → 装饰 → 文字）；\n" +
	"- 每一步只建一个图层或只改一个图层，改完再进入下一步；\n" +
	"- 每个图层都在画布坐标系里直接画（不要靠 translate 去拼整体构图）；\n" +
	"- 不要因为要修改就重画整幅，只动需要动的那一层。"

// layerToolsNote 图层工作区的使用约定（仅在图层工具下发时注入）。
func layerToolsNote() string {
	return "## 图层工作区\n" +
		"画布由多个图层叠成，每个图层是独立文件，你可以逐个操作：\n" +
		"- list_layers：先看有哪些层、当前在哪一层；\n" +
		"- read_layer：改之前先读，不要凭记忆改；\n" +
		"- create_layer：新增一层（背景、主体、文字、光效等各占一层更好改）；\n" +
		"- edit_layer：局部字符串替换，改颜色/坐标/文字走它，不要整层重写；\n" +
		"- set_layer：改名字、显示隐藏、锁定、不透明度、混合模式、层级；\n" +
		"- emit_svg：覆盖当前图层（整层重画的逃生口，不要用它代替 create_layer）；\n" +
		"- 锁定层不可改写；不存在的 id 会报错，报错后先 list_layers 重新看。\n" +
		"图层内容可以是完整 <svg>，也可以是裸片段（<g>/<path>/<rect>…）。\n" +
		"用户能看到图层面板，所以图层名字要起得有意义，层级要有序。"
}

// contextClearedNote 用户清空上下文后的提醒：本地历史被清空了，但工程文件还在，
// 必须先读图层重建认知，再按当前需求动手。
func contextClearedNote() string {
	return "## 上下文已重置\n" +
		"用户刚清空了对话上下文，之前的对话你已看不到，**但画布与图层文件都还在**。\n" +
		"在动任何东西之前，先用 list_layers 看清工程结构，再 read_layer 读你打算改的图层，" +
		"并结合作品现有内容判断用户意图；不要假设画布是空的。"
}

// ratioString 把宽高化简成 "w:h"。
func ratioString(w, h int) string {
	if w <= 0 || h <= 0 {
		return "未知"
	}
	g := gcdInt(w, h)
	return fmt.Sprintf("%d:%d", w/g, h/g)
}

// gcdInt 求最大公约数。
func gcdInt(a, b int) int {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}
