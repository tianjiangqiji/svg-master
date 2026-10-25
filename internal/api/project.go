package api

// 工程（Project）管理与会话状态：
//
//   - 快速模式：使用内置临时工程 data/quick/session，退出时整目录清空；
//   - 工程模式：使用用户指定目录下的工程文件夹，改动即落盘；
//   - 不再有进程内的 session map：对话历史存在 project.json 里，重启后接着聊；
//   - 服务端同时只允许打开一个工程（符合「一个窗口一个作品」的使用直觉）。
//
// 这一层也实现了 agent.LayerBridge，把 agent 的图层工具直接桥到 project 包。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"svg-master/internal/agent"
	"svg-master/internal/config"
	"svg-master/internal/llm"
	"svg-master/internal/project"
	"svg-master/internal/svg"
)

// ---- 桥接：agent.LayerBridge ----

// bridge 把 project.Project 适配成 agent.LayerBridge。
type bridge struct {
	p *project.Project
}

// Canvas 画布尺寸。
func (b bridge) Canvas() (int, int, string) {
	c := b.p.Meta().Canvas
	return c.Width, c.Height, c.ViewBox
}

// List 图层列表（已按层级升序）。
func (b bridge) List() []agent.LayerInfo {
	ls := b.p.Layers()
	out := make([]agent.LayerInfo, 0, len(ls))
	for _, l := range ls {
		out = append(out, toLayerInfo(l))
	}
	return out
}

// Active 当前图层 id。
func (b bridge) Active() string { return b.p.Meta().ActiveLayer }

// Read 读图层内容。
func (b bridge) Read(id string) (agent.LayerInfo, string, error) {
	l, err := b.pick(id)
	if err != nil {
		return agent.LayerInfo{}, "", err
	}
	content, err := b.p.LayerContent(l.ID)
	if err != nil {
		return agent.LayerInfo{}, "", err
	}
	return toLayerInfo(l), content, nil
}

// Add 新建图层。
func (b bridge) Add(name, content, blend string, index int) (agent.LayerInfo, error) {
	l, err := b.p.AddLayer(name, content, index)
	if err != nil {
		return agent.LayerInfo{}, err
	}
	if strings.TrimSpace(blend) != "" {
		if l2, err2 := b.p.SetLayerProps(l.ID, project.LayerProps{Blend: &blend}); err2 == nil {
			l = l2
		}
	}
	// 新建的图层自动成为当前图层：模型下一步多半就要写它。
	_ = b.p.SetActive(l.ID)
	return toLayerInfo(l), nil
}

// Write 整层覆盖。
func (b bridge) Write(id, content string) error {
	l, err := b.pick(id)
	if err != nil {
		return err
	}
	return b.p.UpdateLayer(l.ID, content)
}

// Patch 局部替换。
func (b bridge) Patch(id, oldText, newText string, replaceAll bool) (int, error) {
	l, err := b.pick(id)
	if err != nil {
		return 0, err
	}
	return b.p.PatchLayer(l.ID, oldText, newText, replaceAll)
}

// Delete 删除图层。
func (b bridge) Delete(id string) error {
	l, err := b.pick(id)
	if err != nil {
		return err
	}
	return b.p.DeleteLayer(l.ID)
}

// Props 改属性。
func (b bridge) Props(id string, p agent.LayerPatch) (agent.LayerInfo, error) {
	l, err := b.pick(id)
	if err != nil {
		return agent.LayerInfo{}, err
	}
	l2, err := b.p.SetLayerProps(l.ID, project.LayerProps{
		Name: p.Name, Visible: p.Visible, Locked: p.Locked,
		Opacity: p.Opacity, Blend: p.Blend, Index: p.Index,
	})
	if err != nil {
		return agent.LayerInfo{}, err
	}
	return toLayerInfo(l2), nil
}

// SetActive 切换当前图层。
func (b bridge) SetActive(id string) error { return b.p.SetActive(id) }

// Render 合成整幅 SVG。
func (b bridge) Render() string { return b.p.Render() }

// pick 解析图层：id 为空时用当前图层；id 也可以用图层名或 file 名（模型爱这么干）。
func (b bridge) pick(id string) (project.Layer, error) {
	id = strings.TrimSpace(id)
	layers := b.p.Layers()
	if id == "" {
		active := strings.TrimSpace(b.p.Meta().ActiveLayer)
		for _, l := range layers {
			if l.ID == active {
				return l, nil
			}
		}
		if len(layers) > 0 {
			return layers[len(layers)-1], nil
		}
		return project.Layer{}, fmt.Errorf("工程里还没有图层，请先 create_layer")
	}
	for _, l := range layers {
		if l.ID == id {
			return l, nil
		}
	}
	for _, l := range layers {
		if strings.EqualFold(l.Name, id) || strings.EqualFold(l.File, id) || strings.EqualFold(l.File, id+".svg") {
			return l, nil
		}
	}
	return project.Layer{}, fmt.Errorf("找不到图层 %q（可用 list_layers 查看）", id)
}

// toLayerInfo project.Layer → agent.LayerInfo。
func toLayerInfo(l project.Layer) agent.LayerInfo {
	return agent.LayerInfo{
		ID:       l.ID,
		Name:     l.Name,
		Visible:  l.Visible,
		Locked:   l.Locked,
		Opacity:  l.Opacity,
		Blend:    l.Blend,
		Index:    l.Index,
		Bytes:    l.Bytes,
		Elements: l.Elements,
	}
}

// ---- 当前工程（服务端单例） ----

// projectState 当前打开的工程与模式。
type projectState struct {
	mu       sync.RWMutex
	p        *project.Project
	mode     string // quick | project
	dirty    bool   // 快速模式下有未保存的改动（退出时据此询问）
	rootHint string // 工程模式根目录（创建/打开工程的默认位置）
}

// Mode 当前运行模式。
func (s *Server) Project() *project.Project {
	s.proj.mu.RLock()
	defer s.proj.mu.RUnlock()
	return s.proj.p
}

// projectBridge 返回当前工程的桥接（未打开时为 nil）。
func (s *Server) projectBridge() agent.LayerBridge {
	if p := s.Project(); p != nil {
		return bridge{p: p}
	}
	return nil
}

// openQuick 打开快速模式的临时工程（启动时用，会先清空旧内容）。
func (s *Server) openQuick(fresh bool) error {
	dataDir := s.store.Dir()
	// 初次启动（配置里没有模式记录）时按 fresh 处理：清空临时目录。
	// 之后「快速模式」视为恢复上次的临时工程，避免重启就丢东西。
	st := s.store.Settings()
	if st.Project.Mode == "" {
		fresh = true
	}
	if fresh {
		if err := project.ResetQuick(dataDir); err != nil {
			return err
		}
	}
	p, err := project.OpenQuick(dataDir)
	if err != nil {
		return err
	}
	// 新工程用设置里的画布初始化，避免每次都回到默认尺寸。
	if cur := s.store.Settings().Canvas; cur.Width > 0 && cur.Height > 0 {
		if mc := p.Meta().Canvas; mc.Width != cur.Width || mc.Height != cur.Height {
			if fresh {
				_ = p.SetCanvas(project.CanvasMeta{
					Name: cur.Name, Width: cur.Width, Height: cur.Height, ViewBox: cur.ViewBox,
				})
			}
		}
	}
	s.proj.mu.Lock()
	s.proj.p = p
	s.proj.mode = config.ModeQuick
	s.proj.dirty = false
	s.proj.mu.Unlock()

	st.Project.Mode = config.ModeQuick
	if err := s.store.Save(st); err != nil {
		_ = err
	}
	return nil
}

// openProject 打开工程模式下的一个工程文件夹。
func (s *Server) openProject(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("工程目录不能为空")
	}
	p, err := project.Open(dir, project.KindUser)
	if err != nil {
		return err
	}
	s.proj.mu.Lock()
	s.proj.p = p
	s.proj.mode = config.ModeProject
	s.proj.dirty = false
	s.proj.mu.Unlock()

	// 记入最近列表，方便下次启动直接选。
	st := s.store.Settings()
	abs, _ := filepath.Abs(dir)
	st.Project.Mode = config.ModeProject
	st.Project.LastProject = abs
	st.Project.Recent = pushRecent(st.Project.Recent, abs)
	if err := s.store.Save(st); err != nil {
		// 保存失败不影响工程本身，只丢「最近打开」记录。
		_ = err
	}
	return nil
}

// closeProject 关闭当前工程（快速模式删除临时目录）。
func (s *Server) closeProject(purgeQuick bool) error {
	s.proj.mu.Lock()
	p, mode := s.proj.p, s.proj.mode
	s.proj.p = nil
	s.proj.dirty = false
	s.proj.mu.Unlock()
	if p == nil {
		return nil
	}
	if err := p.Save(); err != nil {
		return err
	}
	if purgeQuick && mode == config.ModeQuick {
		return project.PurgeQuick(s.store.Dir())
	}
	return nil
}

// markDirty 记录一次改动并即时落盘。
//
// 两种模式都会写盘：工程模式落到用户目录（关窗时不必再问），
// 快速模式落到内置临时目录（只在内存里标脏，退出时问用户要不要另存）。
// 这样进程被强杀也不会丢内容。
func (s *Server) markDirty() {
	s.proj.mu.Lock()
	mode, p := s.proj.mode, s.proj.p
	if mode == config.ModeQuick {
		s.proj.dirty = true
	}
	s.proj.mu.Unlock()

	if p != nil {
		// 落盘失败不打断当前操作，前端仍会看到内存里的最新状态。
		_ = p.Save()
	}
}

// ---- 会话（对话历史落在 project.json） ----

// sessionHistory 取工程里的对话历史。
func (s *Server) sessionHistory() []llm.Message {
	p := s.Project()
	if p == nil {
		return nil
	}
	return p.History()
}

// saveSession 把对话历史写回工程。
func (s *Server) saveSession(msgs []llm.Message) {
	p := s.Project()
	if p == nil {
		return
	}
	if err := p.SetHistory(msgs); err != nil {
		return
	}
	s.markDirty()
}

// clearContext 清空对话上下文（工程文件与图层保留）。
func (s *Server) clearContext() error {
	p := s.Project()
	if p == nil {
		return fmt.Errorf("尚未打开工程")
	}
	if err := p.ClearContext(); err != nil {
		return err
	}
	return nil
}

// contextClearedAt 最近一次清空上下文的时间戳。
func (s *Server) contextClearedAt() int64 {
	if p := s.Project(); p != nil {
		return p.ContextClearedAt()
	}
	return 0
}

// pushRecent 把路径插到最近列表头部（去重、最多 12 条）。
func pushRecent(list []string, dir string) []string {
	out := make([]string, 0, len(list)+1)
	out = append(out, dir)
	for _, it := range list {
		if it == dir {
			continue
		}
		out = append(out, it)
		if len(out) >= 12 {
			break
		}
	}
	return out
}

// ---- 画布同步 ----

// syncCanvasToProject 把设置里的画布写进工程（工程有自己的画布，以工程为准）。
func (s *Server) projectCanvas() config.CanvasPreset {
	p := s.Project()
	if p == nil {
		// 没有工程时退回上次使用的画布：新建工程会以它初始化，保持手感一致。
		return s.store.Settings().Canvas
	}
	c := p.Meta().Canvas
	return config.CanvasPreset{Name: c.Name, Width: c.Width, Height: c.Height, ViewBox: c.ViewBox}
}

// setProjectCanvas 改工程画布。
func (s *Server) setProjectCanvas(c config.CanvasPreset) error {
	p := s.Project()
	if p == nil {
		return fmt.Errorf("尚未打开工程")
	}
	if err := p.SetCanvas(project.CanvasMeta{
		Name: c.Name, Width: c.Width, Height: c.Height, ViewBox: c.ViewBox,
	}); err != nil {
		return err
	}
	s.markDirty()
	return nil
}

// renderCurrent 当前工程的合成 SVG（空工程返回空串）。
func (s *Server) renderCurrent() string {
	if p := s.Project(); p != nil {
		return p.Render()
	}
	return ""
}

// validateCurrent 校验合成后的 SVG。
func (s *Server) validateCurrent() svg.Report {
	return svg.Validate(s.renderCurrent())
}

// ---- 辅助 ----

// dirExists 判断目录是否存在。
func dirExists(dir string) bool {
	st, err := os.Stat(dir)
	return err == nil && st.IsDir()
}

// ---- 退出前的保存询问 ----

// NeedsSavePrompt 快速模式下是否有需要询问保存的改动。
func (s *Server) NeedsSavePrompt() bool {
	s.proj.mu.RLock()
	defer s.proj.mu.RUnlock()
	return s.proj.mode == config.ModeQuick && s.proj.dirty && s.proj.p != nil
}

// BackupQuickOnExit 关窗时的兜底：快速模式还有未保存改动就留一份快照。
// 询问本身由前端完成（窗口过程拦 WM_CLOSE 后调 window.dshSaveChoice），
// 这里绝不能再阻塞等回执，否则关窗会卡住。
func (s *Server) BackupQuickOnExit() {
	if s.NeedsSavePrompt() {
		_ = s.SaveQuickSnapshot()
	}
}

// SaveQuickSnapshot 把快速模式的临时工程复制到 data/quick-backup/<时间戳>，
// 用于「用户关闭时没做选择」的兜底，避免内容凭空消失。
func (s *Server) SaveQuickSnapshot() error {
	p := s.Project()
	if p == nil {
		return nil
	}
	dst := filepath.Join(s.store.Dir(), "quick-backup", time.Now().Format("20060102-150405"))
	return p.SaveAs(dst)
}
