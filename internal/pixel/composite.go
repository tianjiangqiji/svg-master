package pixel

import (
	"fmt"
	"image"
	"math"
)

type Layer struct {
	Image   *image.NRGBA
	Visible bool
	Opacity float64
	Blend   string
}

type blendMode uint8

const (
	normal blendMode = iota
	multiply
	screen
	overlay
	darken
	lighten
	dodge
	burn
	hardLight
	softLight
	difference
	exclusion
	hue
	saturation
	colorBlend
	luminosity
)

var blends = map[string]blendMode{
	"": normal, "normal": normal, "multiply": multiply, "screen": screen, "overlay": overlay, "darken": darken, "lighten": lighten,
	"color-dodge": dodge, "color-burn": burn, "hard-light": hardLight, "soft-light": softLight, "difference": difference,
	"exclusion": exclusion, "hue": hue, "saturation": saturation, "color": colorBlend, "luminosity": luminosity,
}

func ValidBlend(mode string) bool {
	_, ok := blends[mode]
	return ok
}

const (
	alphaBits = 24
	alphaOne  = 1 << alphaBits
	alphaHalf = alphaOne / 2
)

type compositeSource struct {
	img     *image.NRGBA
	opacity float64
	mode    blendMode
	alpha   [256]uint32
}

// Composite flattens straight-alpha layer pixels in bottom-to-top order using Canvas source-over and CSS blend formulas.
func Composite(w, h int, layers []Layer) (*image.NRGBA, error) {
	out, err := New(w, h)
	if err != nil {
		return nil, err
	}
	visible := make([]compositeSource, 0, len(layers))
	allNormal := true
	for i, l := range layers {
		if !l.Visible {
			continue
		}
		if l.Image == nil || l.Image.Bounds() != out.Bounds() {
			return nil, fmt.Errorf("图层 %d 的尺寸与画布不一致", i+1)
		}
		mode, ok := blends[l.Blend]
		if !ok {
			return nil, fmt.Errorf("未知图层混合模式 %q", l.Blend)
		}
		if math.IsNaN(l.Opacity) || l.Opacity < 0 || l.Opacity > 1 {
			return nil, fmt.Errorf("图层不透明度必须在 0 到 1 之间")
		}
		if l.Opacity > 0 {
			src := compositeSource{img: l.Image, opacity: l.Opacity, mode: mode}
			for a := range src.alpha {
				src.alpha[a] = uint32(math.Round(float64(a) / 255 * l.Opacity * alphaOne))
			}
			allNormal = allNormal && mode == normal
			visible = append(visible, src)
		}
	}
	if len(visible) == 0 {
		return out, nil
	}
	if allNormal {
		compositeNormal(out, visible)
		return out, nil
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			off := y*out.Stride + x*4
			compositeFloat(out.Pix[off:off+4], visible, x*4, y)
		}
	}
	return out, nil
}

func compositeNormal(out *image.NRGBA, layers []compositeSource) {
	// A layer can contribute at most two fixed-point units of alpha error and
	// at most 256 units of premultiplied RGB error. Use the original floating
	// calculation near byte-rounding boundaries to preserve the rendered result.
	uncertainty := 2048 * int64(len(layers))
	last := &layers[len(layers)-1]
	for y := 0; y < out.Rect.Dy(); y++ {
		for x := 0; x < out.Rect.Dx()*4; x += 4 {
			off := y*out.Stride + x
			dst := out.Pix[off : off+4]
			i := y*last.img.Stride + x
			if last.opacity == 1 && last.img.Pix[i+3] == 255 {
				copy(dst, last.img.Pix[i:i+4])
				continue
			}
			var alpha uint32
			var back [3]uint32
			for j := range layers {
				l := &layers[j]
				i := y*l.img.Stride + x
				src := l.img.Pix[i : i+4]
				a := l.alpha[src[3]]
				if a == 0 {
					continue
				}
				if a == alphaOne && l.opacity == 1 {
					alpha = alphaOne
					for k := range back {
						back[k] = uint32(src[k]) * alphaOne
					}
					continue
				}
				rest := uint64(alphaOne - a)
				alpha = a + uint32((uint64(alpha)*rest+alphaHalf)>>alphaBits)
				for k := range back {
					back[k] = uint32(src[k])*a + uint32((uint64(back[k])*rest+alphaHalf)>>alphaBits)
				}
			}
			if alpha == 0 {
				continue
			}
			alphaByte := uint64(alpha)*255 + alphaHalf
			fraction := int64(alphaByte & (alphaOne - 1))
			if 2*min(fraction, alphaOne-fraction) <= uncertainty {
				compositeFloat(dst, layers, x, y)
				continue
			}
			if alphaByte < alphaOne {
				continue
			}
			dst[3] = uint8(alphaByte >> alphaBits)
			for k, value := range back {
				if withinRoundingError(2*int64(value%alpha)-int64(alpha), uncertainty) {
					compositeFloat(dst, layers, x, y)
					break
				}
				dst[k] = uint8((value + alpha/2) / alpha)
			}
		}
	}
}

func withinRoundingError(distance, uncertainty int64) bool {
	return -uncertainty <= distance && distance <= uncertainty
}

func compositeFloat(dst []byte, layers []compositeSource, x, y int) {
	var back [3]float64
	alpha := 0.0
	for i := range layers {
		l := &layers[i]
		off := y*l.img.Stride + x
		c := l.img.Pix[off : off+4]
		if c[3] == 0 {
			continue
		}
		a := float64(c[3]) / 255 * l.opacity
		src := [3]float64{float64(c[0]) / 255, float64(c[1]) / 255, float64(c[2]) / 255}
		if alpha == 0 || a == 1 && l.mode == normal {
			back, alpha = src, a
			continue
		}
		mix := blend(back, src, l.mode)
		combined := a + alpha*(1-a)
		for k, v := range src {
			back[k] = (a*((1-alpha)*v+alpha*mix[k]) + (1-a)*alpha*back[k]) / combined
		}
		alpha = combined
	}
	dst[3] = byteValue(alpha * 255)
	if dst[3] != 0 {
		for k, v := range back {
			dst[k] = byteValue(v * 255)
		}
	} else {
		clear(dst[:3])
	}
}

func blend(back, src [3]float64, mode blendMode) [3]float64 {
	switch mode {
	case hue:
		return setLum(setSat(src, sat(back)), lum(back))
	case saturation:
		return setLum(setSat(back, sat(src)), lum(back))
	case colorBlend:
		return setLum(src, lum(back))
	case luminosity:
		return setLum(back, lum(src))
	case normal:
		return src
	}
	var out [3]float64
	for i, b := range back {
		out[i] = blendComponent(b, src[i], mode)
	}
	return out
}

func blendComponent(b, s float64, mode blendMode) float64 {
	switch mode {
	case multiply:
		return b * s
	case screen:
		return b + s - b*s
	case overlay:
		return blendComponent(s, b, hardLight)
	case darken:
		return min(b, s)
	case lighten:
		return max(b, s)
	case dodge:
		if b == 0 {
			return 0
		}
		if s == 1 {
			return 1
		}
		return min(1, b/(1-s))
	case burn:
		if b == 1 {
			return 1
		}
		if s == 0 {
			return 0
		}
		return 1 - min(1, (1-b)/s)
	case hardLight:
		if s <= 0.5 {
			return 2 * b * s
		}
		return blendComponent(b, 2*s-1, screen)
	case softLight:
		if s <= 0.5 {
			return b - (1-2*s)*b*(1-b)
		}
		d := math.Sqrt(b)
		if b <= 0.25 {
			d = ((16*b-12)*b + 4) * b
		}
		return b + (2*s-1)*(d-b)
	case difference:
		return math.Abs(b - s)
	case exclusion:
		return b + s - 2*b*s
	default:
		return s
	}
}

func lum(c [3]float64) float64 { return 0.3*c[0] + 0.59*c[1] + 0.11*c[2] }

func sat(c [3]float64) float64 { return max(c[0], c[1], c[2]) - min(c[0], c[1], c[2]) }

func setLum(c [3]float64, l float64) [3]float64 {
	d := l - lum(c)
	for i := range c {
		c[i] += d
	}
	low, high := min(c[0], c[1], c[2]), max(c[0], c[1], c[2])
	if low < 0 {
		for i := range c {
			c[i] = l + (c[i]-l)*l/(l-low)
		}
	}
	if high > 1 {
		for i := range c {
			c[i] = l + (c[i]-l)*(1-l)/(high-l)
		}
	}
	return c
}

func setSat(c [3]float64, s float64) [3]float64 {
	low, high := min(c[0], c[1], c[2]), max(c[0], c[1], c[2])
	if high == low {
		return [3]float64{}
	}
	for i := range c {
		c[i] = (c[i] - low) * s / (high - low)
	}
	return c
}
