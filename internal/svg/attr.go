package svg

import (
	"encoding/xml"
	"strconv"
	"strings"
)

const (
	svgNS   = "http://www.w3.org/2000/svg"
	xlinkNS = "http://www.w3.org/1999/xlink"
	xmlNS   = "http://www.w3.org/XML/1998/namespace"
)

// attrKV 是解析出来的一个属性（名字保留原样）。
type attrKV struct {
	Name  string
	Value string
}

func isXMLSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// parseAttrs 解析开始标签里 `<svg` 之后、`>` 之前的属性串。
// 支持双引号、单引号与无引号三种写法。
func parseAttrs(raw string) []attrKV {
	var out []attrKV
	i := 0
	for i < len(raw) {
		for i < len(raw) && isXMLSpace(raw[i]) {
			i++
		}
		if i >= len(raw) {
			break
		}
		if raw[i] == '/' { // 自闭合斜杠
			i++
			continue
		}
		start := i
		for i < len(raw) && !isXMLSpace(raw[i]) && raw[i] != '=' {
			i++
		}
		name := raw[start:i]
		for i < len(raw) && isXMLSpace(raw[i]) {
			i++
		}
		val := ""
		if i < len(raw) && raw[i] == '=' {
			i++
			for i < len(raw) && isXMLSpace(raw[i]) {
				i++
			}
			if i < len(raw) && (raw[i] == '"' || raw[i] == '\'') {
				q := raw[i]
				i++
				vs := i
				for i < len(raw) && raw[i] != q {
					i++
				}
				val = raw[vs:i]
				if i < len(raw) {
					i++
				}
			} else {
				vs := i
				for i < len(raw) && !isXMLSpace(raw[i]) {
					i++
				}
				val = raw[vs:i]
			}
		}
		if name != "" {
			out = append(out, attrKV{Name: name, Value: val})
		}
	}
	return out
}

// findTagEnd 从 start 起找不在引号内的 '>'。
func findTagEnd(s string, start int) int {
	var q byte
	for i := start; i < len(s); i++ {
		c := s[i]
		if q != 0 {
			if c == q {
				q = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			q = c
		case '>':
			return i
		}
	}
	return -1
}

// attrValue 取属性值（名字大小写不敏感）。
func attrValue(attrs []attrKV, name string) string {
	for _, a := range attrs {
		if strings.EqualFold(a.Name, name) {
			return a.Value
		}
	}
	return ""
}

// attrName 还原属性的完整名字（含命名空间前缀）。
func attrName(a xml.Attr) string {
	sp, local := a.Name.Space, a.Name.Local
	switch {
	case local == "xmlns" && sp == "":
		return "xmlns"
	case sp == "xmlns":
		return "xmlns:" + local
	case sp == "":
		return local
	case sp == xlinkNS:
		return "xlink:" + local
	case sp == xmlNS:
		return "xml:" + local
	case sp == svgNS:
		return local
	case strings.Contains(sp, "://") || strings.HasPrefix(sp, "urn:"):
		return local
	default:
		return sp + ":" + local
	}
}

// numStr 输出紧凑数字（最多 6 位小数，去掉尾随 0）。
func numStr(v float64) string {
	s := strconv.FormatFloat(v, 'f', 6, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "" || s == "-0" {
		s = "0"
	}
	return s
}

// parseViewBox 解析 viewBox 的四个数字。
func parseViewBox(vb string) (minX, minY, w, h float64, ok bool) {
	f := strings.FieldsFunc(vb, func(r rune) bool {
		return r == ' ' || r == ',' || r == '\t' || r == '\n' || r == '\r'
	})
	if len(f) != 4 {
		return 0, 0, 0, 0, false
	}
	nums := make([]float64, 4)
	for i, s := range f {
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, 0, 0, 0, false
		}
		nums[i] = n
	}
	if nums[2] <= 0 || nums[3] <= 0 {
		return 0, 0, 0, 0, false
	}
	return nums[0], nums[1], nums[2], nums[3], true
}

// parseLength 解析 length 属性里的数值（忽略 px/pt 等单位；百分比视为失败）。
func parseLength(v string) (float64, bool) {
	v = strings.TrimSpace(v)
	if v == "" || strings.Contains(v, "%") {
		return 0, false
	}
	end := 0
	for end < len(v) {
		c := v[end]
		if (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '+' || c == 'e' || c == 'E' {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.ParseFloat(v[:end], 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
