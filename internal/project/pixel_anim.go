package project

import (
	"crypto/rand"
	"errors"
	"fmt"
	"image"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"svg-master/internal/pixel"
)

const (
	PixelModeStatic = "static"
	PixelModeAnim   = "anim"
)

var (
	ErrPixelStatic     = errors.New("此操作仅适用于静态像素工程")
	ErrPixelAnim       = errors.New("此操作仅适用于像素动图工程")
	ErrPixelModeLocked = errors.New("本工程已开始 AI 生成，请清空作品或打开其他工程后切换像素形态")
	ErrFrameNotFound   = errors.New("帧不存在")
)

type PixelFrame struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	File        string      `json:"file"`
	Visible     bool        `json:"visible"`
	Locked      bool        `json:"locked"`
	Index       int         `json:"index"`
	DelayMult   int         `json:"delayMult"`
	Bytes       int         `json:"bytes"`
	Updated     int64       `json:"updated"`
	Elements    int         `json:"elements,omitempty"`
	PixelRegion *pixel.Rect `json:"pixelRegion,omitempty"`
}

type PixelAnimMeta struct {
	FPS           int          `json:"fps"`
	Loop          bool         `json:"loop"`
	ActiveFrameID string       `json:"activeFrameId"`
	Frames        []PixelFrame `json:"frames"`
}

type PixelFrameOp struct {
	Action        string   `json:"action"`
	FrameID       string   `json:"frameId,omitempty"`
	SourceFrameID string   `json:"sourceFrameId,omitempty"`
	FrameIDs      []string `json:"frameIds,omitempty"`
	Name          string   `json:"name,omitempty"`
	Mode          string   `json:"mode,omitempty"`
	Index         *int     `json:"index,omitempty"`
	FPS           *int     `json:"fps,omitempty"`
	DelayMult     *int     `json:"delayMult,omitempty"`
	Visible       *bool    `json:"visible,omitempty"`
	Locked        *bool    `json:"locked,omitempty"`
	Loop          *bool    `json:"loop,omitempty"`
	DX            int      `json:"dx,omitempty"`
	DY            int      `json:"dy,omitempty"`
	Wrap          bool     `json:"wrap,omitempty"`
	Flatten       bool     `json:"flatten,omitempty"`
}

func selectedPixelMode(modes []string) (string, error) {
	if len(modes) == 0 || modes[0] == "" {
		return PixelModeStatic, nil
	}
	switch modes[0] {
	case PixelModeStatic, PixelModeAnim:
		return modes[0], nil
	default:
		return "", fmt.Errorf("未知像素形态 %q", modes[0])
	}
}

func validatePixelModeSize(mode string, w, h int) error {
	if _, err := selectedPixelMode([]string{mode}); err != nil {
		return err
	}
	if mode == PixelModeAnim && (w > pixel.MaxAnimSize || h > pixel.MaxAnimSize) {
		return fmt.Errorf("像素动图宽高不能超过 %d，请先裁剪或缩小画布", pixel.MaxAnimSize)
	}
	return pixel.ValidateSize(w, h)
}

func maxPixelLayersFor(w, h int) int {
	if w > pixel.MaxPresetSize || h > pixel.MaxPresetSize {
		return 14
	}
	return 32
}

func checkPixelLayerCapacity(w, h, count int) error {
	limit := maxPixelLayersFor(w, h)
	if count <= limit {
		return nil
	}
	if limit == 14 {
		return fmt.Errorf("画布宽或高超过 %d 时最多支持 %d 个图层，请先精简图层", pixel.MaxPresetSize, limit)
	}
	return fmt.Errorf("像素工程最多支持 %d 个图层", limit)
}

func clonePixelAnim(anim *PixelAnimMeta) *PixelAnimMeta {
	if anim == nil {
		return nil
	}
	out := *anim
	out.Frames = slices.Clone(anim.Frames)
	for i := range out.Frames {
		if region := out.Frames[i].PixelRegion; region != nil {
			copyOfRegion := *region
			out.Frames[i].PixelRegion = &copyOfRegion
		}
	}
	return &out
}

func samePixelAnimation(a, b *PixelAnimMeta) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.FPS == b.FPS && a.Loop == b.Loop && a.ActiveFrameID == b.ActiveFrameID &&
		slices.EqualFunc(a.Frames, b.Frames, func(x, y PixelFrame) bool {
			// The immutable raster filename also determines its coverage metadata.
			x.PixelRegion, y.PixelRegion = nil, nil
			return x == y
		})
}

func checkPixelAnim(f File) error {
	anim := f.Pixel.Anim
	if len(f.Layers) != 0 || f.ActiveLayer != "" || anim == nil || len(anim.Frames) == 0 {
		return errors.New("像素动图至少需要 1 帧且不能包含静态图层")
	}
	if anim.FPS < 1 || anim.FPS > 30 {
		return errors.New("帧率必须在 1 到 30 之间")
	}
	ids, files := make(map[string]bool), make(map[string]bool)
	for _, frame := range anim.Frames {
		if frame.ID == "" || ids[frame.ID] || !localPixelName(frame.File) ||
			!strings.EqualFold(filepath.Ext(frame.File), ".png") || files[strings.ToLower(frame.File)] {
			return fmt.Errorf("无效或重复的帧文件 %q", frame.File)
		}
		if frame.DelayMult < 0 || frame.DelayMult > 8 {
			return fmt.Errorf("帧 %q 的延时倍率必须在 1 到 8 之间", frame.Name)
		}
		ids[frame.ID], files[strings.ToLower(frame.File)] = true, true
	}
	return nil
}

func normalizePixelAnim(anim *PixelAnimMeta) {
	if anim == nil {
		return
	}
	sort.SliceStable(anim.Frames, func(i, j int) bool { return anim.Frames[i].Index < anim.Frames[j].Index })
	for i := range anim.Frames {
		frame := &anim.Frames[i]
		frame.Index = i + 1
		if frame.DelayMult == 0 {
			frame.DelayMult = 1
		}
		if frame.Name == "" {
			frame.Name = fmt.Sprintf("帧 %d", i)
		}
	}
	if pixelFrameIndex(anim, anim.ActiveFrameID) < 0 {
		anim.ActiveFrameID = anim.Frames[0].ID
	}
}

func emptyPixelCanvas(f File, img *image.NRGBA) (File, map[string][]byte, error) {
	if f.Pixel.Mode != PixelModeAnim {
		next, resources, _, err := addPixelLayer(f, "背景", "", 0, img)
		return next, resources, err
	}
	f.Pixel.Anim = &PixelAnimMeta{FPS: 8, Loop: true}
	frame := newPixelFrame("帧 0", 0)
	resources, err := setFrameRaster(&frame, img)
	insertPixelFrame(f.Pixel.Anim, frame, 1)
	return f, resources, err
}

func (p *Project) PixelSetMode(mode string, flatten bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.file.Pixel == nil {
		return ErrPixelProject
	}
	if mode != PixelModeStatic && mode != PixelModeAnim {
		return fmt.Errorf("未知像素形态 %q", mode)
	}
	f := p.clonePixelFileLocked()
	if f.Pixel.Mode == mode || f.Pixel.Mode == "" && mode == PixelModeStatic {
		return nil
	}
	if f.Pixel.ModeLocked {
		return ErrPixelModeLocked
	}
	if f.Pixel.Mode == PixelModeAnim && len(f.Pixel.Anim.Frames) > 1 && !flatten {
		return errors.New("当前动图有多帧，请先精简到一帧或确认保留当前帧")
	}
	if mode == PixelModeAnim && len(f.Layers) > 1 && !flatten {
		return errors.New("静态工程有多个图层，请先精简到一层或确认合并为首帧")
	}
	if err := validatePixelModeSize(mode, f.Pixel.Width, f.Pixel.Height); err != nil {
		return err
	}
	var art *image.NRGBA
	var err error
	locked, visible := false, true
	if f.Pixel.Anim != nil {
		i := pixelFrameIndex(f.Pixel.Anim, "")
		frame := f.Pixel.Anim.Frames[i]
		locked, visible = frame.Locked, frame.Visible
		art, err = p.pixelFrameImageLocked(i)
	} else if len(f.Layers) == 1 {
		layer := f.Layers[0]
		locked, visible = layer.Locked, layer.Visible
		if !visible || layer.Opacity == 0 {
			visible = false
			art, err = p.pixelLayerLocked(0)
		} else {
			art, err = p.composePixelLocked()
		}
	} else {
		art, err = p.composePixelLocked()
	}
	if err != nil {
		return err
	}
	f.Layers, f.ActiveLayer = nil, ""
	f.Pixel.Mode, f.Pixel.Anim = mode, nil
	f.Pixel.DraftLayerID, f.Pixel.HumanEdits = "", nil
	f.ContextCleared, f.History = time.Now().UnixNano(), nil
	next, resources, err := emptyPixelCanvas(f, art)
	if err != nil {
		return err
	}
	if len(next.Layers) > 0 {
		layer := &next.Layers[0]
		layer.Locked, layer.Visible = locked, visible
	} else {
		frame := &next.Pixel.Anim.Frames[0]
		frame.Locked, frame.Visible = locked, visible
	}
	if err := p.commitPixelLocked(next, resources); err != nil {
		return err
	}
	p.pixelUndo, p.pixelRedo = nil, nil
	return nil
}

func newPixelFrame(name string, fallback int) PixelFrame {
	name = strings.TrimSpace(name)
	if name == "" {
		name = fmt.Sprintf("帧 %d", fallback)
	}
	return PixelFrame{ID: "F" + rand.Text(), Name: name, Visible: true, DelayMult: 1}
}

func insertPixelFrame(anim *PixelAnimMeta, frame PixelFrame, index int) PixelFrame {
	if index < 1 {
		index = len(anim.Frames) + 1
	}
	index = min(index, len(anim.Frames)+1)
	anim.Frames = slices.Insert(anim.Frames, index-1, frame)
	for i := range anim.Frames {
		anim.Frames[i].Index = i + 1
	}
	anim.ActiveFrameID = frame.ID
	return anim.Frames[index-1]
}

func pixelFrameIndex(anim *PixelAnimMeta, id string) int {
	if anim == nil {
		return -1
	}
	if id == "" {
		id = anim.ActiveFrameID
	}
	return slices.IndexFunc(anim.Frames, func(frame PixelFrame) bool { return frame.ID == id })
}

func pixelFrameFile(id string) string { return "frame-" + id + "-" + rand.Text() + ".png" }

func storeFramePNG(frame *PixelFrame, data []byte) map[string][]byte {
	frame.File = pixelFrameFile(frame.ID)
	frame.Bytes = len(data)
	frame.Updated = time.Now().UnixNano()
	return map[string][]byte{filepath.Join("frames", frame.File): data}
}

func setFrameRaster(frame *PixelFrame, img *image.NRGBA) (map[string][]byte, error) {
	data, err := pixelPNG(img)
	if err != nil {
		return nil, err
	}
	count, region := pixel.Coverage(img)
	frame.Elements, frame.PixelRegion = count, nil
	if count > 0 {
		frame.PixelRegion = &region
	}
	return storeFramePNG(frame, data), nil
}

func (p *Project) PixelFramePNG(id string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.file.Pixel == nil || p.file.Pixel.Anim == nil {
		return nil, ErrPixelAnim
	}
	return p.readPixelFrameLocked(pixelFrameIndex(p.file.Pixel.Anim, id))
}

func (p *Project) readPixelFrameLocked(i int) ([]byte, error) {
	if i < 0 {
		return nil, ErrFrameNotFound
	}
	return readPixelFile(filepath.Join(p.dir, "frames", p.file.Pixel.Anim.Frames[i].File), pixel.MaxLayerPNGBytes)
}

func (p *Project) pixelFrameImageLocked(i int) (*image.NRGBA, error) {
	raw, err := p.readPixelFrameLocked(i)
	if err != nil {
		return nil, err
	}
	return pixel.DecodeLayer(raw, p.file.Pixel.Width, p.file.Pixel.Height)
}

func (p *Project) PixelFrameImage(id string) (*image.NRGBA, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.file.Pixel == nil || p.file.Pixel.Anim == nil {
		return nil, ErrPixelAnim
	}
	return p.pixelFrameImageLocked(pixelFrameIndex(p.file.Pixel.Anim, id))
}

func (p *Project) PixelFrameDraw(id string, ops []pixel.DrawOp, sel *pixel.Selection) (PixelFrame, pixel.DrawSummary, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.clonePixelFileLocked()
	if f.Pixel == nil || f.Pixel.Anim == nil {
		return PixelFrame{}, pixel.DrawSummary{}, ErrPixelAnim
	}
	i := pixelFrameIndex(f.Pixel.Anim, id)
	if i < 0 {
		return PixelFrame{}, pixel.DrawSummary{}, ErrFrameNotFound
	}
	frame := &f.Pixel.Anim.Frames[i]
	if frame.Locked {
		return PixelFrame{}, pixel.DrawSummary{}, ErrLocked
	}
	img, err := p.pixelFrameImageLocked(i)
	if err != nil {
		return *frame, pixel.DrawSummary{}, err
	}
	summary, err := pixel.Draw(img, ops, sel)
	if err != nil || summary.ChangedPixels == 0 {
		return *frame, summary, err
	}
	resources, err := setFrameRaster(frame, img)
	if err != nil {
		return *frame, summary, err
	}
	f.Pixel.Anim.ActiveFrameID = frame.ID
	return *frame, summary, p.commitPixelLocked(f, resources)
}

func (p *Project) PixelApplyFrameHuman(id string, img *image.NRGBA, region *pixel.Rect, rgba []byte) (HumanEdit, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.clonePixelFileLocked()
	if f.Pixel == nil || f.Pixel.Anim == nil {
		return HumanEdit{}, ErrPixelAnim
	}
	i := pixelFrameIndex(f.Pixel.Anim, id)
	if i < 0 {
		return HumanEdit{}, ErrFrameNotFound
	}
	frame := &f.Pixel.Anim.Frames[i]
	if frame.Locked {
		return HumanEdit{}, ErrLocked
	}
	before, err := p.pixelFrameImageLocked(i)
	if err != nil {
		return HumanEdit{}, err
	}
	after, err := applyHumanRaster(before, img, region, rgba)
	if err != nil {
		return HumanEdit{}, err
	}
	edit := newHumanEdit("", "", before, after)
	edit.FrameID, edit.FrameName = frame.ID, frame.Name
	if edit.ChangedPixels == 0 {
		return edit, nil
	}
	f.Pixel.recordHumanEdit(edit)
	resources, err := setFrameRaster(frame, after)
	if err != nil {
		return HumanEdit{}, err
	}
	return edit, p.commitPixelLocked(f, resources)
}

func (p *Project) PixelFrameOperation(op PixelFrameOp, ai bool) (PixelFrame, error) {
	if op.Action == "modeSwitch" {
		if ai {
			return PixelFrame{}, errors.New("像素形态只能由用户切换")
		}
		return PixelFrame{}, p.PixelSetMode(op.Mode, op.Flatten)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.clonePixelFileLocked()
	if f.Pixel == nil || f.Pixel.Anim == nil {
		return PixelFrame{}, ErrPixelAnim
	}
	anim := f.Pixel.Anim
	switch op.Action {
	case "fps", "loop":
		if ai {
			return PixelFrame{}, errors.New("播放设置只能由用户修改")
		}
		if op.Action == "fps" {
			if op.FPS == nil || *op.FPS < 1 || *op.FPS > 30 {
				return PixelFrame{}, errors.New("帧率必须在 1 到 30 之间")
			}
			anim.FPS = *op.FPS
		} else {
			if op.Loop == nil {
				return PixelFrame{}, errors.New("缺少循环播放设置")
			}
			anim.Loop = *op.Loop
		}
		return PixelFrame{}, p.commitPixelLocked(f, nil)

	case "add", "create", "duplicate":
		return p.addFrameLocked(f, op, ai)
	case "reorder":
		return p.reorderFramesLocked(f, op, ai)
	}

	i := pixelFrameIndex(anim, op.FrameID)
	if i < 0 {
		return PixelFrame{}, ErrFrameNotFound
	}
	frame := &anim.Frames[i]
	if op.Action == "active" {
		if anim.ActiveFrameID == frame.ID {
			return *frame, nil
		}
		anim.ActiveFrameID = frame.ID
		return *frame, p.commitPixelLocked(f, nil)
	}
	if (frame.Locked && (ai || op.Action == "delete" || op.Action == "shift")) || (ai && (op.Action == "locked" || op.Locked != nil)) {
		return *frame, ErrLocked
	}
	switch op.Action {
	case "delete":
		return p.deleteFrameLocked(f, i)
	case "shift":
		return p.shiftFrameLocked(f, i, op)
	case "set", "rename", "visible", "locked", "delayMult":
		id := frame.ID
		if op.Index != nil && ai && movesLockedFrame(anim.Frames, i, *op.Index) {
			return *frame, ErrLocked
		}
		if err := setFrameProperties(anim, i, op); err != nil {
			return *frame, err
		}
		return anim.Frames[pixelFrameIndex(anim, id)], p.commitPixelLocked(f, nil)
	default:
		return PixelFrame{}, fmt.Errorf("未知帧操作 %q", op.Action)
	}
}

func (p *Project) quantizePixelFrameLocked(f File, name string, img *image.NRGBA) error {
	anim := f.Pixel.Anim
	i := pixelFrameIndex(anim, "")
	frame := &anim.Frames[i]
	if frame.Locked {
		return ErrLocked
	}
	resources, err := setFrameRaster(frame, img)
	if err != nil {
		return err
	}
	frame.Name = name
	f.Pixel.DraftLayerID = frame.ID
	return p.commitPixelLocked(f, resources)
}

func (p *Project) addFrameLocked(f File, op PixelFrameOp, ai bool) (PixelFrame, error) {
	anim := f.Pixel.Anim
	frame := newPixelFrame(op.Name, len(anim.Frames))
	index := len(anim.Frames) + 1
	var resources map[string][]byte
	if op.Action == "duplicate" {
		sourceID := op.SourceFrameID
		if sourceID == "" {
			sourceID = op.FrameID
		}
		i := pixelFrameIndex(anim, sourceID)
		data, err := p.readPixelFrameLocked(i)
		if err != nil {
			return PixelFrame{}, err
		}
		source := anim.Frames[i]
		index = i + 2
		frame.Elements, frame.PixelRegion, frame.DelayMult = source.Elements, source.PixelRegion, source.DelayMult
		resources = storeFramePNG(&frame, data)
	} else {
		img, err := pixel.New(f.Pixel.Width, f.Pixel.Height)
		if err != nil {
			return PixelFrame{}, err
		}
		resources, err = setFrameRaster(&frame, img)
		if err != nil {
			return PixelFrame{}, err
		}
	}
	if op.Index != nil {
		index = *op.Index
	}
	if op.DelayMult != nil {
		if *op.DelayMult < 1 || *op.DelayMult > 8 {
			return PixelFrame{}, errors.New("延时倍率必须在 1 到 8 之间")
		}
		frame.DelayMult = *op.DelayMult
	}
	if op.Visible != nil {
		frame.Visible = *op.Visible
	}
	if !ai && op.Locked != nil {
		frame.Locked = *op.Locked
	}
	frame = insertPixelFrame(anim, frame, index)
	return frame, p.commitPixelLocked(f, resources)
}

func (p *Project) deleteFrameLocked(f File, i int) (PixelFrame, error) {
	anim := f.Pixel.Anim
	frame := anim.Frames[i]
	if f.Pixel.DraftLayerID == frame.ID {
		f.Pixel.DraftLayerID = ""
	}
	anim.Frames = slices.Delete(anim.Frames, i, i+1)
	if len(anim.Frames) == 0 {
		img, err := pixel.New(f.Pixel.Width, f.Pixel.Height)
		if err != nil {
			return PixelFrame{}, err
		}
		blank := newPixelFrame("", 0)
		resources, err := setFrameRaster(&blank, img)
		if err != nil {
			return PixelFrame{}, err
		}
		return insertPixelFrame(anim, blank, 1), p.commitPixelLocked(f, resources)
	}
	for i := range anim.Frames {
		anim.Frames[i].Index = i + 1
	}
	if anim.ActiveFrameID == frame.ID {
		anim.ActiveFrameID = anim.Frames[min(i, len(anim.Frames)-1)].ID
	}
	return anim.Frames[pixelFrameIndex(anim, "")], p.commitPixelLocked(f, nil)
}

func (p *Project) shiftFrameLocked(f File, i int, op PixelFrameOp) (PixelFrame, error) {
	anim := f.Pixel.Anim
	frame := &anim.Frames[i]
	img, err := p.pixelFrameImageLocked(i)
	if err != nil {
		return *frame, err
	}
	shifted := pixel.Shift(img, op.DX, op.DY, op.Wrap)
	if pixel.Changes(img, shifted).ChangedPixels == 0 {
		return *frame, nil
	}
	resources, err := setFrameRaster(frame, shifted)
	if err != nil {
		return *frame, err
	}
	anim.ActiveFrameID = frame.ID
	return *frame, p.commitPixelLocked(f, resources)
}

func setFrameProperties(anim *PixelAnimMeta, i int, op PixelFrameOp) error {
	frame := &anim.Frames[i]
	if op.Action == "rename" && strings.TrimSpace(op.Name) == "" {
		return errors.New("帧名不能为空")
	}
	if name := strings.TrimSpace(op.Name); name != "" {
		frame.Name = name
	}
	if op.Action == "visible" && op.Visible == nil || op.Action == "locked" && op.Locked == nil ||
		op.Action == "delayMult" && op.DelayMult == nil {
		return errors.New("缺少帧属性值")
	}
	if op.Visible != nil {
		frame.Visible = *op.Visible
	}
	if op.Locked != nil {
		frame.Locked = *op.Locked
	}
	if op.DelayMult != nil {
		if *op.DelayMult < 1 || *op.DelayMult > 8 {
			return errors.New("延时倍率必须在 1 到 8 之间")
		}
		frame.DelayMult = *op.DelayMult
	}
	frame.Updated = time.Now().UnixNano()
	if op.Index != nil {
		movePixelFrame(anim.Frames, i, *op.Index)
	}
	return nil
}

func movePixelFrame(frames []PixelFrame, i, index int) {
	target := min(max(index, 1), len(frames)) - 1
	frame := frames[i]
	if i < target {
		copy(frames[i:target], frames[i+1:target+1])
	} else {
		copy(frames[target+1:i+1], frames[target:i])
	}
	frames[target] = frame
	for i := range frames {
		frames[i].Index = i + 1
	}
}

func movesLockedFrame(frames []PixelFrame, i, index int) bool {
	target := min(max(index, 1), len(frames)) - 1
	if i == target {
		return false
	}
	for at := min(i, target); at <= max(i, target); at++ {
		if frames[at].Locked {
			return true
		}
	}
	return false
}

func (p *Project) reorderFramesLocked(f File, op PixelFrameOp, ai bool) (PixelFrame, error) {
	anim := f.Pixel.Anim
	if len(op.FrameIDs) == 0 {
		i := pixelFrameIndex(anim, op.FrameID)
		if i < 0 || op.Index == nil {
			return PixelFrame{}, errors.New("请指定要移动的帧及新位置")
		}
		if ai && movesLockedFrame(anim.Frames, i, *op.Index) {
			return PixelFrame{}, ErrLocked
		}
		movePixelFrame(anim.Frames, i, *op.Index)
	} else {
		if len(op.FrameIDs) != len(anim.Frames) {
			return PixelFrame{}, errors.New("排序必须包含所有帧")
		}
		ordered := make([]PixelFrame, 0, len(op.FrameIDs))
		seen := make(map[string]bool, len(op.FrameIDs))
		for i, id := range op.FrameIDs {
			source := pixelFrameIndex(anim, id)
			if id == "" || source < 0 || seen[id] {
				return PixelFrame{}, errors.New("排序包含重复或未知帧")
			}
			frame := anim.Frames[source]
			if ai && frame.Locked && frame.Index != i+1 {
				return PixelFrame{}, ErrLocked
			}
			seen[id] = true
			frame.Index = i + 1
			ordered = append(ordered, frame)
		}
		anim.Frames = ordered
	}
	return anim.Frames[pixelFrameIndex(anim, "")], p.commitPixelLocked(f, nil)
}
