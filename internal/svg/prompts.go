package svg

import (
	_ "embed"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
)

// promptsJSON 是随二进制打包的提示词库。
//
//go:embed prompts.json
var promptsJSON []byte

var (
	promptsOnce  sync.Once
	promptList   []Prompt
	categoryList []string
)

// loadPrompts 解析内置 JSON；解析失败或内容不合法时回落到代码内置切片。
func loadPrompts() {
	promptsOnce.Do(func() {
		list, ok := parsePromptsJSON(promptsJSON)
		if !ok || len(list) == 0 {
			list = normalizePrompts(builtinPrompts())
		}
		if len(list) == 0 {
			list = normalizePrompts(fallbackPrompt())
		}
		promptList = list
		categoryList = collectCategories(list)
	})
}

// parsePromptsJSON 解析并清洗 JSON 提示词库。
func parsePromptsJSON(data []byte) ([]Prompt, bool) {
	if len(data) == 0 {
		return nil, false
	}
	var list []Prompt
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, false
	}
	out := normalizePrompts(list)
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// normalizePrompts 补全缺失字段、去重、丢弃不可用的条目。
func normalizePrompts(in []Prompt) []Prompt {
	out := make([]Prompt, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, p := range in {
		p.ID = strings.TrimSpace(p.ID)
		p.Category = strings.TrimSpace(p.Category)
		p.Title = strings.TrimSpace(p.Title)
		p.Template = strings.TrimSpace(p.Template)
		p.Note = strings.TrimSpace(p.Note)
		p.Ratio = strings.TrimSpace(p.Ratio)
		p.Source = strings.TrimSpace(p.Source)
		if p.Template == "" {
			continue
		}
		// pelican 基准模板与 DefaultPrompt 逐字一致，本身不含占位符，单独放行
		if p.Template != DefaultPrompt && !strings.Contains(p.Template, SubjectPlaceholder) {
			continue
		}
		// 输出规范写在系统提示词里，模板里残留的旧收尾句一并剥掉
		p.Template = stripReplySuffix(p.Template)
		if p.Category == "" {
			p.Category = "Illustration"
		}
		if p.Title == "" {
			p.Title = p.ID
		}
		if p.ID == "" {
			p.ID = slugID(p.Category, p.Title)
		}
		if seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		if p.Source == "" {
			p.Source = "builtin"
		}
		out = append(out, p)
	}
	return out
}

// stripReplySuffix 剥掉模板尾部遗留的「只回代码」收尾句。
// 输出规范已移入系统提示词，提示词库只负责描述主题。
func stripReplySuffix(t string) string {
	t = strings.TrimSpace(t)
	for {
		lower := strings.ToLower(t)
		if !strings.HasSuffix(lower, strings.ToLower(promptReplySuffixLegacy)) {
			break
		}
		t = strings.TrimSpace(t[:len(t)-len(promptReplySuffixLegacy)])
	}
	return t
}

// promptReplySuffixLegacy 是历史模板的收尾句，仅用于清洗。
const promptReplySuffixLegacy = "Reply with the SVG code only."

// slugID 在没有 id 时生成一个稳定标识。
func slugID(category, title string) string {
	base := strings.ToLower(category)
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '/' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	id := strings.Trim(b.String(), "-")
	if id == "" {
		id = "prompt"
	}
	if title != "" {
		id += "-" + strconv.Itoa(len(title))
	}
	return id
}

// collectCategories 按首次出现顺序收集分类。
func collectCategories(list []Prompt) []string {
	out := make([]string, 0, 8)
	seen := make(map[string]bool, 8)
	for _, p := range list {
		if p.Category == "" || seen[p.Category] {
			continue
		}
		seen[p.Category] = true
		out = append(out, p.Category)
	}
	return out
}

// prompts 返回全部提示词（副本，调用方可自由修改）。
func prompts() []Prompt {
	loadPrompts()
	out := make([]Prompt, len(promptList))
	copy(out, promptList)
	return out
}

// categories 返回分类列表。
func categories() []string {
	loadPrompts()
	out := make([]string, len(categoryList))
	copy(out, categoryList)
	return out
}

// searchPrompts 在 title/template/note/category 上做大小写不敏感的包含匹配。
func searchPrompts(q string) []Prompt {
	loadPrompts()
	needle := strings.ToLower(strings.TrimSpace(q))
	if needle == "" {
		return prompts()
	}
	out := make([]Prompt, 0, 8)
	for _, p := range promptList {
		if strings.Contains(strings.ToLower(p.Title), needle) ||
			strings.Contains(strings.ToLower(p.Template), needle) ||
			strings.Contains(strings.ToLower(p.Note), needle) ||
			strings.Contains(strings.ToLower(p.Category), needle) ||
			strings.Contains(strings.ToLower(p.ID), needle) {
			out = append(out, p)
		}
	}
	return out
}

// fallbackPrompt 是最后的兜底：只有 pelican 一条，保证界面不空。
func fallbackPrompt() []Prompt {
	return []Prompt{{
		ID:       "illustration-pelican-bicycle",
		Category: "Illustration",
		Title:    "骑自行车的鹈鹕（经典基准）",
		Template: DefaultPrompt,
		Note:     "LLM 画图能力的老牌基准，适合快速验证模型是否真的输出 SVG。",
		Ratio:    "1:1 方形",
		Source:   "https://simonwillison.net/tags/pelican-riding-a-bicycle/",
	}}
}

// builtinPrompts 是 JSON 不可用时的内置副本（每类至少一条，含 pelican）。
func builtinPrompts() []Prompt {
	return []Prompt{
		{
			ID:       "illustration-pelican-bicycle",
			Category: "Illustration",
			Title:    "骑自行车的鹈鹕（经典基准）",
			Template: DefaultPrompt,
			Note:     "LLM 画图能力的老牌基准，适合快速验证模型是否真的输出 SVG。",
			Ratio:    "1:1 方形",
			Source:   "https://simonwillison.net/tags/pelican-riding-a-bicycle/",
		},
		{
			ID:       "logo-brand-monogram",
			Category: "Logo/Brand",
			Title:    "几何字母标志",
			Template: "Design a flat vector logo mark for {{subject}}. Build the mark from simple geometric primitives, one accent color plus neutral tones, generous clear space, no gradients, no photorealism, centered on a square canvas. ",
			Note:     "最通用的品牌标志：几何构图 + 单色点缀，模型不容易画出脏图。",
			Ratio:    "Logo 方形 512",
			Source:   "https://github.com/cliprise/awesome-ai-logo-generator-prompts",
		},
		{
			ID:       "icon-line-24",
			Category: "Icon",
			Title:    "24px 线性图标",
			Template: "Draw a single 24x24 line icon representing {{subject}}. Use a 2px stroke, round caps and joins, no fill, align to a 24 unit grid with 2 unit padding, monochrome. ",
			Note:     "线性图标规范写死在提示词里，产出可直接用于工具栏。",
			Ratio:    "图标 24×24",
			Source:   "builtin",
		},
		{
			ID:       "diagram-flow",
			Category: "Diagram",
			Title:    "流程示意图",
			Template: "Create a clean left-to-right flow diagram explaining {{subject}}. Use rounded rectangles for steps, arrows between them, a muted palette, readable sans-serif labels at 16px, and a light background. ",
			Note:     "流程图关键是把方向、节点形状和字号写清楚，否则模型会乱放文字。",
			Ratio:    "16:9 宽屏",
			Source:   "builtin",
		},
		{
			ID:       "ui-mock-dashboard",
			Category: "UI Mock",
			Title:    "仪表盘界面稿",
			Template: "Produce a wireframe-style dashboard UI mockup for {{subject}}. Include a top bar, a left sidebar, three stat cards and one large chart area, gray placeholder blocks, one accent color, crisp 8px spacing grid. ",
			Note:     "线框稿比高保真稿稳定，重点是网格与占位块。",
			Ratio:    "16:9 宽屏",
			Source:   "builtin",
		},
		{
			ID:       "chart-bar",
			Category: "Chart",
			Title:    "柱状对比图",
			Template: "Generate a bar chart visualizing {{subject}}. Draw axes with ticks, five bars with value labels above them, a subtle grid, a legend, and a title in 20px bold. Use a restrained two-color palette. ",
			Note:     "图表的可读性靠刻度、数值标签和图例三件套。",
			Ratio:    "4:3 标准",
			Source:   "builtin",
		},
		{
			ID:       "character-flat-avatar",
			Category: "Character",
			Title:    "扁平角色半身像",
			Template: "Illustrate a flat vector character portrait of {{subject}} in a half-body composition. Use bold simple shapes, limited palette, no outlines heavier than 3px, friendly proportions, plain circular background. ",
			Note:     "角色类提示词要限定构图范围，否则模型会画出全身细节堆叠。",
			Ratio:    "头像 400×400",
			Source:   "builtin",
		},
		{
			ID:       "pattern-geometric-seamless",
			Category: "Pattern/Decorative",
			Title:    "无缝几何纹样",
			Template: "Create a seamless repeating geometric pattern inspired by {{subject}}. Tile it so edges match exactly, use three colors, keep shapes flat and evenly spaced, fill the whole canvas. ",
			Note:     "无缝纹样必须强调边缘对齐，否则拼接会露缝。",
			Ratio:    "1:1 方形 512",
			Source:   "builtin",
		},
	}
}
