package project

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"svg-master/internal/llm"
	"svg-master/internal/pixel"
)

const (
	EngineSVG   = "svg"
	EnginePixel = "pixel"

	maxReferences  = 8
	maxAssetBytes  = 12 << 20
	maxAssetsBytes = 48 << 20
)

var ErrPixelProject = errors.New("需要打开像素画工程")
var ErrSVGProject = errors.New("此操作仅适用于 SVG 工程")
var ErrPixelRevision = errors.New("像素工程已发生变化，请同步后重试")

type PixelMeta struct {
	Width          int            `json:"width"`
	Height         int            `json:"height"`
	Revision       uint64         `json:"revision"`
	Palette        []string       `json:"palette,omitempty"`
	PrimaryColor   string         `json:"primaryColor,omitempty"`
	SecondaryColor string         `json:"secondaryColor,omitempty"`
	MainImage      *PixelAsset    `json:"mainImage,omitempty"`
	RefImages      []PixelAsset   `json:"refImages,omitempty"`
	NextReference  int            `json:"nextReference,omitempty"`
	Fit            string         `json:"fit,omitempty"`
	DraftLayerID   string         `json:"draftLayerId,omitempty"`
	DraftMode      string         `json:"draftMode,omitempty"`
	HumanEdits     []HumanEdit    `json:"humanEdits,omitempty"`
	Mode           string         `json:"mode,omitempty"`
	ModeLocked     bool           `json:"modeLocked,omitempty"`
	Anim           *PixelAnimMeta `json:"anim,omitempty"`
}

type PixelAsset struct {
	ID   string `json:"id"`
	File string `json:"file"`
	Name string `json:"name"`
	MIME string `json:"mime"`
	Size int    `json:"size"`
}

type HumanEdit struct {
	LayerID   string `json:"layerId,omitempty"`
	LayerName string `json:"layerName,omitempty"`
	FrameID   string `json:"frameId,omitempty"`
	FrameName string `json:"frameName,omitempty"`
	pixel.DrawSummary
	Timestamp int64 `json:"timestamp"`
}

func (p *Project) Engine() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.file.Engine == EnginePixel {
		return EnginePixel
	}
	return EngineSVG
}

func (p *Project) ID() string { return p.id }

func (p *Project) CheckPixelRevision(id string, revision uint64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.file.Pixel == nil {
		return ErrPixelProject
	}
	if id != p.id || revision != p.file.Pixel.Revision {
		return ErrPixelRevision
	}
	return nil
}

func CreatePixel(parent, name, kind string, width, height int, modes ...string) (*Project, error) {
	mode, err := selectedPixelMode(modes)
	if err != nil {
		return nil, err
	}
	if err := validatePixelModeSize(mode, width, height); err != nil {
		return nil, err
	}
	name = sanitizeName(name)
	if name == "" {
		name = "未命名像素画"
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	dir, err := uniqueDir(parent, name)
	if err != nil {
		return nil, err
	}
	return initPixel(dir, name, kind, width, height, mode)
}

func QuickPixelRoot(dataDir string) string { return filepath.Join(dataDir, "quick-pixel") }

func OpenQuickPixel(dataDir string, fresh bool, modes ...string) (*Project, error) {
	mode, err := selectedPixelMode(modes)
	if err != nil {
		return nil, err
	}
	dir := QuickPixelRoot(dataDir)
	if _, err := os.Stat(filepath.Join(dir, "project.json")); errors.Is(err, os.ErrNotExist) {
		return initPixel(dir, "像素画草稿", KindQuick, pixel.DefaultW, pixel.DefaultH, mode)
	} else if err != nil {
		return nil, err
	}
	p, err := Open(dir, KindQuick)
	if err != nil {
		return nil, err
	}
	if p.Engine() != EnginePixel {
		return nil, ErrPixelProject
	}
	if fresh {
		err = p.ResetPixel(modes...)
	} else if len(modes) > 0 && modes[0] != "" {
		err = p.PixelSetMode(mode, false)
	}
	return p, err
}

func initPixel(dir, name, kind string, width, height int, mode string) (*Project, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	p := &Project{dir: dir, kind: kind, id: rand.Text()}
	f := newFile(dir, kind)
	f.Name = name
	f.Engine = EnginePixel
	f.Pixel = &PixelMeta{Width: width, Height: height, DraftMode: "vision", Mode: mode}
	f.Canvas = pixelCanvas(width, height)
	img, err := pixel.New(width, height)
	if err != nil {
		return nil, err
	}
	next, resources, err := emptyPixelCanvas(f, img)
	if err != nil {
		return nil, err
	}
	return p, p.commitPixelLocked(next, resources)
}

func pixelCanvas(width, height int) CanvasMeta {
	return CanvasMeta{Name: "像素画布", Width: width, Height: height, ViewBox: fmt.Sprintf("0 0 %d %d", width, height)}
}

func clonePixel(meta *PixelMeta) *PixelMeta {
	if meta == nil {
		return nil
	}
	out := *meta
	out.Palette = slices.Clone(meta.Palette)
	out.RefImages = slices.Clone(meta.RefImages)
	out.HumanEdits = slices.Clone(meta.HumanEdits)
	for i := range out.HumanEdits {
		out.HumanEdits[i].NewColors = slices.Clone(meta.HumanEdits[i].NewColors)
	}
	if meta.MainImage != nil {
		main := *meta.MainImage
		out.MainImage = &main
	}
	out.Anim = clonePixelAnim(meta.Anim)
	return &out
}

func (p *Project) clonePixelFileLocked() File {
	f := p.file
	f.Pixel = clonePixel(f.Pixel)
	f.Layers = slices.Clone(f.Layers)
	return f
}

func checkPixelFile(f File) error {
	if f.Pixel == nil {
		return errors.New("像素工程缺少画布信息")
	}
	if err := validatePixelModeSize(f.Pixel.Mode, f.Pixel.Width, f.Pixel.Height); err != nil {
		return err
	}
	if f.Pixel.Mode == PixelModeAnim {
		if err := checkPixelAnim(f); err != nil {
			return err
		}
	} else if f.Pixel.Anim != nil {
		return errors.New("静态像素工程不能包含动图帧序列")
	}
	if err := checkPixelLayerCapacity(f.Pixel.Width, f.Pixel.Height, len(f.Layers)); err != nil {
		return err
	}
	if len(f.Pixel.RefImages) > maxReferences {
		return errors.New("像素工程的参考图超出上限")
	}
	for _, l := range f.Layers {
		if !localPixelName(l.File) || !strings.EqualFold(filepath.Ext(l.File), ".png") {
			return fmt.Errorf("无效的像素图层文件 %q", l.File)
		}
	}
	for _, a := range pixelAssets(f.Pixel) {
		if !localPixelName(a.File) || a.Size < 0 || a.Size > maxAssetBytes {
			return fmt.Errorf("无效的图片资产 %q", a.ID)
		}
	}
	return nil
}

func localPixelName(name string) bool {
	return filepath.IsLocal(name) && filepath.Base(name) == name && strings.IndexAny(name, "\\/:") == -1
}

func pixelAssets(meta *PixelMeta) []PixelAsset {
	if meta.MainImage == nil {
		return meta.RefImages
	}
	return append([]PixelAsset{*meta.MainImage}, meta.RefImages...)
}

func pixelResources(f File) map[string]struct{} {
	out := make(map[string]struct{})
	if f.Pixel == nil {
		return out
	}
	for _, l := range f.Layers {
		out[filepath.Join("layers", l.File)] = struct{}{}
	}
	if f.Pixel.Anim != nil {
		for _, frame := range f.Pixel.Anim.Frames {
			out[filepath.Join("frames", frame.File)] = struct{}{}
		}
	}
	for _, a := range pixelAssets(f.Pixel) {
		out[filepath.Join("assets", a.File)] = struct{}{}
	}
	return out
}

// commitPixelLocked stages new PNGs/assets before publishing their filenames in project.json.
func (p *Project) commitPixelLocked(next File, resources map[string][]byte) error {
	next.Pixel.Revision++
	next.Modified = time.Now().UnixNano()
	sortLayers(next.Layers)
	written := make([]string, 0, len(resources))
	committed := false
	defer func() {
		if !committed {
			for _, file := range written {
				_ = os.Remove(file)
			}
		}
	}()
	for rel, data := range resources {
		file := filepath.Join(p.dir, rel)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return err
		}
		if err := writePixelFile(file, data); err != nil {
			return err
		}
		written = append(written, file)
	}
	if err := savePixelManifest(p.dir, next); err != nil {
		return err
	}
	old := pixelResources(p.file)
	p.file = next
	p.pixelComposite = nil
	committed = true
	for rel := range pixelResources(next) {
		delete(old, rel)
	}
	for rel := range old {
		_ = os.Remove(filepath.Join(p.dir, rel))
	}
	return nil
}

func savePixelManifest(dir string, f File) error {
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return writePixelFile(filepath.Join(dir, "project.json"), raw)
}

func writePixelFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pixel-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(data)
	if writeErr == nil {
		writeErr = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func pixelPNG(img *image.NRGBA) ([]byte, error) {
	var out bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if img.Bounds().Dx()*img.Bounds().Dy() > pixel.MaxAnimSize*pixel.MaxAnimSize {
		enc.CompressionLevel = png.DefaultCompression
	}
	if err := enc.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func pixelLayerFile(id string) string { return "layer-" + id + "-" + rand.Text() + ".png" }

func updatePixelLayer(f *File, index int, img *image.NRGBA) (map[string][]byte, error) {
	data, err := pixelPNG(img)
	if err != nil {
		return nil, err
	}
	l := &f.Layers[index]
	l.File = pixelLayerFile(l.ID)
	l.Bytes = len(data)
	l.Updated = time.Now().UnixNano()
	count, region := pixel.Coverage(img)
	l.Elements = count
	l.PixelRegion = nil
	if count > 0 {
		l.PixelRegion = &region
	}
	return map[string][]byte{filepath.Join("layers", l.File): data}, nil
}

func addPixelLayer(f File, name, blend string, index int, img *image.NRGBA) (File, map[string][]byte, Layer, error) {
	if f.Pixel.Mode == PixelModeAnim {
		return f, nil, Layer{}, ErrPixelStatic
	}
	if err := checkPixelLayerCapacity(f.Pixel.Width, f.Pixel.Height, len(f.Layers)+1); err != nil {
		return f, nil, Layer{}, err
	}
	if blend == "" {
		blend = "normal"
	}
	if !validBlend(blend) {
		return f, nil, Layer{}, fmt.Errorf("未知混合模式 %q", blend)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "新图层"
	}
	l := Layer{ID: "L" + rand.Text(), Name: name, Blend: blend, Visible: true, Opacity: 1}
	f.Layers = append(f.Layers, l)
	if index < 1 {
		index = len(f.Layers)
	}
	positionPixelLayer(f.Layers, len(f.Layers)-1, index)
	i := pixelLayerIndex(f, l.ID)
	resources, err := updatePixelLayer(&f, i, img)
	f.ActiveLayer = l.ID
	return f, resources, f.Layers[i], err
}

func positionPixelLayer(layers []Layer, current, target int) {
	target = min(max(target, 1), len(layers)) - 1
	layer := layers[current]
	if current < target {
		copy(layers[current:target], layers[current+1:target+1])
	} else {
		copy(layers[target+1:current+1], layers[target:current])
	}
	layers[target] = layer
	for i := range layers {
		layers[i].Index = i + 1
	}
}

func pixelLayerIndex(f File, id string) int {
	if id == "" {
		id = f.ActiveLayer
	}
	for i := range f.Layers {
		if f.Layers[i].ID == id {
			return i
		}
	}
	return -1
}

func (p *Project) PixelLayerPNG(id string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := pixelLayerIndex(p.file, id)
	if p.file.Pixel == nil || i < 0 {
		return nil, ErrNotFound
	}
	return p.readPixelLayerLocked(i)
}

func (p *Project) readPixelLayerLocked(i int) ([]byte, error) {
	return readPixelFile(filepath.Join(p.dir, "layers", p.file.Layers[i].File), pixel.MaxLayerPNGBytes)
}

func readPixelFile(name string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if int64(len(raw)) > maxBytes {
		return nil, errors.New("图片文件超过大小上限")
	}
	return raw, err
}

func (p *Project) pixelLayerLocked(i int) (*image.NRGBA, error) {
	raw, err := p.readPixelLayerLocked(i)
	if err != nil {
		return nil, err
	}
	return pixel.DecodeLayer(raw, p.file.Pixel.Width, p.file.Pixel.Height)
}

func (p *Project) PixelLayer(id string) (*image.NRGBA, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := pixelLayerIndex(p.file, id)
	if p.file.Pixel == nil || i < 0 {
		return nil, ErrNotFound
	}
	return p.pixelLayerLocked(i)
}

func (p *Project) PixelComposite() (*image.NRGBA, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.file.Pixel == nil {
		return nil, ErrPixelProject
	}
	if p.pixelComposite == nil || p.pixelCompositeRevision != p.file.Pixel.Revision {
		img, err := p.composePixelLocked()
		if err != nil {
			return nil, err
		}
		p.pixelComposite, p.pixelCompositeRevision = img, p.file.Pixel.Revision
	}
	return copyPixelImage(p.pixelComposite), nil
}

func (p *Project) composePixelLocked() (*image.NRGBA, error) {
	if p.file.Pixel.Mode == PixelModeAnim {
		return p.pixelFrameImageLocked(pixelFrameIndex(p.file.Pixel.Anim, ""))
	}
	layers := make([]pixel.Layer, 0, len(p.file.Layers))
	for i, l := range p.file.Layers {
		if !l.Visible || l.Opacity == 0 {
			continue
		}
		img, err := p.pixelLayerLocked(i)
		if err != nil {
			return nil, err
		}
		layers = append(layers, pixel.Layer{Image: img, Visible: true, Opacity: l.Opacity, Blend: l.Blend})
	}
	return pixel.Composite(p.file.Pixel.Width, p.file.Pixel.Height, layers)
}

func copyPixelImage(img *image.NRGBA) *image.NRGBA {
	clone := image.NewNRGBA(img.Rect)
	for y := img.Rect.Min.Y; y < img.Rect.Max.Y; y++ {
		src := img.PixOffset(img.Rect.Min.X, y)
		dst := clone.PixOffset(clone.Rect.Min.X, y)
		copy(clone.Pix[dst:dst+clone.Stride], img.Pix[src:src+clone.Stride])
	}
	return clone
}

func (p *Project) PixelAddLayer(name, blend string, index int, img *image.NRGBA) (Layer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.clonePixelFileLocked()
	if f.Pixel == nil {
		return Layer{}, ErrPixelProject
	}
	if img == nil {
		var err error
		img, err = pixel.New(f.Pixel.Width, f.Pixel.Height)
		if err != nil {
			return Layer{}, err
		}
	}
	if img.Rect.Dx() != f.Pixel.Width || img.Rect.Dy() != f.Pixel.Height {
		return Layer{}, errors.New("图层尺寸与像素画布不一致")
	}
	next, resources, layer, err := addPixelLayer(f, name, blend, index, img)
	if err != nil {
		return Layer{}, err
	}
	return layer, p.commitPixelLocked(next, resources)
}

func (p *Project) PixelDuplicate(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.clonePixelFileLocked()
	i := pixelLayerIndex(f, id)
	if f.Pixel == nil || i < 0 {
		return ErrNotFound
	}
	old := f.Layers[i]
	img, err := p.pixelLayerLocked(i)
	if err != nil {
		return err
	}
	next, resources, l, err := addPixelLayer(f, old.Name+" 副本", old.Blend, old.Index+1, img)
	if err != nil {
		return err
	}
	copyLayer := &next.Layers[pixelLayerIndex(next, l.ID)]
	copyLayer.Opacity, copyLayer.Visible = old.Opacity, old.Visible
	return p.commitPixelLocked(next, resources)
}

func (p *Project) PixelProps(id string, props LayerProps, ai bool) (Layer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.clonePixelFileLocked()
	i := pixelLayerIndex(f, id)
	if f.Pixel == nil || i < 0 {
		return Layer{}, ErrNotFound
	}
	l := &f.Layers[i]
	if ai && (l.Locked || props.Locked != nil) {
		return Layer{}, ErrLocked
	}
	if props.Name != nil && strings.TrimSpace(*props.Name) != "" {
		l.Name = strings.TrimSpace(*props.Name)
	}
	if props.Visible != nil {
		l.Visible = *props.Visible
	}
	if props.Locked != nil {
		l.Locked = *props.Locked
	}
	if props.Opacity != nil {
		if math.IsNaN(*props.Opacity) {
			return Layer{}, errors.New("图层透明度无效")
		}
		l.Opacity = min(max(*props.Opacity, 0), 1)
	}
	if props.Blend != nil {
		b := strings.ToLower(strings.TrimSpace(*props.Blend))
		if !validBlend(b) {
			return Layer{}, fmt.Errorf("未知混合模式 %q", b)
		}
		l.Blend = b
	}
	if props.Index != nil {
		layerID := l.ID
		positionPixelLayer(f.Layers, i, *props.Index)
		l = &f.Layers[pixelLayerIndex(f, layerID)]
	}
	return *l, p.commitPixelLocked(f, nil)
}

func (p *Project) PixelDelete(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.clonePixelFileLocked()
	i := pixelLayerIndex(f, id)
	if f.Pixel == nil || i < 0 {
		return ErrNotFound
	}
	if f.Layers[i].Locked {
		return ErrLocked
	}
	id = f.Layers[i].ID
	if f.Pixel.DraftLayerID == id {
		f.Pixel.DraftLayerID = ""
	}
	f.Layers = slices.Delete(f.Layers, i, i+1)
	if len(f.Layers) == 0 {
		img, err := pixel.New(f.Pixel.Width, f.Pixel.Height)
		if err != nil {
			return err
		}
		next, resources, _, err := addPixelLayer(f, "背景", "", 0, img)
		if err != nil {
			return err
		}
		return p.commitPixelLocked(next, resources)
	}
	for i := range f.Layers {
		f.Layers[i].Index = i + 1
	}
	if f.ActiveLayer == id {
		f.ActiveLayer = f.Layers[min(i, len(f.Layers)-1)].ID
	}
	return p.commitPixelLocked(f, nil)
}

func (p *Project) PixelActivate(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.clonePixelFileLocked()
	if f.Pixel == nil || id == "" || pixelLayerIndex(f, id) < 0 {
		return ErrNotFound
	}
	if id == f.ActiveLayer {
		return nil
	}
	f.ActiveLayer = id
	return p.commitPixelLocked(f, nil)
}

func (p *Project) PixelReorder(ids []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.clonePixelFileLocked()
	if f.Pixel == nil {
		return ErrPixelProject
	}
	if f.Pixel.Mode == PixelModeAnim {
		return ErrPixelStatic
	}
	if len(ids) != len(f.Layers) {
		return errors.New("排序必须包含所有图层")
	}
	seen := make(map[string]bool, len(ids))
	ordered := make([]Layer, 0, len(ids))
	for i, id := range ids {
		index := pixelLayerIndex(f, id)
		if id == "" || index < 0 || seen[id] {
			return errors.New("排序包含重复或未知图层")
		}
		seen[id] = true
		l := f.Layers[index]
		l.Index = i + 1
		ordered = append(ordered, l)
	}
	f.Layers = ordered
	return p.commitPixelLocked(f, nil)
}

func (p *Project) PixelDraw(id string, ops []pixel.DrawOp, sel *pixel.Selection) (Layer, pixel.DrawSummary, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.clonePixelFileLocked()
	i := pixelLayerIndex(f, id)
	if f.Pixel == nil || i < 0 {
		return Layer{}, pixel.DrawSummary{}, ErrNotFound
	}
	l := f.Layers[i]
	if l.Locked {
		return l, pixel.DrawSummary{}, ErrLocked
	}
	img, err := p.pixelLayerLocked(i)
	if err != nil {
		return l, pixel.DrawSummary{}, err
	}
	summary, err := pixel.Draw(img, ops, sel)
	if err != nil || summary.ChangedPixels == 0 {
		return l, summary, err
	}
	resources, err := updatePixelLayer(&f, i, img)
	if err != nil {
		return l, summary, err
	}
	return f.Layers[i], summary, p.commitPixelLocked(f, resources)
}

func (p *Project) PixelApplyHuman(id string, img *image.NRGBA, region *pixel.Rect, rgba []byte) (HumanEdit, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.clonePixelFileLocked()
	i := pixelLayerIndex(f, id)
	if f.Pixel == nil || i < 0 {
		return HumanEdit{}, ErrNotFound
	}
	layer := f.Layers[i]
	if layer.Locked {
		return HumanEdit{}, ErrLocked
	}
	before, err := p.pixelLayerLocked(i)
	if err != nil {
		return HumanEdit{}, err
	}
	after, err := applyHumanRaster(before, img, region, rgba)
	if err != nil {
		return HumanEdit{}, err
	}
	edit := newHumanEdit(layer.ID, layer.Name, before, after)
	if edit.ChangedPixels == 0 {
		return edit, nil
	}
	f.Pixel.recordHumanEdit(edit)
	resources, err := updatePixelLayer(&f, i, after)
	if err != nil {
		return HumanEdit{}, err
	}
	return edit, p.commitPixelLocked(f, resources)
}

func applyHumanRaster(before, img *image.NRGBA, region *pixel.Rect, rgba []byte) (*image.NRGBA, error) {
	after := img
	if img == nil {
		if region == nil || region.W < 1 || region.H < 1 || region.X < 0 || region.Y < 0 ||
			region.W > before.Rect.Dx()-region.X || region.H > before.Rect.Dy()-region.Y ||
			len(rgba) != region.W*region.H*4 {
			return nil, errors.New("像素更新区域或 RGBA 长度无效")
		}
		after = copyPixelImage(before)
		row := region.W * 4
		for y := 0; y < region.H; y++ {
			start := after.PixOffset(region.X, region.Y+y)
			copy(after.Pix[start:start+row], rgba[y*row:(y+1)*row])
		}
	}
	if after.Bounds() != before.Bounds() {
		return nil, errors.New("图层或帧的尺寸与像素画布不一致")
	}
	return after, nil
}

func newHumanEdit(layerID, layerName string, before, after *image.NRGBA) HumanEdit {
	return HumanEdit{
		LayerID: layerID, LayerName: layerName,
		DrawSummary: pixel.Changes(before, after), Timestamp: time.Now().UnixNano(),
	}
}

func (m *PixelMeta) recordHumanEdit(edit HumanEdit) {
	m.HumanEdits = append(m.HumanEdits, edit)
	if len(m.HumanEdits) > 10 {
		m.HumanEdits = m.HumanEdits[len(m.HumanEdits)-10:]
	}
}

func (p *Project) PixelResize(w, h int, mode string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.clonePixelFileLocked()
	if f.Pixel == nil {
		return ErrPixelProject
	}
	if err := validatePixelModeSize(f.Pixel.Mode, w, h); err != nil {
		return err
	}
	if err := checkPixelLayerCapacity(w, h, len(f.Layers)); err != nil {
		return err
	}
	if mode != "crop" && mode != "resample" {
		return errors.New("画布调整方式无效")
	}
	if f.Pixel.Width == w && f.Pixel.Height == h {
		return nil
	}
	resources := make(map[string][]byte, len(f.Layers))
	if anim := f.Pixel.Anim; anim != nil {
		for i := range anim.Frames {
			img, err := p.pixelFrameImageLocked(i)
			if err != nil {
				return err
			}
			resized, err := pixel.Resize(img, w, h, mode)
			if err != nil {
				return err
			}
			data, err := setFrameRaster(&anim.Frames[i], resized)
			if err != nil {
				return err
			}
			for rel, raw := range data {
				resources[rel] = raw
			}
		}
	}
	for i := range f.Layers {
		img, err := p.pixelLayerLocked(i)
		if err != nil {
			return err
		}
		resized, err := pixel.Resize(img, w, h, mode)
		if err != nil {
			return err
		}
		data, err := updatePixelLayer(&f, i, resized)
		if err != nil {
			return err
		}
		for rel, bytes := range data {
			resources[rel] = bytes
		}
	}
	f.Canvas = pixelCanvas(w, h)
	f.Pixel.Width, f.Pixel.Height = w, h
	f.Pixel.HumanEdits = nil
	if err := p.commitPixelLocked(f, resources); err != nil {
		return err
	}
	p.pixelUndo, p.pixelRedo = nil, nil
	return nil
}

func validatePixelColor(value string) (string, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return value, nil
	}
	if (len(value) != 7 && len(value) != 9) || value[0] != '#' {
		return "", errors.New("颜色必须为 #RRGGBB 或 #RRGGBBAA")
	}
	for _, ch := range value[1:] {
		if !strings.ContainsRune("0123456789ABCDEF", ch) {
			return "", errors.New("颜色包含无效的十六进制字符")
		}
	}
	return value, nil
}

func (p *Project) PixelSetPalette(colors []string, appendColors bool, primary, secondary *string) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.clonePixelFileLocked()
	if f.Pixel == nil {
		return nil, ErrPixelProject
	}
	if colors != nil {
		palette := []string{}
		if appendColors {
			palette = append(palette, f.Pixel.Palette...)
		}
		for _, raw := range colors {
			color, err := validatePixelColor(raw)
			if err != nil || color == "" {
				return nil, errors.New("调色板颜色必须为 #RRGGBB 或 #RRGGBBAA")
			}
			if !slices.Contains(palette, color) {
				palette = append(palette, color)
				if len(palette) > 64 {
					return nil, errors.New("调色板最多支持 64 种颜色")
				}
			}
		}
		f.Pixel.Palette = palette
	}
	for _, item := range []struct{ dest, src *string }{
		{&f.Pixel.PrimaryColor, primary}, {&f.Pixel.SecondaryColor, secondary},
	} {
		if item.src != nil {
			color, err := validatePixelColor(*item.src)
			if err != nil {
				return nil, err
			}
			*item.dest = color
		}
	}
	return slices.Clone(f.Pixel.Palette), p.commitPixelLocked(f, nil)
}

func (p *Project) PixelAddAsset(role, name string, data []byte) (PixelAsset, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.clonePixelFileLocked()
	if f.Pixel == nil {
		return PixelAsset{}, ErrPixelProject
	}
	if len(data) == 0 || len(data) > maxAssetBytes {
		return PixelAsset{}, errors.New("单张图片须小于等于 12 MiB")
	}
	total := len(data)
	for _, asset := range pixelAssets(f.Pixel) {
		if role != "main" || asset.ID != "main" {
			total += asset.Size
		}
	}
	if total > maxAssetsBytes {
		return PixelAsset{}, errors.New("工程图片素材合计不能超过 48 MiB")
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return PixelAsset{}, errors.New("无法识别图片，请使用 PNG、JPEG 或 GIF")
	}
	if _, err := pixel.Decode(data); err != nil {
		return PixelAsset{}, err
	}
	asset := PixelAsset{
		ID: "main", File: "asset-" + rand.Text() + "." + format,
		Name: strings.TrimSpace(name), MIME: "image/" + format, Size: len(data),
	}
	switch role {
	case "main":
		f.Pixel.MainImage = &asset
		f.Pixel.DraftLayerID = ""
	case "ref":
		if len(f.Pixel.RefImages) >= maxReferences {
			return PixelAsset{}, fmt.Errorf("最多可上传 %d 张参考图", maxReferences)
		}
		f.Pixel.NextReference++
		asset.ID = fmt.Sprintf("ref_%d", f.Pixel.NextReference)
		f.Pixel.RefImages = append(f.Pixel.RefImages, asset)
	default:
		return PixelAsset{}, errors.New("图片角色必须为主图或参考图")
	}
	return asset, p.commitPixelLocked(f, map[string][]byte{filepath.Join("assets", asset.File): data})
}

func (p *Project) PixelRemoveAsset(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.clonePixelFileLocked()
	if f.Pixel == nil {
		return ErrPixelProject
	}
	if id == "main" && f.Pixel.MainImage != nil {
		f.Pixel.MainImage = nil
		f.Pixel.DraftLayerID = ""
		return p.commitPixelLocked(f, nil)
	}
	for i, asset := range f.Pixel.RefImages {
		if asset.ID == id {
			f.Pixel.RefImages = slices.Delete(f.Pixel.RefImages, i, i+1)
			return p.commitPixelLocked(f, nil)
		}
	}
	return ErrNotFound
}

func (p *Project) PixelAsset(id string) (PixelAsset, []byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pixelAssetLocked(id)
}

func (p *Project) pixelAssetLocked(id string) (PixelAsset, []byte, error) {
	if p.file.Pixel == nil {
		return PixelAsset{}, nil, ErrPixelProject
	}
	for _, a := range pixelAssets(p.file.Pixel) {
		if a.ID == id {
			raw, err := readPixelFile(filepath.Join(p.dir, "assets", a.File), maxAssetBytes)
			return a, raw, err
		}
	}
	return PixelAsset{}, nil, ErrNotFound
}

func (p *Project) PixelQuantize(opts pixel.QuantizeOpts, name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.clonePixelFileLocked()
	if f.Pixel == nil {
		return ErrPixelProject
	}
	if f.Pixel.MainImage == nil {
		return errors.New("请先上传主图")
	}
	_, raw, err := p.pixelAssetLocked("main")
	if err != nil {
		return err
	}
	src, err := pixel.Decode(raw)
	if err != nil {
		return err
	}
	img, palette, err := pixel.Quantize(src, f.Pixel.Width, f.Pixel.Height, opts)
	if err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		name = "采样底稿"
	}
	f.Pixel.Palette = palette
	f.Pixel.DraftMode = "quantize"
	f.Pixel.Fit = opts.Fit
	if f.Pixel.Anim != nil {
		return p.quantizePixelFrameLocked(f, name, img)
	}
	oldIndex := pixelLayerIndex(f, f.Pixel.DraftLayerID)
	position := 1
	if f.Pixel.DraftLayerID != "" && oldIndex >= 0 {
		position = oldIndex + 2
		if !f.Layers[oldIndex].Locked {
			f.Layers[oldIndex].Visible = false
		}
	}
	next, resources, draft, err := addPixelLayer(f, name, "", position, img)
	if err != nil {
		return err
	}
	next.Pixel.DraftLayerID = draft.ID
	return p.commitPixelLocked(next, resources)
}

func (p *Project) ResetPixel(modes ...string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.clonePixelFileLocked()
	if f.Pixel == nil {
		return ErrPixelProject
	}
	if len(modes) > 0 && modes[0] != "" {
		mode, err := selectedPixelMode(modes)
		if err != nil {
			return err
		}
		if err := validatePixelModeSize(mode, f.Pixel.Width, f.Pixel.Height); err != nil {
			return err
		}
		f.Pixel.Mode = mode
	}
	f.Layers = nil
	f.ActiveLayer = ""
	f.History = nil
	f.ContextCleared = time.Now().UnixNano()
	f.Pixel.HumanEdits = nil
	f.Pixel.MainImage = nil
	f.Pixel.RefImages = nil
	f.Pixel.DraftMode = "vision"
	f.Pixel.DraftLayerID = ""
	f.Pixel.Fit = ""
	f.Pixel.ModeLocked = false
	f.Pixel.Anim = nil
	img, err := pixel.New(f.Pixel.Width, f.Pixel.Height)
	if err != nil {
		return err
	}
	next, resources, err := emptyPixelCanvas(f, img)
	if err != nil {
		return err
	}
	if err := p.commitPixelLocked(next, resources); err != nil {
		return err
	}
	p.pixelUndo, p.pixelRedo = nil, nil
	p.pixelPending = nil
	return nil
}

type pixelCheckpoint struct {
	layers       []Layer
	data         [][]byte
	active       string
	palette      []string
	draftLayerID string
	anim         *PixelAnimMeta
}

func (p *Project) capturePixelLocked() (*pixelCheckpoint, error) {
	checkpoint := &pixelCheckpoint{
		layers: slices.Clone(p.file.Layers), active: p.file.ActiveLayer,
		palette: slices.Clone(p.file.Pixel.Palette), draftLayerID: p.file.Pixel.DraftLayerID,
		anim: clonePixelAnim(p.file.Pixel.Anim),
	}
	if checkpoint.anim != nil {
		for i := range checkpoint.anim.Frames {
			raw, err := p.readPixelFrameLocked(i)
			if err != nil {
				return nil, err
			}
			checkpoint.data = append(checkpoint.data, raw)
		}
		return checkpoint, nil
	}
	for i := range checkpoint.layers {
		raw, err := p.readPixelLayerLocked(i)
		if err != nil {
			return nil, err
		}
		checkpoint.data = append(checkpoint.data, raw)
	}
	return checkpoint, nil
}

func (p *Project) BeginPixelTurn(mode string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.clonePixelFileLocked()
	if f.Pixel == nil {
		return ErrPixelProject
	}
	checkpoint, err := p.capturePixelLocked()
	if err != nil {
		return err
	}
	if f.Pixel.DraftMode != mode || !f.Pixel.ModeLocked {
		f.Pixel.DraftMode = mode
		f.Pixel.ModeLocked = true
		if err := p.commitPixelLocked(f, nil); err != nil {
			return err
		}
	}
	p.pixelPending = checkpoint
	return nil
}

func (p *Project) EndPixelTurn(history []llm.Message, success bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.clonePixelFileLocked()
	if f.Pixel == nil || p.pixelPending == nil {
		return ErrPixelProject
	}
	pending := p.pixelPending
	changed := !slices.Equal(pending.layers, f.Layers) || !slices.Equal(pending.palette, f.Pixel.Palette) ||
		pending.active != f.ActiveLayer || !samePixelAnimation(pending.anim, f.Pixel.Anim)
	f.History = historyRecords(history)
	if success {
		f.Pixel.HumanEdits = nil
	}
	if err := p.commitPixelLocked(f, nil); err != nil {
		return err
	}
	p.pixelPending = nil
	if changed {
		p.pixelUndo, p.pixelRedo = pending, nil
	}
	return nil
}

func (p *Project) ClearPixelCheckpoint() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pixelUndo, p.pixelRedo = nil, nil
}

func (p *Project) PixelCheckpoint(action string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.clonePixelFileLocked()
	if f.Pixel == nil {
		return ErrPixelProject
	}
	var target *pixelCheckpoint
	switch action {
	case "undo":
		target = p.pixelUndo
	case "redo":
		target = p.pixelRedo
	default:
		return errors.New("未知的撤销操作")
	}
	if target == nil {
		return errors.New("没有可还原的 AI 操作")
	}
	current, err := p.capturePixelLocked()
	if err != nil {
		return err
	}
	f.Layers = slices.Clone(target.layers)
	f.ActiveLayer = target.active
	f.Pixel.Palette = slices.Clone(target.palette)
	f.Pixel.DraftLayerID = target.draftLayerID
	anim := clonePixelAnim(target.anim)
	if anim != nil && f.Pixel.Anim != nil {
		anim.FPS, anim.Loop = f.Pixel.Anim.FPS, f.Pixel.Anim.Loop
	}
	f.Pixel.Anim = anim
	// The next model turn reads the restored canvas even if its previous tool calls
	// are still visible in the conversation history.
	f.ContextCleared = time.Now().UnixNano()
	resources := make(map[string][]byte, len(f.Layers))
	if anim := f.Pixel.Anim; anim != nil {
		for i := range anim.Frames {
			for rel, raw := range storeFramePNG(&anim.Frames[i], target.data[i]) {
				resources[rel] = raw
			}
		}
	}
	for i := range f.Layers {
		l := &f.Layers[i]
		l.File = pixelLayerFile(l.ID)
		l.Updated = time.Now().UnixNano()
		resources[filepath.Join("layers", l.File)] = target.data[i]
	}
	if err := p.commitPixelLocked(f, resources); err != nil {
		return err
	}
	if action == "undo" {
		p.pixelUndo, p.pixelRedo = nil, current
	} else {
		p.pixelUndo, p.pixelRedo = current, nil
	}
	return nil
}

func (p *Project) savePixelAsLocked(target string) error {
	src, err := filepath.Abs(p.dir)
	if err != nil {
		return err
	}
	if sameProjectDirectory(src, target) {
		next := p.clonePixelFileLocked()
		next.Kind = KindUser
		if err := p.commitPixelLocked(next, nil); err != nil {
			return err
		}
		p.kind = KindUser
		return nil
	}
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		return errors.New("另存为目标须为文件夹")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	entries, err := os.ReadDir(target)
	if err == nil && len(entries) != 0 {
		return errors.New("请选择空文件夹作为另存目标")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(target), ".pixel-project-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	for rel := range pixelResources(p.file) {
		limit := int64(pixel.MaxLayerPNGBytes)
		if filepath.Dir(rel) == "assets" {
			limit = maxAssetBytes
		}
		raw, err := readPixelFile(filepath.Join(p.dir, rel), limit)
		if err != nil {
			return err
		}
		dest := filepath.Join(staging, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := writePixelFile(dest, raw); err != nil {
			return err
		}
	}
	next := p.clonePixelFileLocked()
	next.Kind = KindUser
	next.Pixel.Revision++
	next.Modified = time.Now().UnixNano()
	if err := savePixelManifest(staging, next); err != nil {
		return err
	}
	if entries != nil {
		// Remove succeeds only for the empty directory validated above.
		if err := os.Remove(target); err != nil {
			return err
		}
	}
	if err := os.Rename(staging, target); err != nil {
		if entries != nil {
			_ = os.Mkdir(target, 0o755)
		}
		return err
	}
	p.file, p.dir, p.kind = next, target, KindUser
	p.pixelComposite = nil
	return nil
}

func sameProjectDirectory(a, b string) bool {
	return a == b || runtime.GOOS == "windows" && strings.EqualFold(a, b)
}
