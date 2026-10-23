package svg

import (
	"encoding/xml"
	"strings"
	"testing"
)

const goodSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100" width="100" height="100">` +
	`<rect x="10" y="10" width="80" height="80" fill="#4f7cff"/>` +
	`<text x="50" y="55" text-anchor="middle">Hi</text>` +
	`</svg>`

// ---------- Extract ----------

func TestExtract(t *testing.T) {
	raw := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><path d="M0 0L10 10"/></svg>`

	cases := []struct {
		name   string
		input  string
		source string
		found  bool
		closed bool
	}{
		{
			name:   "svg 围栏",
			input:  "这是图：\n```svg\n" + raw + "\n```\n希望你喜欢。",
			source: "fence-svg",
			found:  true,
			closed: true,
		},
		{
			name:   "xml 围栏",
			input:  "```xml\n<?xml version=\"1.0\"?>\n" + raw + "\n```",
			source: "fence-xml",
			found:  true,
			closed: true,
		},
		{
			name:   "无语言围栏",
			input:  "```\n" + raw + "\n```",
			source: "fence-plain",
			found:  true,
			closed: true,
		},
		{
			name:   "裸文本",
			input:  "先解释一下，然后\n" + raw + "\n完毕。",
			source: "raw",
			found:  true,
			closed: true,
		},
		{
			name:   "无 SVG",
			input:  "抱歉，我只会写文字。",
			source: "none",
			found:  false,
			closed: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Extract(c.input)
			if got.Found != c.found {
				t.Fatalf("Found = %v，期望 %v", got.Found, c.found)
			}
			if got.Source != c.source {
				t.Fatalf("Source = %q，期望 %q", got.Source, c.source)
			}
			if got.Closed != c.closed {
				t.Fatalf("Closed = %v，期望 %v", got.Closed, c.closed)
			}
			if c.found {
				if !strings.Contains(got.SVG, "<svg") {
					t.Fatalf("抽出的 SVG 不含 <svg：%q", got.SVG)
				}
				if err := xml.Unmarshal([]byte(got.SVG), new(struct {
					XMLName xml.Name
				})); err != nil {
					t.Fatalf("抽出的 SVG 不合法：%v（%q）", err, got.SVG)
				}
			}
		})
	}
}

func TestExtractUnclosed(t *testing.T) {
	// 流式中间态：模型只吐了一半
	in := "```svg\n<svg viewBox=\"0 0 10 10\"><path d=\"M0 0L10 10\"/>"
	got := Extract(in)
	if !got.Found {
		t.Fatal("未闭合的围栏也应能抽出 SVG")
	}
	if got.Closed {
		t.Fatal("没有 </svg> 时 Closed 应为 false")
	}
	if !strings.HasSuffix(got.SVG, "</svg>") {
		t.Fatalf("未闭合时应补上 </svg>，实际 %q", got.SVG)
	}
	if got.Source != "fence-svg" {
		t.Fatalf("Source = %q，期望 fence-svg", got.Source)
	}
}

func TestExtractPicksLongest(t *testing.T) {
	short := "```svg\n<svg viewBox=\"0 0 1 1\"/>\n```"
	long := "```svg\n<svg viewBox=\"0 0 2 2\"><rect width=\"2\" height=\"2\"/></svg>\n```"
	got := Extract(short + "\n" + long)
	if !strings.Contains(got.SVG, "0 0 2 2") {
		t.Fatalf("应取更完整的候选，实际 %q", got.SVG)
	}
}

// ---------- Validate ----------

func TestValidateGood(t *testing.T) {
	rep := Validate(goodSVG)
	if !rep.OK {
		t.Fatalf("合法 SVG 应通过，issues=%+v", rep.Issues)
	}
	if rep.Root != "svg" {
		t.Fatalf("Root = %q，期望 svg", rep.Root)
	}
	if rep.ViewBox != "0 0 100 100" {
		t.Fatalf("ViewBox = %q", rep.ViewBox)
	}
	if rep.Elements < 3 {
		t.Fatalf("Elements = %d，至少应有 svg/rect/text 三个", rep.Elements)
	}
	if rep.Paths != 0 {
		t.Fatalf("Paths = %d，样例里没有 path", rep.Paths)
	}
	if rep.Texts != 1 {
		t.Fatalf("Texts = %d，期望 1", rep.Texts)
	}
	if rep.Bytes != len(goodSVG) {
		t.Fatalf("Bytes = %d，期望 %d", rep.Bytes, len(goodSVG))
	}
	for _, is := range rep.Issues {
		if is.Level == "error" {
			t.Fatalf("不应有 error 级 issue：%+v", is)
		}
	}
}

func TestValidateCounts(t *testing.T) {
	in := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">` +
		`<defs><linearGradient id="g"><stop offset="0"/></linearGradient>` +
		`<radialGradient id="r"><stop offset="1"/></radialGradient></defs>` +
		`<path d="M0 0L1 1"/><path d="M1 1L2 2"/>` +
		`<text x="1" y="1">a</text><text x="2" y="2">b</text>` +
		`</svg>`
	rep := Validate(in)
	if rep.Paths != 2 {
		t.Fatalf("Paths = %d，期望 2", rep.Paths)
	}
	if rep.Texts != 2 {
		t.Fatalf("Texts = %d，期望 2", rep.Texts)
	}
	if rep.Gradients != 2 {
		t.Fatalf("Gradients = %d，期望 2", rep.Gradients)
	}
}

func TestValidateBad(t *testing.T) {
	t.Run("XML 解析失败", func(t *testing.T) {
		// 裸 & 不是合法 XML 实体，结构修复也救不回来
		rep := Validate(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><text x="1" y="1">a & b</text></svg>`)
		if rep.OK {
			t.Fatal("XML 不合法时应 OK=false")
		}
		if !hasIssue(rep, "error", "xml") {
			t.Fatalf("应报 xml 错误：%+v", rep.Issues)
		}
	})

	t.Run("非 svg 根", func(t *testing.T) {
		rep := Validate(`<html><body>hi</body></html>`)
		if rep.OK {
			t.Fatal("非 svg 根应 OK=false")
		}
		if !hasIssue(rep, "error", "root") {
			t.Fatalf("应报 root 错误：%+v", rep.Issues)
		}
	})

	t.Run("缺 viewBox", func(t *testing.T) {
		rep := Validate(`<svg xmlns="http://www.w3.org/2000/svg"><text x="1" y="1">a</text></svg>`)
		if rep.OK {
			t.Fatal("缺 viewBox 应 OK=false")
		}
		if !hasIssue(rep, "error", "viewBox") {
			t.Fatalf("应报 viewBox 错误：%+v", rep.Issues)
		}
	})

	t.Run("空输入", func(t *testing.T) {
		rep := Validate("   ")
		if rep.OK {
			t.Fatal("空输入应 OK=false")
		}
		if len(rep.Issues) == 0 {
			t.Fatal("空输入应至少有一条 issue")
		}
	})

	t.Run("比例不一致给 warn", func(t *testing.T) {
		in := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100" width="200" height="50">` +
			`<text x="1" y="1">a</text></svg>`
		rep := Validate(in)
		if !hasIssue(rep, "warn", "ratioMismatch") {
			t.Fatalf("比例不一致应给 warn：%+v", rep.Issues)
		}
	})

	// warn：不再报风格类问题（无文字 / 位图 / 滤镜 / 遮罩 / 元素数量）
	t.Run("纯图形无 warn", func(t *testing.T) {
		in := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">` +
			`<defs><filter id="f"><feGaussianBlur stdDeviation="2"/></filter></defs>` +
			`<image href="data:image/png;base64,iVBORw0KGgo=" width="10" height="10"/></svg>`
		rep := Validate(in)
		if !rep.OK {
			t.Fatalf("没有文字/含位图/含滤镜都不该判失败：%+v", rep.Issues)
		}
		for _, it := range rep.Issues {
			t.Fatalf("不应有任何 issue：%+v", it)
		}
	})
}

func hasIssue(rep Report, level, code string) bool {
	for _, is := range rep.Issues {
		if is.Level == level && is.Code == code {
			return true
		}
	}
	return false
}

// ---------- Presets ----------

func TestPresets(t *testing.T) {
	list := Presets()
	if len(list) < 14 {
		t.Fatalf("预设数量 = %d，至少要 14 个", len(list))
	}
	// 默认画布名必须存在，前端按名字回填
	if list[0].Name != "1:1 方形" || list[0].Width != 1024 || list[0].Height != 1024 {
		t.Fatalf("第一个预设应为 1:1 方形 1024，实际 %+v", list[0])
	}
	want := map[string][2]int{
		"1:1 方形":          {1024, 1024},
		"1:1 方形 512":      {512, 512},
		"16:9 宽屏":         {1920, 1080},
		"4:3 标准":          {1200, 900},
		"3:2 横版照片":        {1500, 1000},
		"2:3 竖版照片":        {1000, 1500},
		"9:16 竖屏":         {1080, 1920},
		"Logo 方形 512":     {512, 512},
		"Logo 横版 800×240": {800, 240},
		"图标 24×24":        {24, 24},
		"图标 64×64":        {64, 64},
		"A4 打印 1240×1754": {1240, 1754},
		"横幅 1584×396":     {1584, 396},
		"头像 400×400":      {400, 400},
		"卡片 1200×630":     {1200, 630},
	}
	byName := make(map[string]Canvas, len(list))
	for _, c := range list {
		byName[c.Name] = c
		if c.Width <= 0 || c.Height <= 0 {
			t.Fatalf("预设 %q 尺寸非法：%+v", c.Name, c)
		}
		if _, _, w, h, ok := parseViewBox(c.ViewBox); !ok || int(w+0.5) != c.Width || int(h+0.5) != c.Height {
			t.Fatalf("预设 %q 的 viewBox 与尺寸不一致：%+v", c.Name, c)
		}
	}
	for name, wh := range want {
		c, ok := byName[name]
		if !ok {
			t.Fatalf("缺少预设 %q", name)
		}
		if c.Width != wh[0] || c.Height != wh[1] {
			t.Fatalf("预设 %q 尺寸 = %d×%d，期望 %d×%d", name, c.Width, c.Height, wh[0], wh[1])
		}
	}
}

// ---------- Prompts ----------

func TestPromptsLibrary(t *testing.T) {
	list := Prompts()
	if len(list) < 40 {
		t.Fatalf("提示词数量 = %d，至少要 40 条", len(list))
	}
	cats := Categories()
	if len(cats) < 8 {
		t.Fatalf("分类数量 = %d，至少要 8 个：%v", len(cats), cats)
	}

	// 每类至少 4 条
	perCat := make(map[string]int, len(cats))
	// 预设名集合，用于校验 ratio 字段
	presetNames := make(map[string]bool)
	for _, c := range Presets() {
		presetNames[c.Name] = true
	}
	ids := make(map[string]bool, len(list))
	pelican := false
	for _, p := range list {
		if p.ID == "" {
			t.Fatalf("提示词缺少 id：%+v", p)
		}
		if ids[p.ID] {
			t.Fatalf("提示词 id 重复：%q", p.ID)
		}
		ids[p.ID] = true
		if p.Category == "" || p.Title == "" || p.Note == "" {
			t.Fatalf("提示词字段不完整：%+v", p)
		}
		if strings.TrimSpace(p.Template) == "" {
			t.Fatalf("提示词 %q 的 template 为空", p.ID)
		}
		// 输出规范已写进系统提示词，模板只描述主题，不再要求收尾句
		if strings.Contains(strings.ToLower(p.Template), "reply with the svg code only") {
			t.Fatalf("提示词 %q 仍带旧收尾句，应已剥离：%q", p.ID, p.Template)
		}
		if !strings.Contains(p.Template, SubjectPlaceholder) && p.Template != DefaultPrompt {
			t.Fatalf("提示词 %q 的 template 缺少 {{subject}}：%q", p.ID, p.Template)
		}
		if p.Source == "" {
			t.Fatalf("提示词 %q 缺少 source", p.ID)
		}
		if p.Ratio == "" {
			t.Fatalf("提示词 %q 缺少 ratio", p.ID)
		}
		if !presetNames[p.Ratio] {
			t.Fatalf("提示词 %q 的 ratio = %q 不是 Presets 里的名字", p.ID, p.Ratio)
		}
		perCat[p.Category]++
		if p.Template == DefaultPrompt {
			pelican = true
		}
	}
	for _, c := range cats {
		if perCat[c] < 4 {
			t.Fatalf("分类 %q 只有 %d 条，每类至少要 4 条", c, perCat[c])
		}
	}
	if len(perCat) != len(cats) {
		t.Fatalf("分类清单与实际条目不一致：%v vs %v", cats, perCat)
	}
	if !pelican {
		t.Fatal("必须包含一条 template 与 DefaultPrompt 完全一致的 pelican 模板")
	}
}

func TestSearchPrompts(t *testing.T) {
	all := SearchPrompts("")
	if len(all) != len(Prompts()) {
		t.Fatalf("空查询应返回全部，得到 %d / %d", len(all), len(Prompts()))
	}

	if got := SearchPrompts("pelican"); len(got) == 0 {
		t.Fatal("搜索 pelican 应有结果")
	}
	if got := SearchPrompts("PELICAN"); len(got) == 0 {
		t.Fatal("搜索应大小写不敏感")
	}
	if got := SearchPrompts("Logo"); len(got) == 0 {
		t.Fatal("搜索分类 Logo 应有结果")
	}
	for _, p := range SearchPrompts("图标") {
		if !strings.Contains(p.Title, "图标") && !strings.Contains(p.Note, "图标") &&
			!strings.Contains(p.Template, "图标") && !strings.Contains(p.Category, "图标") {
			t.Fatalf("命中项与关键词无关：%+v", p)
		}
	}
	if got := SearchPrompts("这个词肯定不存在zzz"); len(got) != 0 {
		t.Fatalf("无关键词应返回空，得到 %d 条", len(got))
	}
}

func TestCategoriesOrder(t *testing.T) {
	cats := Categories()
	if len(cats) == 0 {
		t.Fatal("分类不能为空")
	}
	seen := make(map[string]bool, len(cats))
	for _, c := range cats {
		if c == "" {
			t.Fatal("分类名不能为空")
		}
		if seen[c] {
			t.Fatalf("分类重复：%q", c)
		}
		seen[c] = true
	}
}

// 确保默认提示词常量没被改坏（api 层直接透传）。
func TestDefaultPrompt(t *testing.T) {
	if DefaultPrompt != "A pelican riding a bicycle." {
		t.Fatalf("DefaultPrompt 被改动：%q", DefaultPrompt)
	}
	if SubjectPlaceholder != "{{subject}}" {
		t.Fatalf("SubjectPlaceholder 被改动：%q", SubjectPlaceholder)
	}
	if !strings.Contains(OutputSpec, "emit_svg") {
		t.Fatalf("OutputSpec 必须写死 emit_svg 交付通道：%q", OutputSpec)
	}
	if strings.Contains(DefaultPrompt, "SVG code only") {
		t.Fatal("默认提示词不应再夹带输出规范，规范属于系统提示词")
	}
}

// ---------- 不改写内容的契约测试 ----------

// TestNoRewriteOfDangerousContent 确认本包不再净化 SVG：
// 交付内容与输入逐字一致（连 <script> / on* / 外链都原样保留，
// 防线在 agent 提示词与前端渲染沙箱，不在这里）。
func TestNoRewriteOfDangerousContent(t *testing.T) {
	in := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10" onload="x()">` +
		`<script>alert(1)</script><image href="https://e.example/a.png"/></svg>`

	if !strings.Contains(in, "<script") {
		t.Fatal("样例应含 script，用于验证不再净化")
	}
	// Validate 只读，不应改写任何东西
	rep := Validate(in)
	if rep.Bytes != len(in) {
		t.Fatalf("Bytes = %d，期望 %d（Validate 不应改动内容）", rep.Bytes, len(in))
	}
}

// TestCheckCanvasSize 确认尺寸规范：符合的不报，不符的报 error，省略 width/height 的报 warn。
func TestCheckCanvasSize(t *testing.T) {
	canvas := Canvas{Name: "2:3 竖版照片", Width: 1000, Height: 1500, ViewBox: "0 0 1000 1500"}

	good := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1000 1500" width="1000" height="1500"><rect width="10" height="10"/></svg>`
	if issues := CheckCanvasSize(good, canvas); len(issues) != 0 {
		t.Fatalf("尺寸合规不该报问题：%+v", issues)
	}

	badVB := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512" width="512" height="512"><rect width="10" height="10"/></svg>`
	issues := CheckCanvasSize(badVB, canvas)
	if len(issues) == 0 {
		t.Fatal("尺寸不符应报问题")
	}
	err := 0
	for _, it := range issues {
		if it.Level == "error" {
			err++
		}
	}
	if err < 2 {
		t.Fatalf("viewBox 与 width/height 各应有一条 error，实际 %+v", issues)
	}

	noWH := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1000 1500"><rect width="10" height="10"/></svg>`
	issues = CheckCanvasSize(noWH, canvas)
	if len(issues) != 1 || issues[0].Level != "warn" {
		t.Fatalf("省略 width/height 只应一条 warn：%+v", issues)
	}

	// 没有尺寸规范时一律不报
	if got := CheckCanvasSize(badVB, Canvas{}); got != nil {
		t.Fatalf("canvas 无尺寸时应返回 nil，实际 %+v", got)
	}
}
