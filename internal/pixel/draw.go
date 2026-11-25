package pixel

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxOperations = 256
	maxCoordinate = 4096
	maxDrawWork   = 8 * MaxSize * MaxSize
)

type DrawOp struct {
	Op           string            `json:"op"`
	X            int               `json:"x,omitempty"`
	Y            int               `json:"y,omitempty"`
	W            int               `json:"w,omitempty"`
	H            int               `json:"h,omitempty"`
	X0           int               `json:"x0,omitempty"`
	Y0           int               `json:"y0,omitempty"`
	X1           int               `json:"x1,omitempty"`
	Y1           int               `json:"y1,omitempty"`
	CX           int               `json:"cx,omitempty"`
	CY           int               `json:"cy,omitempty"`
	RX           int               `json:"rx,omitempty"`
	RY           int               `json:"ry,omitempty"`
	Color        string            `json:"color,omitempty"`
	ColorA       string            `json:"colorA,omitempty"`
	ColorB       string            `json:"colorB,omitempty"`
	From         string            `json:"from,omitempty"`
	To           string            `json:"to,omitempty"`
	Fill         bool              `json:"fill,omitempty"`
	Thickness    int               `json:"thickness,omitempty"`
	Brush        string            `json:"brush,omitempty"`
	Points       [][]int           `json:"points,omitempty"`
	Tolerance    int               `json:"tolerance,omitempty"`
	Contiguous   *bool             `json:"contiguous,omitempty"`
	Legend       map[string]string `json:"legend,omitempty"`
	Rows         []string          `json:"rows,omitempty"`
	Pattern      string            `json:"pattern,omitempty"`
	Ratio        *float64          `json:"ratio,omitempty"`
	Connectivity int               `json:"connectivity,omitempty"`
	Axis         string            `json:"axis,omitempty"`
}

func cloneImage(src *image.NRGBA) *image.NRGBA {
	dst := image.NewNRGBA(src.Bounds())
	b := src.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		start := src.PixOffset(b.Min.X, y)
		copy(dst.Pix[dst.PixOffset(b.Min.X, y):], src.Pix[start:start+b.Dx()*4])
	}
	return dst
}

// Draw applies a bounded batch atomically. A bad operation leaves the input unchanged.
func Draw(img *image.NRGBA, ops []DrawOp, clip *Selection) (DrawSummary, error) {
	if img == nil || img.Bounds().Min != (image.Point{}) {
		return DrawSummary{}, fmt.Errorf("缺少有效的像素图层")
	}
	b := img.Bounds()
	if err := ValidateSize(b.Dx(), b.Dy()); err != nil {
		return DrawSummary{}, err
	}
	if err := ValidateSelection(clip, b.Dx(), b.Dy()); err != nil {
		return DrawSummary{}, err
	}
	if len(ops) < 1 || len(ops) > maxOperations {
		return DrawSummary{}, fmt.Errorf("一次绘制必须包含 1 到 %d 个操作", maxOperations)
	}
	d := painter{img: cloneImage(img), active: b, remaining: maxDrawWork}
	if clip != nil {
		d.active = clip.Rect.imageRect()
		if clip.Runs != nil {
			d.mask = make([]bool, clip.W*clip.H)
			for _, run := range clip.Runs {
				for i := run[0]; i < run[0]+run[1]; i++ {
					d.mask[i] = true
				}
			}
		}
	}
	for i, op := range ops {
		if err := d.apply(op); err != nil {
			return DrawSummary{}, fmt.Errorf("绘制操作 %d (%s): %w", i+1, op.Op, err)
		}
	}
	summary := Changes(img, d.img)
	summary.OutOfBounds, summary.Warnings = d.outside, d.warnings
	if summary.ChangedPixels > 0 {
		for y := b.Min.Y; y < b.Max.Y; y++ {
			start := d.img.PixOffset(b.Min.X, y)
			copy(img.Pix[img.PixOffset(b.Min.X, y):], d.img.Pix[start:start+b.Dx()*4])
		}
	}
	return summary, nil
}

type painter struct {
	img       *image.NRGBA
	active    image.Rectangle
	mask      []bool
	remaining int
	outside   int
	warnings  []string
}

func (d *painter) budget(work int) error {
	if work > d.remaining {
		return fmt.Errorf("绘制批次超过像素操作预算，请拆分后重试")
	}
	d.remaining -= work
	return nil
}

func (d *painter) selected(p image.Point) bool {
	return p.In(d.active) && (d.mask == nil || d.mask[(p.Y-d.active.Min.Y)*d.active.Dx()+p.X-d.active.Min.X])
}

func (d *painter) put(x, y int, c color.NRGBA) {
	p := image.Pt(x, y)
	if !p.In(d.img.Bounds()) {
		d.outside++
		return
	}
	if d.selected(p) {
		d.img.SetNRGBA(x, y, c)
	}
}

func (d *painter) apply(o DrawOp) error {
	for _, n := range []int{o.X, o.Y, o.X0, o.Y0, o.X1, o.Y1, o.CX, o.CY} {
		if n < -maxCoordinate || n > maxCoordinate {
			return fmt.Errorf("坐标必须在 -%d 到 %d 之间", maxCoordinate, maxCoordinate)
		}
	}
	if o.W < 0 || o.W > maxCoordinate || o.H < 0 || o.H > maxCoordinate || o.RX < 0 || o.RX > MaxSize || o.RY < 0 || o.RY > MaxSize {
		return fmt.Errorf("形状尺寸超出允许范围")
	}
	if o.Tolerance < 0 || o.Tolerance > 255 {
		return fmt.Errorf("颜色容差必须在 0 到 255 之间")
	}
	if o.Thickness < 0 || o.Thickness > 32 {
		return fmt.Errorf("笔刷粗细必须在 1 到 32 之间")
	}
	if o.Brush != "" && o.Brush != "round" && o.Brush != "square" {
		return fmt.Errorf("未知笔刷形状 %q", o.Brush)
	}
	thickness := max(1, o.Thickness)
	rect := image.Rect(o.X, o.Y, o.X+o.W, o.Y+o.H)
	area := d.active.Dx() * d.active.Dy()
	var c color.NRGBA
	switch o.Op {
	case "clear":
		if o.Color == "" {
			break
		}
		fallthrough
	case "pixel", "line", "rect", "ellipse", "polygon", "flood_fill", "outline":
		var err error
		c, err = ParseColor(o.Color)
		if err != nil {
			return err
		}
	}
	switch o.Op {
	case "clear":
		return d.fill(d.active, c)
	case "pixel":
		if err := d.budget(1); err != nil {
			return err
		}
		d.put(o.X, o.Y, c)
	case "line":
		if err := d.budget(lineWork(o.X0, o.Y0, o.X1, o.Y1, thickness)); err != nil {
			return err
		}
		d.line(o.X0, o.Y0, o.X1, o.Y1, thickness, o.Brush == "square", c)
	case "rect", "erase_rect":
		if rect.Empty() {
			return fmt.Errorf("矩形的宽和高必须大于零")
		}
		if o.Fill || o.Op == "erase_rect" {
			return d.fill(rect, c)
		}
		if err := d.budget(2 * (rect.Dx() + rect.Dy()) * thickness * thickness); err != nil {
			return err
		}
		l, r, t, b := rect.Min.X, rect.Max.X-1, rect.Min.Y, rect.Max.Y-1
		square := o.Brush == "square"
		d.line(l, t, r, t, thickness, square, c)
		d.line(r, t, r, b, thickness, square, c)
		d.line(r, b, l, b, thickness, square, c)
		d.line(l, b, l, t, thickness, square, c)
	case "ellipse":
		if o.Fill {
			if err := d.budget(2 * (2*o.RX + 1) * (2*o.RY + 1)); err != nil {
				return err
			}
		} else if err := d.budget(4 * (o.RX + o.RY + 1) * thickness * thickness); err != nil {
			return err
		}
		d.ellipse(o.CX, o.CY, o.RX, o.RY, c, o.Fill, thickness, o.Brush == "square")
	case "polygon":
		return d.polygon(o.Points, o.Fill, thickness, o.Brush == "square", c)
	case "flood_fill":
		if err := d.budget(area); err != nil {
			return err
		}
		seed := image.Pt(o.X, o.Y)
		if !seed.In(d.img.Bounds()) {
			d.outside++
		} else if d.selected(seed) {
			target := d.img.NRGBAAt(seed.X, seed.Y)
			if o.Contiguous != nil && !*o.Contiguous {
				d.replace(target, c, o.Tolerance)
			} else {
				d.flood(seed, target, c, o.Tolerance)
			}
		}
	case "replace_color":
		from, err := ParseColor(o.From)
		if err != nil {
			return err
		}
		to, err := ParseColor(o.To)
		if err != nil {
			return err
		}
		if err := d.budget(area); err != nil {
			return err
		}
		d.replace(from, to, o.Tolerance)
	case "blit":
		return d.blit(o)
	case "dither_rect":
		return d.dither(rect, o)
	case "outline":
		if o.Connectivity != 0 && o.Connectivity != 4 && o.Connectivity != 8 {
			return fmt.Errorf("描边邻域必须为 4 或 8")
		}
		if err := d.budget(area * min(8, max(4, o.Connectivity))); err != nil {
			return err
		}
		d.outline(c, o.Connectivity == 8)
	case "mirror":
		if o.Axis != "x" && o.Axis != "y" {
			return fmt.Errorf("镜像轴必须为 x 或 y")
		}
		if rect.Empty() {
			rect = d.active
		}
		if !rect.In(d.img.Bounds()) {
			return fmt.Errorf("镜像区域超出画布")
		}
		if err := d.budget(rect.Dx() * rect.Dy()); err != nil {
			return err
		}
		d.mirror(rect, o.Axis == "x")
	default:
		return fmt.Errorf("未知绘图原语 %q", o.Op)
	}
	return nil
}

func (d *painter) fill(r image.Rectangle, c color.NRGBA) error {
	inBounds := r.Intersect(d.img.Bounds())
	d.outside += r.Dx()*r.Dy() - inBounds.Dx()*inBounds.Dy()
	active := r.Intersect(d.active)
	if err := d.budget(active.Dx() * active.Dy()); err != nil {
		return err
	}
	for y := active.Min.Y; y < active.Max.Y; y++ {
		for x := active.Min.X; x < active.Max.X; x++ {
			d.put(x, y, c)
		}
	}
	return nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func lineWork(x0, y0, x1, y1, thickness int) int {
	return (max(abs(x1-x0), abs(y1-y0)) + 1) * thickness * thickness
}

func (d *painter) stamp(x, y, thickness int, square bool, c color.NRGBA) {
	if thickness == 1 {
		d.put(x, y, c)
		return
	}
	start, end := -(thickness-1)/2, thickness/2
	shift := start + end
	for dy := start; dy <= end; dy++ {
		for dx := start; dx <= end; dx++ {
			cx, cy := 2*dx-shift, 2*dy-shift
			if square || cx*cx+cy*cy <= thickness*thickness {
				d.put(x+dx, y+dy, c)
			}
		}
	}
}

func (d *painter) line(x0, y0, x1, y1, thickness int, square bool, c color.NRGBA) {
	dx, dy := abs(x1-x0), -abs(y1-y0)
	sx, sy := -1, -1
	if x0 < x1 {
		sx = 1
	}
	if y0 < y1 {
		sy = 1
	}
	err := dx + dy
	for {
		d.stamp(x0, y0, thickness, square, c)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func (d *painter) ellipse(cx, cy, rx, ry int, c color.NRGBA, fill bool, thickness int, square bool) {
	if rx == 0 || ry == 0 {
		if fill {
			thickness = 1
		}
		d.line(cx-rx, cy-ry, cx+rx, cy+ry, thickness, square, c)
		return
	}
	var spans []int
	if fill {
		spans = make([]int, ry+1)
	}
	plot := func(x, y int) {
		if spans != nil {
			spans[y] = max(spans[y], x)
			return
		}
		d.stamp(cx+x, cy+y, thickness, square, c)
		if x != 0 {
			d.stamp(cx-x, cy+y, thickness, square, c)
		}
		if y != 0 {
			d.stamp(cx+x, cy-y, thickness, square, c)
			if x != 0 {
				d.stamp(cx-x, cy-y, thickness, square, c)
			}
		}
	}
	// The midpoint test is multiplied by four to avoid fractional coordinates.
	rx2, ry2 := int64(rx)*int64(rx), int64(ry)*int64(ry)
	x, y := 0, ry
	dx, dy := int64(0), 2*rx2*int64(y)
	p := 4*ry2 - 4*rx2*int64(ry) + rx2
	for dx < dy {
		plot(x, y)
		x++
		dx += 2 * ry2
		if p < 0 {
			p += 4 * (dx + ry2)
		} else {
			y--
			dy -= 2 * rx2
			p += 4 * (dx - dy + ry2)
		}
	}
	p = ry2*int64(2*x+1)*int64(2*x+1) + 4*rx2*int64(y-1)*int64(y-1) - 4*rx2*ry2
	for y >= 0 {
		plot(x, y)
		y--
		dy -= 2 * rx2
		if p > 0 {
			p += 4 * (rx2 - dy)
		} else {
			x++
			dx += 2 * ry2
			p += 4 * (dx - dy + rx2)
		}
	}
	for y, extent := range spans {
		for x := cx - extent; x <= cx+extent; x++ {
			d.put(x, cy+y, c)
			if y != 0 {
				d.put(x, cy-y, c)
			}
		}
	}
}

func (d *painter) polygon(points [][]int, fill bool, thickness int, square bool, c color.NRGBA) error {
	if len(points) < 3 || len(points) > 256 {
		return fmt.Errorf("多边形需要 3 到 256 个顶点")
	}
	vertices := make([]image.Point, 0, len(points))
	lo, hi := maxCoordinate, -maxCoordinate
	for _, p := range points {
		if len(p) != 2 || p[0] < -maxCoordinate || p[0] > maxCoordinate || p[1] < -maxCoordinate || p[1] > maxCoordinate {
			return fmt.Errorf("多边形顶点必须是画布坐标对")
		}
		vertices = append(vertices, image.Pt(p[0], p[1]))
		lo, hi = min(lo, p[1]), max(hi, p[1])
	}
	for i, a := range vertices {
		b := vertices[(i+1)%len(vertices)]
		if err := d.budget(lineWork(a.X, a.Y, b.X, b.Y, thickness)); err != nil {
			return err
		}
	}
	from, to := max(lo, d.active.Min.Y), min(hi+1, d.active.Max.Y)
	if fill {
		if err := d.budget(max(0, to-from) * (d.active.Dx() + len(vertices))); err != nil {
			return err
		}
		xs := make([]float64, 0, len(vertices))
		for y := from; y < to; y++ {
			xs = xs[:0]
			py := float64(y) + 0.5
			for i, a := range vertices {
				b := vertices[(i+1)%len(vertices)]
				if py >= float64(min(a.Y, b.Y)) && py < float64(max(a.Y, b.Y)) {
					xs = append(xs, float64(a.X)+(py-float64(a.Y))*float64(b.X-a.X)/float64(b.Y-a.Y))
				}
			}
			sort.Float64s(xs)
			for i := 0; i+1 < len(xs); i += 2 {
				start := max(int(math.Ceil(xs[i]-0.5)), d.active.Min.X)
				end := min(int(math.Ceil(xs[i+1]-0.5)), d.active.Max.X)
				for x := start; x < end; x++ {
					d.put(x, y, c)
				}
			}
		}
	}
	for i, a := range vertices {
		b := vertices[(i+1)%len(vertices)]
		d.line(a.X, a.Y, b.X, b.Y, thickness, square, c)
	}
	return nil
}

var neighbors = [...]image.Point{{X: 1}, {X: -1}, {Y: 1}, {Y: -1}, {X: 1, Y: 1}, {X: -1, Y: 1}, {X: 1, Y: -1}, {X: -1, Y: -1}}

func matches(c, target color.NRGBA, tolerance int) bool {
	if c.A == 0 && target.A == 0 {
		return true
	}
	return abs(int(c.R)-int(target.R)) <= tolerance && abs(int(c.G)-int(target.G)) <= tolerance && abs(int(c.B)-int(target.B)) <= tolerance && abs(int(c.A)-int(target.A)) <= tolerance
}

func (d *painter) flood(seed image.Point, target, c color.NRGBA, tolerance int) {
	if target == c && tolerance == 0 {
		return
	}
	seen := make([]bool, d.active.Dx()*d.active.Dy())
	queue := []image.Point{seed}
	seen[(seed.Y-d.active.Min.Y)*d.active.Dx()+seed.X-d.active.Min.X] = true
	for i := 0; i < len(queue); i++ {
		p := queue[i]
		d.put(p.X, p.Y, c)
		for _, step := range neighbors[:4] {
			n := p.Add(step)
			if !d.selected(n) {
				continue
			}
			j := (n.Y-d.active.Min.Y)*d.active.Dx() + n.X - d.active.Min.X
			if !seen[j] && matches(d.img.NRGBAAt(n.X, n.Y), target, tolerance) {
				queue = append(queue, n)
			}
			seen[j] = true
		}
	}
}

func (d *painter) replace(from, to color.NRGBA, tolerance int) {
	for y := d.active.Min.Y; y < d.active.Max.Y; y++ {
		for x := d.active.Min.X; x < d.active.Max.X; x++ {
			if matches(d.img.NRGBAAt(x, y), from, tolerance) {
				d.put(x, y, to)
			}
		}
	}
}

func (d *painter) outline(c color.NRGBA, diagonals bool) {
	original := cloneImage(d.img)
	steps := neighbors[:4]
	if diagonals {
		steps = neighbors[:]
	}
	for y := d.active.Min.Y; y < d.active.Max.Y; y++ {
		for x := d.active.Min.X; x < d.active.Max.X; x++ {
			if original.NRGBAAt(x, y).A != 0 {
				continue
			}
			for _, step := range steps {
				if original.NRGBAAt(x+step.X, y+step.Y).A != 0 {
					d.put(x, y, c)
					break
				}
			}
		}
	}
}

func (d *painter) mirror(r image.Rectangle, horizontal bool) {
	original := cloneImage(d.img)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if !d.selected(image.Pt(x, y)) {
				continue
			}
			c := original.NRGBAAt(x, y)
			if c.A == 0 {
				continue
			}
			if horizontal {
				d.put(r.Max.X-1-(x-r.Min.X), y, c)
			} else {
				d.put(x, r.Max.Y-1-(y-r.Min.Y), c)
			}
		}
	}
}

func parseRow(row string) ([]rune, error) {
	if !strings.HasPrefix(row, "rle:") {
		pixels := []rune(row)
		if len(pixels) > MaxBlitSpan {
			return nil, fmt.Errorf("像素行宽不能超过 %d", MaxBlitSpan)
		}
		return pixels, nil
	}
	var out []rune
	row = strings.TrimPrefix(row, "rle:")
	for len(row) > 0 {
		i := 0
		for i < len(row) && row[i] >= '0' && row[i] <= '9' {
			i++
		}
		if i == 0 || i == len(row) {
			return nil, fmt.Errorf("RLE 行应使用计数加单个字符，例如 rle:2.3K2.")
		}
		n, err := strconv.Atoi(row[:i])
		if err != nil || n < 1 || n > MaxBlitSpan-len(out) {
			return nil, fmt.Errorf("RLE 行宽不能超过 %d，且每段计数必须大于零", MaxBlitSpan)
		}
		r, size := utf8.DecodeRuneInString(row[i:])
		if r == '\\' {
			r, size = utf8.DecodeRuneInString(row[i+1:])
			if size == 0 {
				return nil, fmt.Errorf("RLE 行尾缺少转义字符")
			}
			size++
		}
		for j := 0; j < n; j++ {
			out = append(out, r)
		}
		row = row[i+size:]
	}
	return out, nil
}

func (d *painter) blit(o DrawOp) error {
	if len(o.Rows) == 0 || len(o.Rows) > MaxBlitSpan || len(o.Legend) > MaxBlitSpan*MaxBlitSpan {
		return fmt.Errorf("网格宽高须在 1 到 %d 之间，每个色标一个字符", MaxBlitSpan)
	}
	legend := make(map[rune]color.NRGBA, len(o.Legend)+1)
	for symbol, hex := range o.Legend {
		r, size := utf8.DecodeRuneInString(symbol)
		if size == 0 || size != len(symbol) || r == '.' || r == ' ' {
			return fmt.Errorf("色标 %q 无效，点号和空格保留为跳过像素", symbol)
		}
		c, err := ParseColor(hex)
		if err != nil {
			return err
		}
		legend[r] = c
	}
	if _, ok := legend['_']; !ok {
		legend['_'] = color.NRGBA{}
	}
	width, unknown := -1, 0
	ragged := false
	for y, row := range o.Rows {
		if len(row) > 8*MaxBlitSpan {
			return fmt.Errorf("网格行超出字符预算")
		}
		pixels, err := parseRow(row)
		if err != nil {
			return fmt.Errorf("网格第 %d 行: %w", y+1, err)
		}
		if width < 0 {
			width = len(pixels)
		}
		ragged = ragged || width != len(pixels)
		if err := d.budget(len(pixels)); err != nil {
			return err
		}
		for x, r := range pixels {
			c, ok := legend[r]
			if ok {
				d.put(o.X+x, o.Y+y, c)
			} else if r != '.' && r != ' ' {
				unknown++
			}
		}
	}
	if ragged {
		d.warnings = append(d.warnings, "网格行宽不同，按各行的实际长度绘制")
	}
	if unknown > 0 {
		d.warnings = append(d.warnings, fmt.Sprintf("已跳过 %d 个未定义的色标", unknown))
	}
	return nil
}

var bayer = [...]int{0, 8, 2, 10, 12, 4, 14, 6, 3, 11, 1, 9, 15, 7, 13, 5}
var bayer2 = [...]int{0, 2, 3, 1}

func (d *painter) dither(rect image.Rectangle, o DrawOp) error {
	if rect.Empty() {
		return fmt.Errorf("抖动矩形的宽和高必须大于零")
	}
	a, err := ParseColor(o.ColorA)
	if err != nil {
		return err
	}
	b, err := ParseColor(o.ColorB)
	if err != nil {
		return err
	}
	ratio := 0.25
	if o.Ratio != nil {
		ratio = *o.Ratio
		if math.IsNaN(ratio) || ratio < 0 || ratio > 1 {
			return fmt.Errorf("抖动比例必须在 0 到 1 之间")
		}
	}
	switch o.Pattern {
	case "", "checker", "bayer2", "bayer4", "hgrad", "vgrad":
	default:
		return fmt.Errorf("未知抖动模式 %q", o.Pattern)
	}
	inBounds := rect.Intersect(d.img.Bounds())
	d.outside += rect.Dx()*rect.Dy() - inBounds.Dx()*inBounds.Dy()
	active := rect.Intersect(d.active)
	if err := d.budget(active.Dx() * active.Dy()); err != nil {
		return err
	}
	for y := active.Min.Y; y < active.Max.Y; y++ {
		for x := active.Min.X; x < active.Max.X; x++ {
			dx, dy := x-rect.Min.X, y-rect.Min.Y
			threshold := (float64(bayer[(dy%4)*4+dx%4]) + 0.5) / 16
			amount := ratio
			switch o.Pattern {
			case "", "checker":
				threshold, amount = float64((dx+dy)%2), 0.5
			case "bayer2":
				threshold = (float64(bayer2[(dy%2)*2+dx%2]) + 0.5) / 4
			case "hgrad":
				amount = float64(dx) / float64(max(1, rect.Dx()-1))
			case "vgrad":
				amount = float64(dy) / float64(max(1, rect.Dy()-1))
			}
			c := a
			if threshold < amount {
				c = b
			}
			d.put(x, y, c)
		}
	}
	return nil
}
