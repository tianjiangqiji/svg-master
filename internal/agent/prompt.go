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
	b.WriteString(svg.OutputSpec)

	return strings.TrimRight(b.String(), "\n")
}

// multiStepNote 多步开关打开时额外注入的一句话（需求：打开后多注入一句话）。
// 它只负责「允许并鼓励分步」，具体工具箱在 layerToolsNote 里声明。
const multiStepNote = "本次会话允许多步修改：先把任务拆成若干步，逐步调整，每步只动需要动的地方；" +
	"不必一次到位，也不要因为要修改就重画整幅。"

// layerToolsNote 图层工作区的使用约定（仅在图层工具下发时注入）。
func layerToolsNote() string {
	return "## 图层工作区\n" +
		"画布由多个图层叠成，每个图层是独立文件，你可以逐个操作：\n" +
		"- list_layers：先看有哪些层、当前在哪一层；\n" +
		"- read_layer：改之前先读，不要凭记忆改；\n" +
		"- create_layer：新增一层（背景、主体、文字、光效等各占一层更好改）；\n" +
		"- edit_layer：局部字符串替换，改颜色/坐标/文字走它，不要整层重写；\n" +
		"- set_layer：改名字、显示隐藏、锁定、不透明度、混合模式、层级；\n" +
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

// animatedNote 动图模式的交付约定：仍然是一张画布、一个图层，但画面要会动。
// 只有动图模式下才注入，且此时不下发图层工具。
func animatedNote() string {
	return "## 动图模式\n" +
		"本次交付**一张会动的 SVG**，仍然是一整幅画面、一个图层，不要拆图层、不要分多个文件。\n" +
		"用 emit_svg 交付**完整可播放**的 SVG，动画部分遵守以下约定：\n" +
		"- 首选**帧分组**：在根 <svg> 下按播放顺序写 <g data-frame=\"0\">…</g>、<g data-frame=\"1\">…</g>…，" +
		"每帧是一张独立的完整画面（自己带齐需要的图形），导出 GIF 时逐帧精确采样；\n" +
		"- 根元素上标注帧率与循环：data-fps=\"8\"（1–30 的整数，默认 8）、data-loop=\"1\"；\n" +
		"- 帧数控制在 4–24 帧：太少看不出动作，太多文件大且导出慢；\n" +
		"- 所有帧共用同一画布尺寸与构图基准，只改需要动的部分（位置、形状、颜色、透明度），" +
		"不要每帧换一套构图，否则播放时会跳；\n" +
		"- 也支持 SMIL（<animate> / <animateTransform> / <set>）做连续动画，但**帧分组优先**：" +
		"SMIL 在导出 GIF 时只能按时间采样，精度不如帧分组；\n" +
		"- 动画要能无缝循环：末帧接回首帧时不出现明显跳变。"
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
