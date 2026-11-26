package agent

import (
	"svg-master/internal/llm"
	"svg-master/internal/pixel"
)

// PixelBridge gives the model access to the current pixel project.
// Implementations enforce project revisions, layer locks and restricted selections.
type PixelBridge interface {
	CanvasInfo() PixelCanvasInfo
	SetPalette(colors []string, mode string, maxColors int) ([]string, error)
	CreateLayer(name string, index int, blend string) (LayerInfo, error)
	SetLayer(id string, patch LayerPatch) (LayerInfo, error)
	DeleteLayer(id string) error
	FrameOperation(PixelFrameOperation) (PixelFrameInfo, error)
	Draw(layerID string, ops []pixel.DrawOp, clip *pixel.Selection) (pixel.DrawSummary, error)
	ReadRegion(layerID string, region pixel.Rect) (pixel.RegionView, error)
	Inspect(opts pixel.InspectOpts) (pngBase64 string, stats pixel.InspectStats, err error)
	ModelCompositePNG() (pngBase64 string, err error)
}

const pixelAnimMode = "anim"

type PixelCanvasInfo struct {
	Width        int              `json:"width"`
	Height       int              `json:"height"`
	Palette      []string         `json:"palette"`
	Layers       []LayerInfo      `json:"layers"`
	ActiveLayer  string           `json:"activeLayer"`
	Selection    *pixel.Selection `json:"selection,omitempty"`
	HasMainImage bool             `json:"hasMainImage"`
	RefIDs       []string         `json:"refIds"`
	Mode         string           `json:"mode,omitempty"`
	FPS          int              `json:"fps,omitempty"`
	Loop         *bool            `json:"loop,omitempty"`
	ActiveFrame  string           `json:"activeFrameId,omitempty"`
	Frames       []PixelFrameInfo `json:"frames,omitempty"`
}

type PixelFrameInfo struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Index     int         `json:"index"`
	Visible   bool        `json:"visible"`
	Locked    bool        `json:"locked"`
	DelayMult int         `json:"delayMult"`
	Elements  int         `json:"elements"`
	Region    *pixel.Rect `json:"pixelRegion,omitempty"`
}

type PixelFrameOperation struct {
	Action        string   `json:"action"`
	FrameID       string   `json:"frameId"`
	SourceFrameID string   `json:"sourceFrameId"`
	Name          string   `json:"name"`
	Index         *int     `json:"index"`
	Visible       *bool    `json:"visible"`
	DelayMult     *int     `json:"delayMult"`
	DX            int      `json:"dx"`
	DY            int      `json:"dy"`
	FrameIDs      []string `json:"frameIds"`
	Wrap          bool     `json:"wrap"`
}

type PixelInput struct {
	DraftMode           string
	GIF                 bool
	MainImage           *llm.ImageData
	RefImages           []PixelReference
	HumanEdits          string
	Selection           *pixel.Selection
	RestrictToSelection bool
	Composite           *llm.ImageData
}

type PixelReference struct {
	ID     string
	Number int
	Name   string
	Image  llm.ImageData
}
