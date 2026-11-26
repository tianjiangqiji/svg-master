package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"svg-master/internal/config"
	"svg-master/internal/llm"
	"svg-master/internal/pixel"
)

type pixelTestBridge struct {
	PixelBridge
	clip         *pixel.Selection
	read         pixel.Rect
	inspect      pixel.InspectOpts
	inspectImage string
	draws        int
	inspects     int
	width        int
	height       int
}

func (b *pixelTestBridge) CanvasInfo() PixelCanvasInfo {
	width, height := b.width, b.height
	if width == 0 {
		width, height = 256, 128
	}
	return PixelCanvasInfo{Width: width, Height: height, HasMainImage: true, RefIDs: []string{"ref_3"}}
}

func (b *pixelTestBridge) ModelCompositePNG() (string, error) { return "canvas-data", nil }
func (b *pixelTestBridge) FrameOperation(PixelFrameOperation) (PixelFrameInfo, error) {
	return PixelFrameInfo{}, nil
}

func (b *pixelTestBridge) Draw(_ string, _ []pixel.DrawOp, clip *pixel.Selection) (pixel.DrawSummary, error) {
	b.clip = clip
	b.draws++
	return pixel.DrawSummary{ChangedPixels: 1}, nil
}

func (b *pixelTestBridge) Inspect(opts pixel.InspectOpts) (string, pixel.InspectStats, error) {
	b.inspect = opts
	b.inspects++
	img := b.inspectImage
	if img == "" {
		img = "inspect-data"
	}
	return img, pixel.InspectStats{ColorCount: 1}, nil
}

func (b *pixelTestBridge) ReadRegion(_ string, rect pixel.Rect) (pixel.RegionView, error) {
	b.read = rect
	return pixel.RegionView{Rect: rect, Rows: []string{"..."}}, nil
}

func TestPixelTurnInputsSelectionAndInspection(t *testing.T) {
	fake := &fakeClient{steps: []fakeStep{
		toolStep("draw", ToolPixelDraw, map[string]any{
			"clipToSelection": false, "ops": []pixel.DrawOp{{Op: "clear", Color: "#112233"}},
		}),
		toolStep("inspect", ToolPixelInspect, pixel.InspectOpts{Mode: "compare_ref", RefID: "ref_3"}),
		textStep("已绘制和检查。"),
	}}
	bridge := &pixelTestBridge{}
	recorder := &recordingClient{inner: fake}
	deps := testDeps(fake, nil)
	deps.Client, deps.Pixel, deps.Vision = recorder, bridge, true
	deps.Settings.GenMode = config.GenModePixel
	deps.Settings.CompressOn = true
	engine := New(deps)
	selection := &pixel.Selection{Rect: pixel.Rect{X: 4, Y: 3, W: 4, H: 4}, Runs: [][]int{{0, 2}, {6, 2}}}
	_, err := engine.Run(context.Background(), Task{
		Prompt: "按主图布局，选区内换成参考图 #3 的人物",
		Pixel: &PixelInput{
			DraftMode: "quantize", MainImage: &llm.ImageData{Data: "main-data"},
			RefImages:  []PixelReference{{ID: "ref_3", Number: 3, Name: "角色", Image: llm.ImageData{Data: "ref-data"}}},
			HumanEdits: "人物区一像素", Selection: selection, RestrictToSelection: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if bridge.draws != 1 || bridge.clip != selection || bridge.inspects != 1 || bridge.inspect.RefID != "ref_3" {
		t.Fatalf("pixel tool inputs lost selection or reference: %+v", bridge)
	}
	if len(recorder.requests) != 3 {
		t.Fatalf("expected draw, inspect and completion requests, got %d", len(recorder.requests))
	}
	req := recorder.requests[0]
	if !req.Pixel || strings.Contains(req.System, "256×128") || !strings.Contains(req.Messages[1].Text(), "256×128") {
		t.Fatalf("incorrect pixel system instructions: %s", req.System)
	}
	for _, spec := range req.Tools {
		if spec.Name == ToolEmitSVG || spec.Name == ToolValidateSVG || isLayerTool(spec.Name) {
			t.Fatalf("SVG tool leaked into pixel request: %s", spec.Name)
		}
	}
	checkPixelAssetLabels(t, req.Messages[0], "【主图", "参考图 #3")
	checkPixelAssetLabels(t, req.Messages[1], "当前工程的合成画布")
	results := pixelTestResults(recorder.requests[2].Messages)
	if count := len(results); count != 2 || results[1].CallID != "inspect" || len(results[1].Images) != 1 ||
		results[1].Images[0].Data != "inspect-data" {
		t.Fatalf("inspection image did not reach the next request: %+v", results)
	}
	if pixelUserImageBytes(engine.History()[0]) != 0 || len(pixelTestResults(engine.History())[1].Images) != 1 {
		t.Fatal("history should persist bounded inspection results and refer to user assets by label")
	}
}

func TestPixelTextInspectionDropsOldVisionAttachments(t *testing.T) {
	fake := &fakeClient{steps: []fakeStep{
		toolStep("inspect", ToolPixelInspect, pixel.InspectOpts{Mode: "crop", Region: &pixel.Rect{X: 3, Y: 4, W: 36, H: 36}}),
		textStep("已检查。"),
	}}
	bridge := &pixelTestBridge{}
	recorder := &recordingClient{inner: fake}
	deps := testDeps(fake, nil)
	deps.Client, deps.Pixel = recorder, bridge
	old := llm.Block{
		Type:       llm.BlockToolResult,
		ToolResult: &llm.ToolResult{CallID: "old", Images: []llm.ImageData{{Data: "past"}}},
	}
	_, err := New(deps).Run(context.Background(), Task{
		Prompt:  "检查画布",
		History: []llm.Message{{Role: llm.RoleTool, Blocks: []llm.Block{old}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	results := pixelTestResults(recorder.requests[1].Messages)
	if len(results) != 2 || len(results[0].Images)+len(results[1].Images) != 0 ||
		!strings.Contains(results[1].Content, `"rows":["..."]`) || bridge.read != (pixel.Rect{X: 3, Y: 4, W: 32, H: 32}) {
		t.Fatalf("expected bounded textual inspection: results=%+v region=%+v", results, bridge.read)
	}
	if len(old.ToolResult.Images) != 1 {
		t.Fatal("projection modified source history")
	}
}

func TestPixelDrawRequiresInspectionBeforeSuccess(t *testing.T) {
	fake := &fakeClient{steps: []fakeStep{
		toolStep("draw", ToolPixelDraw, map[string]any{"ops": []pixel.DrawOp{{Op: "clear", Color: "#112233"}}}),
		textStep("已绘制。"),
	}}
	deps := testDeps(fake, nil)
	deps.Pixel = &pixelTestBridge{}
	_, err := New(deps).Run(context.Background(), Task{Prompt: "画成纯色"})
	if err == nil || !strings.Contains(err.Error(), "未完成自检") {
		t.Fatalf("unchecked drawing was reported as complete: %v", err)
	}
}

func TestPixelAnimationToolsAndInspectionAfterLastChange(t *testing.T) {
	for _, animated := range []bool{false, true} {
		seen := make(map[string]bool)
		for _, spec := range pixelToolSpecs(animated) {
			seen[spec.Name] = true
			if !json.Valid(spec.Parameters) {
				t.Fatalf("%s schema is invalid JSON", spec.Name)
			}
		}
		if seen[ToolPixelFrameOp] != animated || seen[ToolPixelAnimInfo] != animated || seen[ToolPixelCreateLayer] == animated ||
			seen[ToolPixelSetLayer] == animated || seen[ToolPixelDeleteLayer] == animated {
			t.Fatalf("unexpected animation=%v tool set: %v", animated, seen)
		}
	}
	fake := &fakeClient{steps: []fakeStep{
		toolStep("draw", ToolPixelDraw, map[string]any{"frameId": "F1", "ops": []pixel.DrawOp{{Op: "clear"}}}),
		toolStep("inspect", ToolPixelInspect, pixel.InspectOpts{Mode: "onion", FrameID: "F1"}),
		toolStep("duplicate", ToolPixelFrameOp, PixelFrameOperation{Action: "duplicate", FrameID: "F1"}),
		textStep("完成。"),
	}}
	base := &pixelTestBridge{}
	deps := testDeps(fake, nil)
	deps.Pixel = animationTestBridge{base}
	_, err := New(deps).Run(context.Background(), Task{Prompt: "制作动画"})
	if err == nil || !strings.Contains(err.Error(), "未完成自检") || base.inspect.FrameID != "F1" {
		t.Fatalf("final frame mutation must be followed by inspection, got %+v and %v", base.inspect, err)
	}
}

type animationTestBridge struct {
	*pixelTestBridge
}

func (b animationTestBridge) CanvasInfo() PixelCanvasInfo {
	info := b.pixelTestBridge.CanvasInfo()
	info.Mode = pixelAnimMode
	return info
}

func TestPixelProjectionRetainsCurrentAttachmentsAndToolPairs(t *testing.T) {
	image := llm.ImageData{Data: "still-here"}
	inputs := []llm.Message{{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockImage, Image: &image}}}}
	for _, id := range []string{"old", "newer", "last"} {
		inputs = append(inputs,
			llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolCall, ToolCall: &llm.ToolCall{ID: id}}}},
			llm.Message{Role: llm.RoleTool, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolResult: &llm.ToolResult{CallID: id, Images: []llm.ImageData{image}}}}},
		)
	}
	bounded := projectPixelImages(inputs, 0, 2, 0)
	results := pixelTestResults(bounded)
	if len(results) != 3 || len(results[0].Images) != 0 || len(results[1].Images) != 1 || len(results[2].Images) != 1 ||
		pixelUserImageBytes(bounded[0]) == 0 || len(pixelTestResults(inputs)[0].Images) != 1 {
		t.Fatal("bounded pixel projection lost active assets, tool pairs or source history")
	}

	comp := newCompressor(16000, 75).(*compressor)
	comp.protectPixelTurn(0, 0)
	comp.Project(bounded, 0)
	created, _, warn, err := comp.Compress([]CompressRange{
		{StartRef: "m00001", EndRef: "m00001", Summary: "cannot hide the current user"},
		{StartRef: "m00002", EndRef: "m00002", Summary: "hide an old tool pair together"},
	})
	if err != nil || created != 1 || len(warn) != 1 {
		t.Fatalf("active attachment protection failed: created %d, warnings %v, error %v", created, warn, err)
	}
	projected, _ := comp.Project(bounded, 0)
	if len(projected) != len(bounded)-1 || pixelUserImageBytes(projected[0]) == 0 || len(pixelTestResults(projected)) != 2 {
		t.Fatal("manual compression hid an active asset or orphaned an old tool pair")
	}
}

func pixelTestResults(msgs []llm.Message) []*llm.ToolResult {
	var results []*llm.ToolResult
	for _, msg := range msgs {
		for _, part := range msg.Blocks {
			if part.ToolResult != nil {
				results = append(results, part.ToolResult)
			}
		}
	}
	return results
}
