package pixel

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"strings"
	"testing"
)

func TestLargeCanvasPNGRoundTripAndExportBounds(t *testing.T) {
	img := canvas(t, MaxSize, MaxSize)
	if size := canvas(t, 1555, 1999).Bounds().Size(); size != image.Pt(1555, 1999) {
		t.Fatalf("custom size: %v", size)
	}
	if _, err := ExportPNG(img, 8); err == nil {
		t.Fatal("8× export exceeded the limits but was accepted")
	}
	data, err := ExportPNG(img, 4)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != 8192 || cfg.Height != 8192 {
		t.Fatalf("4× export: %+v, %v", cfg, err)
	}
	// A high-entropy layer exercises the encoded-size overhead above 16 MiB of RGBA.
	rng := rand.NewChaCha8([32]byte{42})
	if _, err := rng.Read(img.Pix); err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	if encoded.Len() <= len(img.Pix) {
		t.Fatalf("fixture did not exercise encoding overhead: %d", encoded.Len())
	}
	restored, err := DecodeLayer(encoded.Bytes(), MaxSize, MaxSize)
	if err != nil || !bytes.Equal(restored.Pix, img.Pix) {
		t.Fatalf("large layer round trip: %v (encoded length %d)", err, encoded.Len())
	}
}

func TestLimitedBlitAndCrispSVG(t *testing.T) {
	img := canvas(t, MaxSize, 8)
	base := DrawOp{Op: "blit", Legend: map[string]string{"K": "#153000"}}
	for _, rows := range [][]string{{strings.Repeat("K", MaxBlitSpan+1)}, {"rle:129K"}, make([]string, MaxBlitSpan+1)} {
		base.Rows = rows
		if _, err := Draw(img, []DrawOp{base}, nil); err == nil {
			t.Fatalf("oversized blit accepted (rows=%d width=%d)", len(rows), len(rows[0]))
		}
	}
	base.Rows = []string{"rle:128K"}
	if got := paint(t, img, base).ChangedPixels; got != MaxBlitSpan {
		t.Fatalf("128 px blit changed %d pixels", got)
	}
	dense := canvas(t, 512, 512)
	for i := range maxCrispSVGRects + 1 {
		dense.Pix[4*i] = uint8(i % 2)
		dense.Pix[4*i+3] = 255
	}
	if svg, err := ExportCrispSVG(dense); err == nil || svg != "" || !strings.Contains(err.Error(), "PNG") {
		t.Fatalf("dense SVG yielded %d bytes, %v", len(svg), err)
	}
}

func TestNormalCompositeMatchesPriorRounding(t *testing.T) {
	const w, h = 256, 193
	rng := rand.NewChaCha8([32]byte{43})
	opacities := []float64{0.5, 0.01, 0.94, 1, 0.4, 0.6, 0.99, 1, 1 / 255.0, 0.75837, 0.001, 1, 1, 0.96}
	layers := make([]Layer, len(opacities))
	for i, opacity := range opacities {
		img := canvas(t, w, h)
		if _, err := rng.Read(img.Pix); err != nil {
			t.Fatal(err)
		}
		layers[i] = Layer{Image: img, Visible: true, Opacity: opacity}
	}
	for _, n := range []int{1, 2, 3, 4, 8, len(layers)} {
		fixed, err := Composite(w, h, layers[:n])
		if err != nil {
			t.Fatal(err)
		}
		// An identity step using multiply dispatches the established floating
		// compositor while exercising the same normal layers and opacities.
		identity := canvas(t, w, h)
		mixed := append(append([]Layer(nil), layers[:n]...), Layer{Image: identity, Visible: true, Opacity: 1, Blend: "multiply"})
		previous, err := Composite(w, h, mixed)
		if err != nil {
			t.Fatal(err)
		}
		for i, got := range fixed.Pix {
			if got != previous.Pix[i] {
				t.Fatalf("%d layers, byte %d: fixed-point result %d, prior result %d", n, i, got, previous.Pix[i])
			}
		}
	}
}

func TestFrameShiftAndInspection(t *testing.T) {
	frame := canvas(t, 8, 8)
	frame.SetNRGBA(7, 1, color.NRGBA{R: 255, A: 253})
	frame.SetNRGBA(0, 7, color.NRGBA{R: 33, G: 52, B: 91, A: 1})
	checkPixel(t, Shift(frame, 1, -1, false), 1, 6, "#21345B01")
	checkPixel(t, Shift(frame, 0, -100000, false), 0, 7, "#00000000")
	shifted := Shift(frame, 17, -17, true)
	checkPixel(t, shifted, 0, 0, "#FF0000FD")
	checkPixel(t, shifted, 1, 6, "#21345B01")
	checkPixel(t, frame, 7, 1, "#FF0000FD")

	current, previous := canvas(t, 8, 8), canvas(t, 8, 8)
	current.SetNRGBA(1, 0, color.NRGBA{R: 190, A: 255})
	previous.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	previous.SetNRGBA(1, 0, color.NRGBA{R: 255, A: 255})
	onionPNG, stats, err := InspectOnion(current, previous)
	if err != nil || stats.NonTransparentPixels != 1 {
		t.Fatalf("onion inspection stats %+v, %v", stats, err)
	}
	onion, err := png.Decode(bytes.NewReader(onionPNG))
	if err != nil {
		t.Fatal(err)
	}
	checkAt := func(img image.Image, x, y int, want color.NRGBA) {
		t.Helper()
		if got := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA); got != want {
			t.Errorf("inspection pixel (%d,%d) = %v, want %v", x, y, got, want)
		}
	}
	checkAt(onion, inspectLeft+2, inspectTop-14, frameInk) // x-axis label "0"
	checkAt(onion, inspectLeft+2, inspectTop+2, color.NRGBA{R: 199, G: 216, B: 243, A: 255})
	checkAt(onion, inspectLeft+125, inspectTop+2, color.NRGBA{R: 190, A: 255})
	filmPNG, stats, err := InspectFilmstrip([]*image.NRGBA{previous, current}, []int{0, 7})
	if err != nil || len(stats.Frames) != 2 || stats.Frames[1].Index != 7 || stats.Frames[0].NonTransparentPixels != 2 {
		t.Fatalf("filmstrip stats %+v, %v", stats, err)
	}
	film, err := png.Decode(bytes.NewReader(filmPNG))
	if err != nil {
		t.Fatal(err)
	}
	if b := film.Bounds(); b.Dx() <= b.Dy() || b.Dx() > inspectMaxSide {
		t.Fatalf("filmstrip bounds: %v", b)
	}
	checkAt(film, 6, 4, frameInk) // first frame number prefix "#"
	if _, _, err := InspectFilmstrip(make([]*image.NRGBA, 9), nil); err == nil {
		t.Fatal("filmstrip accepted more than eight frames")
	}
}

func TestLargeInspectionOverviewAndLocalCrop(t *testing.T) {
	img := canvas(t, 1441, MaxSize)
	img.SetNRGBA(1409, 2000, color.NRGBA{G: 255, A: 255})
	for _, mode := range []string{"composite", "compare_main"} {
		data, stats, err := Inspect(img, img, InspectOpts{Mode: mode})
		if err != nil || stats.NonTransparentPixels != 1 || stats.Region.W != 1441 || stats.Region.H != MaxSize {
			t.Fatalf("%s stats %+v, error %v", mode, stats, err)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil || max(cfg.Width, cfg.Height) > inspectMaxSide {
			t.Fatalf("%s PNG configuration %+v, error %v", mode, cfg, err)
		}
	}
	region := Rect{X: 1408, Y: 1999, W: 8, H: 8}
	if _, stats, err := Inspect(img, nil, InspectOpts{Mode: "crop", Region: &region, ShowGrid: true}); err != nil || stats.Region != region || stats.NonTransparentPixels != 1 {
		t.Fatalf("crop stats %+v, error %v", stats, err)
	}
}
