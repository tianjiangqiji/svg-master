// Package pixel implements the raster operations shared by the Pixel Studio server and agent.
package pixel

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"strconv"
	"strings"
)

const (
	MinSize         = 8
	MaxPresetSize   = 1024
	MaxSize         = 2048
	MaxAnimSize     = 512
	DefaultW        = 32
	DefaultH        = 32
	MaxReadSpan     = 32
	MaxBlitSpan     = 128
	MaxExportSide   = 8192
	MaxExportPixels = 64 << 20
	// PNG row filters and DEFLATE framing can slightly exceed the raw RGBA size.
	MaxLayerPNGBytes = (16 << 20) + (64 << 10)
	maxCrispSVGRects = 250000

	MaxAssetBytes  = 16 << 20
	maxAssetSide   = 8192
	maxAssetPixels = 24 << 20
)

type Rect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

func (r Rect) imageRect() image.Rectangle {
	return image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H)
}

func fromRect(r image.Rectangle) Rect {
	return Rect{X: r.Min.X, Y: r.Min.Y, W: r.Dx(), H: r.Dy()}
}

// Selection contains sorted, non-overlapping runs in row-major order relative to Rect.
// A nil runs list represents a filled rectangle, whereas an empty list selects nothing.
type Selection struct {
	Rect
	Runs [][]int `json:"runs"`
}

func ValidateSelection(sel *Selection, w, h int) error {
	if sel == nil {
		return nil
	}
	if sel.W < 1 || sel.H < 1 || sel.W > w || sel.H > h || sel.X < 0 || sel.Y < 0 || sel.X > w-sel.W || sel.Y > h-sel.H {
		return fmt.Errorf("选区超出 %d×%d 画布", w, h)
	}
	end, area := 0, sel.W*sel.H
	for _, run := range sel.Runs {
		if len(run) != 2 || run[0] < end || run[0] >= area || run[1] <= 0 || run[1] > area-run[0] {
			return fmt.Errorf("选区的像素掩码行程无效")
		}
		end = run[0] + run[1]
	}
	return nil
}

func ValidateSize(w, h int) error {
	if w < MinSize || w > MaxSize || h < MinSize || h > MaxSize {
		return fmt.Errorf("像素画布的宽和高都必须在 %d 到 %d 之间", MinSize, MaxSize)
	}
	return nil
}

func New(w, h int) (*image.NRGBA, error) {
	if err := ValidateSize(w, h); err != nil {
		return nil, err
	}
	return image.NewNRGBA(image.Rect(0, 0, w, h)), nil
}

// Decode normalizes a bounded source image to straight-alpha NRGBA. A GIF uses its first image.
func Decode(data []byte) (*image.NRGBA, error) {
	if len(data) == 0 || len(data) > MaxAssetBytes {
		return nil, fmt.Errorf("参考图片必须小于等于 %d MiB", MaxAssetBytes>>20)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("读取图片失败，请使用 PNG、JPEG 或 GIF: %w", err)
	}
	if format != "png" && format != "jpeg" && format != "gif" {
		return nil, fmt.Errorf("不支持 %s 图片，请使用 PNG、JPEG 或 GIF", format)
	}
	if cfg.Width <= 0 || cfg.Width > maxAssetSide || cfg.Height <= 0 || cfg.Height > maxAssetSide || cfg.Width*cfg.Height > maxAssetPixels {
		return nil, fmt.Errorf("参考图片过大，最长边不得超过 %d 像素，总像素数不得超过 %d", maxAssetSide, maxAssetPixels)
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("解码图片失败: %w", err)
	}
	if format == "gif" {
		out := image.NewNRGBA(image.Rect(0, 0, cfg.Width, cfg.Height))
		draw.Draw(out, src.Bounds(), src, src.Bounds().Min, draw.Src)
		return out, nil
	}
	return normalize(src), nil
}

// DecodeLayer validates the PNG format and its declared dimensions before decoding its pixels.
func DecodeLayer(data []byte, w, h int) (*image.NRGBA, error) {
	if err := ValidateSize(w, h); err != nil {
		return nil, err
	}
	if len(data) > MaxLayerPNGBytes {
		return nil, fmt.Errorf("像素图层 PNG 超过 %d 字节的大小上限", MaxLayerPNGBytes)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("读取图层 PNG 失败: %w", err)
	}
	if cfg.Width != w || cfg.Height != h {
		return nil, fmt.Errorf("图层 PNG 必须为 %d×%d，实际为 %d×%d", w, h, cfg.Width, cfg.Height)
	}
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("解码图层 PNG 失败: %w", err)
	}
	return normalize(src), nil
}

func normalize(src image.Image) *image.NRGBA {
	b := src.Bounds()
	if nrgba, ok := src.(*image.NRGBA); ok && b.Min == (image.Point{}) {
		return nrgba
	}
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), src, b.Min, draw.Src)
	return out
}

func Resize(src *image.NRGBA, w, h int, mode string) (*image.NRGBA, error) {
	if src == nil || src.Bounds().Empty() {
		return nil, fmt.Errorf("缺少有效的源图层")
	}
	out, err := New(w, h)
	if err != nil {
		return nil, err
	}
	switch mode {
	case "", "crop":
		// Top-left pixels keep their coordinates when cropping or extending a layer.
		draw.Draw(out, out.Bounds(), src, src.Bounds().Min, draw.Src)
	case "resample":
		sampleInto(out, src, "nearest", "stretch")
	default:
		return nil, fmt.Errorf("未知画布调整方式 %q", mode)
	}
	return out, nil
}

// Shift returns a copy translated by whole pixels, with transparent edges unless wrap is enabled.
func Shift(img *image.NRGBA, dx, dy int, wrap bool) *image.NRGBA {
	if img == nil {
		return nil
	}
	bounds := img.Bounds()
	out := image.NewNRGBA(bounds)
	w, h := bounds.Dx(), bounds.Dy()
	if bounds.Empty() {
		return out
	}
	if wrap {
		dx, dy = (dx%w+w)%w, (dy%h+h)%h
		for y := 0; y < h; y++ {
			src := img.PixOffset(bounds.Min.X, bounds.Min.Y+y)
			dst := out.PixOffset(bounds.Min.X, bounds.Min.Y+(y+dy)%h)
			split := (w - dx) * 4
			copy(out.Pix[dst+dx*4:dst+w*4], img.Pix[src:src+split])
			copy(out.Pix[dst:dst+dx*4], img.Pix[src+split:src+w*4])
		}
		return out
	}
	if dx <= -w || dx >= w || dy <= -h || dy >= h {
		return out
	}
	for y := max(0, -dy); y < min(h, h-dy); y++ {
		x := max(0, -dx)
		src := img.PixOffset(bounds.Min.X+x, bounds.Min.Y+y)
		dst := out.PixOffset(bounds.Min.X+x+dx, bounds.Min.Y+y+dy)
		length := (min(w, w-dx) - x) * 4
		copy(out.Pix[dst:dst+length], img.Pix[src:src+length])
	}
	return out
}

func ParseColor(s string) (color.NRGBA, error) {
	if (len(s) != 7 && len(s) != 9) || s[0] != '#' {
		return color.NRGBA{}, fmt.Errorf("颜色 %q 无效，请使用 #RRGGBB 或 #RRGGBBAA", s)
	}
	n, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return color.NRGBA{}, fmt.Errorf("颜色 %q 无效，请使用 #RRGGBB 或 #RRGGBBAA", s)
	}
	if len(s) == 7 {
		n = n<<8 | 0xff
	}
	return color.NRGBA{R: uint8(n >> 24), G: uint8(n >> 16), B: uint8(n >> 8), A: uint8(n)}, nil
}

func FormatColor(c color.NRGBA) string {
	const hex = "0123456789ABCDEF"
	out := [9]byte{'#'}
	for i, component := range [4]uint8{c.R, c.G, c.B, c.A} {
		out[1+2*i] = hex[component>>4]
		out[2+2*i] = hex[component&15]
	}
	if c.A == 255 {
		return string(out[:7])
	}
	return string(out[:])
}

type DrawSummary struct {
	Region        Rect     `json:"region"`
	ChangedPixels int      `json:"changedPixels"`
	NewColors     []string `json:"newColors,omitempty"`
	OutOfBounds   int      `json:"outOfBounds,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
}

func Changes(before, after *image.NRGBA) DrawSummary {
	var s DrawSummary
	if before == nil || after == nil {
		return s
	}
	bounds := before.Bounds().Union(after.Bounds())
	var region image.Rectangle
	var modifiedColors []color.NRGBA
	candidates := make(map[color.NRGBA]bool)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			old, now := before.NRGBAAt(x, y), after.NRGBAAt(x, y)
			if old == now {
				continue
			}
			s.ChangedPixels++
			region = region.Union(image.Rect(x, y, x+1, y+1))
			if now.A != 0 && !candidates[now] {
				candidates[now] = true
				modifiedColors = append(modifiedColors, now)
			}
		}
	}
	if s.ChangedPixels == 0 {
		return s
	}
	s.Region = fromRect(region)
	if len(candidates) > 0 {
		b := before.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				delete(candidates, before.NRGBAAt(x, y))
			}
		}
		for _, c := range modifiedColors {
			if candidates[c] {
				s.NewColors = append(s.NewColors, FormatColor(c))
				if len(s.NewColors) == 16 {
					break
				}
			}
		}
	}
	return s
}

// Coverage returns the count and bounding rectangle of pixels with nonzero alpha.
func Coverage(img *image.NRGBA) (int, Rect) {
	if img == nil {
		return 0, Rect{}
	}
	b := img.Bounds()
	var region image.Rectangle
	count := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if img.Pix[img.PixOffset(x, y)+3] != 0 {
				count++
				region = region.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return count, fromRect(region)
}

type scaledImage struct {
	img   *image.NRGBA
	scale int
}

func (i scaledImage) Bounds() image.Rectangle {
	b := i.img.Bounds()
	return image.Rect(0, 0, b.Dx()*i.scale, b.Dy()*i.scale)
}

func (i scaledImage) ColorModel() color.Model { return color.NRGBAModel }

func (i scaledImage) Opaque() bool { return i.img.Opaque() }

func (i scaledImage) At(x, y int) color.Color {
	p := i.img.Bounds().Min
	return i.img.NRGBAAt(p.X+x/i.scale, p.Y+y/i.scale)
}

// ExportPNG renders at an integer nearest-neighbor scale without allocating a second full bitmap.
func ExportPNG(img *image.NRGBA, scale int) ([]byte, error) {
	if img == nil || img.Bounds().Empty() {
		return nil, fmt.Errorf("缺少可导出的像素画布")
	}
	var src image.Image
	switch scale {
	case 1:
		src = img
	case 2, 4, 8, 16:
		src = scaledImage{img: img, scale: scale}
	default:
		return nil, fmt.Errorf("PNG 倍率必须为 1、2、4、8 或 16")
	}
	b := img.Bounds()
	if b.Dx() > MaxExportSide/scale || b.Dy() > MaxExportSide/scale || b.Dx()*scale*b.Dy()*scale > MaxExportPixels {
		return nil, fmt.Errorf("当前画布的 %d 倍 PNG 导出超过单边 %d 像素或总计 %d 像素的上限，请降低导出倍率", scale, MaxExportSide, MaxExportPixels)
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, src); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ExportCrispSVG stores equal colors in horizontal runs, including straight alpha.
func ExportCrispSVG(img *image.NRGBA) (string, error) {
	if img == nil || img.Bounds().Empty() {
		return "", fmt.Errorf("缺少可导出的像素画布")
	}
	var out strings.Builder
	rects := 0
	b := img.Bounds()
	fmt.Fprintf(&out, "<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"%d\" height=\"%d\" viewBox=\"0 0 %d %d\" shape-rendering=\"crispEdges\">\n", b.Dx(), b.Dy(), b.Dx(), b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; {
			c := img.NRGBAAt(x, y)
			end := x + 1
			if c.A == 0 {
				x = end
				continue
			}
			for end < b.Max.X && img.NRGBAAt(end, y) == c {
				end++
			}
			rects++
			if rects > maxCrispSVGRects {
				return "", fmt.Errorf("当前图像色块过密（超过 25 万个矢量矩形），不适合导出 Crisp SVG，请使用 PNG 导出")
			}
			fill := FormatColor(c)
			fmt.Fprintf(&out, "<rect x=\"%d\" y=\"%d\" width=\"%d\" height=\"1\" fill=\"%s\"", x-b.Min.X, y-b.Min.Y, end-x, fill[:7])
			if c.A != 255 {
				fmt.Fprintf(&out, " fill-opacity=\"%.8f\"", float64(c.A)/255)
			}
			out.WriteString("/>\n")
			x = end
		}
	}
	out.WriteString("</svg>\n")
	return out.String(), nil
}
