package svg

import "strings"

// 本文件只做「读取」：定位根标签、切出 SVG 主体。
// 任何改写 SVG 内容的逻辑（净化、补属性、包裹 transform）都已按需求移除，
// 交付的 SVG 与模型给出的内容逐字一致。

// rootTag 定位根开始标签：返回标签在原文中的区间与属性。
type rootTag struct {
	start   int // '<' 的下标
	end     int // '>' 之后的下标
	attrs   []attrKV
	selfEnd bool // <svg/> 自闭合
}

// findRoot 找到第一个 <svg ...> 开始标签。
func findRoot(s string) (rootTag, bool) {
	i := indexOpenSVG(s, 0)
	if i < 0 {
		return rootTag{}, false
	}
	end := findTagEnd(s, i)
	if end < 0 {
		return rootTag{}, false
	}
	inner := s[i+4 : end]
	selfEnd := strings.HasSuffix(strings.TrimSpace(inner), "/")
	return rootTag{start: i, end: end + 1, attrs: parseAttrs(inner), selfEnd: selfEnd}, true
}

// closeRoot 找与根标签配对的 </svg>，返回它的起始下标（找不到返回 -1）。
func closeRoot(s string, from int) int {
	low := strings.ToLower(s)
	depth := 0
	i := from
	for i < len(low) {
		next := strings.Index(low[i:], "<")
		if next < 0 {
			return -1
		}
		next += i
		if strings.HasPrefix(low[next:], "</svg") {
			if depth == 0 {
				return next
			}
			depth--
			i = next + len("</svg")
			continue
		}
		if j := indexOpenSVG(low, next); j == next {
			e := findTagEnd(s, next)
			if e < 0 {
				return -1
			}
			if !strings.HasSuffix(strings.TrimSpace(s[next:e]), "/") {
				depth++
			}
			i = e + 1
			continue
		}
		i = next + 1
	}
	return -1
}

// unwrapSVG 从任意文本里切出 <svg>…</svg> 主体（含根标签）。
// 只是切片定位，不增删任何属性。
func unwrapSVG(s string) (string, bool) {
	r, ok := findRoot(s)
	if !ok {
		return "", false
	}
	if r.selfEnd {
		return s[r.start:r.end], true
	}
	c := closeRoot(s, r.end)
	if c < 0 {
		return "", false
	}
	e := strings.IndexByte(s[c:], '>')
	if e < 0 {
		return "", false
	}
	return strings.TrimSpace(s[r.start : c+e+1]), true
}
