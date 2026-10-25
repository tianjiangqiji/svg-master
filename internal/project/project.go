// Package project 实现工程文件夹的读写、图层模型与画布合成。
//
// 工程文件格式（对齐专业绘图软件的习惯，见下方 File）：
//
//	<工程文件夹>/
//	  project.json     工程文档（画布、图层树、对话历史、设置快照）
//	  layers/          每个图层一个独立 SVG 文件，文件名即 layer-<id>.svg
//	  assets/          用户插入的外部图片（可选）
//	  history/         对话附件与历次快照（可选）
//
// 设计要点：
//   - 每个图层是独立文件，外部工具可直接打开编辑，工程文件夹也不怕单文件损坏。
//   - project.json 只存图层元数据（名字、可见性、层级、不透明度、混合模式），
//     图层内容始终以 layers/*.svg 为准，避免同一份内容存两遍导致不一致。
//   - 合成画布（Render）按 index 排序，把可见图层拼成一张完整 SVG。
//     任何图层内容都不会被改写，只做外层包裹（opacity / mix-blend-mode）。
package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"svg-master/internal/llm"
)

// DocVersion 是 project.json 的格式版本。
const DocVersion = 1

// 工程种类：快速模式用临时工程，工程模式用用户工程。
const (
	KindQuick = "quick"
	KindUser  = "user"
)

// BlendModes 图层混合模式（对齐 SVG 的 mix-blend-mode，前端可直接映射）。
var BlendModes = []string{
	"normal", "multiply", "screen", "overlay", "darken", "lighten",
	"color-dodge", "color-burn", "hard-light", "soft-light",
	"difference", "exclusion", "hue", "saturation", "color", "luminosity",
}

// Layer 一个图层。内容存放在 layers/<File>，本结构只存元数据。
type Layer struct {
	ID       string  `json:"id"`      // 稳定标识，工具调用与前端都用它
	Name     string  `json:"name"`    // 显示名，可重命名
	File     string  `json:"file"`    // layers/ 下的文件名
	Visible  bool    `json:"visible"` // 可见性
	Locked   bool    `json:"locked"`  // 锁定后禁止 AI 改写
	Opacity  float64 `json:"opacity"` // 0..1
	Blend    string  `json:"blend"`   // 混合模式，默认 normal
	Index    int     `json:"index"`   // 层级，越大越靠上
	Bytes    int     `json:"bytes"`   // 内容字节数（冗余，便于列表展示）
	Updated  int64   `json:"updated"` // UnixNano 时间戳
	Note     string  `json:"note,omitempty"`
	Elements int     `json:"elements,omitempty"` // 粗略元素计数（冗余展示）
}

// MessageRecord 持久化的对话消息。复用 llm.Block 的 JSON 形状，
// 完整保留工具调用与工具结果，才能跨启动续聊。
type MessageRecord struct {
	Role         string            `json:"role"`
	Blocks       []llm.Block       `json:"blocks"`
	Time         int64             `json:"time,omitempty"`
	NativeOutput *llm.NativeOutput `json:"nativeOutput,omitempty"`
}

// File 工程文档（project.json 的顶层结构）。
type File struct {
	Version  int    `json:"version"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Created  int64  `json:"created"`
	Modified int64  `json:"modified"`

	Canvas CanvasMeta `json:"canvas"`

	// Layers 顺序无关，渲染时按 Index 排序；保存时统一写成升序。
	Layers []Layer `json:"layers"`

	// ActiveLayer 当前选中的图层 id（前端高亮 / AI 默认作用对象）。
	ActiveLayer string `json:"activeLayer,omitempty"`

	// History 对话历史（取代旧的进程内 session map）。
	History []MessageRecord `json:"history,omitempty"`

	// ContextCleared 最近一次清空上下文的时间戳；AI 据此知道要重新读图层。
	ContextCleared int64 `json:"contextCleared,omitempty"`

	// Notes 用户写在工程里的备注（可选）。
	Notes string `json:"notes,omitempty"`
}

// CanvasMeta 工程画布。
type CanvasMeta struct {
	Name    string `json:"name"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	ViewBox string `json:"viewBox"`
}

// Project 一个打开中的工程。所有导出方法都可并发调用。
type Project struct {
	mu   sync.Mutex
	dir  string // 工程文件夹绝对路径
	kind string // quick | user
	file File
}

// ErrLocked 图层被锁定。
var ErrLocked = errors.New("图层已锁定")

// ErrNotFound 图层不存在。
var ErrNotFound = errors.New("图层不存在")

// ---- 打开 / 创建 / 保存 ----

// Open 打开一个已有工程文件夹（不存在或缺少 project.json 时按新建处理）。
func Open(dir, kind string) (*Project, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("解析工程路径: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(abs, "layers"), 0o755); err != nil {
		return nil, fmt.Errorf("创建工程目录: %w", err)
	}
	p := &Project{dir: abs, kind: kind}
	raw, err := os.ReadFile(filepath.Join(abs, "project.json"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		p.mu.Lock()
		p.file = newFile(abs, kind)
		saveErr := p.saveLocked()
		p.mu.Unlock()
		if saveErr != nil {
			return nil, saveErr
		}
	case err != nil:
		return nil, fmt.Errorf("读取 project.json: %w", err)
	default:
		var f File
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("project.json 格式错误: %w", err)
		}
		p.file = normalizeFile(f, abs, kind)
	}
	return p, nil
}

// Create 在 parentDir 下新建一个工程文件夹并打开它。
func Create(parentDir, name, kind string) (*Project, error) {
	name = sanitizeName(name)
	if name == "" {
		name = "未命名工程"
	}
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建工程根目录: %w", err)
	}
	dir, err := uniqueDir(parentDir, name)
	if err != nil {
		return nil, err
	}
	p, err := Open(dir, kind)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.file.Name = name
	saveErr := p.saveLocked()
	p.mu.Unlock()
	return p, saveErr
}

// newFile 构造空白工程文档。
func newFile(dir, kind string) File {
	now := time.Now().UnixNano()
	return File{
		Version:  DocVersion,
		Kind:     kind,
		Name:     filepath.Base(dir),
		Created:  now,
		Modified: now,
		Canvas:   CanvasMeta{Name: "1:1 方形", Width: 1024, Height: 1024, ViewBox: "0 0 1024 1024"},
		Layers:   []Layer{},
	}
}

// normalizeFile 修掉外部手改造成的非法值。
func normalizeFile(f File, dir, kind string) File {
	if f.Version <= 0 {
		f.Version = DocVersion
	}
	if f.Kind == "" {
		f.Kind = kind
	}
	if f.Name == "" {
		f.Name = filepath.Base(dir)
	}
	if f.Canvas.Width <= 0 || f.Canvas.Height <= 0 {
		f.Canvas = CanvasMeta{Name: "1:1 方形", Width: 1024, Height: 1024, ViewBox: "0 0 1024 1024"}
	}
	if strings.TrimSpace(f.Canvas.ViewBox) == "" {
		f.Canvas.ViewBox = fmt.Sprintf("0 0 %d %d", f.Canvas.Width, f.Canvas.Height)
	}
	for i := range f.Layers {
		l := &f.Layers[i]
		if l.ID == "" {
			l.ID = fmt.Sprintf("L%03d", i+1)
		}
		if l.File == "" {
			l.File = layerFileName(l.ID)
		}
		if l.Name == "" {
			l.Name = "图层 " + l.ID
		}
		if l.Opacity < 0 || l.Opacity > 1 {
			l.Opacity = 1
		}
		if l.Blend == "" || !validBlend(l.Blend) {
			l.Blend = "normal"
		}
		if l.Index == 0 {
			l.Index = i + 1
		}
	}
	sortLayers(f.Layers)
	if f.ActiveLayer == "" && len(f.Layers) > 0 {
		f.ActiveLayer = f.Layers[len(f.Layers)-1].ID
	}
	return f
}

// saveLocked 原子写 project.json（调用方须持锁）。
func (p *Project) saveLocked() error {
	p.file.Modified = time.Now().UnixNano()
	sortLayers(p.file.Layers)
	raw, err := json.MarshalIndent(p.file, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化工程: %w", err)
	}
	path := filepath.Join(p.dir, "project.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("写入工程临时文件: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return fmt.Errorf("替换 project.json: %w", err)
		}
		if err := os.Rename(tmp, path); err != nil {
			return fmt.Errorf("替换 project.json: %w", err)
		}
	}
	return nil
}

// Save 立即落盘。
func (p *Project) Save() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.saveLocked()
}

// Dir 工程文件夹绝对路径。
func (p *Project) Dir() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dir
}

// Kind quick | user。
func (p *Project) Kind() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.kind
}

// Name 工程名。
func (p *Project) Name() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.file.Name
}

// Meta 返回文档副本（不含历史，避免大对象到处传递）。
func (p *Project) Meta() File {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.file
	out.History = nil
	out.Layers = make([]Layer, len(p.file.Layers))
	copy(out.Layers, p.file.Layers)
	return out
}

// SetCanvas 更新画布。
func (p *Project) SetCanvas(c CanvasMeta) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c.Width <= 0 || c.Height <= 0 {
		return errors.New("画布尺寸必须为正数")
	}
	if strings.TrimSpace(c.ViewBox) == "" {
		c.ViewBox = fmt.Sprintf("0 0 %d %d", c.Width, c.Height)
	}
	p.file.Canvas = c
	return p.saveLocked()
}

// Rename 重命名工程（只改文档名，不改文件夹）。
func (p *Project) Rename(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("工程名不能为空")
	}
	p.file.Name = name
	return p.saveLocked()
}

// SaveAs 把当前工程另存到 target 目录（含全部图层与对话历史），
// 完成后当前工程切换到新位置并转为用户工程。
func (p *Project) SaveAs(target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return errors.New("目标目录不能为空")
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("解析目标路径: %w", err)
	}
	src, _ := filepath.Abs(p.dir)
	if abs == src {
		// 同一个目录：只改类型，无需复制。
		p.mu.Lock()
		p.kind = KindUser
		p.file.Kind = KindUser
		err := p.saveLocked()
		p.mu.Unlock()
		return err
	}
	if err := os.MkdirAll(filepath.Join(abs, "layers"), 0o755); err != nil {
		return fmt.Errorf("创建目标工程目录: %w", err)
	}

	// 复制图层文件。
	p.mu.Lock()
	layers := append([]Layer(nil), p.file.Layers...)
	p.mu.Unlock()
	for _, l := range layers {
		content, err := p.LayerContent(l.ID)
		if err != nil {
			continue
		}
		path := filepath.Join(abs, "layers", l.File)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("写入图层 %s: %w", l.File, err)
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.dir = abs
	p.kind = KindUser
	p.file.Kind = KindUser
	p.file.Modified = time.Now().Unix()
	return p.saveLocked()
}

// ---- 图层 ----

// Layers 返回按层级升序的图层元数据副本。
func (p *Project) Layers() []Layer {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Layer, len(p.file.Layers))
	copy(out, p.file.Layers)
	sortLayers(out)
	return out
}

// LayerContent 读取图层内容。
func (p *Project) LayerContent(id string) (string, error) {
	p.mu.Lock()
	l := p.findLocked(id)
	dir := p.dir
	var file string
	if l != nil {
		file = l.File
	}
	p.mu.Unlock()
	if l == nil {
		return "", ErrNotFound
	}
	raw, err := os.ReadFile(filepath.Join(dir, "layers", file))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取图层文件: %w", err)
	}
	return string(raw), nil
}

// IsEmpty 判断工程是否「本来就是空的」：所有图层都没有可见内容，且没有对话历史。
// 前端用它决定「快速生成」要不要弹「将清空图层与上下文」的确认框。
func (p *Project) IsEmpty() bool {
	p.mu.Lock()
	layers := make([]Layer, len(p.file.Layers))
	copy(layers, p.file.Layers)
	hasHistory := len(p.file.History) > 0
	dir := p.dir
	p.mu.Unlock()

	if hasHistory {
		return false
	}
	for _, l := range layers {
		raw, err := os.ReadFile(filepath.Join(dir, "layers", l.File))
		if err != nil {
			continue
		}
		if strings.TrimSpace(stripSVGEnvelope(string(raw))) != "" {
			return false
		}
	}
	return true
}

// AddLayer 新建图层并写入内容，返回新图层。
func (p *Project) AddLayer(name, content string, index int) (Layer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("图层 %d", len(p.file.Layers)+1)
	}
	if index <= 0 {
		index = p.maxIndexLocked() + 1
	}
	l := Layer{
		ID:      newLayerID(),
		Name:    strings.TrimSpace(name),
		Visible: true,
		Opacity: 1,
		Blend:   "normal",
		Index:   index,
		Updated: time.Now().UnixNano(),
	}
	l.File = layerFileName(l.ID)
	if err := p.writeLayer(l.File, content); err != nil {
		return Layer{}, err
	}
	l.Bytes = len(content)
	l.Elements = countElements(content)
	p.file.Layers = append(p.file.Layers, l)
	p.file.ActiveLayer = l.ID
	if err := p.saveLocked(); err != nil {
		return Layer{}, err
	}
	return l, nil
}

// UpdateLayer 覆盖图层内容（锁定图层拒绝）。
func (p *Project) UpdateLayer(id, content string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	l := p.findLocked(id)
	if l == nil {
		return ErrNotFound
	}
	if l.Locked {
		return ErrLocked
	}
	file := l.File
	if err := p.writeLayer(file, content); err != nil {
		return err
	}
	l.Bytes = len(content)
	l.Elements = countElements(content)
	l.Updated = time.Now().UnixNano()
	if p.file.ActiveLayer == "" {
		p.file.ActiveLayer = l.ID
	}
	return p.saveLocked()
}

// PatchLayer 对图层内容做字符串级局部替换（AI 的 edit 工具用）。
// 未命中或一处文本出现多次且未开 replaceAll 时返回错误，让 AI 换策略。
func (p *Project) PatchLayer(id, oldText, newText string, replaceAll bool) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	l := p.findLocked(id)
	if l == nil {
		return 0, ErrNotFound
	}
	if l.Locked {
		return 0, ErrLocked
	}
	if oldText == "" {
		return 0, errors.New("oldText 不能为空")
	}
	path := filepath.Join(p.dir, "layers", l.File)
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("读取图层文件: %w", err)
	}
	cur := string(raw)
	n := strings.Count(cur, oldText)
	if n == 0 {
		return 0, fmt.Errorf("图层 %q 里找不到这段文本（%s）", l.Name, clip(oldText, 60))
	}
	if n > 1 && !replaceAll {
		return 0, fmt.Errorf("这段文本在图层 %q 里出现了 %d 次，请带上更多上下文，或把 replaceAll 设为 true", l.Name, n)
	}
	next := strings.Replace(cur, oldText, newText, 1)
	if replaceAll {
		next = strings.ReplaceAll(cur, oldText, newText)
	} else {
		n = 1
	}
	if err := p.writeLayer(l.File, next); err != nil {
		return 0, err
	}
	l.Bytes = len(next)
	l.Elements = countElements(next)
	l.Updated = time.Now().UnixNano()
	return n, p.saveLocked()
}

// LayerProps 图层属性增量（nil = 不改）。
type LayerProps struct {
	Name    *string  `json:"name"`
	Visible *bool    `json:"visible"`
	Locked  *bool    `json:"locked"`
	Opacity *float64 `json:"opacity"`
	Blend   *string  `json:"blend"`
	Index   *int     `json:"index"`
}

// SetLayerProps 更新图层属性。
func (p *Project) SetLayerProps(id string, pr LayerProps) (Layer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	l := p.findLocked(id)
	if l == nil {
		return Layer{}, ErrNotFound
	}
	if pr.Name != nil && strings.TrimSpace(*pr.Name) != "" {
		l.Name = strings.TrimSpace(*pr.Name)
	}
	if pr.Visible != nil {
		l.Visible = *pr.Visible
	}
	if pr.Locked != nil {
		l.Locked = *pr.Locked
	}
	if pr.Opacity != nil {
		o := *pr.Opacity
		if o < 0 {
			o = 0
		}
		if o > 1 {
			o = 1
		}
		l.Opacity = o
	}
	if pr.Blend != nil {
		b := strings.ToLower(strings.TrimSpace(*pr.Blend))
		if !validBlend(b) {
			return Layer{}, fmt.Errorf("未知混合模式 %q，可用：%s", *pr.Blend, strings.Join(BlendModes, ", "))
		}
		l.Blend = b
	}
	if pr.Index != nil {
		// 层级是「插入位置」语义：把本层从列表里抽出来，再插入到目标位置，
		// 其余层依次顺延编号，不会出现两层共用一个 index 的情况。
		target := *pr.Index
		if target < 1 {
			target = 1
		}
		if target > len(p.file.Layers) {
			target = len(p.file.Layers)
		}
		rest := make([]Layer, 0, len(p.file.Layers)-1)
		for i := range p.file.Layers {
			if p.file.Layers[i].ID != l.ID {
				rest = append(rest, p.file.Layers[i])
			}
		}
		sortLayers(rest)
		rest = append(rest, Layer{})
		copy(rest[target:], rest[target-1:])
		rest[target-1] = *l
		p.file.Layers = rest
		for i := range p.file.Layers {
			p.file.Layers[i].Index = i + 1
		}
	}
	l.Updated = time.Now().UnixNano()
	if err := p.saveLocked(); err != nil {
		return Layer{}, err
	}
	return *l, nil
}

// SetActive 设置当前图层。
func (p *Project) SetActive(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if id != "" && p.findLocked(id) == nil {
		return ErrNotFound
	}
	p.file.ActiveLayer = id
	return p.saveLocked()
}

// DeleteLayer 删除图层及其文件。
func (p *Project) DeleteLayer(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	idx := -1
	for i := range p.file.Layers {
		if p.file.Layers[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrNotFound
	}
	l := p.file.Layers[idx]
	p.file.Layers = append(p.file.Layers[:idx], p.file.Layers[idx+1:]...)
	_ = os.Remove(filepath.Join(p.dir, "layers", l.File))
	if p.file.ActiveLayer == id {
		p.file.ActiveLayer = ""
		if len(p.file.Layers) > 0 {
			p.file.ActiveLayer = p.file.Layers[len(p.file.Layers)-1].ID
		}
	}
	return p.saveLocked()
}

// ResetLayers 把工程清成一张空白画板：删掉所有图层文件，只留一个空图层。
func (p *Project) ResetLayers() error {
	return p.resetLayers(false)
}

// ResetContent 清空当前作品的全部图层内容、对话及续传状态，保留工程和画布规格。
func (p *Project) ResetContent() error {
	return p.resetLayers(true)
}

func (p *Project) resetLayers(clearHistory bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	previous := p.file
	now := time.Now().UnixNano()
	l := Layer{
		ID:      newLayerID(),
		Name:    "图层 1",
		Visible: true,
		Opacity: 1,
		Blend:   "normal",
		Index:   1,
		Updated: now,
	}
	l.File = layerFileName(l.ID)
	if err := p.writeLayer(l.File, ""); err != nil {
		return err
	}
	p.file.Layers = []Layer{l}
	p.file.ActiveLayer = l.ID
	if clearHistory {
		p.file.History = nil
		p.file.ContextCleared = 0
		p.file.Notes = ""
	}
	if err := p.saveLocked(); err != nil {
		p.file = previous
		_ = os.Remove(filepath.Join(p.dir, "layers", l.File))
		return err
	}
	for _, old := range previous.Layers {
		if old.File != "" && filepath.Base(old.File) == old.File {
			_ = os.Remove(filepath.Join(p.dir, "layers", old.File))
		}
	}
	return nil
}

// Reorder 按给定 id 顺序重排层级（数组顺序 = 从下到上）。
func (p *Project) Reorder(ids []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	pos := make(map[string]int, len(ids))
	for i, id := range ids {
		pos[id] = i + 1
	}
	for i := range p.file.Layers {
		if v, ok := pos[p.file.Layers[i].ID]; ok {
			p.file.Layers[i].Index = v
		}
	}
	sortLayers(p.file.Layers)
	return p.saveLocked()
}

// DuplicateLayer 复制图层（内容一并复制）。
func (p *Project) DuplicateLayer(id string) (Layer, error) {
	src, err := p.LayerContent(id)
	if err != nil {
		return Layer{}, err
	}
	layers := p.Layers()
	var srcLayer Layer
	var found bool
	for _, l := range layers {
		if l.ID == id {
			srcLayer, found = l, true
			break
		}
	}
	if !found {
		return Layer{}, ErrNotFound
	}
	l, err := p.AddLayer(srcLayer.Name+" 副本", src, srcLayer.Index+1)
	if err != nil {
		return Layer{}, err
	}
	op, blend, visible, locked := srcLayer.Opacity, srcLayer.Blend, srcLayer.Visible, srcLayer.Locked
	return p.SetLayerProps(l.ID, LayerProps{Opacity: &op, Blend: &blend, Visible: &visible, Locked: &locked})
}

// ---- 渲染合成 ----

// Render 把可见图层合成为一张 SVG。
func (p *Project) Render() string {
	p.mu.Lock()
	layers := make([]Layer, len(p.file.Layers))
	copy(layers, p.file.Layers)
	canvas := p.file.Canvas
	dir := p.dir
	p.mu.Unlock()

	sortLayers(layers)
	var body strings.Builder
	for _, l := range layers {
		if !l.Visible {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, "layers", l.File))
		if err != nil {
			continue
		}
		inner := stripSVGEnvelope(string(raw))
		if strings.TrimSpace(inner) == "" {
			continue
		}
		// 只在外层包 g：不透明度和混合模式挂在 g 上，图层内容逐字保留。
		attrs := " data-layer=\"" + xmlEscape(l.ID) + "\""
		if l.Opacity >= 0 && l.Opacity < 1 {
			attrs += " opacity=\"" + trimFloat(l.Opacity) + "\""
		}
		if l.Blend != "" && l.Blend != "normal" {
			attrs += " style=\"mix-blend-mode:" + xmlEscape(l.Blend) + "\""
		}
		body.WriteString("<g" + attrs + ">")
		body.WriteString(inner)
		body.WriteString("</g>\n")
	}

	vb := canvas.ViewBox
	if strings.TrimSpace(vb) == "" {
		vb = fmt.Sprintf("0 0 %d %d", canvas.Width, canvas.Height)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="%s" width="%d" height="%d"`,
		xmlEscape(vb), canvas.Width, canvas.Height)
	if body.Len() == 0 {
		b.WriteString("></svg>")
		return b.String()
	}
	b.WriteString(">\n<g id=\"layers\">\n")
	b.WriteString(body.String())
	b.WriteString("</g>\n</svg>")
	return b.String()
}

// LayerSummary 图层列表项（含统计，供 AI 快速了解工程现状）。
type LayerSummary struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Visible  bool    `json:"visible"`
	Locked   bool    `json:"locked"`
	Opacity  float64 `json:"opacity"`
	Blend    string  `json:"blend"`
	Index    int     `json:"index"`
	Bytes    int     `json:"bytes"`
	Elements int     `json:"elements"`
}

// Summary 返回图层摘要列表（升序）。
func (p *Project) Summary() []LayerSummary {
	layers := p.Layers()
	out := make([]LayerSummary, 0, len(layers))
	for _, l := range layers {
		out = append(out, LayerSummary{
			ID: l.ID, Name: l.Name, Visible: l.Visible, Locked: l.Locked,
			Opacity: l.Opacity, Blend: l.Blend, Index: l.Index,
			Bytes: l.Bytes, Elements: l.Elements,
		})
	}
	return out
}

// ---- 对话历史 ----

// History 返回持久化消息历史。
func (p *Project) History() []llm.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]llm.Message, 0, len(p.file.History))
	for _, r := range p.file.History {
		out = append(out, llm.Message{Role: r.Role, Blocks: r.Blocks, NativeOutput: r.NativeOutput})
	}
	return out
}

// SetHistory 覆盖写入历史。
func (p *Project) SetHistory(msgs []llm.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	recs := make([]MessageRecord, 0, len(msgs))
	now := time.Now().UnixNano()
	for _, m := range msgs {
		recs = append(recs, MessageRecord{Role: m.Role, Blocks: m.Blocks, Time: now, NativeOutput: m.NativeOutput})
	}
	p.file.History = recs
	return p.saveLocked()
}

// ClearContext 清空对话历史，并记下时间戳（AI 下次据此重新读图层）。
func (p *Project) ClearContext() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.file.History = nil
	p.file.ContextCleared = time.Now().UnixNano()
	return p.saveLocked()
}

// ContextClearedAt 最近一次清空上下文的时间。
func (p *Project) ContextClearedAt() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.file.ContextCleared
}

// WriteDeliverable 把一次交付的 SVG 写成工程里的独立文件（用于回看/导出）。
// name 是相对工程目录的路径，必须落在工程内部，防止模型或前端写入工程外。
func (p *Project) WriteDeliverable(name, body string) error {
	rel := filepath.Clean(filepath.FromSlash(strings.TrimSpace(name)))
	if rel == "" || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("非法交付路径: %q", name)
	}
	full := filepath.Join(p.dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("创建交付目录: %w", err)
	}
	tmp := full + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return fmt.Errorf("写入交付临时文件: %w", err)
	}
	if err := os.Rename(tmp, full); err != nil {
		if rmErr := os.Remove(full); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return fmt.Errorf("替换交付文件: %w", err)
		}
		if err := os.Rename(tmp, full); err != nil {
			return fmt.Errorf("替换交付文件: %w", err)
		}
	}
	// 同步更新 last.svg，方便直接打开最近一次成品。
	_ = os.WriteFile(filepath.Join(p.dir, "last.svg"), []byte(body), 0o644)
	return nil
}

// ---- 内部辅助 ----

// findLocked 查找图层（调用方持锁）。
func (p *Project) findLocked(id string) *Layer {
	for i := range p.file.Layers {
		if p.file.Layers[i].ID == id {
			return &p.file.Layers[i]
		}
	}
	return nil
}

// maxIndexLocked 当前最大层级（调用方持锁）。
func (p *Project) maxIndexLocked() int {
	m := 0
	for _, l := range p.file.Layers {
		if l.Index > m {
			m = l.Index
		}
	}
	return m
}

// writeLayer 写图层文件（原子替换）。
func (p *Project) writeLayer(file, content string) error {
	dir := filepath.Join(p.dir, "layers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建 layers 目录: %w", err)
	}
	path := filepath.Join(dir, file)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return fmt.Errorf("写入图层临时文件: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return fmt.Errorf("替换图层文件: %w", err)
		}
		if err := os.Rename(tmp, path); err != nil {
			return fmt.Errorf("替换图层文件: %w", err)
		}
	}
	return nil
}

// sortLayers 按 Index 升序（相同 Index 时保持原始相对顺序）。
func sortLayers(ls []Layer) {
	sort.SliceStable(ls, func(i, j int) bool { return ls[i].Index < ls[j].Index })
}

// layerFileName 图层文件名。
func layerFileName(id string) string { return "layer-" + id + ".svg" }

// layerSeq 图层 id 序号（同进程内保证唯一）。
var layerSeq struct {
	mu sync.Mutex
	n  int
}

// newLayerID 生成图层 id。
func newLayerID() string {
	layerSeq.mu.Lock()
	defer layerSeq.mu.Unlock()
	layerSeq.n++
	return fmt.Sprintf("L%s%03d", strconv.FormatInt(time.Now().UnixNano()%1000000, 10), layerSeq.n%1000)
}

// validBlend 校验混合模式。
func validBlend(b string) bool {
	for _, m := range BlendModes {
		if m == b {
			return true
		}
	}
	return false
}

// sanitizeName 去掉文件系统非法字符。
func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\\', '/', ':', '*', '?', '"', '<', '>', '|':
			return '-'
		}
		return r
	}, s)
	s = strings.Trim(s, ". ")
	if len([]rune(s)) > 80 {
		s = string([]rune(s)[:80])
	}
	return s
}

// uniqueDir 在 parent 下找一个未占用的目录名。
func uniqueDir(parent, name string) (string, error) {
	base := filepath.Join(parent, name)
	for i := 0; i < 1000; i++ {
		cand := base
		if i > 0 {
			cand = filepath.Join(parent, name+" ("+strconv.Itoa(i)+")")
		}
		if _, err := os.Stat(cand); errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(cand, 0o755); err != nil {
				return "", fmt.Errorf("创建工程文件夹: %w", err)
			}
			return cand, nil
		}
	}
	return "", errors.New("同名工程过多，请换个名字")
}

// stripSVGEnvelope 去掉图层文件自己的 <svg> 外壳，只留内部内容。
// 图层文件可以是完整 SVG，也可以是裸片段，两种都支持。
func stripSVGEnvelope(s string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return ""
	}
	lower := strings.ToLower(t)
	open := strings.Index(lower, "<svg")
	if open < 0 {
		return t // 裸片段，原样返回
	}
	gt := strings.Index(t[open:], ">")
	if gt < 0 {
		return t
	}
	start := open + gt + 1
	end := strings.LastIndex(lower, "</svg>")
	if end < start {
		return t
	}
	return strings.TrimSpace(t[start:end])
}

// countElements 粗略统计图层里的元素个数。
func countElements(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '<' {
			continue
		}
		j := i + 1
		for j < len(s) && s[j] == '/' {
			j++
		}
		if j >= len(s) || !isAlpha(s[j]) {
			continue
		}
		n++
	}
	return n
}

func isAlpha(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// trimFloat 去掉浮点尾随 0。
func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// xmlEscape 最小转义（属性值用）。
func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// clip 截断长文本，用于错误提示。
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---- 工程目录扫描 ----

// Info 一个工程的概览（列表用）。
type Info struct {
	Dir      string `json:"dir"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Canvas   string `json:"canvas"`
	Layers   int    `json:"layers"`
	Modified int64  `json:"modified"`
}

// Scan 列出 rootDir 下的所有工程（含 project.json 的子目录）。
func Scan(rootDir string) []Info {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return nil
	}
	out := make([]Info, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(rootDir, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "project.json"))
		if err != nil {
			continue
		}
		var f File
		if json.Unmarshal(raw, &f) != nil {
			continue
		}
		out = append(out, Info{
			Dir:      dir,
			Name:     f.Name,
			Kind:     f.Kind,
			Canvas:   fmt.Sprintf("%d×%d", f.Canvas.Width, f.Canvas.Height),
			Layers:   len(f.Layers),
			Modified: f.Modified,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified > out[j].Modified })
	return out
}

// ---- 快速模式的临时工程 ----

// QuickRoot 返回内置临时工程根目录（data/quick）。
func QuickRoot(dataDir string) string { return filepath.Join(dataDir, "quick") }

// ResetQuick 清空并重建临时工程目录。
func ResetQuick(dataDir string) error {
	dir := QuickRoot(dataDir)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("清空临时工程: %w", err)
	}
	return os.MkdirAll(dir, 0o755)
}

// PurgeQuick 退出时删除临时工程目录（快速模式「自动清空」语义）。
func PurgeQuick(dataDir string) error {
	return os.RemoveAll(QuickRoot(dataDir))
}

// OpenQuick 打开（必要时新建）快速模式工程。
func OpenQuick(dataDir string) (*Project, error) {
	dir := filepath.Join(QuickRoot(dataDir), "session")
	p, err := Open(dir, KindQuick)
	if err != nil {
		return nil, err
	}
	// 空工程时给一个默认图层，避免 AI 面对空画布无从下手。
	if len(p.Layers()) == 0 {
		if _, err := p.AddLayer("背景", "", 1); err != nil {
			return nil, err
		}
	}
	return p, nil
}
