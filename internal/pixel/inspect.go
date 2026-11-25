package pixel

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"sort"
	"strconv"
	"strings"
)

type InspectOpts struct {
	Mode     string   `json:"mode,omitempty"`
	LayerID  string   `json:"layerId,omitempty"`
	FrameID  string   `json:"frameId,omitempty"`
	FrameIDs []string `json:"frameIds,omitempty"`
	RefID    string   `json:"refId,omitempty"`
	Region   *Rect    `json:"region,omitempty"`
	ShowGrid bool     `json:"showGrid,omitempty"`
	Fit      string   `json:"fit,omitempty"`
}

type InspectStats struct {
	Region               Rect              `json:"region"`
	NonTransparentPixels int               `json:"nonTransparentPixels"`
	Coverage             float64           `json:"coverage"`
	ColorCount           int               `json:"colorCount"`
	DominantColors       []string          `json:"dominantColors,omitempty"`
	Comparison           *Comparison       `json:"comparison,omitempty"`
	Frames               []FrameInspection `json:"frames,omitempty"`
}

type FrameInspection struct {
	Index                int     `json:"index"`
	Region               Rect    `json:"region"`
	NonTransparentPixels int     `json:"nonTransparentPixels"`
	Coverage             float64 `json:"coverage"`
}

type Comparison struct {
	ColorError           float64          `json:"colorError"`
	StructuralSimilarity float64          `json:"structuralSimilarity"`
	Grid                 []ComparisonTile `json:"grid"`
}

type ComparisonTile struct {
	Column               int     `json:"column"`
	Row                  int     `json:"row"`
	Region               Rect    `json:"region"`
	ColorError           float64 `json:"colorError"`
	StructuralSimilarity float64 `json:"structuralSimilarity"`
}

type RegionView struct {
	Rect
	Legend  map[string]string `json:"legend"`
	Rows    []string          `json:"rows,omitempty"`
	Indices [][]int           `json:"indices,omitempty"`
}

func regionInCanvas(r Rect, b image.Rectangle) bool {
	return r.W > 0 && r.H > 0 && r.W <= b.Dx() && r.H <= b.Dy() && r.X >= b.Min.X && r.Y >= b.Min.Y && r.X <= b.Max.X-r.W && r.Y <= b.Max.Y-r.H
}

// ReadRegion uses single characters for small palettes and integer indices when more colors are present.
func ReadRegion(img *image.NRGBA, r Rect) (RegionView, error) {
	if img == nil || !regionInCanvas(r, img.Bounds()) || r.W > MaxReadSpan || r.H > MaxReadSpan {
		return RegionView{}, fmt.Errorf("读取区域须在画布内，且宽高都不超过 %d", MaxReadSpan)
	}
	const symbols = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	colors := []color.NRGBA{{}}
	indices := make(map[color.NRGBA]int)
	rows := make([][]int, r.H)
	for y := range rows {
		rows[y] = make([]int, r.W)
		for x := range rows[y] {
			c := img.NRGBAAt(r.X+x, r.Y+y)
			if c.A == 0 {
				continue
			}
			i, ok := indices[c]
			if !ok {
				i = len(colors)
				colors = append(colors, c)
				indices[c] = i
			}
			rows[y][x] = i
		}
	}
	view := RegionView{Rect: r, Legend: make(map[string]string, len(colors))}
	if len(colors) > len(symbols)+1 {
		view.Indices = rows
		for i, c := range colors {
			view.Legend[strconv.Itoa(i)] = FormatColor(c)
		}
		return view, nil
	}
	for i, c := range colors[1:] {
		view.Legend[symbols[i:i+1]] = FormatColor(c)
	}
	view.Rows = make([]string, r.H)
	for y, row := range rows {
		var b strings.Builder
		b.Grow(len(row))
		for _, i := range row {
			if i == 0 {
				b.WriteByte('.')
			} else {
				b.WriteByte(symbols[i-1])
			}
		}
		view.Rows[y] = b.String()
	}
	return view, nil
}

type inspectPanel struct {
	img    *image.NRGBA
	region image.Rectangle
	label  string
}

func Inspect(composite, reference *image.NRGBA, opts InspectOpts) ([]byte, InspectStats, error) {
	if opts.Mode == "onion" {
		return InspectOnion(composite, reference)
	}
	if composite == nil || composite.Bounds().Empty() {
		return nil, InspectStats{}, fmt.Errorf("没有可检查的画布")
	}
	if err := ValidateFit(opts.Fit); err != nil {
		return nil, InspectStats{}, err
	}
	bound := composite.Bounds()
	region := bound
	if opts.Region != nil {
		if !regionInCanvas(*opts.Region, bound) {
			return nil, InspectStats{}, fmt.Errorf("检查区域超出画布")
		}
		region = opts.Region.imageRect()
	}
	stat := summarize(composite, region)
	panel := inspectPanel{img: composite, region: region, label: "CANVAS"}
	var panels []inspectPanel
	switch opts.Mode {
	case "", "composite":
		panels = []inspectPanel{panel}
	case "layer":
		panel.label = "LAYER"
		panels = []inspectPanel{panel}
	case "crop":
		if opts.Region == nil {
			return nil, InspectStats{}, fmt.Errorf("局部检查需要指定区域")
		}
		panel.label = "CROP"
		panels = []inspectPanel{panel}
	case "compare_main", "compare_ref":
		if reference == nil || reference.Bounds().Empty() {
			return nil, InspectStats{}, fmt.Errorf("没有要对照的图片")
		}
		aligned := image.NewNRGBA(bound)
		sampleInto(aligned, reference, "box", opts.Fit)
		heat, comparison := compareImages(composite, aligned, region)
		stat.Comparison = &comparison
		panels = []inspectPanel{{img: aligned, region: region, label: "REFERENCE"}, panel, {img: heat, region: region, label: "DIFFERENCE"}}
	default:
		return nil, InspectStats{}, fmt.Errorf("未知像素检查模式 %q", opts.Mode)
	}
	bitmap := renderPanels(panels, opts.ShowGrid)
	png, err := ExportPNG(bitmap, 1)
	if err != nil {
		return nil, InspectStats{}, err
	}
	return png, stat, nil
}

// InspectOnion shows the previous frame behind the current one as a pale-blue alignment guide.
func InspectOnion(current, previous *image.NRGBA) ([]byte, InspectStats, error) {
	if current == nil || current.Bounds().Empty() {
		return nil, InspectStats{}, fmt.Errorf("没有可检查的当前帧")
	}
	bounds := current.Bounds()
	canvas := current
	if previous != nil {
		if previous.Bounds() != bounds {
			return nil, InspectStats{}, fmt.Errorf("上一帧尺寸与当前帧不一致")
		}
		guide := image.NewNRGBA(bounds)
		fill := color.NRGBA{R: 116, G: 165, B: 241}
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				fill.A = previous.NRGBAAt(x, y).A
				guide.SetNRGBA(x, y, fill)
			}
		}
		canvas = image.NewNRGBA(bounds)
		draw.DrawMask(canvas, bounds, guide, bounds.Min, image.NewUniform(color.Alpha{A: 89}), image.Point{}, draw.Over)
		draw.Draw(canvas, bounds, current, bounds.Min, draw.Over)
	}
	panel := inspectPanel{img: canvas, region: bounds, label: "ONION"}
	png, err := ExportPNG(renderPanels([]inspectPanel{panel}, true), 1)
	return png, summarize(current, bounds), err
}

// InspectFilmstrip renders one to eight equal-sized frames in order with their zero-based frame numbers.
func InspectFilmstrip(images []*image.NRGBA, indices []int) ([]byte, InspectStats, error) {
	if len(images) < 1 || len(images) > 8 || (indices != nil && len(indices) != len(images)) {
		return nil, InspectStats{}, fmt.Errorf("胶片检查需要 1 到 8 张帧图和对应帧号")
	}
	if images[0] == nil || images[0].Bounds().Empty() {
		return nil, InspectStats{}, fmt.Errorf("胶片检查缺少有效的首帧")
	}
	bounds := images[0].Bounds()
	stats := summarize(images[0], bounds)
	panels := make([]inspectPanel, 0, len(images))
	for i, img := range images {
		if img == nil || img.Bounds() != bounds {
			return nil, InspectStats{}, fmt.Errorf("胶片检查的帧图尺寸不一致")
		}
		index := i
		if indices != nil {
			index = indices[i]
		}
		if index < 0 {
			return nil, InspectStats{}, fmt.Errorf("帧号必须为非负数")
		}
		count, region := Coverage(img)
		stats.Frames = append(stats.Frames, FrameInspection{Index: index, Region: region, NonTransparentPixels: count, Coverage: float64(count) / float64(bounds.Dx()*bounds.Dy())})
		panels = append(panels, inspectPanel{img: img, region: bounds, label: "#" + strconv.Itoa(index)})
	}
	png, err := ExportPNG(renderPanels(panels, false), 1)
	return png, stats, err
}

func summarize(img *image.NRGBA, r image.Rectangle) InspectStats {
	stat := InspectStats{Region: fromRect(r)}
	hist := make(map[color.NRGBA]int)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.NRGBAAt(x, y)
			if c.A != 0 {
				stat.NonTransparentPixels++
				hist[c]++
			}
		}
	}
	stat.Coverage = float64(stat.NonTransparentPixels) / float64(r.Dx()*r.Dy())
	stat.ColorCount = len(hist)
	var top []colorCount
	for c, n := range hist {
		index := sort.Search(len(top), func(i int) bool { return n > top[i].n || n == top[i].n && colorKey(c) < colorKey(top[i].c) })
		if index >= 8 {
			continue
		}
		if len(top) < 8 {
			top = append(top, colorCount{})
		}
		copy(top[index+1:], top[index:])
		top[index] = colorCount{c: c, n: n}
	}
	for _, c := range top {
		stat.DominantColors = append(stat.DominantColors, FormatColor(c.c))
	}
	return stat
}

type comparisonSum struct {
	n                float64
	error            float64
	x, y, xx, yy, xy float64
}

func (s *comparisonSum) add(x, y, e float64) {
	s.n++
	s.error += e
	s.x += x
	s.y += y
	s.xx += x * x
	s.yy += y * y
	s.xy += x * y
}

func (s *comparisonSum) merge(o comparisonSum) {
	s.n += o.n
	s.error += o.error
	s.x += o.x
	s.y += o.y
	s.xx += o.xx
	s.yy += o.yy
	s.xy += o.xy
}

func roundMetric(n float64) float64 { return math.Round(n*10000) / 10000 }

func (s comparisonSum) scores() (float64, float64) {
	x, y := s.x/s.n, s.y/s.n
	varX, varY := max(0.0, s.xx/s.n-x*x), max(0.0, s.yy/s.n-y*y)
	cov := s.xy/s.n - x*y
	const c1, c2 = 0.0001, 0.0009
	sim := (2*x*y + c1) * (2*cov + c2) / ((x*x + y*y + c1) * (varX + varY + c2))
	return roundMetric(s.error / s.n), roundMetric(max(-1, min(1, sim)))
}

func signal(c color.NRGBA) [4]float64 {
	a := float64(c.A) / 255
	return [4]float64{float64(c.R) / 255 * a, float64(c.G) / 255 * a, float64(c.B) / 255 * a, a}
}

func compareImages(a, b *image.NRGBA, r image.Rectangle) (*image.NRGBA, Comparison) {
	heat := image.NewNRGBA(r)
	var whole comparisonSum
	var result Comparison
	cols, rows := min(4, r.Dx()), min(4, r.Dy())
	for row := 0; row < rows; row++ {
		for col := 0; col < cols; col++ {
			x, y := r.Min.X+col*r.Dx()/cols, r.Min.Y+row*r.Dy()/rows
			x1, y1 := r.Min.X+(col+1)*r.Dx()/cols, r.Min.Y+(row+1)*r.Dy()/rows
			var tile comparisonSum
			for j := y; j < y1; j++ {
				for i := x; i < x1; i++ {
					p, q := signal(a.NRGBAAt(i, j)), signal(b.NRGBAAt(i, j))
					var error float64
					for k, v := range p {
						d := v - q[k]
						error += d * d
					}
					distance := math.Sqrt(error / 4)
					light := func(v [4]float64) float64 { return .2126*v[0] + .7152*v[1] + .0722*v[2] + .5*(1-v[3]) }
					tile.add(light(p), light(q), distance)
					heat.SetNRGBA(i, j, color.NRGBA{R: byteValue(32 + 360*distance), G: byteValue(20 + 380*max(0, distance-.4)), B: byteValue(88 * (1 - distance)), A: 255})
				}
			}
			e, s := tile.scores()
			result.Grid = append(result.Grid, ComparisonTile{Column: col, Row: row, Region: fromRect(image.Rect(x, y, x1, y1)), ColorError: e, StructuralSimilarity: s})
			whole.merge(tile)
		}
	}
	result.ColorError, result.StructuralSimilarity = whole.scores()
	return heat, result
}

func plotSize(r image.Rectangle, limit int) (int, int) {
	w, h := r.Dx(), r.Dy()
	t := max(w, h)
	if t <= limit {
		s := limit / t
		return w * s, h * s
	}
	return max(1, w*limit/t), max(1, h*limit/t)
}

var (
	frameBackground = color.NRGBA{R: 22, G: 26, B: 35, A: 255}
	frameInk        = color.NRGBA{R: 220, G: 227, B: 240, A: 255}
	frameGrid       = color.NRGBA{R: 124, G: 136, B: 159, A: 255}
)

const (
	inspectMaxSide                                                   = 1024
	inspectLeft, inspectTop, inspectRight, inspectBottom, inspectGap = 30, 33, 12, 10, 8
)

func renderPanels(panels []inspectPanel, grid bool) *image.NRGBA {
	const left, top, right, bottom, gap = inspectLeft, inspectTop, inspectRight, inspectBottom, inspectGap
	limit := min(inspectMaxSide-top-bottom, (inspectMaxSide-(len(panels)-1)*gap)/len(panels)-left-right)
	w, h := plotSize(panels[0].region, limit)
	position := left
	width := len(panels)*(left+w+right) + (len(panels)-1)*gap
	dst := image.NewNRGBA(image.Rect(0, 0, width, top+h+bottom))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(frameBackground), image.Point{}, draw.Src)
	for _, p := range panels {
		// Keep long labels visible even when an extremely tall image has a narrow viewport.
		label := p.label
		if len(label)*6 > w+left+right {
			label = label[:min(len(label), (w+left+right)/6)]
		}
		drawLabel(dst, position-left+5, 4, label)
		renderPanel(dst, position, top, w, h, p, grid)
		position += left + w + right + gap
	}
	return dst
}

func renderPanel(dst *image.NRGBA, ox, oy, w, h int, p inspectPanel, grid bool) {
	r := p.region
	for y := 0; y < h; y++ {
		sy := r.Min.Y + (2*y+1)*r.Dy()/(2*h)
		for x := 0; x < w; x++ {
			sx := r.Min.X + (2*x+1)*r.Dx()/(2*w)
			c := p.img.NRGBAAt(sx, sy)
			if c.A < 255 {
				gray := uint8(206)
				if (x/10+y/10)%2 == 0 {
					gray = 244
				}
				blend := func(channel uint8) uint8 {
					return uint8((int(channel)*int(c.A) + int(gray)*(255-int(c.A)) + 127) / 255)
				}
				c = color.NRGBA{R: blend(c.R), G: blend(c.G), B: blend(c.B), A: 255}
			}
			dst.SetNRGBA(ox+x, oy+y, c)
		}
	}
	draw.Draw(dst, image.Rect(ox, oy-1, ox+w, oy), image.NewUniform(frameGrid), image.Point{}, draw.Src)
	draw.Draw(dst, image.Rect(ox-1, oy, ox, oy+h), image.NewUniform(frameGrid), image.Point{}, draw.Src)
	for x := r.Min.X; x < r.Max.X; x++ {
		local := (x - r.Min.X) * w / r.Dx()
		if x != r.Min.X && x%tickStep(w, r.Dx(), 25) != 0 {
			continue
		}
		if x == r.Min.X || local >= 18 {
			drawLabel(dst, ox+local, oy-14, strconv.Itoa(x))
			dst.SetNRGBA(ox+local, oy-2, frameInk)
		}
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		local := (y - r.Min.Y) * h / r.Dy()
		if y != r.Min.Y && y%tickStep(h, r.Dy(), 13) != 0 {
			continue
		}
		if y == r.Min.Y || local >= 9 {
			s := strconv.Itoa(y)
			drawLabel(dst, ox-5-6*len(s), oy+local, s)
			dst.SetNRGBA(ox-2, oy+local, frameInk)
		}
	}
	if !grid {
		return
	}
	sx, sy := tickStep(w, r.Dx(), 7), tickStep(h, r.Dy(), 7)
	for x := r.Min.X + 1; x < r.Max.X; x++ {
		if x%sx != 0 {
			continue
		}
		gridLine(dst, ox+(x-r.Min.X)*w/r.Dx(), oy, h, false)
	}
	for y := r.Min.Y + 1; y < r.Max.Y; y++ {
		if y%sy != 0 {
			continue
		}
		gridLine(dst, ox, oy+(y-r.Min.Y)*h/r.Dy(), w, true)
	}
}

func tickStep(length, pixels, spacing int) int {
	step := 1
	for step*length < spacing*pixels {
		step *= 2
	}
	return step
}

func gridLine(dst *image.NRGBA, x, y, length int, horizontal bool) {
	for i := 0; i < length; i++ {
		px, py := x, y+i
		if horizontal {
			px, py = x+i, y
		}
		c := dst.NRGBAAt(px, py)
		dst.SetNRGBA(px, py, color.NRGBA{R: uint8((3*int(c.R) + int(frameGrid.R)) / 4), G: uint8((3*int(c.G) + int(frameGrid.G)) / 4), B: uint8((3*int(c.B) + int(frameGrid.B)) / 4), A: 255})
	}
}

var glyphs = map[rune][7]uint8{
	'0': {14, 17, 19, 21, 25, 17, 14}, '1': {4, 12, 4, 4, 4, 4, 14}, '2': {14, 17, 1, 2, 4, 8, 31},
	'3': {30, 1, 1, 14, 1, 1, 30}, '4': {2, 6, 10, 18, 31, 2, 2}, '5': {31, 16, 16, 30, 1, 1, 30},
	'6': {14, 16, 16, 30, 17, 17, 14}, '7': {31, 1, 2, 4, 8, 8, 8}, '8': {14, 17, 17, 14, 17, 17, 14},
	'9': {14, 17, 17, 15, 1, 1, 14}, '-': {0, 0, 0, 31, 0, 0, 0}, '#': {10, 10, 31, 10, 31, 10, 10}, 'A': {14, 17, 17, 31, 17, 17, 17},
	'C': {14, 17, 16, 16, 16, 17, 14}, 'D': {30, 17, 17, 17, 17, 17, 30}, 'E': {31, 16, 16, 30, 16, 16, 31},
	'F': {31, 16, 16, 30, 16, 16, 16}, 'I': {14, 4, 4, 4, 4, 4, 14}, 'L': {16, 16, 16, 16, 16, 16, 31},
	'N': {17, 25, 21, 19, 17, 17, 17}, 'O': {14, 17, 17, 17, 17, 17, 14}, 'P': {30, 17, 17, 30, 16, 16, 16},
	'R': {30, 17, 17, 30, 20, 18, 17}, 'S': {15, 16, 16, 14, 1, 1, 30}, 'V': {17, 17, 17, 17, 17, 10, 4},
	'Y': {17, 17, 10, 4, 4, 4, 4},
}

func drawLabel(dst *image.NRGBA, x, y int, text string) {
	for _, r := range text {
		for row, bits := range glyphs[r] {
			for col := 0; col < 5; col++ {
				if bits&(1<<(4-col)) != 0 {
					dst.SetNRGBA(x+col, y+row, frameInk)
				}
			}
		}
		x += 6
	}
}
