package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"svg-master/internal/agent"
	"svg-master/internal/config"
	"svg-master/internal/pixel"
	"svg-master/internal/project"
)

func pixelTestRequest(t *testing.T, srv *httptest.Server, method, path string, payload any, status int) []byte {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-App-Token", "test-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status {
		t.Fatalf("%s %s = %d, want %d: %.500s", method, path, resp.StatusCode, status, raw)
	}
	return raw
}

func pixelTestSnapshot(t *testing.T, raw []byte) projectSnapshot {
	t.Helper()
	var result struct {
		Project projectSnapshot
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Project.Engine != project.EnginePixel || result.Project.ProjectID == "" {
		t.Fatalf("not a pixel project: %.400s", raw)
	}
	return result.Project
}

func pixelTestVersion(snap projectSnapshot, values map[string]any) map[string]any {
	if values == nil {
		values = make(map[string]any)
	}
	values["projectId"] = snap.ProjectID
	values["revision"] = snap.Revision
	return values
}

func pixelTestImage(t *testing.T) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	img.SetNRGBA(1, 0, color.NRGBA{R: 230, G: 70, B: 55, A: 255})
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(data.Bytes())
}

func TestPixelProjectAssetsRevisionsAndSaveAs(t *testing.T) {
	_, srv := newTestAPI(t, "http://127.0.0.1:1")
	call := func(method, route string, body any, status int) []byte {
		return pixelTestRequest(t, srv, method, route, body, status)
	}
	snap := pixelTestSnapshot(t, call("POST", "/api/project/create", map[string]any{
		"engine": "pixel", "name": "像素工程", "dir": t.TempDir(),
		"pixel": map[string]int{"width": 200, "height": 240},
	}, 200))
	if snap.Pixel.Width != 200 || snap.Pixel.Height != 240 || len(snap.Layers) != 1 || snap.SVG != "" {
		t.Fatalf("unexpected initial snapshot: %+v", snap)
	}
	original := snap
	for _, role := range []string{"main", "ref", "ref"} {
		snap = pixelTestSnapshot(t, call("POST", "/api/pixel/assets", pixelTestVersion(snap, map[string]any{
			"role": role, "name": role + ".png", "data": pixelTestImage(t),
		}), 200))
	}
	snap = pixelTestSnapshot(t, call("DELETE", "/api/pixel/assets", pixelTestVersion(snap, map[string]any{"id": "ref_1"}), 200))
	snap = pixelTestSnapshot(t, call("POST", "/api/pixel/assets", pixelTestVersion(snap, map[string]any{
		"role": "ref", "data": pixelTestImage(t),
	}), 200))
	if len(snap.Pixel.RefImages) != 2 || snap.Pixel.RefImages[0].ID != "ref_2" || snap.Pixel.RefImages[1].ID != "ref_3" {
		t.Fatalf("reference IDs changed: %+v", snap.Pixel.RefImages)
	}
	call("POST", "/api/pixel/quantize", pixelTestVersion(original, nil), http.StatusConflict)
	snap = pixelTestSnapshot(t, call("POST", "/api/pixel/quantize", pixelTestVersion(snap, map[string]any{
		"maxColors": 8, "fit": "fit",
	}), 200))
	if snap.Pixel.DraftLayerID == "" || snap.Pixel.DraftLayerID != snap.Active || len(snap.Pixel.Palette) == 0 {
		t.Fatalf("invalid draft snapshot: %+v", snap)
	}
	change := pixelTestVersion(snap, map[string]any{
		"layerId": snap.Active, "region": map[string]int{"x": 0, "y": 0, "w": 1, "h": 1},
		"rgbaBase64": base64.StdEncoding.EncodeToString([]byte{91, 187, 45, 1}),
	})
	raw := call("PUT", "/api/pixel/layer", change, 200)
	var result struct{ HumanEdit project.HumanEdit }
	if err := json.Unmarshal(raw, &result); err != nil || result.HumanEdit.ChangedPixels != 1 {
		t.Fatalf("human edit missing: %.400s %v", raw, err)
	}
	snap = pixelTestSnapshot(t, raw)
	call("PUT", "/api/pixel/layer", change, http.StatusConflict)
	target := filepath.Join(t.TempDir(), "saved")
	call("POST", "/api/project/saveas", map[string]string{"dir": target}, 200)
	snap = pixelTestSnapshot(t, call("POST", "/api/project/open", map[string]string{"mode": "project", "dir": target, "engine": "pixel"}, 200))
	asset := call("GET", snap.Pixel.MainImage.URL, nil, 200)
	if base64.StdEncoding.EncodeToString(asset) != pixelTestImage(t) {
		t.Fatal("save-as did not keep the original main image")
	}
	rgba := call("GET", "/api/pixel/layer?format=rgba&id="+snap.Active, nil, 200)
	if !bytes.Equal(rgba[:4], []byte{91, 187, 45, 1}) {
		t.Fatalf("low-alpha pixel did not round trip: %v", rgba[:4])
	}
	pngData := call("GET", "/api/pixel/export?format=png&scale=2", nil, 200)
	cfg, err := png.DecodeConfig(bytes.NewReader(pngData))
	if err != nil || cfg.Width != 400 || cfg.Height != 480 {
		t.Fatalf("bad scaled PNG: %+v %v", cfg, err)
	}
	svg := call("GET", "/api/pixel/export?format=svg", nil, 200)
	if !bytes.Contains(svg, []byte("fill-opacity")) || !bytes.Contains(svg, []byte("crispEdges")) {
		t.Fatalf("invalid crisp SVG: %.400s", svg)
	}
	call("POST", "/api/chat", map[string]string{"prompt": "SVG"}, http.StatusConflict)
	snap = pixelTestSnapshot(t, call("POST", "/api/session/drop", map[string]any{}, 200))
	if len(snap.Layers) != 1 || !snap.Empty || snap.Pixel.MainImage != nil || len(snap.Pixel.RefImages) != 0 {
		t.Fatalf("reset retained pixel content: %+v", snap)
	}
}

func TestPixelChatVisualInspectionAndSelectionCheckpoint(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		step := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		var item string
		switch step {
		case 1:
			if !strings.Contains(string(raw), "参考图 #1") || !bytes.Contains(raw, []byte("data:image/png;base64,")) {
				t.Errorf("the model did not receive labeled reference images")
			}
			item = "pixel_create_layer"
		case 2:
			item = "pixel_draw"
		case 3:
			item = "pixel_inspect"
		case 4:
			if !bytes.Contains(raw, []byte("function_call_output")) || bytes.Count(raw, []byte("data:image/png;base64,")) < 2 {
				t.Errorf("the model did not receive the visual tool result")
			}
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"修改完成\"}\n\n")
		}
		if item != "" {
			args := map[string]string{
				"pixel_create_layer": "{\"name\":\"AI 图层\"}",
				"pixel_draw":         "{\"ops\":[{\"op\":\"rect\",\"x\":0,\"y\":0,\"w\":16,\"h\":16,\"fill\":true,\"color\":\"#FF0000\"}]}",
				"pixel_inspect":      "{\"mode\":\"compare_ref\",\"refId\":\"ref_1\"}",
			}[item]
			data, _ := json.Marshal(map[string]any{
				"type": "response.output_item.done", "output_index": 0,
				"item": map[string]any{"type": "function_call", "id": item, "call_id": item, "name": item, "arguments": args},
			})
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer upstream.Close()
	s, srv := newTestAPI(t, upstream.URL)
	settings := s.store.Settings()
	vision := true
	settings.Providers[0].ModelCaps = map[string]config.PartialCaps{"test": {Vision: &vision}}
	if err := s.store.Save(settings); err != nil {
		t.Fatal(err)
	}
	call := func(method, route string, body any, status int) []byte {
		return pixelTestRequest(t, srv, method, route, body, status)
	}
	snap := pixelTestSnapshot(t, call("POST", "/api/project/open", map[string]string{"engine": "pixel", "mode": "quick"}, 200))
	snap = pixelTestSnapshot(t, call("POST", "/api/pixel/assets", pixelTestVersion(snap, map[string]any{
		"role": "ref", "data": pixelTestImage(t),
	}), 200))
	snap = pixelTestSnapshot(t, call("POST", "/api/layers/ops", pixelTestVersion(snap, map[string]any{
		"op": "locked", "id": snap.Active, "locked": true,
	}), 200))
	stream := call("POST", "/api/chat", pixelTestVersion(snap, map[string]any{
		"engine": "pixel", "prompt": "参考图 #1，只修改选区", "pixelDraftMode": "prompt",
		"session": "pixel", "runId": "r1", "restrictToSelection": true,
		"selection": map[string]any{"x": 1, "y": 1, "w": 3, "h": 3, "runs": [][]int{{4, 1}}},
	}), 200)
	if !bytes.Contains(stream, []byte("\"kind\":\"done\"")) || !bytes.Contains(stream, []byte("pixel_update")) {
		t.Fatalf("pixel work did not finish: %.3000s", stream)
	}
	snap = pixelTestSnapshot(t, call("GET", "/api/project", nil, 200))
	if len(snap.Layers) != 2 || !snap.Layers[0].Locked || snap.Layers[0].Elements != 0 || snap.Layers[1].Elements != 1 {
		t.Fatalf("AI drew beyond the selection or lock: %+v", snap.Layers)
	}
	snap = pixelTestSnapshot(t, call("POST", "/api/pixel/checkpoint", pixelTestVersion(snap, map[string]any{"action": "undo"}), 200))
	if len(snap.Layers) != 1 || !snap.Layers[0].Locked {
		t.Fatalf("whole-turn undo failed: %+v", snap.Layers)
	}
	snap = pixelTestSnapshot(t, call("POST", "/api/pixel/checkpoint", pixelTestVersion(snap, map[string]any{"action": "redo"}), 200))
	if len(snap.Layers) != 2 || snap.Layers[1].Elements != 1 {
		t.Fatalf("whole-turn redo failed: %+v", snap.Layers)
	}
	if len(s.Project().History()) == 0 {
		t.Fatal("pixel conversation was not saved")
	}
}

func TestPixelAbortConfirmsNoLateWrites(t *testing.T) {
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	_, srv := newTestAPI(t, upstream.URL)
	call := func(method, route string, body any, status int) []byte {
		return pixelTestRequest(t, srv, method, route, body, status)
	}
	snap := pixelTestSnapshot(t, call("POST", "/api/project/open", map[string]string{"mode": "quick", "engine": "pixel"}, 200))
	chatFields := pixelTestVersion(snap, map[string]any{
		"engine": "pixel", "prompt": "画一个像素", "pixelDraftMode": "prompt", "session": "pixel", "runId": "r1",
	})
	chat := apiPost(t, srv, "/api/chat", chatFields)
	defer chat.Body.Close()
	if chat.StatusCode != 200 {
		t.Fatalf("pixel chat did not start: HTTP %d", chat.StatusCode)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not receive the request")
	}
	layerChanges := map[string]any{
		"layerId": snap.Active, "region": map[string]int{"x": 0, "y": 0, "w": 1, "h": 1},
		"rgbaBase64": base64.StdEncoding.EncodeToString([]byte{128, 48, 80, 255}),
	}
	call("PUT", "/api/pixel/layer", pixelTestVersion(snap, layerChanges), http.StatusConflict)
	call("POST", "/api/chat/abort", map[string]any{"session": "pixel", "runId": "wrong", "wait": true}, http.StatusConflict)
	snap = pixelTestSnapshot(t, call("POST", "/api/chat/abort", map[string]any{
		"session": "pixel", "runId": "r1", "wait": true,
	}, 200))
	snap = pixelTestSnapshot(t, call("PUT", "/api/pixel/layer", pixelTestVersion(snap, layerChanges), 200))
	if snap.Layers[0].Elements != 1 {
		t.Fatal("a human could not resume drawing after the confirmed stop")
	}
	call("POST", "/api/chat/abort", map[string]any{"session": "pixel", "runId": "r2", "wait": true}, 200)
	chatFields["runId"] = "r2"
	call("POST", "/api/chat", pixelTestVersion(snap, chatFields), http.StatusConflict)
}

func TestLargePixelLayerTransportAndModelSnapshot(t *testing.T) {
	s, srv := newTestAPI(t, "http://127.0.0.1:1")
	call := func(method, route string, body any, status int) []byte {
		return pixelTestRequest(t, srv, method, route, body, status)
	}
	snap := pixelTestSnapshot(t, call("POST", "/api/project/create", map[string]any{
		"engine": "pixel", "name": "大画布", "dir": t.TempDir(),
		"pixel": map[string]int{"width": 2048, "height": 1024},
	}, 200))
	img := image.NewNRGBA(image.Rect(0, 0, 2048, 1024))
	var seed uint32 = 0x12345678
	for i := range img.Pix {
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		img.Pix[i] = byte(seed)
	}
	var raw bytes.Buffer
	if err := png.Encode(&raw, img); err != nil || raw.Len() <= 2<<20 {
		t.Fatalf("large PNG fixture: %d bytes, %v", raw.Len(), err)
	}
	rgba := base64.StdEncoding.EncodeToString([]byte{1, 2, 3, 255})
	call("PUT", "/api/pixel/layer", pixelTestVersion(snap, map[string]any{
		"layerId": snap.Active, "rgbaBase64": rgba,
	}), http.StatusBadRequest)
	snap = pixelTestSnapshot(t, call("PUT", "/api/pixel/layer", pixelTestVersion(snap, map[string]any{
		"layerId": snap.Active, "pngBase64": base64.StdEncoding.EncodeToString(raw.Bytes()),
	}), 200))
	layer := call("GET", "/api/pixel/layer?layerId="+snap.Active, nil, 200)
	imgLayer, err := png.Decode(bytes.NewReader(layer))
	if err != nil || imgLayer.Bounds() != img.Bounds() || imgLayer.At(1024, 512) != img.At(1024, 512) {
		t.Fatalf("full layer PNG failed to round trip: %v", err)
	}
	bridge := pixelAgentBridge{ctx: t.Context(), server: s, project: s.Project(), emit: func(event agent.Event) {
		data := event.Pixel.(map[string]any)
		if data["pngBase64"] == nil || data["rgbaBase64"] != nil || data["region"] != nil {
			t.Error("large SSE update must send a full PNG")
		}
	}}
	bridge.changed(snap.Active, &pixel.Rect{W: 2048, H: 1024})
	preview, err := bridge.ModelCompositePNG()
	if err != nil {
		t.Fatal(err)
	}
	config, err := png.DecodeConfig(base64.NewDecoder(base64.StdEncoding, strings.NewReader(preview)))
	if err != nil || config.Width != 1024 || config.Height != 512 {
		t.Fatalf("model image dimensions = %dx%d, %v", config.Width, config.Height, err)
	}
}

func TestPixelAnimationChatFramesAndCheckpoints(t *testing.T) {
	type fakeTool struct{ name, args string }
	steps := []fakeTool{
		{"pixel_anim_info", "{}"},
		{"pixel_draw", `{"ops":[{"op":"pixel","x":1,"y":2,"color":"#FF477E"}]}`},
		{"pixel_frame_op", `{"action":"duplicate"}`},
		{"pixel_frame_op", `{"action":"shift","dx":3,"dy":2,"wrap":true}`},
		{"pixel_inspect", `{"mode":"onion"}`},
		{"pixel_inspect", `{"mode":"filmstrip"}`},
	}
	var count atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		n := int(count.Add(1)) - 1
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 0 && (!bytes.Contains(raw, []byte("pixel_anim_info")) || bytes.Contains(raw, []byte("pixel_create_layer"))) {
			t.Error("animation chat received the wrong tools")
		}
		if n >= 5 && !bytes.Contains(raw, []byte("data:image/png;base64,")) {
			t.Error("animation inspection image missing from tool result")
		}
		if n < len(steps) {
			id := fmt.Sprintf("f%d", n)
			data, _ := json.Marshal(map[string]any{
				"type": "response.output_item.done", "output_index": 0,
				"item": map[string]any{"type": "function_call", "id": id, "call_id": id,
					"name": steps[n].name, "arguments": steps[n].args},
			})
			fmt.Fprintf(w, "data: %s\n\n", data)
		} else {
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"动画完成\"}\n\n")
		}
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer upstream.Close()
	s, srv := newTestAPI(t, upstream.URL)
	settings := s.store.Settings()
	vision := true
	settings.Providers[0].ModelCaps = map[string]config.PartialCaps{"test": {Vision: &vision}}
	if err := s.store.Save(settings); err != nil {
		t.Fatal(err)
	}
	call := func(method, route string, body any, status int) []byte {
		return pixelTestRequest(t, srv, method, route, body, status)
	}
	snap := pixelTestSnapshot(t, call("POST", "/api/project/open", map[string]any{
		"engine": "pixel", "mode": "quick", "pixelMode": "anim", "fresh": true,
	}, 200))
	first := snap.Pixel.Anim.ActiveFrameID
	stream := call("POST", "/api/chat", pixelTestVersion(snap, map[string]any{
		"engine": "pixel", "pixelMode": "anim", "pixelDraftMode": "prompt", "prompt": "制作像素动画",
		"session": "pixel", "runId": "animation",
	}), 200)
	if !bytes.Contains(stream, []byte(`"kind":"done"`)) || !bytes.Contains(stream, []byte(`"frameId":"`+first+`"`)) {
		t.Fatalf("animation stream did not finish or update the addressed frame: %.1600s", stream)
	}
	snap = pixelTestSnapshot(t, call("GET", "/api/project", nil, 200))
	if !snap.Pixel.ModeLocked || snap.Empty || len(snap.Pixel.Anim.Frames) != 2 || snap.Pixel.Anim.ActiveFrameID == first {
		t.Fatalf("animation was not saved: %+v", snap.Pixel.Anim)
	}
	call("POST", "/api/pixel/frames", pixelTestVersion(snap, map[string]any{
		"action": "modeSwitch", "pixelMode": "static", "flatten": true,
	}), http.StatusConflict)
	snap = pixelTestSnapshot(t, call("POST", "/api/pixel/frames", pixelTestVersion(snap, map[string]any{
		"action": "fps", "fps": 12,
	}), 200))
	framePNG := call("GET", "/api/pixel/export?format=png&frameId="+snap.Pixel.Anim.ActiveFrameID, nil, 200)
	frame, err := png.Decode(bytes.NewReader(framePNG))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, oldAlpha := frame.At(1, 2).RGBA()
	_, _, _, newAlpha := frame.At(4, 4).RGBA()
	if oldAlpha != 0 || newAlpha != 0xffff {
		t.Fatalf("frame copy/shift failed: before alpha=%d, after alpha=%d", oldAlpha, newAlpha)
	}
	for _, action := range []string{"undo", "redo"} {
		snap = pixelTestSnapshot(t, call("POST", "/api/pixel/checkpoint", pixelTestVersion(snap, map[string]any{"action": action}), 200))
		if (len(snap.Pixel.Anim.Frames) == 1) != (action == "undo") || snap.Pixel.Anim.FPS != 12 {
			t.Fatalf("%s restored wrong frames or playback settings: %+v", action, snap.Pixel.Anim)
		}
	}
	call("POST", "/api/project/open", map[string]any{
		"engine": "pixel", "mode": "quick", "pixelMode": "static",
	}, http.StatusConflict)
	reopened := pixelTestSnapshot(t, call("POST", "/api/project/open", map[string]any{
		"engine": "pixel", "mode": "quick",
	}, 200))
	if reopened.ProjectID != snap.ProjectID || !reopened.Dirty || len(reopened.Pixel.Anim.Frames) != 2 {
		t.Fatalf("reopening a running animation draft lost the project state: %+v", reopened)
	}
}
