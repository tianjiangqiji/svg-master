package api

// 工程与图层的 HTTP 接口。
//
//	GET  /api/project            当前工程状态 + 图层树 + 合成 SVG
//	POST /api/project/open       打开工程（快速模式或指定文件夹）
//	POST /api/project/create     在工程根目录下新建工程文件夹并打开
//	POST /api/project/save       把当前工程另存（快速模式 → 用户选定的位置）
//	POST /api/project/close      关闭工程（快速模式可选清空临时目录）
//	GET  /api/layers             图层树（轻量）
//	POST /api/layers/ops         图层人工操作：新增/删除/改名/显隐/锁定/透明度/层级/当前层
//
// 图层的人工操作与模型工具走同一套 project 方法，保证「模型改的」和「人改的」不打架。

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"svg-master/internal/config"
	"svg-master/internal/project"
)

// projectSnapshot 前端需要的工程快照。
type projectSnapshot struct {
	Engine    string         `json:"engine,omitempty"`
	ProjectID string         `json:"projectId,omitempty"`
	Revision  uint64         `json:"revision,omitempty"`
	Pixel     *pixelSnapshot `json:"pixel,omitempty"`
	Open      bool           `json:"open"`
	Mode      string         `json:"mode"`
	Name      string         `json:"name"`
	Dir       string         `json:"dir"`
	Kind      string         `json:"kind"`
	Canvas    any            `json:"canvas"`
	Active    string         `json:"activeLayer"`
	Layers    []layerPayload `json:"layers"`
	SVG       string         `json:"svg"`
	Dirty     bool           `json:"dirty"`
	CanSaveTo bool           `json:"canSaveAs"`
	Empty     bool           `json:"empty"`
}

// layerPayload 单个图层的展示数据（含内容，前端画布直接渲染）。
type layerPayload struct {
	Updated  int64   `json:"updated,omitempty"`
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	File     string  `json:"file"`
	Visible  bool    `json:"visible"`
	Locked   bool    `json:"locked"`
	Opacity  float64 `json:"opacity"`
	Blend    string  `json:"blend"`
	Index    int     `json:"index"`
	Bytes    int     `json:"bytes"`
	Elements int     `json:"elements"`
	Content  string  `json:"content"`
}

// snapshot 组装当前工程快照（未打开时返回 {open:false}）。
func (s *Server) snapshot() projectSnapshot {
	s.proj.mu.RLock()
	p, mode, dirty := s.proj.p, s.proj.mode, s.proj.dirty
	s.proj.mu.RUnlock()
	if p == nil {
		return projectSnapshot{Open: false, Mode: mode}
	}
	doc := p.Meta()
	ls := doc.Layers
	out := make([]layerPayload, 0, len(ls))
	for _, l := range ls {
		var content string
		if doc.Pixel == nil {
			content, _ = p.LayerContent(l.ID)
		}
		out = append(out, layerPayload{
			ID: l.ID, Name: l.Name, File: l.File, Updated: l.Updated,
			Visible: l.Visible, Locked: l.Locked, Opacity: l.Opacity,
			Blend: l.Blend, Index: l.Index, Bytes: l.Bytes, Elements: l.Elements,
			Content: content,
		})
	}
	c := doc.Canvas
	snap := projectSnapshot{
		Engine:    project.EngineSVG,
		Open:      true,
		Mode:      mode,
		Name:      p.Name(),
		Dir:       p.Dir(),
		Kind:      p.Kind(),
		Canvas:    config.CanvasPreset{Name: c.Name, Width: c.Width, Height: c.Height, ViewBox: c.ViewBox},
		Active:    doc.ActiveLayer,
		Layers:    out,
		Dirty:     dirty,
		CanSaveTo: p.Kind() == project.KindQuick,
	}
	if doc.Pixel == nil {
		snap.SVG, snap.Empty = p.Render(), p.IsEmpty()
	} else {
		snap.Engine, snap.ProjectID, snap.Revision = project.EnginePixel, p.ID(), doc.Pixel.Revision
		snap.Pixel = s.pixelView(p, doc.Pixel)
		snap.Empty = true
		for _, layer := range ls {
			if layer.Elements > 0 {
				snap.Empty = false
				break
			}
		}
		if doc.Pixel.Anim != nil {
			for _, frame := range doc.Pixel.Anim.Frames {
				if frame.Elements > 0 {
					snap.Empty = false
					break
				}
			}
		}
	}
	return snap
}

// handleProject 读取当前工程状态。
func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	st := s.store.Settings()
	writeJSON(w, map[string]any{
		"project": s.snapshot(),
		"modes": []map[string]string{
			{"key": config.ModeQuick, "label": "快速模式", "hint": "内置临时文件夹，退出时可另存或丢弃"},
			{"key": config.ModeProject, "label": "工程模式", "hint": "选定文件夹，图层与对话即时落盘"},
		},
		"rootDir": st.Project.Dir,
		"recent":  st.Project.Recent,
		"last":    st.Project.LastProject,
		"quick":   filepath.Join(s.store.Dir(), "quick"),
	})
}

// handleProjectOpen 打开工程。
func (s *Server) handleProjectOpen(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Engine    string `json:"engine"`
		Mode      string `json:"mode"` // quick | project
		PixelMode string `json:"pixelMode"`
		Dir       string `json:"dir"`   // 工程模式下的工程文件夹
		Fresh     bool   `json:"fresh"` // 快速模式：是否清空旧内容（默认清）
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectEngineOK(in.Engine) {
		writeErr(w, http.StatusBadRequest, "未知工程类型")
		return
	}
	if !pixelModeOK(in.PixelMode) {
		writeErr(w, http.StatusBadRequest, "未知像素画模式")
		return
	}
	if !s.lockProjectAction(w) {
		return
	}
	defer s.mu.Unlock()
	var err error
	switch in.Mode {
	case config.ModeQuick:
		if in.Engine == project.EnginePixel {
			err = s.openPixelQuick(in.Fresh, in.PixelMode)
		} else if err = s.closeProject(false); err == nil {
			err = s.openQuick(in.Fresh)
		}
	case config.ModeProject:
		err = s.openProject(in.Dir, in.Engine, in.PixelMode)
	default:
		writeErr(w, http.StatusBadRequest, "未知模式: "+in.Mode)
		return
	}
	if err != nil {
		pixelError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "project": s.snapshot()})
}

// handleProjectCreate 在工程根目录下新建工程文件夹。
func (s *Server) handleProjectCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Engine    string            `json:"engine"`
		Pixel     *pixelSizePayload `json:"pixel"`
		PixelMode string            `json:"pixelMode"`
		Name      string            `json:"name"`
		Dir       string            `json:"dir"` // 可选：父目录，默认用设置里的工程根目录
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "工程名不能为空")
		return
	}
	if !projectEngineOK(in.Engine) {
		writeErr(w, http.StatusBadRequest, "未知工程类型")
		return
	}
	if !s.lockProjectAction(w) {
		return
	}
	defer s.mu.Unlock()
	parent := strings.TrimSpace(in.Dir)
	if parent == "" {
		parent = s.store.Settings().Project.Dir
	}
	if parent == "" {
		parent = filepath.Join(s.store.Dir(), "projects")
	}
	// 在工程根目录下新建工程文件夹（重名自动加序号），再改回用户输入的名字。
	var created *project.Project
	var err error
	if in.Engine == project.EnginePixel {
		width, height := 32, 32
		if in.Pixel != nil {
			width, height = in.Pixel.Width, in.Pixel.Height
			if in.PixelMode == "" {
				in.PixelMode = in.Pixel.Mode
			}
		}
		created, err = project.CreatePixel(parent, name, project.KindUser, width, height, in.PixelMode)
	} else {
		created, err = project.Create(parent, name, project.KindUser)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.closeProject(false); err != nil {
		pixelError(w, err)
		return
	}
	if in.Engine != project.EnginePixel {
		_ = created.Rename(name)
	}
	s.installNamedProject(created)
	writeJSON(w, map[string]any{"ok": true, "project": s.snapshot()})
}

// handleProjectSaveAs 把快速模式的临时工程另存为正式工程。
func (s *Server) handleProjectSaveAs(w http.ResponseWriter, r *http.Request) {
	if !s.lockProjectAction(w) {
		return
	}
	defer s.mu.Unlock()
	var in struct {
		Dir string `json:"dir"` // 目标工程文件夹（可以是尚不存在的路径）
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	p := s.Project()
	if p == nil {
		writeErr(w, http.StatusBadRequest, "尚未打开工程")
		return
	}
	target := strings.TrimSpace(in.Dir)
	if target == "" {
		writeErr(w, http.StatusBadRequest, "目标目录不能为空")
		return
	}
	if err := p.SaveAs(target); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 另存后当前工程切换到新位置，模式变为工程模式。
	s.proj.mu.Lock()
	s.proj.mode = config.ModeProject
	s.proj.dirty = false
	s.proj.mu.Unlock()
	st := s.store.Settings()
	abs, _ := filepath.Abs(target)
	st.Project.LastProject = abs
	st.Project.Mode, st.Workspace = config.ModeProject, p.Engine()
	st.Project.Recent = pushRecent(st.Project.Recent, abs)
	_ = s.store.Save(st)
	writeJSON(w, map[string]any{"ok": true, "project": s.snapshot()})
}

// handleProjectSave 把当前工程立即落盘（图层文件 + project.json）。
// 不改变工程目录，用于「保存」按钮与退出前的自动保存。
func (s *Server) handleProjectSave(w http.ResponseWriter, r *http.Request) {
	if !s.lockProjectAction(w) {
		return
	}
	defer s.mu.Unlock()
	p := s.Project()
	if p == nil {
		writeErr(w, http.StatusBadRequest, "尚未打开工程")
		return
	}
	if err := p.Save(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.proj.mu.Lock()
	s.proj.dirty = false
	s.proj.mu.Unlock()
	files := 0
	for _, l := range p.Layers() {
		if l.File != "" {
			files++
		}
	}
	writeJSON(w, map[string]any{"ok": true, "files": files, "dir": p.Dir(), "project": s.snapshot()})
}

// handleProjectClose 关闭工程；快速模式下可选择是否保留。
//
// 带 keep=true 时只表示「保留内容、不要清理临时目录」（关窗询问里选另存后，
// 前端会先 saveas 再走到这里），其余情况按 purgeQuick 处理。
func (s *Server) handleProjectClose(w http.ResponseWriter, r *http.Request) {
	if !s.lockProjectAction(w) {
		return
	}
	defer s.mu.Unlock()
	var in struct {
		PurgeQuick bool `json:"purgeQuick"`
		Keep       bool `json:"keep"`
	}
	_ = readJSON(r, &in)
	if in.Keep {
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	if err := s.closeProject(in.PurgeQuick); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleLayers 图层树（带内容的完整快照，前端用来重绘）。
func (s *Server) handleLayers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.snapshot())
}

// handleLayerOps 图层人工操作。
type layerOperation struct {
	pixelVersion
	Op      string   `json:"op"` // add|delete|duplicate|rename|visible|locked|opacity|blend|index|active|reorder
	ID      string   `json:"id"`
	Name    *string  `json:"name"`
	Visible *bool    `json:"visible"`
	Locked  *bool    `json:"locked"`
	Opacity *float64 `json:"opacity"`
	Blend   *string  `json:"blend"`
	Index   *int     `json:"index"`
	IDs     []string `json:"ids"`
	Content string   `json:"content"`
}

func (s *Server) handleLayerOps(w http.ResponseWriter, r *http.Request) {
	var in layerOperation
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	p := s.Project()
	if p == nil {
		writeErr(w, http.StatusBadRequest, "尚未打开工程")
		return
	}
	if p.Engine() == project.EnginePixel || in.pixelVersion.ID != "" {
		s.withPixelMutation(w, in.pixelVersion, func(p *project.Project) (map[string]any, error) {
			err := applyPixelLayerOperation(p, in)
			if err == nil && in.Op != "active" {
				p.ClearPixelCheckpoint()
			}
			return nil, err
		})
		return
	}
	var err error
	switch in.Op {
	case "add":
		name := "新图层"
		if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
			name = strings.TrimSpace(*in.Name)
		}
		idx := -1
		if in.Index != nil {
			idx = *in.Index
		}
		var l project.Layer
		if l, err = p.AddLayer(name, in.Content, idx); err == nil {
			err = p.SetActive(l.ID)
		}
	case "delete":
		err = p.DeleteLayer(in.ID)
	case "duplicate":
		var l project.Layer
		if l, err = p.DuplicateLayer(in.ID); err == nil {
			err = p.SetActive(l.ID)
		}
	case "rename", "visible", "locked", "opacity", "blend", "index":
		_, err = p.SetLayerProps(in.ID, project.LayerProps{
			Name: in.Name, Visible: in.Visible, Locked: in.Locked,
			Opacity: in.Opacity, Blend: in.Blend, Index: in.Index,
		})
	case "active":
		err = p.SetActive(in.ID)
	case "reorder":
		err = p.Reorder(in.IDs)
	default:
		writeErr(w, http.StatusBadRequest, "未知操作: "+in.Op)
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.markDirty()
	writeJSON(w, map[string]any{"ok": true, "project": s.snapshot()})
}

func applyPixelLayerOperation(p *project.Project, in layerOperation) error {
	switch in.Op {
	case "add":
		name, index := "新图层", 0
		if in.Name != nil {
			name = *in.Name
		}
		if in.Index != nil {
			index = *in.Index
		}
		_, err := p.PixelAddLayer(name, "", index, nil)
		return err
	case "delete":
		return p.PixelDelete(in.ID)
	case "duplicate":
		return p.PixelDuplicate(in.ID)
	case "rename", "visible", "locked", "opacity", "blend", "index":
		_, err := p.PixelProps(in.ID, project.LayerProps{Name: in.Name, Visible: in.Visible, Locked: in.Locked, Opacity: in.Opacity, Blend: in.Blend, Index: in.Index}, false)
		return err
	case "active":
		return p.PixelActivate(in.ID)
	case "reorder":
		return p.PixelReorder(in.IDs)
	default:
		return fmt.Errorf("未知操作: %s", in.Op)
	}
}

type pixelSizePayload struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Mode   string `json:"mode"`
}

func pixelModeOK(mode string) bool {
	return mode == "" || mode == project.PixelModeStatic || mode == project.PixelModeAnim
}

func projectEngineOK(engine string) bool {
	return engine == "" || engine == project.EngineSVG || engine == project.EnginePixel
}

func (s *Server) lockProjectAction(w http.ResponseWriter) bool {
	s.mu.Lock()
	if s.resetting || len(s.engines) > 0 {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "请先停止生成再切换或保存工程")
		return false
	}
	return true
}

// sanitizeName 去掉文件名里的非法字符。（现由 project 包内部处理，此函数供前端提示复用）
func sanitizeName(name string) string {
	bad := []string{"\\", "/", ":", "*", "?", "\"", "<", ">", "|"}
	out := name
	for _, b := range bad {
		out = strings.ReplaceAll(out, b, "-")
	}
	out = strings.TrimSpace(out)
	if out == "" {
		out = "未命名工程"
	}
	return out
}
