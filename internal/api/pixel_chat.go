package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"slices"
	"strconv"
	"strings"

	"svg-master/internal/agent"
	"svg-master/internal/llm"
	"svg-master/internal/pixel"
	"svg-master/internal/project"
)

type pixelChatFields struct {
	pixelVersion
	Engine              string           `json:"engine"`
	PixelMode           string           `json:"pixelMode"`
	PixelDraftMode      string           `json:"pixelDraftMode"`
	PixelGif            bool             `json:"pixelGif"`
	MainImage           *llm.ImageData   `json:"mainImage"`
	RefImages           []pixelChatImage `json:"refImages"`
	Selection           *pixel.Selection `json:"selection"`
	RestrictToSelection bool             `json:"restrictToSelection"`
}

type pixelChatImage struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	llm.ImageData
}

type pixelAgentBridge struct {
	ctx       context.Context
	server    *Server
	project   *project.Project
	emit      func(agent.Event)
	selection *pixel.Selection
	restrict  bool
	assets    map[string]pixelChatImage
}

func (b pixelAgentBridge) changed(targetID string, region *pixel.Rect) {
	b.server.markPixelDirty(b.project)
	if b.ctx.Err() != nil {
		return
	}
	snap := b.server.snapshot()
	data := map[string]any{"projectId": snap.ProjectID, "revision": snap.Revision, "project": snap}
	if targetID != "" {
		readPNG, readImage := b.project.PixelLayerPNG, b.project.PixelLayer
		idKey := "layerId"
		if snap.Pixel.Anim != nil {
			readPNG, readImage = b.project.PixelFramePNG, b.project.PixelFrameImage
			idKey = "frameId"
		}
		data[idKey] = targetID
		if region == nil || region.W*region.H*4 > 1<<20 {
			if pngData, err := readPNG(targetID); err == nil {
				data["pngBase64"] = base64.StdEncoding.EncodeToString(pngData)
			}
		} else if img, err := readImage(targetID); err == nil {
			patch := make([]byte, region.W*region.H*4)
			row := region.W * 4
			for y := 0; y < region.H; y++ {
				offset := img.PixOffset(region.X, region.Y+y)
				copy(patch[y*row:(y+1)*row], img.Pix[offset:offset+row])
			}
			data["rgbaBase64"] = base64.StdEncoding.EncodeToString(patch)
			data["region"] = *region
		}
	}
	b.emit(agent.Event{Kind: agent.EvPixelUpdate, Pixel: data})
}

func (b pixelAgentBridge) CanvasInfo() agent.PixelCanvasInfo {
	doc := b.project.Meta()
	out := agent.PixelCanvasInfo{
		Width: doc.Pixel.Width, Height: doc.Pixel.Height, Palette: doc.Pixel.Palette,
		ActiveLayer: doc.ActiveLayer, Selection: b.selection,
		HasMainImage: b.assets["main"].ID != "", Layers: make([]agent.LayerInfo, 0, len(doc.Layers)),
		RefIDs: make([]string, 0, len(b.assets)),
		Mode:   doc.Pixel.Mode,
	}
	if out.Mode == "" {
		out.Mode = project.PixelModeStatic
	}
	if anim := doc.Pixel.Anim; anim != nil {
		out.FPS, out.Loop, out.ActiveFrame = anim.FPS, &anim.Loop, anim.ActiveFrameID
		out.Frames = make([]agent.PixelFrameInfo, 0, len(anim.Frames))
		for _, frame := range anim.Frames {
			out.Frames = append(out.Frames, toPixelFrameInfo(frame))
		}
	}
	for _, l := range doc.Layers {
		layer := toLayerInfo(l)
		layer.Region = l.PixelRegion
		out.Layers = append(out.Layers, layer)
	}
	for id := range b.assets {
		if id != "main" {
			out.RefIDs = append(out.RefIDs, id)
		}
	}
	slices.SortFunc(out.RefIDs, func(a, b string) int { return referenceNumber(a) - referenceNumber(b) })
	return out
}

func toPixelFrameInfo(frame project.PixelFrame) agent.PixelFrameInfo {
	return agent.PixelFrameInfo{
		ID: frame.ID, Name: frame.Name, Index: frame.Index, Visible: frame.Visible, Locked: frame.Locked,
		DelayMult: max(1, frame.DelayMult), Elements: frame.Elements, Region: frame.PixelRegion,
	}
}

func referenceNumber(id string) int {
	if !strings.HasPrefix(id, "ref_") {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimPrefix(id, "ref_"))
	if n < 1 || fmt.Sprintf("ref_%d", n) != id {
		return 0
	}
	return n
}

func (b pixelAgentBridge) image(id string) (*image.NRGBA, error) {
	src, ok := b.assets[id]
	if !ok {
		return nil, fmt.Errorf("未找到图像 %q", id)
	}
	if src.Data == "" {
		return pixelAssetImage(b.project, id)
	}
	raw, err := decodePixelBase64(src.Data, 12<<20)
	if err != nil {
		return nil, err
	}
	return pixel.Decode(raw)
}

func (b pixelAgentBridge) SetPalette(colors []string, mode string, maxColors int) ([]string, error) {
	if err := b.ctx.Err(); err != nil {
		return nil, err
	}
	switch mode {
	case "extract_from_main":
		img, err := b.image("main")
		if err != nil {
			return nil, err
		}
		colors = pixel.ExtractPalette(img, maxColors)
		if colors == nil {
			colors = []string{}
		}
	case "set", "replace", "add", "append":
	default:
		return nil, errors.New("未知的调色板操作")
	}
	palette, err := b.project.PixelSetPalette(colors, mode == "add" || mode == "append", nil, nil)
	if err == nil {
		b.changed("", nil)
	}
	return palette, err
}

func (b pixelAgentBridge) CreateLayer(name string, index int, blend string) (agent.LayerInfo, error) {
	if err := b.ctx.Err(); err != nil {
		return agent.LayerInfo{}, err
	}
	layer, err := b.project.PixelAddLayer(name, blend, index, nil)
	if err == nil {
		b.changed(layer.ID, nil)
	}
	return toLayerInfo(layer), err
}

func (b pixelAgentBridge) SetLayer(id string, patch agent.LayerPatch) (agent.LayerInfo, error) {
	if err := b.ctx.Err(); err != nil {
		return agent.LayerInfo{}, err
	}
	if b.restrict && (patch.Visible != nil || patch.Opacity != nil || patch.Blend != nil || patch.Index != nil) {
		return agent.LayerInfo{}, errors.New("仅修改选区时不能改变整层的显示、透明度、混合方式或层级")
	}
	layer, err := b.project.PixelProps(id, project.LayerProps{
		Name: patch.Name, Visible: patch.Visible, Locked: patch.Locked,
		Opacity: patch.Opacity, Blend: patch.Blend, Index: patch.Index,
	}, true)
	if err == nil {
		b.changed("", nil)
	}
	return toLayerInfo(layer), err
}

func (b pixelAgentBridge) DeleteLayer(id string) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if b.restrict {
		return errors.New("仅修改选区时不能删除整层，请在选区内擦除像素")
	}
	err := b.project.PixelDelete(id)
	if err == nil {
		b.changed("", nil)
	}
	return err
}

func (b pixelAgentBridge) FrameOperation(in agent.PixelFrameOperation) (agent.PixelFrameInfo, error) {
	if err := b.ctx.Err(); err != nil {
		return agent.PixelFrameInfo{}, err
	}
	if b.restrict {
		return agent.PixelFrameInfo{}, errors.New("仅修改选区时不能操作整帧")
	}
	frame, err := b.project.PixelFrameOperation(project.PixelFrameOp{
		Action: in.Action, FrameID: in.FrameID, SourceFrameID: in.SourceFrameID, Name: in.Name,
		Index: in.Index, Visible: in.Visible, DelayMult: in.DelayMult, FrameIDs: in.FrameIDs,
		DX: in.DX, DY: in.DY, Wrap: in.Wrap,
	}, true)
	if err == nil {
		// Frame metadata includes changed filenames, so the browser can reload moved or duplicated frames.
		b.changed("", nil)
	}
	return toPixelFrameInfo(frame), err
}

func (b pixelAgentBridge) Draw(id string, ops []pixel.DrawOp, clip *pixel.Selection) (pixel.DrawSummary, error) {
	if err := b.ctx.Err(); err != nil {
		return pixel.DrawSummary{}, err
	}
	if b.restrict || clip != nil {
		clip = b.selection
	}
	if b.project.Meta().Pixel.Anim != nil {
		frame, summary, err := b.project.PixelFrameDraw(id, ops, clip)
		if err == nil && summary.ChangedPixels > 0 {
			b.changed(frame.ID, &summary.Region)
		}
		return summary, err
	}
	layer, summary, err := b.project.PixelDraw(id, ops, clip)
	if err == nil && summary.ChangedPixels > 0 {
		b.changed(layer.ID, &summary.Region)
	}
	return summary, err
}

func (b pixelAgentBridge) ReadRegion(id string, rect pixel.Rect) (pixel.RegionView, error) {
	var img *image.NRGBA
	var err error
	if b.project.Meta().Pixel.Anim != nil {
		img, err = b.project.PixelFrameImage(id)
	} else if id == "composite" {
		img, err = b.project.PixelComposite()
	} else {
		img, err = b.project.PixelLayer(id)
	}
	if err != nil {
		return pixel.RegionView{}, err
	}
	return pixel.ReadRegion(img, rect)
}

func (b pixelAgentBridge) Inspect(opts pixel.InspectOpts) (string, pixel.InspectStats, error) {
	if err := b.ctx.Err(); err != nil {
		return "", pixel.InspectStats{}, err
	}
	meta := b.project.Meta().Pixel
	if opts.Mode == "onion" || opts.Mode == "filmstrip" {
		if meta.Anim == nil {
			return "", pixel.InspectStats{}, errors.New("洋葱皮和胶片检查仅支持像素动图")
		}
		data, stats, err := b.inspectAnimation(meta.Anim, opts)
		return base64.StdEncoding.EncodeToString(data), stats, err
	}
	if len(opts.FrameIDs) != 0 || (meta.Anim == nil && opts.FrameID != "") || (meta.Anim != nil && opts.LayerID != "") {
		return "", pixel.InspectStats{}, errors.New("请指定当前模式支持的检查目标")
	}
	var current, ref *image.NRGBA
	var err error
	if meta.Anim != nil {
		current, err = b.project.PixelFrameImage(opts.FrameID)
	} else if opts.Mode == "layer" || opts.Mode == "crop" && opts.LayerID != "" {
		current, err = b.project.PixelLayer(opts.LayerID)
	} else {
		current, err = b.project.PixelComposite()
	}
	if err != nil {
		return "", pixel.InspectStats{}, err
	}
	switch opts.Mode {
	case "compare_main":
		ref, err = b.image("main")
	case "compare_ref":
		ref, err = b.image(opts.RefID)
	}
	if err != nil {
		return "", pixel.InspectStats{}, err
	}
	if opts.Fit == "" {
		opts.Fit = meta.Fit
	}
	data, stats, err := pixel.Inspect(current, ref, opts)
	if err != nil {
		return "", stats, err
	}
	return base64.StdEncoding.EncodeToString(data), stats, nil
}

func (b pixelAgentBridge) inspectAnimation(anim *project.PixelAnimMeta, opts pixel.InspectOpts) ([]byte, pixel.InspectStats, error) {
	if opts.Mode == "onion" {
		id := opts.FrameID
		if id == "" {
			id = anim.ActiveFrameID
		}
		current, err := b.project.PixelFrameImage(id)
		if err != nil {
			return nil, pixel.InspectStats{}, err
		}
		var previous *image.NRGBA
		for i := 1; i < len(anim.Frames); i++ {
			if anim.Frames[i].ID == id {
				previous, err = b.project.PixelFrameImage(anim.Frames[i-1].ID)
				if err != nil {
					return nil, pixel.InspectStats{}, err
				}
				break
			}
		}
		return pixel.InspectOnion(current, previous)
	}
	const filmstripLimit = 8
	if len(opts.FrameIDs) > filmstripLimit {
		return nil, pixel.InspectStats{}, errors.New("每张胶片最多检查 8 帧")
	}
	var images []*image.NRGBA
	var indices []int
	for _, frame := range anim.Frames {
		if len(opts.FrameIDs) > 0 && !slices.Contains(opts.FrameIDs, frame.ID) {
			continue
		}
		img, err := b.project.PixelFrameImage(frame.ID)
		if err != nil {
			return nil, pixel.InspectStats{}, err
		}
		images = append(images, img)
		indices = append(indices, frame.Index-1)
		if len(images) == filmstripLimit {
			break
		}
	}
	if len(opts.FrameIDs) > 0 && len(images) != len(opts.FrameIDs) {
		return nil, pixel.InspectStats{}, errors.New("指定的帧不存在或重复")
	}
	return pixel.InspectFilmstrip(images, indices)
}

func (b pixelAgentBridge) ModelCompositePNG() (string, error) {
	img, err := b.project.PixelComposite()
	if err != nil {
		return "", err
	}
	return encodeModelImage(pixel.Preview(img, 1024))
}

func encodeModelImage(img *image.NRGBA) (string, error) {
	var out bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&out, img); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(out.Bytes()), nil
}

func (s *Server) preparePixelChat(ctx context.Context, p *project.Project, fields pixelChatFields,
	prompt string, vision bool, emit func(agent.Event)) (*pixelAgentBridge, *agent.PixelInput, string, error) {
	doc := p.Meta()
	meta := doc.Pixel
	if fields.PixelMode != "" && (fields.PixelMode != meta.Mode &&
		!(meta.Mode == "" && fields.PixelMode == project.PixelModeStatic)) {
		return nil, nil, prompt, errors.New("像素画模式已发生变化，请同步后重试")
	}
	mode := fields.PixelDraftMode
	if mode == "" {
		mode = meta.DraftMode
	}
	if mode == "" {
		mode = "vision"
	}
	if mode != "vision" && mode != "quantize" && mode != "prompt" {
		return nil, nil, prompt, errors.New("未知的像素起稿方式")
	}
	if err := pixel.ValidateSelection(fields.Selection, meta.Width, meta.Height); err != nil {
		return nil, nil, prompt, err
	}
	if fields.RestrictToSelection && fields.Selection == nil {
		return nil, nil, prompt, errors.New("请先在画布上选择要修改的像素")
	}
	b := &pixelAgentBridge{
		ctx: ctx, server: s, project: p, emit: emit, selection: fields.Selection, restrict: fields.RestrictToSelection,
		assets: make(map[string]pixelChatImage),
	}
	if mode != "prompt" {
		if fields.MainImage != nil {
			b.assets["main"] = pixelChatImage{ID: "main", ImageData: *fields.MainImage}
		} else if meta.MainImage != nil {
			b.assets["main"] = pixelChatImage{ID: "main", Name: meta.MainImage.Name}
		}
	}
	if fields.RefImages != nil {
		if len(fields.RefImages) > 8 {
			return nil, nil, prompt, errors.New("最多可附加 8 张参考图")
		}
		for i, ref := range fields.RefImages {
			if ref.ID == "" {
				ref.ID = fmt.Sprintf("ref_%d", i+1)
			}
			if _, exists := b.assets[ref.ID]; exists || referenceNumber(ref.ID) < 1 {
				return nil, nil, prompt, errors.New("参考图编号重复或无效")
			}
			b.assets[ref.ID] = ref
		}
	} else {
		for _, ref := range meta.RefImages {
			b.assets[ref.ID] = pixelChatImage{ID: ref.ID, Name: ref.Name}
		}
	}
	if mode == "quantize" && (b.assets["main"].ID == "" || meta.DraftLayerID == "") {
		return nil, nil, prompt, errors.New("采样精修需要先上传主图并生成底稿")
	}
	if !vision && len(b.assets) > 0 {
		return nil, nil, prompt, errors.New("当前模型未标记支持图像输入，请在设置→能力覆盖里勾选「视觉」或换模型")
	}
	if strings.TrimSpace(prompt) == "" {
		if b.assets["main"].ID != "" {
			prompt = "请按主图构图与像素画布尺寸完成像素画。"
		} else if len(b.assets) > 0 {
			prompt = "请参考附图创作当前画布上的像素画。"
		} else {
			return nil, nil, prompt, errors.New("请输入绘画指令或添加图片")
		}
		if meta.Anim != nil {
			prompt = "请参考当前帧与工程素材，制作循环像素动画。"
		}
	}
	input := &agent.PixelInput{
		DraftMode: mode, GIF: fields.PixelGif, Selection: fields.Selection, RestrictToSelection: fields.RestrictToSelection,
	}
	if len(meta.HumanEdits) > 0 {
		raw, err := json.Marshal(meta.HumanEdits)
		if err != nil {
			return nil, nil, prompt, err
		}
		input.HumanEdits = string(raw)
	}
	for _, id := range b.CanvasInfo().RefIDs {
		if err := ctx.Err(); err != nil {
			return nil, nil, prompt, err
		}
		img, err := b.image(id)
		if err != nil {
			return nil, nil, prompt, err
		}
		data, err := encodeModelImage(pixel.Preview(img, 384))
		if err != nil {
			return nil, nil, prompt, err
		}
		input.RefImages = append(input.RefImages, agent.PixelReference{
			ID: id, Number: referenceNumber(id), Name: b.assets[id].Name,
			Image: llm.ImageData{MIME: "image/png", Data: data},
		})
	}
	if _, exists := b.assets["main"]; exists {
		img, err := b.image("main")
		if err != nil {
			return nil, nil, prompt, err
		}
		data, err := encodeModelImage(pixel.Preview(img, 512))
		if err != nil {
			return nil, nil, prompt, err
		}
		input.MainImage = &llm.ImageData{MIME: "image/png", Data: data}
	}
	if vision {
		hasPixels := false
		for _, layer := range doc.Layers {
			if layer.Elements > 0 {
				hasPixels = true
				break
			}
		}
		if meta.Anim != nil {
			for _, frame := range meta.Anim.Frames {
				if frame.ID == meta.Anim.ActiveFrameID {
					hasPixels = frame.Elements > 0
					break
				}
			}
		}
		if hasPixels {
			data, err := b.ModelCompositePNG()
			if err != nil {
				return nil, nil, prompt, err
			}
			input.Composite = &llm.ImageData{MIME: "image/png", Data: data}
		}
	}
	return b, input, prompt, nil
}
