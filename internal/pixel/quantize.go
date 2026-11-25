package pixel

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"sort"
)

type QuantizeOpts struct {
	SampleMode     string   `json:"sampleMode,omitempty"`
	Dither         string   `json:"dither,omitempty"`
	MaxColors      int      `json:"maxColors,omitempty"`
	Palette        []string `json:"palette,omitempty"`
	AlphaThreshold *int     `json:"alphaThreshold,omitempty"`
	Fit            string   `json:"fit,omitempty"`
}

func channels(c color.NRGBA) [3]uint8 { return [3]uint8{c.R, c.G, c.B} }

type colorCount struct {
	c color.NRGBA
	n int
}

type colorBucket struct {
	colors []colorCount
	weight int
	axis   int
	span   int
}

func bucket(colors []colorCount) colorBucket {
	b := colorBucket{colors: colors}
	lo, hi := [3]int{255, 255, 255}, [3]int{}
	for _, entry := range colors {
		b.weight += entry.n
		for i, v := range channels(entry.c) {
			lo[i] = min(lo[i], int(v))
			hi[i] = max(hi[i], int(v))
		}
	}
	for i := range lo {
		if hi[i]-lo[i] > b.span {
			b.axis, b.span = i, hi[i]-lo[i]
		}
	}
	return b
}

func colorKey(c color.NRGBA) uint32 {
	return uint32(c.R)<<24 | uint32(c.G)<<16 | uint32(c.B)<<8 | uint32(c.A)
}

func picker(src image.Image) func(int, int) color.NRGBA {
	if raster, ok := src.(*image.NRGBA); ok {
		return raster.NRGBAAt
	}
	return func(x, y int) color.NRGBA { return color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA) }
}

// ExtractPalette selects representative opaque RGB colors with frequency-weighted median cut.
func ExtractPalette(src image.Image, maxColors int) []string {
	if src == nil || src.Bounds().Empty() {
		return nil
	}
	if maxColors == 0 {
		maxColors = 16
	}
	maxColors = min(64, max(4, maxColors))
	b := src.Bounds()
	get := picker(src)
	histogram := make(map[color.NRGBA]int)
	// Keep the histogram bounded for both large source assets and large canvases.
	const paletteSamplePixels = 512 * 512
	step := max(1, int(math.Ceil(math.Sqrt(float64(b.Dx()*b.Dy())/paletteSamplePixels))))
	for y := min(b.Max.Y-1, b.Min.Y+step/2); y < b.Max.Y; y += step {
		for x := min(b.Max.X-1, b.Min.X+step/2); x < b.Max.X; x += step {
			c := get(x, y)
			if c.A != 0 {
				c.A = 255
				histogram[c]++
			}
		}
	}
	if len(histogram) == 0 && step > 1 {
		// Very thin features can fall between sample points on a transparent asset.
		for y := b.Min.Y; y < b.Max.Y && len(histogram) == 0; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if c := get(x, y); c.A != 0 {
					c.A = 255
					histogram[c] = 1
					break
				}
			}
		}
	}
	if len(histogram) == 0 {
		return nil
	}
	colors := make([]colorCount, 0, len(histogram))
	for c, n := range histogram {
		colors = append(colors, colorCount{c: c, n: n})
	}
	buckets := []colorBucket{bucket(colors)}
	for len(buckets) < maxColors {
		index, score := -1, 0
		for i, b := range buckets {
			if v := b.span * b.weight; len(b.colors) > 1 && v > score {
				index, score = i, v
			}
		}
		if index < 0 {
			break
		}
		b := buckets[index]
		sort.Slice(b.colors, func(i, j int) bool {
			ci, cj := b.colors[i].c, b.colors[j].c
			ai, aj := channels(ci)[b.axis], channels(cj)[b.axis]
			if ai == aj {
				return colorKey(ci) < colorKey(cj)
			}
			return ai < aj
		})
		cut, acc := 1, b.colors[0].n
		for cut < len(b.colors)-1 && acc*2 < b.weight {
			acc += b.colors[cut].n
			cut++
		}
		buckets[index] = bucket(b.colors[:cut])
		buckets = append(buckets, bucket(b.colors[cut:]))
	}
	palette := make([]color.NRGBA, 0, len(buckets))
	seen := make(map[color.NRGBA]bool, len(buckets))
	for _, b := range buckets {
		var sums [3]int
		for _, entry := range b.colors {
			for i, v := range channels(entry.c) {
				sums[i] += int(v) * entry.n
			}
		}
		for i := range sums {
			sums[i] = (sums[i] + b.weight/2) / b.weight
		}
		c := color.NRGBA{R: uint8(sums[0]), G: uint8(sums[1]), B: uint8(sums[2]), A: 255}
		if !seen[c] {
			palette = append(palette, c)
			seen[c] = true
		}
	}
	luma := func(c color.NRGBA) int { return 2126*int(c.R) + 7152*int(c.G) + 722*int(c.B) }
	sort.Slice(palette, func(i, j int) bool {
		a, b := luma(palette[i]), luma(palette[j])
		if a == b {
			return colorKey(palette[i]) < colorKey(palette[j])
		}
		return a < b
	})
	result := make([]string, 0, len(palette))
	for _, c := range palette {
		result = append(result, FormatColor(c))
	}
	return result
}

func ValidateFit(fit string) error {
	switch fit {
	case "", "fit", "cover", "stretch":
		return nil
	default:
		return fmt.Errorf("未知图片适配方式 %q", fit)
	}
}

// Preview produces a nearest-neighbor image bounded by maxSide, retaining original pixels when possible.
func Preview(src image.Image, maxSide int) *image.NRGBA {
	if src == nil || src.Bounds().Empty() || maxSide < 1 {
		return nil
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if max(w, h) <= maxSide {
		return normalize(src)
	}
	factor := float64(maxSide) / float64(max(w, h))
	dst := image.NewNRGBA(image.Rect(0, 0, max(1, int(math.Round(float64(w)*factor))), max(1, int(math.Round(float64(h)*factor)))))
	sampleInto(dst, src, "nearest", "stretch")
	return dst
}

func sampleInto(dst *image.NRGBA, src image.Image, mode, fit string) {
	r := dst.Bounds()
	b := src.Bounds()
	sx0, sy0 := float64(b.Min.X), float64(b.Min.Y)
	sw, sh := float64(b.Dx()), float64(b.Dy())
	w, h := float64(r.Dx()), float64(r.Dy())
	switch fit {
	case "", "fit":
		s := min(w/sw, h/sh)
		dw, dh := max(1, int(math.Round(sw*s))), max(1, int(math.Round(sh*s)))
		x, y := r.Min.X+(r.Dx()-dw)/2, r.Min.Y+(r.Dy()-dh)/2
		r = image.Rect(x, y, x+dw, y+dh)
	case "cover":
		s := max(w/sw, h/sh)
		sx0 += (sw - w/s) / 2
		sy0 += (sh - h/s) / 2
		sw, sh = w/s, h/s
	}
	sx, sy := sw/float64(r.Dx()), sh/float64(r.Dy())
	get := picker(src)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		sy1 := sy0 + float64(y-r.Min.Y)*sy
		for x := r.Min.X; x < r.Max.X; x++ {
			sx1 := sx0 + float64(x-r.Min.X)*sx
			var c color.NRGBA
			switch mode {
			case "nearest":
				c = get(min(b.Max.X-1, int(math.Floor(sx1+0.5*sx))), min(b.Max.Y-1, int(math.Floor(sy1+0.5*sy))))
			case "center":
				// Choose the lower of two pixels whose centers are equidistant from the sampling point.
				c = get(max(b.Min.X, int(math.Ceil(sx1+0.5*sx))-1), max(b.Min.Y, int(math.Ceil(sy1+0.5*sy))-1))
			default:
				c = averageSample(get, b, sx1, sy1, sx1+sx, sy1+sy)
			}
			dst.SetNRGBA(x, y, c)
		}
	}
}

func averageSample(get func(int, int) color.NRGBA, b image.Rectangle, x1, y1, x2, y2 float64) color.NRGBA {
	var ar, ag, ab, alpha float64
	for y := max(b.Min.Y, int(math.Floor(y1))); y < b.Max.Y && float64(y) < y2; y++ {
		wy := min(y2, float64(y+1)) - max(y1, float64(y))
		for x := max(b.Min.X, int(math.Floor(x1))); x < b.Max.X && float64(x) < x2; x++ {
			wx := min(x2, float64(x+1)) - max(x1, float64(x))
			c := get(x, y)
			a := float64(c.A) * wx * wy
			ar += float64(c.R) * a
			ag += float64(c.G) * a
			ab += float64(c.B) * a
			alpha += a
		}
	}
	if alpha == 0 {
		return color.NRGBA{}
	}
	return color.NRGBA{R: byteValue(ar / alpha), G: byteValue(ag / alpha), B: byteValue(ab / alpha), A: byteValue(alpha / ((x2 - x1) * (y2 - y1)))}
}

func byteValue(n float64) uint8 { return uint8(math.Round(min(255, max(0, n)))) }

func Quantize(src image.Image, w, h int, opts QuantizeOpts) (*image.NRGBA, []string, error) {
	if src == nil || src.Bounds().Empty() {
		return nil, nil, fmt.Errorf("缺少可采样的图片")
	}
	dst, err := New(w, h)
	if err != nil {
		return nil, nil, err
	}
	if err := ValidateFit(opts.Fit); err != nil {
		return nil, nil, err
	}
	switch opts.SampleMode {
	case "", "box", "nearest", "center":
	default:
		return nil, nil, fmt.Errorf("未知采样方式 %q", opts.SampleMode)
	}
	switch opts.Dither {
	case "", "none", "bayer4", "floyd":
	default:
		return nil, nil, fmt.Errorf("未知量化抖动方式 %q", opts.Dither)
	}
	if opts.MaxColors != 0 && (opts.MaxColors < 4 || opts.MaxColors > 64) {
		return nil, nil, fmt.Errorf("提取调色板的颜色数量必须在 4 到 64 之间")
	}
	if len(opts.Palette) > 256 {
		return nil, nil, fmt.Errorf("量化调色板不能超过 256 个颜色")
	}
	threshold := 128
	if opts.AlphaThreshold != nil {
		threshold = *opts.AlphaThreshold
	}
	if threshold < 0 || threshold > 255 {
		return nil, nil, fmt.Errorf("透明度阈值必须在 0 到 255 之间")
	}
	sampleInto(dst, src, opts.SampleMode, opts.Fit)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := dst.NRGBAAt(x, y)
			if c.A == 0 || int(c.A) < threshold {
				dst.SetNRGBA(x, y, color.NRGBA{})
			}
		}
	}
	palette := opts.Palette
	if len(palette) == 0 {
		palette = ExtractPalette(dst, opts.MaxColors)
	}
	colors := make([]color.NRGBA, 0, len(palette))
	canonical := make([]string, 0, len(palette))
	seen := make(map[color.NRGBA]bool, len(palette))
	for _, hex := range palette {
		c, err := ParseColor(hex)
		if err != nil {
			return nil, nil, err
		}
		if !seen[c] {
			colors = append(colors, c)
			canonical = append(canonical, FormatColor(c))
			seen[c] = true
		}
	}
	if len(colors) > 0 {
		quantizePixels(dst, colors, opts.Dither)
	}
	return dst, canonical, nil
}

var channelWeights = [...]float64{2, 4, 3}

func nearestColors(target [3]float64, colors []color.NRGBA) (int, int) {
	best, second := 0, 0
	small, next := math.Inf(1), math.Inf(1)
	for i, c := range colors {
		distance := 0.0
		for j, v := range channels(c) {
			d := target[j] - float64(v)
			distance += channelWeights[j] * d * d
		}
		if distance < small {
			second, next = best, small
			best, small = i, distance
		} else if distance < next {
			second, next = i, distance
		}
	}
	return best, second
}

func orderedAmount(target [3]float64, best, second color.NRGBA) float64 {
	other := channels(second)
	var sum, denominator float64
	for i, v := range channels(best) {
		d := float64(other[i]) - float64(v)
		sum += channelWeights[i] * (target[i] - float64(v)) * d
		denominator += channelWeights[i] * d * d
	}
	if denominator == 0 {
		return 0
	}
	return sum / denominator
}

func quantizePixels(img *image.NRGBA, colors []color.NRGBA, dither string) {
	b := img.Bounds()
	w := b.Dx()
	var current, next [][3]float64
	if dither == "floyd" {
		current, next = make([][3]float64, w+2), make([][3]float64, w+2)
	}
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < w; x++ {
			c := img.NRGBAAt(x, y)
			if c.A == 0 {
				continue
			}
			target := [3]float64{float64(c.R), float64(c.G), float64(c.B)}
			if current != nil {
				for i := range target {
					target[i] = min(255, max(0, target[i]+current[x+1][i]))
				}
			}
			best, second := nearestColors(target, colors)
			c = colors[best]
			if dither == "bayer4" {
				threshold := (float64(bayer[(y%4)*4+x%4]) + 0.5) / 16
				if orderedAmount(target, c, colors[second]) > threshold {
					c = colors[second]
				}
			}
			img.SetNRGBA(x, y, c)
			if current != nil {
				for i, v := range channels(c) {
					delta := (target[i] - float64(v)) / 16
					current[x+2][i] += 7 * delta
					next[x][i] += 3 * delta
					next[x+1][i] += 5 * delta
					next[x+2][i] += delta
				}
			}
		}
		if current != nil {
			current, next = next, current
			clear(next)
		}
	}
}
