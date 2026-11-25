package agent

import (
	"fmt"
	"strings"

	"svg-master/internal/llm"
)

const (
	maxPixelInputImageBytes   = 12 << 20
	maxPixelToolImageBytes    = 4 << 20
	maxPixelRequestImageBytes = 16 << 20
	maxRecentPixelToolImages  = 2
)

func pixelSystemPrompt(vision bool) string {
	system := `你在 Pixel Studio 中编辑静态多层像素画。只用 pixel_* 工具读写画布。坐标从 0 开始，x 向右，y 向下；保持硬边像素与连贯色阶。
当前轮用户指令优先。会话开头的工程素材供当前轮参考：主图提供整体构图或复刻蓝本，编号参考图只参考指令提到的角色、画风或配件。改图前读取当前图层状态，尊重锁定图层与选区；已有内容需要保留时只局部修改。
新绘制可以先分层组织背景、主体轮廓，再刻画五官、光影和描边。大画布优先用矩形、多边形、椭圆等操作铺大色块；局部细节用小块 blit 或短线。将连续绘制操作合并为有意义的批次。
凡使用 pixel_draw 改动了作品，收尾前至少调用一次 pixel_inspect 检验新画面，按需再修正。纯咨询可以直接回答。只有工具成功写入的改动才算画布已被编辑。`
	if vision {
		return system + "\n有主图时可用 compare_main 查看位置差异，有指定参考图时按 ID 调用 compare_ref。色差只作为线索，风格变化和用户要求的替换允许出现差异。用 crop 与 pixel_read_region 核对单像素细节。"
	}
	return system + "\n当前模型使用文字检验。通过色彩统计和局部像素矩阵核对轮廓、位置和边界。"
}

// pixelAssetMessage builds the request-only prefix. The project supplies assets
// in stable reference-number order, and upload IDs and labels travel with them.
func (e *Engine) pixelAssetMessage(input PixelInput) (llm.Message, error) {
	message := llm.Message{Role: llm.RoleUser}
	if !e.deps.Vision && (input.MainImage != nil || len(input.RefImages) > 0) {
		return message, fmt.Errorf("当前模型未标记支持图像输入，请切换到支持视觉的模型")
	}
	addAsset := func(label string, img llm.ImageData) {
		message.Blocks = append(message.Blocks,
			llm.Block{Type: llm.BlockText, Text: label},
			llm.Block{Type: llm.BlockImage, Image: &img},
		)
	}
	if input.MainImage != nil && input.MainImage.Data != "" {
		addAsset("【主图（复刻或构图蓝本）】", *input.MainImage)
	}
	for i, ref := range input.RefImages {
		if ref.Image.Data == "" {
			continue
		}
		number := ref.Number
		if number < 1 {
			number = i + 1
		}
		addAsset(fmt.Sprintf("【参考图 #%d，ID %s，名称 %q】", number, ref.ID, ref.Name), ref.Image)
	}
	return message, nil
}

func (e *Engine) pixelUserMessage(prompt string, input PixelInput, info PixelCanvasInfo) (llm.Message, error) {
	message := llm.Message{Role: llm.RoleUser}
	addText := func(text string) {
		message.Blocks = append(message.Blocks, llm.Block{Type: llm.BlockText, Text: text})
	}
	addImage := func(img llm.ImageData) {
		message.Blocks = append(message.Blocks, llm.Block{Type: llm.BlockImage, Image: &img})
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		switch {
		case info.Mode == pixelAnimMode:
			prompt = "参考当前帧和工程素材，绘制循环像素动画。"
		case input.DraftMode == "quantize":
			prompt = "在当前采样底稿上精修，整理色块、边缘和适合分层的部分。"
		case input.MainImage != nil:
			prompt = "参考主图的构图与主体，绘制分层的像素画。"
		case len(input.RefImages) > 0:
			prompt = "参考所附素材创作分层的像素画。"
		default:
			prompt = "请询问我想绘制的主题。"
		}
	}
	addText("[用户指令]\n" + prompt)
	addText(fmt.Sprintf("当前画布尺寸：%d×%d。", info.Width, info.Height))
	if info.Mode == pixelAnimMode {
		addText(fmt.Sprintf("当前编辑帧 %s，共 %d 帧，帧率 %d fps。", info.ActiveFrame, len(info.Frames), info.FPS))
	}
	if info.Width > 512 || info.Height > 512 {
		addText("高分辨率画布优先用 rect、polygon、ellipse、flood_fill 或 dither_rect 铺色。总览会缩放，细节请用 crop 分区检查，并用不超过 128×128 的 blit 局部修整。")
	}
	if input.DraftMode == "quantize" {
		if info.Mode == pixelAnimMode {
			addText("第 0 帧已有主图采样底稿。保留用户修改与构图，用它作为动作序列的基准。")
		} else {
			addText("当前画布已有量化采样底稿。保留用户修改与构图，在现有图层上按要求精修。")
		}
	}
	if strings.TrimSpace(input.HumanEdits) != "" {
		addText("本次发送前，用户手动修改的画布区域：\n" + input.HumanEdits)
	}
	if choice := input.Selection; choice != nil {
		description := fmt.Sprintf("当前选区的范围 x=%d, y=%d, w=%d, h=%d。", choice.X, choice.Y, choice.W, choice.H)
		if choice.Runs != nil {
			description += "这是非矩形选区，范围内也可能有未选像素。"
		}
		if input.RestrictToSelection {
			description += "本轮只允许改动选中的像素，所有绘制必须保留选区外内容。"
		} else {
			description += "若用户要求只改选区，绘制时传 clipToSelection=true。"
		}
		addText(description)
	}

	if e.deps.Vision {
		snapshot := input.Composite
		if snapshot == nil {
			hasPixels := false
			for _, layer := range info.Layers {
				if layer.Visible && layer.Elements > 0 {
					hasPixels = true
					break
				}
			}
			for _, frame := range info.Frames {
				if frame.ID == info.ActiveFrame && frame.Elements > 0 {
					hasPixels = true
					break
				}
			}
			if hasPixels || input.DraftMode == "quantize" || input.HumanEdits != "" {
				png, err := e.deps.Pixel.ModelCompositePNG()
				if err != nil {
					return message, fmt.Errorf("读取当前像素画布快照：%w", err)
				}
				snapshot = &llm.ImageData{MIME: "image/png", Data: png}
			}
		}
		if snapshot != nil && snapshot.Data != "" {
			if info.Mode == pixelAnimMode {
				addText("【当前编辑帧快照】")
			} else {
				addText("【当前工程的合成画布快照】")
			}
			addImage(*snapshot)
		}
	}
	return message, nil
}

func pixelUserImageBytes(message llm.Message) int {
	size := 0
	for _, b := range message.Blocks {
		if b.Image != nil {
			size += len(b.Image.Data)
		}
	}
	return size
}

// projectPixelImages preserves message positions and tool call IDs. Attachments in
// older user messages are represented by their labels. Inspection images share
// the request budget with both the asset prefix and the current canvas image.
func projectPixelImages(msgs []llm.Message, currentUser, keepToolResults, assetBytes int) []llm.Message {
	remaining := maxPixelRequestImageBytes - assetBytes
	if currentUser >= 0 {
		remaining -= pixelUserImageBytes(msgs[currentUser])
	}
	out := append([]llm.Message(nil), msgs...)
	for i := len(out) - 1; i >= 0; i-- {
		message := &out[i]
		switch message.Role {
		case llm.RoleUser:
			if i == currentUser || pixelUserImageBytes(*message) == 0 {
				continue
			}
			blocks := make([]llm.Block, 0, len(message.Blocks))
			for _, b := range message.Blocks {
				if b.Type != llm.BlockImage {
					blocks = append(blocks, b)
				}
			}
			message.Blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: "[历史附图已省略，以工程素材和当前画布为准]"})
		case llm.RoleTool:
			copied := false
			for j := len(message.Blocks) - 1; j >= 0; j-- {
				result := message.Blocks[j].ToolResult
				if result == nil || len(result.Images) == 0 {
					continue
				}
				size := 0
				for _, img := range result.Images {
					size += len(img.Data)
				}
				if keepToolResults > 0 && size <= remaining {
					remaining -= size
					keepToolResults--
					continue
				}
				if !copied {
					message.Blocks = append([]llm.Block(nil), message.Blocks...)
					copied = true
				}
				textResult := *result
				textResult.Images = nil
				textResult.Content += "\n[历史检验图已折叠]"
				message.Blocks[j].ToolResult = &textResult
			}
		}
	}
	return out
}
