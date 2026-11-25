package pixel

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"strings"
	"testing"
)

func canvas(t *testing.T, w, h int) *image.NRGBA {
	t.Helper()
	img, err := New(w, h)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func paint(t *testing.T, img *image.NRGBA, ops ...DrawOp) DrawSummary {
	t.Helper()
	summary, err := Draw(img, ops, nil)
	if err != nil {
		t.Fatal(err)
	}
	return summary
}

func checkPixel(t *testing.T, img *image.NRGBA, x, y int, hex string) {
	t.Helper()
	if got := FormatColor(img.NRGBAAt(x, y)); got != hex {
		t.Errorf("pixel (%d,%d): got %s, want %s", x, y, got, hex)
	}
}

func TestSelectionMaskAndAtomicDraw(t *testing.T) {
	img := canvas(t, 200, 201)
	var selection Selection
	err := json.Unmarshal([]byte(`{"x":5,"y":8,"w":3,"h":3,"runs":[[0,1],[4,2],[8,1]]}`), &selection)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := Draw(img, []DrawOp{{Op: "clear", Color: "#36AB8701"}}, &selection)
	if err != nil {
		t.Fatal(err)
	}
	wantRegion := Rect{X: 5, Y: 8, W: 3, H: 3}
	if summary.ChangedPixels != 4 || summary.Region != wantRegion || !reflect.DeepEqual(summary.NewColors, []string{"#36AB8701"}) {
		t.Fatalf("unexpected change: %+v", summary)
	}
	checkPixel(t, img, 6, 8, "#00000000")
	checkPixel(t, img, 6, 9, "#36AB8701")
	checkPixel(t, img, 7, 10, "#36AB8701")
	before := append([]byte(nil), img.Pix...)
	if _, err := Draw(img, []DrawOp{{Op: "clear"}, {Op: "pixel", Color: "invalid"}}, nil); err == nil || !bytes.Equal(img.Pix, before) {
		t.Fatal("an invalid batch must leave every original pixel intact")
	}
	selection.Runs = [][]int{}
	if summary, err := Draw(img, []DrawOp{{Op: "clear"}}, &selection); err != nil || summary.ChangedPixels != 0 {
		t.Fatalf("empty mask changed artwork: %+v, %v", summary, err)
	}
	selection.Runs = [][]int{{0, 3}, {2, 1}}
	if err := ValidateSelection(&selection, 200, 201); err == nil {
		t.Fatal("overlapping mask runs accepted")
	}
}

func TestDeterministicDrawingPrimitives(t *testing.T) {
	img := canvas(t, 32, 32)
	summary := paint(t, img,
		DrawOp{Op: "line", X0: -1, Y0: 1, X1: 5, Y1: 1, Color: "#FF0000"},
		DrawOp{Op: "ellipse", CX: 13, CY: 8, RX: 4, RY: 2, Color: "#FFFF00"},
		DrawOp{Op: "polygon", Points: [][]int{{18, 3}, {26, 3}, {22, 10}}, Fill: true, Color: "#FFFFFF"},
		DrawOp{Op: "rect", X: 2, Y: 14, W: 8, H: 8, Color: "#00FF00"},
		DrawOp{Op: "flood_fill", X: 3, Y: 15, Color: "#0000FF"},
		DrawOp{Op: "blit", X: 10, Y: 24, Legend: map[string]string{"1": "#880000", "K": "#008800"}, Rows: []string{`rle:2.2\11K`, ".Z"}},
		DrawOp{Op: "mirror", X: 10, Y: 24, W: 14, H: 2, Axis: "x"},
	)
	if summary.OutOfBounds != 1 || len(summary.Warnings) != 2 {
		t.Fatalf("expected out-of-bounds, ragged, and unknown-symbol warnings: %+v", summary)
	}
	for _, p := range []struct {
		x, y  int
		color string
	}{
		{0, 1, "#FF0000"}, {5, 1, "#FF0000"}, {6, 1, "#00000000"},
		{9, 8, "#FFFF00"}, {17, 8, "#FFFF00"}, {13, 6, "#FFFF00"}, {13, 10, "#FFFF00"}, {13, 8, "#00000000"},
		{22, 6, "#FFFFFF"}, {19, 9, "#00000000"},
		{2, 14, "#00FF00"}, {3, 15, "#0000FF"}, {1, 15, "#00000000"},
		{11, 24, "#00000000"}, {12, 24, "#880000"}, {14, 24, "#008800"}, {19, 24, "#008800"},
	} {
		checkPixel(t, img, p.x, p.y, p.color)
	}
	isolated := canvas(t, 8, 8)
	paint(t, isolated, DrawOp{Op: "pixel", X: 3, Y: 3, Color: "#000000"}, DrawOp{Op: "outline", Color: "#FF00FF"})
	checkPixel(t, isolated, 2, 3, "#FF00FF")
	checkPixel(t, isolated, 2, 2, "#00000000")
	checkPixel(t, isolated, 1, 3, "#00000000")
}

func TestCompositeAndExactExport(t *testing.T) {
	bottom, top := canvas(t, 8, 8), canvas(t, 8, 8)
	paint(t, bottom, DrawOp{Op: "clear", Color: "#FF0000"})
	paint(t, top, DrawOp{Op: "clear", Color: "#0000FF"})
	base := Layer{Image: bottom, Visible: true, Opacity: 1}
	over := Layer{Image: top, Visible: true, Opacity: .5, Blend: "normal"}
	merged, err := Composite(8, 8, []Layer{base, over})
	if err != nil {
		t.Fatal(err)
	}
	checkPixel(t, merged, 0, 0, "#800080")
	over.Opacity, over.Blend = 1, "screen"
	merged, err = Composite(8, 8, []Layer{base, over})
	if err != nil {
		t.Fatal(err)
	}
	checkPixel(t, merged, 0, 0, "#FF00FF")
	over.Blend = "multiply"
	merged, err = Composite(8, 8, []Layer{base, over})
	if err != nil {
		t.Fatal(err)
	}
	checkPixel(t, merged, 0, 0, "#000000")
	raster := canvas(t, 8, 8)
	paint(t, raster, DrawOp{Op: "rect", X: 1, Y: 1, W: 2, H: 1, Fill: true, Color: "#36AB8701"})
	data, err := ExportPNG(raster, 4)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeLayer(data, 32, 32)
	if err != nil {
		t.Fatal(err)
	}
	checkPixel(t, decoded, 4, 4, "#36AB8701")
	checkPixel(t, decoded, 11, 7, "#36AB8701")
	checkPixel(t, decoded, 12, 7, "#00000000")
	if svg, err := ExportCrispSVG(raster); err != nil || !strings.Contains(svg, `fill-opacity="0.00392157"`) || !strings.Contains(svg, `x="1" y="1" width="2"`) || strings.Count(svg, "<rect ") != 1 {
		t.Fatalf("SVG did not preserve straight alpha and a single horizontal run: %s", svg)
	}
}

func TestQuantizationAndResize(t *testing.T) {
	wide := canvas(t, 16, 8)
	paint(t, wide, DrawOp{Op: "clear", Color: "#FF0000"})
	fit, palette, err := Quantize(wide, 8, 8, QuantizeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(palette, []string{"#FF0000"}) {
		t.Fatalf("palette: %v", palette)
	}
	checkPixel(t, fit, 0, 1, "#00000000")
	checkPixel(t, fit, 0, 2, "#FF0000")
	checkPixel(t, fit, 7, 5, "#FF0000")
	checkPixel(t, fit, 7, 6, "#00000000")
	cover, _, err := Quantize(wide, 8, 8, QuantizeOpts{Fit: "cover"})
	if err != nil {
		t.Fatal(err)
	}
	checkPixel(t, cover, 0, 0, "#FF0000")
	for x := 1; x < 16; x += 2 {
		for y := 0; y < 8; y++ {
			wide.SetNRGBA(x, y, color.NRGBA{B: 255})
		}
	}
	avg, _, err := Quantize(wide, 8, 8, QuantizeOpts{Fit: "stretch", Dither: "floyd"})
	if err != nil {
		t.Fatal(err)
	}
	checkPixel(t, avg, 0, 0, "#FF0000")
	resized, err := Resize(fit, 512, 200, "crop")
	if err != nil {
		t.Fatal(err)
	}
	checkPixel(t, resized, 7, 5, "#FF0000")
	checkPixel(t, resized, 511, 199, "#00000000")
	if _, err := New(MaxSize+1, 8); err == nil {
		t.Fatal("canvas over maximum size accepted")
	}
}

func TestInspectAndNonIndexedRegion(t *testing.T) {
	img := canvas(t, 10, 8)
	for i := range 80 {
		img.SetNRGBA(i%10, i/10, color.NRGBA{R: uint8(i), B: 42, A: 255})
	}
	region := Rect{X: 0, Y: 0, W: 10, H: 8}
	view, err := ReadRegion(img, region)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Rows) != 0 || len(view.Legend) != 81 || view.Indices[7][9] != 80 || view.Legend["80"] != "#4F002A" {
		t.Fatalf("high-color fallback lost entries: %+v", view)
	}
	plain, err := ReadRegion(img, Rect{X: 9, Y: 7, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Indices) != 0 || plain.Rows[0] != "0" || plain.Legend["0"] != "#4F002A" {
		t.Fatalf("unexpected ASCII region: %+v", plain)
	}
	data, stat, err := Inspect(img, img, InspectOpts{Mode: "compare_ref", ShowGrid: true})
	if err != nil {
		t.Fatal(err)
	}
	if stat.NonTransparentPixels != 80 || stat.ColorCount != 80 || stat.Comparison.ColorError != 0 || stat.Comparison.StructuralSimilarity != 1 || len(stat.Comparison.Grid) != 16 {
		t.Fatalf("unexpected image statistics: %+v, %+v", stat, stat.Comparison)
	}
	info, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if info.Width <= 512 || info.Width > 1024 {
		t.Fatalf("triple image dimensions: %dx%d", info.Width, info.Height)
	}
	_, stat, err = Inspect(img, canvas(t, 10, 8), InspectOpts{Mode: "compare_main", Region: &Rect{X: 8, Y: 6, W: 2, H: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if stat.Comparison.ColorError <= 0 || len(stat.Comparison.Grid) != 4 || stat.Comparison.Grid[0].Region != (Rect{X: 8, Y: 6, W: 1, H: 1}) {
		t.Fatalf("localized comparison: %+v", stat.Comparison)
	}
}
