package svg

import "strings"

// fenceBlock 表示 markdown 代码围栏中的一段内容。
type fenceBlock struct {
	lang string // 围栏语言标记，已小写，可能为空
	body string // 围栏内的原始文本
	open bool   // 围栏是否没有闭合（流式中间态）
}

// sourceRank 给来源定优先级：显式 svg 围栏最可信，其次 xml，再次裸围栏。
func sourceRank(src string) int {
	switch src {
	case "fence-svg":
		return 3
	case "fence-xml":
		return 2
	case "fence-plain":
		return 1
	case "raw":
		return 0
	}
	return -1
}

// fenceBlocks 扫描文本中的所有 ``` / ~~~ 围栏。
// 逐行处理，避免误判正文里出现的三个反引号。
func fenceBlocks(text string) []fenceBlock {
	lines := strings.Split(text, "\n")
	var out []fenceBlock
	var cur []string
	lang := ""
	in := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		isFence := strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
		if isFence {
			if !in {
				in = true
				lang = strings.ToLower(strings.TrimSpace(t[3:]))
				if i := strings.IndexAny(lang, " \t"); i >= 0 {
					lang = lang[:i]
				}
				lang = strings.Trim(lang, "{}")
				cur = nil
			} else {
				out = append(out, fenceBlock{lang: lang, body: strings.Join(cur, "\n")})
				in = false
				cur = nil
				lang = ""
			}
			continue
		}
		if in {
			cur = append(cur, ln)
		}
	}
	if in {
		out = append(out, fenceBlock{lang: lang, body: strings.Join(cur, "\n"), open: true})
	}
	return out
}

// indexOpenSVG 在 s 中从 from 开始找 "<svg" 的位置。
// 要求其后紧跟空白、'>' 或 '/'，避免把 <svgfoo 当成根标签。
func indexOpenSVG(s string, from int) int {
	low := strings.ToLower(s)
	if from < 0 {
		from = 0
	}
	for from < len(low) {
		i := strings.Index(low[from:], "<svg")
		if i < 0 {
			return -1
		}
		i += from
		j := i + 4
		if j >= len(low) {
			return i
		}
		switch low[j] {
		case '>', '/', ' ', '\t', '\n', '\r':
			return i
		}
		from = j
	}
	return -1
}

// lastCloseSVG 找最后一个 </svg> 的起始下标；找不到返回 -1。
func lastCloseSVG(s string) int {
	low := strings.ToLower(s)
	idx := strings.LastIndex(low, "</svg")
	for idx >= 0 {
		// 允许 </svg > 这种带空白的写法
		j := idx + len("</svg")
		for j < len(low) && (low[j] == ' ' || low[j] == '\t' || low[j] == '\r' || low[j] == '\n') {
			j++
		}
		if j < len(low) && low[j] == '>' {
			return idx
		}
		low = low[:idx]
		idx = strings.LastIndex(low, "</svg")
	}
	return -1
}

// trimFenceTail 去掉结尾残留的围栏标记行。
func trimFenceTail(s string) string {
	lines := strings.Split(s, "\n")
	for len(lines) > 0 {
		t := strings.TrimSpace(lines[len(lines)-1])
		if t == "" || strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			lines = lines[:len(lines)-1]
			continue
		}
		break
	}
	return strings.Join(lines, "\n")
}

// sliceSVG 从一段文本里切出 SVG 片段。
// 优先「第一个 <svg 到最后一个 </svg>」；没有闭合标签时截到末尾并补 </svg>。
func sliceSVG(s string) (string, bool) {
	start := indexOpenSVG(s, 0)
	if start < 0 {
		return "", false
	}
	if end := lastCloseSVG(s); end > start {
		frag := strings.TrimSpace(s[start : end+len("</svg>")])
		return frag, true
	}
	frag := trimFenceTail(strings.TrimSpace(s[start:]))
	if frag == "" {
		return "", false
	}
	// 未闭合：补齐结束标签，便于后续解析/渲染。
	frag = strings.TrimRight(frag, " \t\r\n") + "</svg>"
	return frag, false
}

// extract 从任意模型输出里抽取 SVG。
//
// 顺序：```svg > ```xml > ```plain（裸围栏）；都没有时退回裸文本。
// 同一优先级下取最长候选（模型常先给草稿再给终版）。
func extract(text string) ExtractResult {
	if strings.TrimSpace(text) == "" {
		return ExtractResult{Source: "none"}
	}

	best := ExtractResult{Source: "none"}
	bestRank := -1

	for _, b := range fenceBlocks(text) {
		if indexOpenSVG(b.body, 0) < 0 {
			continue
		}
		src := "fence-plain"
		switch b.lang {
		case "svg":
			src = "fence-svg"
		case "xml":
			src = "fence-xml"
		}
		frag, closed := sliceSVG(b.body)
		if frag == "" {
			continue
		}
		r := sourceRank(src)
		if r > bestRank || (r == bestRank && len(frag) > len(best.SVG)) {
			best = ExtractResult{SVG: frag, Found: true, Closed: closed, Source: src}
			bestRank = r
		}
	}
	if best.Found {
		return best
	}

	frag, closed := sliceSVG(text)
	if frag == "" {
		return ExtractResult{Source: "none"}
	}
	return ExtractResult{SVG: frag, Found: true, Closed: closed, Source: "raw"}
}
