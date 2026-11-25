package project

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"svg-master/internal/llm"
	"svg-master/internal/pixel"
)

func TestPixelLargeCanvasPNGRoundTrip(t *testing.T) {
	p, err := CreatePixel(t.TempDir(), "large", KindUser, 2048, 2048)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 2048, 2048))
	random := rand.NewChaCha8([32]byte{1})
	_, _ = random.Read(img.Pix)
	id := p.Meta().ActiveLayer
	if _, err := p.PixelApplyHuman(id, img, nil, nil); err != nil {
		t.Fatal(err)
	}
	encoded, err := p.PixelLayerPNG(id)
	if err != nil || len(encoded) <= 16<<20 || len(encoded) > pixel.MaxLayerPNGBytes {
		t.Fatalf("high-entropy PNG size %d, error %v", len(encoded), err)
	}
	target := filepath.Join(t.TempDir(), "save-as")
	if err := p.SaveAs(target); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(target, KindUser)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := reopened.PixelLayer(id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.Pix, img.Pix) {
		t.Fatal("large straight-alpha PNG changed during save and reopen")
	}
}

func TestPixelLayerCapacityTracksCanvasSides(t *testing.T) {
	p, err := CreatePixel(t.TempDir(), "small", KindUser, 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	for len(p.Layers()) < 32 {
		if _, err := p.PixelAddLayer("", "", 0, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.PixelAddLayer("", "", 0, nil); err == nil {
		t.Fatal("allowed a 33rd small static layer")
	}
	before := p.Meta()
	if err := p.PixelResize(1025, 8, "crop"); err == nil || !strings.Contains(err.Error(), "14") {
		t.Fatalf("oversized resize did not enforce its layer cap: %v", err)
	}
	if got := p.Meta().Pixel; got.Revision != before.Pixel.Revision || got.Width != 8 {
		t.Fatal("the rejected resize modified the document")
	}
	for len(p.Layers()) > 14 {
		if err := p.PixelDelete(""); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.PixelResize(8, 1025, "crop"); err != nil {
		t.Fatal(err)
	}
	if err := p.PixelDuplicate(""); err == nil {
		t.Fatal("allowed a 15th layer on a tall canvas")
	}
	if _, err := p.PixelAddLayer("", "", 0, nil); err == nil {
		t.Fatal("allowed a 15th layer on a tall canvas")
	}
	if reopened, err := Open(p.Dir(), KindUser); err != nil || len(reopened.Layers()) != 14 {
		t.Fatalf("could not reopen resized project: %v", err)
	}
}

func TestPixelCompositeCacheReturnsIndependentImages(t *testing.T) {
	p, err := CreatePixel(t.TempDir(), "composite", KindUser, 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.PixelDraw("", []pixel.DrawOp{{Op: "clear", Color: "#ff0000"}}, nil); err != nil {
		t.Fatal(err)
	}
	first, err := p.PixelComposite()
	if err != nil {
		t.Fatal(err)
	}
	cache := p.pixelComposite
	first.SetNRGBA(0, 0, color.NRGBA{})
	second, err := p.PixelComposite()
	if err != nil {
		t.Fatal(err)
	}
	if second.NRGBAAt(0, 0) != (color.NRGBA{R: 255, A: 255}) || p.pixelComposite != cache {
		t.Fatal("same revision did not reuse an intact cached composite")
	}
	if _, err := p.PixelProps("", LayerProps{Visible: new(false)}, false); err != nil {
		t.Fatal(err)
	}
	hidden, err := p.PixelComposite()
	if err != nil || hidden.NRGBAAt(0, 0).A != 0 || p.pixelComposite == cache {
		t.Fatalf("visibility revision did not invalidate the composite: %v", err)
	}
}

func TestPixelAnimationFramesPersistAndFollowLock(t *testing.T) {
	p, err := CreatePixel(t.TempDir(), "animation", KindUser, 8, 8, PixelModeAnim)
	if err != nil {
		t.Fatal(err)
	}
	meta := p.Meta()
	first := meta.Pixel.Anim.Frames[0]
	if meta.Pixel.Mode != PixelModeAnim || len(meta.Layers) != 0 || meta.Pixel.Anim.FPS != 8 || !meta.Pixel.Anim.Loop || !p.IsEmpty() {
		t.Fatalf("incorrect initial animation: %+v", meta)
	}
	if _, err := p.PixelApplyFrameHuman(first.ID, nil, &pixel.Rect{X: 1, Y: 2, W: 1, H: 1}, []byte{90, 40, 7, 255}); err != nil {
		t.Fatal(err)
	}
	edit := p.Meta().Pixel.HumanEdits[0]
	if edit.FrameID != first.ID || edit.FrameName != first.Name || edit.ChangedPixels != 1 {
		t.Fatalf("frame hand edit lost its identity: %+v", edit)
	}
	if _, err := p.PixelFrameOperation(PixelFrameOp{Action: "locked", FrameID: first.ID, Locked: new(true)}, false); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []PixelFrameOp{
		{Action: "set", FrameID: first.ID, Locked: new(false)},
		{Action: "delete", FrameID: first.ID},
		{Action: "shift", FrameID: first.ID, DX: 1},
	} {
		if _, err := p.PixelFrameOperation(operation, true); !errors.Is(err, ErrLocked) {
			t.Fatalf("locked frame accepted AI action %q: %v", operation.Action, err)
		}
	}
	if _, _, err := p.PixelFrameDraw(first.ID, []pixel.DrawOp{{Op: "clear", Color: "#123456"}}, nil); !errors.Is(err, ErrLocked) {
		t.Fatalf("AI draw was allowed on a locked frame: %v", err)
	}

	firstBytes, err := p.PixelFramePNG(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := p.PixelFrameOperation(PixelFrameOp{Action: "duplicate", SourceFrameID: first.ID, Name: "运动", DelayMult: new(3)}, true)
	if err != nil {
		t.Fatal(err)
	}
	duplicateBytes, err := p.PixelFramePNG(duplicate.ID)
	if err != nil || !bytes.Equal(firstBytes, duplicateBytes) || duplicate.ID == first.ID || duplicate.Locked {
		t.Fatalf("duplicating a keyframe lost content or reused its locked identity: %v", err)
	}
	if _, err := p.PixelFrameOperation(PixelFrameOp{Action: "shift", FrameID: duplicate.ID, DX: -2, Wrap: true}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.Dir(), "frames", duplicate.File)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the superseded frame file was left behind: %v", err)
	}
	if _, err := p.PixelFrameOperation(PixelFrameOp{Action: "reorder", FrameID: duplicate.ID, Index: new(1)}, false); err != nil {
		t.Fatal(err)
	}
	if err := p.PixelResize(10, 9, "crop"); err != nil {
		t.Fatal(err)
	}
	if err := p.PixelResize(513, 9, "crop"); err == nil {
		t.Fatal("animation was resized above its canvas cap")
	}
	target := filepath.Join(t.TempDir(), "copy")
	if err := p.SaveAs(target); err != nil {
		t.Fatal(err)
	}
	p, err = Open(target, KindUser)
	if err != nil {
		t.Fatal(err)
	}
	anim := p.Meta().Pixel.Anim
	if anim.Frames[0].ID != duplicate.ID || anim.Frames[0].DelayMult != 3 || !anim.Frames[1].Locked ||
		anim.ActiveFrameID != duplicate.ID || p.IsEmpty() {
		t.Fatalf("lost animation metadata after reopen: %+v", anim)
	}
	composite, err := p.PixelComposite()
	if err != nil || composite.Bounds() != image.Rect(0, 0, 10, 9) || composite.NRGBAAt(7, 2) != (color.NRGBA{R: 90, G: 40, B: 7, A: 255}) {
		t.Fatalf("active frame pixel placement or dimensions did not survive save: %v", err)
	}
}

func TestPixelModeSwitchAndAnimationCheckpoint(t *testing.T) {
	p, err := CreatePixel(t.TempDir(), "convert", KindUser, 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.PixelAddLayer("", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.PixelSetMode(PixelModeAnim, false); err == nil {
		t.Fatal("switch silently flattened multiple layers")
	}
	if _, _, err := p.PixelDraw("", []pixel.DrawOp{{Op: "clear", Color: "#a01234"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.PixelSetMode(PixelModeAnim, true); err != nil {
		t.Fatal(err)
	}
	first := p.Meta().Pixel.Anim.ActiveFrameID
	if err := p.BeginPixelTurn("vision"); err != nil {
		t.Fatal(err)
	}
	second, err := p.PixelFrameOperation(PixelFrameOp{Action: "add"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.PixelFrameDraw(first, []pixel.DrawOp{{Op: "clear", Color: "#0000ff"}}, nil); err != nil {
		t.Fatal(err)
	}
	if p.Meta().Pixel.Anim.ActiveFrameID != first {
		t.Fatal("drawing an explicit frame should focus that frame")
	}
	if err := p.EndPixelTurn([]llm.Message{llm.TextMsg(llm.RoleUser, "animate")}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := p.PixelFrameOperation(PixelFrameOp{Action: "fps", FPS: new(12)}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := p.PixelFrameOperation(PixelFrameOp{Action: "loop", Loop: new(false)}, false); err != nil {
		t.Fatal(err)
	}
	if err := p.PixelCheckpoint("undo"); err != nil {
		t.Fatal(err)
	}
	anim := p.Meta().Pixel.Anim
	img, err := p.PixelFrameImage(first)
	if err != nil || img.NRGBAAt(0, 0) != (color.NRGBA{R: 160, G: 18, B: 52, A: 255}) ||
		len(anim.Frames) != 1 || anim.FPS != 12 || anim.Loop {
		t.Fatalf("undo lost the original raster or new playback settings: %v, %+v", err, anim)
	}
	if err := p.PixelCheckpoint("redo"); err != nil {
		t.Fatal(err)
	}
	anim = p.Meta().Pixel.Anim
	img, err = p.PixelFrameImage(first)
	if err != nil || img.NRGBAAt(0, 0).B != 255 || len(anim.Frames) != 2 || anim.Frames[1].ID != second.ID {
		t.Fatalf("redo lost the animation turn: %v, %+v", err, anim)
	}
	if err := p.PixelSetMode(PixelModeStatic, true); !errors.Is(err, ErrPixelModeLocked) {
		t.Fatalf("mode switched after initial AI turn: %v", err)
	}
	p, err = Open(p.Dir(), KindUser)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.PixelSetMode(PixelModeStatic, true); !errors.Is(err, ErrPixelModeLocked) {
		t.Fatalf("reopening lost the mode lock: %v", err)
	}
	if err := p.ResetPixel(); err != nil {
		t.Fatal(err)
	}
	if meta := p.Meta().Pixel; meta.ModeLocked || meta.Mode != PixelModeAnim || len(meta.Anim.Frames) != 1 {
		t.Fatalf("reset should unlock and preserve animation as a single blank frame: %+v", meta)
	}
	if err := p.PixelSetMode(PixelModeStatic, false); err != nil {
		t.Fatal(err)
	}
	if !p.IsEmpty() || len(p.Layers()) != 1 {
		t.Fatal("reset and conversion should leave a single blank layer")
	}
}

func TestPixelModeSwitchPreservesHiddenRaster(t *testing.T) {
	p, err := CreatePixel(t.TempDir(), "hidden", KindUser, 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.PixelDraw("", []pixel.DrawOp{{Op: "clear", Color: "#ff000080"}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := p.PixelProps("", LayerProps{Visible: new(false), Locked: new(true)}, false); err != nil {
		t.Fatal(err)
	}
	if err := p.PixelSetMode(PixelModeAnim, false); err != nil {
		t.Fatal(err)
	}
	frame := p.Meta().Pixel.Anim.Frames[0]
	img, err := p.PixelFrameImage(frame.ID)
	if err != nil || frame.Visible || !frame.Locked || img.NRGBAAt(0, 0) != (color.NRGBA{R: 255, A: 128}) {
		t.Fatalf("shape switch lost an editable hidden raster or its protection: %v", err)
	}
	if err := p.PixelSetMode(PixelModeStatic, false); err != nil {
		t.Fatal(err)
	}
	layer := p.Layers()[0]
	img, err = p.PixelLayer(layer.ID)
	if err != nil || layer.Visible || !layer.Locked || img.NRGBAAt(0, 0) != (color.NRGBA{R: 255, A: 128}) {
		t.Fatalf("back conversion lost the hidden raster or its protection: %v", err)
	}
}
