package api

import (
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"svg-master/internal/pixel"
	"svg-master/internal/project"
)

type pixelVersion struct {
	ID       string `json:"projectId"`
	Revision uint64 `json:"revision"`
}

type pixelAssetView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type pixelSnapshot struct {
	Width          int                    `json:"width"`
	Height         int                    `json:"height"`
	Palette        []string               `json:"palette"`
	PrimaryColor   string                 `json:"primaryColor,omitempty"`
	SecondaryColor string                 `json:"secondaryColor,omitempty"`
	MainImage      *pixelAssetView        `json:"mainImage,omitempty"`
	RefImages      []pixelAssetView       `json:"refImages"`
	Fit            string                 `json:"fit,omitempty"`
	DraftLayerID   string                 `json:"draftLayerId,omitempty"`
	DraftMode      string                 `json:"draftMode"`
	Mode           string                 `json:"mode"`
	ModeLocked     bool                   `json:"modeLocked"`
	Anim           *project.PixelAnimMeta `json:"anim,omitempty"`
}

func (s *Server) pixelView(p *project.Project, m *project.PixelMeta) *pixelSnapshot {
	view := &pixelSnapshot{
		Width: m.Width, Height: m.Height, Palette: append([]string{}, m.Palette...),
		PrimaryColor: m.PrimaryColor, SecondaryColor: m.SecondaryColor, DraftMode: m.DraftMode, Fit: m.Fit, DraftLayerID: m.DraftLayerID,
		RefImages: make([]pixelAssetView, 0, len(m.RefImages)),
		Mode:      m.Mode, ModeLocked: m.ModeLocked, Anim: m.Anim,
	}
	if view.Mode == "" {
		view.Mode = project.PixelModeStatic
	}
	assetView := func(a project.PixelAsset) pixelAssetView {
		q := url.Values{"id": {a.ID}, "projectId": {p.ID()}, "asset": {a.File}, "k": {s.token}}
		return pixelAssetView{ID: a.ID, Name: a.Name, URL: "/api/pixel/assets?" + q.Encode()}
	}
	if m.MainImage != nil {
		main := assetView(*m.MainImage)
		view.MainImage = &main
	}
	for _, a := range m.RefImages {
		view.RefImages = append(view.RefImages, assetView(a))
	}
	return view
}

func pixelError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, project.ErrPixelRevision), errors.Is(err, project.ErrPixelProject),
		errors.Is(err, project.ErrLocked), errors.Is(err, project.ErrSVGProject),
		errors.Is(err, project.ErrPixelModeLocked):
		status = http.StatusConflict
	case errors.Is(err, project.ErrNotFound), errors.Is(err, project.ErrFrameNotFound):
		status = http.StatusNotFound
	}
	writeErr(w, status, err.Error())
}

func (s *Server) withPixelMutation(w http.ResponseWriter, v pixelVersion, operation func(*project.Project) (map[string]any, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.Project()
	if p == nil {
		pixelError(w, project.ErrPixelProject)
		return
	}
	if s.resetting || len(s.engines) > 0 {
		writeErr(w, http.StatusConflict, "请等当前任务停止后再编辑画布")
		return
	}
	if err := p.CheckPixelRevision(v.ID, v.Revision); err != nil {
		pixelError(w, err)
		return
	}
	res, err := operation(p)
	if err != nil {
		pixelError(w, err)
		return
	}
	s.markPixelDirty(p)
	if res == nil {
		res = make(map[string]any)
	}
	res["ok"], res["project"] = true, s.snapshot()
	writeJSON(w, res)
}

func (s *Server) markPixelDirty(p *project.Project) {
	s.proj.mu.Lock()
	defer s.proj.mu.Unlock()
	if s.proj.p == p && s.proj.mode == "quick" {
		s.proj.dirty = true
	}
}

func (s *Server) pixelReadProject(w http.ResponseWriter, r *http.Request) *project.Project {
	p := s.Project()
	if p == nil || p.Engine() != project.EnginePixel {
		pixelError(w, project.ErrPixelProject)
		return nil
	}
	id := r.URL.Query().Get("projectId")
	if id != "" && id != p.ID() {
		pixelError(w, project.ErrPixelRevision)
		return nil
	}
	return p
}

func (s *Server) handlePixelLayer(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		p := s.pixelReadProject(w, r)
		if p == nil {
			return
		}
		q := r.URL.Query()
		id, frameID := q.Get("layerId"), q.Get("frameId")
		if id == "" {
			id = q.Get("id")
		}
		if id != "" && frameID != "" {
			writeErr(w, http.StatusBadRequest, "请选择图层或帧")
			return
		}
		if q.Get("format") == "rgba" {
			read := p.PixelLayer
			if frameID != "" {
				read, id = p.PixelFrameImage, frameID
			}
			img, err := read(id)
			if err != nil {
				pixelError(w, err)
				return
			}
			writePixelBytes(w, "application/octet-stream", img.Pix)
			return
		}
		read := p.PixelLayerPNG
		if frameID != "" {
			read, id = p.PixelFramePNG, frameID
		}
		raw, err := read(id)
		if err != nil {
			pixelError(w, err)
			return
		}
		writePixelBytes(w, "image/png", raw)
	case http.MethodPut:
		var in struct {
			pixelVersion
			LayerID string      `json:"layerId"`
			FrameID string      `json:"frameId"`
			PNG     string      `json:"pngBase64"`
			RGBA    string      `json:"rgbaBase64"`
			Region  *pixel.Rect `json:"region"`
		}
		if err := readJSON(r, &in); err != nil {
			pixelError(w, err)
			return
		}
		if (in.LayerID == "") == (in.FrameID == "") || (in.PNG == "") == (in.RGBA == "") {
			writeErr(w, http.StatusBadRequest, "须指定图层或帧，并提供一种像素数据格式")
			return
		}
		if (in.RGBA != "") != (in.Region != nil) {
			writeErr(w, http.StatusBadRequest, "RGBA 补丁须指定区域，PNG 用于更新整张画布")
			return
		}
		s.withPixelMutation(w, in.pixelVersion, func(p *project.Project) (map[string]any, error) {
			var img *image.NRGBA
			var rgba []byte
			var err error
			if in.PNG != "" {
				data, decodeErr := decodePixelBase64(in.PNG, pixel.MaxLayerPNGBytes)
				if decodeErr != nil {
					return nil, decodeErr
				}
				meta := p.Meta().Pixel
				img, err = pixel.DecodeLayer(data, meta.Width, meta.Height)
			} else {
				rgba, err = decodePixelBase64(in.RGBA, 4<<20)
			}
			if err != nil {
				return nil, err
			}
			apply, id := p.PixelApplyHuman, in.LayerID
			if in.FrameID != "" {
				apply, id = p.PixelApplyFrameHuman, in.FrameID
			}
			edit, err := apply(id, img, in.Region, rgba)
			return map[string]any{"humanEdit": edit, "layers": p.Layers()}, err
		})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "需要 GET 或 PUT")
	}
}

func (s *Server) handlePixelFrames(w http.ResponseWriter, r *http.Request) {
	if !pixelPost(w, r) {
		return
	}
	var in struct {
		pixelVersion
		project.PixelFrameOp
		PixelMode string `json:"pixelMode"`
	}
	if err := readJSON(r, &in); err != nil {
		pixelError(w, err)
		return
	}
	s.withPixelMutation(w, in.pixelVersion, func(p *project.Project) (map[string]any, error) {
		if in.Action == "modeSwitch" && in.PixelMode != "" {
			in.Mode = in.PixelMode
		}
		frame, err := p.PixelFrameOperation(in.PixelFrameOp, false)
		if err != nil {
			return nil, err
		}
		if in.Action != "active" && in.Action != "fps" && in.Action != "loop" {
			p.ClearPixelCheckpoint()
		}
		if frame.ID == "" {
			return nil, nil
		}
		return map[string]any{"frame": frame}, nil
	})
}

func (s *Server) handlePixelResize(w http.ResponseWriter, r *http.Request) {
	if !pixelPost(w, r) {
		return
	}
	var in struct {
		pixelVersion
		Width  int    `json:"width"`
		Height int    `json:"height"`
		Mode   string `json:"mode"`
	}
	if err := readJSON(r, &in); err != nil {
		pixelError(w, err)
		return
	}
	s.withPixelMutation(w, in.pixelVersion, func(p *project.Project) (map[string]any, error) {
		return nil, p.PixelResize(in.Width, in.Height, in.Mode)
	})
}

func (s *Server) handlePixelQuantize(w http.ResponseWriter, r *http.Request) {
	if !pixelPost(w, r) {
		return
	}
	var in struct {
		pixelVersion
		pixel.QuantizeOpts
		TargetName string `json:"targetLayerName"`
	}
	if err := readJSON(r, &in); err != nil {
		pixelError(w, err)
		return
	}
	s.withPixelMutation(w, in.pixelVersion, func(p *project.Project) (map[string]any, error) {
		err := p.PixelQuantize(in.QuantizeOpts, in.TargetName)
		if err == nil {
			p.ClearPixelCheckpoint()
		}
		return nil, err
	})
}

func (s *Server) handlePixelPalette(w http.ResponseWriter, r *http.Request) {
	if !pixelPost(w, r) {
		return
	}
	var in struct {
		pixelVersion
		Action         string   `json:"action"`
		Source         string   `json:"source"`
		MaxColors      int      `json:"maxColors"`
		Colors         []string `json:"colors"`
		PrimaryColor   *string  `json:"primaryColor"`
		SecondaryColor *string  `json:"secondaryColor"`
	}
	if err := readJSON(r, &in); err != nil {
		pixelError(w, err)
		return
	}
	s.withPixelMutation(w, in.pixelVersion, func(p *project.Project) (map[string]any, error) {
		colors := in.Colors
		switch in.Action {
		case "extract":
			var img *image.NRGBA
			var err error
			if in.Source == "canvas" || in.Source == "composite" {
				img, err = p.PixelComposite()
			} else {
				if in.Source == "" {
					in.Source = "main"
				}
				img, err = pixelAssetImage(p, in.Source)
			}
			if err != nil {
				return nil, err
			}
			colors = pixel.ExtractPalette(img, in.MaxColors)
			if colors == nil {
				colors = []string{}
			}
		case "save":
		default:
			return nil, errors.New("未知的调色板操作")
		}
		palette, err := p.PixelSetPalette(colors, false, in.PrimaryColor, in.SecondaryColor)
		if err == nil && colors != nil {
			p.ClearPixelCheckpoint()
		}
		return map[string]any{"palette": palette}, err
	})
}

func (s *Server) handlePixelAssets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		p := s.pixelReadProject(w, r)
		if p == nil {
			return
		}
		q := r.URL.Query()
		asset, raw, err := p.PixelAsset(q.Get("id"))
		if err != nil {
			pixelError(w, err)
			return
		}
		if file := q.Get("asset"); file != "" && file != asset.File {
			pixelError(w, project.ErrPixelRevision)
			return
		}
		writePixelBytes(w, asset.MIME, raw)
	case http.MethodPost, http.MethodDelete:
		var in struct {
			pixelVersion
			ID   string `json:"id"`
			Role string `json:"role"`
			Data string `json:"data"`
			Name string `json:"name"`
		}
		if err := readJSON(r, &in); err != nil {
			pixelError(w, err)
			return
		}
		s.withPixelMutation(w, in.pixelVersion, func(p *project.Project) (map[string]any, error) {
			if r.Method == http.MethodDelete {
				return nil, p.PixelRemoveAsset(in.ID)
			}
			raw, err := decodePixelBase64(in.Data, 12<<20)
			if err != nil {
				return nil, err
			}
			asset, err := p.PixelAddAsset(in.Role, in.Name, raw)
			return map[string]any{"id": asset.ID}, err
		})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "需要 GET、POST 或 DELETE")
	}
}

func (s *Server) handlePixelCheckpoint(w http.ResponseWriter, r *http.Request) {
	if !pixelPost(w, r) {
		return
	}
	var in struct {
		pixelVersion
		Action string `json:"action"`
	}
	if err := readJSON(r, &in); err != nil {
		pixelError(w, err)
		return
	}
	s.withPixelMutation(w, in.pixelVersion, func(p *project.Project) (map[string]any, error) {
		return nil, p.PixelCheckpoint(in.Action)
	})
}

func (s *Server) handlePixelExport(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Format  string `json:"format"`
		Scale   int    `json:"scale"`
		FrameID string `json:"frameId"`
	}
	switch r.Method {
	case http.MethodGet:
		in.Format = r.URL.Query().Get("format")
		in.FrameID = r.URL.Query().Get("frameId")
		if raw := r.URL.Query().Get("scale"); raw != "" {
			scale, err := strconv.Atoi(raw)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "无效的放大倍率")
				return
			}
			in.Scale = scale
		}
	case http.MethodPost:
		if err := readJSON(r, &in); err != nil {
			pixelError(w, err)
			return
		}
	default:
		writeErr(w, http.StatusMethodNotAllowed, "需要 GET 或 POST")
		return
	}
	p := s.pixelReadProject(w, r)
	if p == nil {
		return
	}
	if in.Format != "png" && in.Format != "svg" {
		writeErr(w, http.StatusBadRequest, "请选择 PNG 或 SVG")
		return
	}
	animated := p.Meta().Pixel.Mode == project.PixelModeAnim
	if animated && in.Format == "svg" {
		writeErr(w, http.StatusBadRequest, "像素动图可导出 GIF 或单帧 PNG")
		return
	}
	read := p.PixelComposite
	if in.FrameID != "" {
		read = func() (*image.NRGBA, error) { return p.PixelFrameImage(in.FrameID) }
	}
	img, err := read()
	if err != nil {
		pixelError(w, err)
		return
	}
	var raw []byte
	var contentType string
	if in.Format == "png" {
		if in.Scale == 0 {
			in.Scale = 1
		}
		raw, err = pixel.ExportPNG(img, in.Scale)
		contentType = "image/png"
	} else {
		var svg string
		svg, err = pixel.ExportCrispSVG(img)
		raw = []byte(svg)
		contentType = "image/svg+xml"
	}
	if err != nil {
		pixelError(w, err)
		return
	}
	filename := sanitizeName(p.Name()) + "." + in.Format
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	writePixelBytes(w, contentType, raw)
}

func pixelPost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "需要 POST")
		return false
	}
	return true
}

func writePixelBytes(w http.ResponseWriter, contentType string, data []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func decodePixelBase64(value string, limit int) ([]byte, error) {
	if len(value) > base64.StdEncoding.EncodedLen(limit) {
		return nil, fmt.Errorf("图片数据超过 %d 字节上限", limit)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, errors.New("图片数据不是有效的 Base64")
	}
	if len(raw) > limit {
		return nil, fmt.Errorf("图片数据超过 %d 字节上限", limit)
	}
	return raw, nil
}

func pixelAssetImage(p *project.Project, id string) (*image.NRGBA, error) {
	_, raw, err := p.PixelAsset(id)
	if err != nil {
		return nil, err
	}
	return pixel.Decode(raw)
}
