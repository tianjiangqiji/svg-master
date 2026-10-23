package svg

import (
	"encoding/xml"
	"errors"
	"io"
	"math"
	"strings"
)

// checkWellFormed 用 encoding/xml 严格解析一遍，确认是单一根元素。
func checkWellFormed(s string) error {
	dec := xml.NewDecoder(strings.NewReader(s))
	dec.Strict = true
	root := ""
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				if root != "" {
					return errors.New("存在多个根元素")
				}
				root = t.Name.Local
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(t)) != "" {
				return errors.New("根元素之外存在文本")
			}
		}
	}
	if root == "" {
		return errors.New("没有根元素")
	}
	return nil
}

// svgRootAttrs 用宽松解析取出根元素名与属性；解析失败时回退到标签扫描。
func svgRootAttrs(s string) (string, []attrKV, bool) {
	dec := xml.NewDecoder(strings.NewReader(s))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok {
			attrs := make([]attrKV, 0, len(se.Attr))
			for _, a := range se.Attr {
				attrs = append(attrs, attrKV{Name: attrName(a), Value: a.Value})
			}
			return se.Name.Local, attrs, true
		}
	}
	// 回退：直接扫开始标签
	r, ok := findRoot(s)
	if !ok {
		return "", nil, false
	}
	return "svg", r.attrs, true
}

// ratioClose 判断两个宽高比是否接近（相对误差 2%）。
func ratioClose(a, b float64) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	d := math.Abs(a-b) / math.Max(a, b)
	return d <= 0.02
}

// validate 只判断两件事：是不是 SVG，以及尺寸声明是否自洽。
// 根元素必须是 <svg>、XML 必须合法、viewBox 必须存在且格式正确，
// viewBox 与 width/height 的比例必须一致。
// 风格、元素数量、有没有文字/位图/滤镜一律不判断，也不改写任何内容。
func validate(s string) Report {
	rep := Report{Bytes: len(s)}
	addIssue := func(level, code, msg string) {
		rep.Issues = append(rep.Issues, Issue{Level: level, Code: code, Message: msg})
	}

	text := strings.TrimSpace(s)
	if text == "" {
		addIssue("error", "empty", "SVG 内容为空")
		return rep
	}
	// 允许直接传入带围栏的模型原文
	if indexOpenSVG(text, 0) < 0 {
		if r := extract(text); r.Found {
			text = r.SVG
		}
	}
	if indexOpenSVG(text, 0) < 0 {
		addIssue("error", "root", "文本中没有 <svg> 根元素")
		return rep
	}
	// 只切片定位，不修补标签平衡
	if body, ok := unwrapSVG(text); ok {
		text = body
	}

	// 严格解析：失败则说明 XML 不合法
	if err := checkWellFormed(text); err != nil {
		rep.Root = "svg"
		addIssue("error", "xml", "XML 解析失败："+err.Error())
		// 仍然尽量统计，便于前端展示
		countTokens(text, &rep)
		if strings.TrimSpace(rep.ViewBox) == "" {
			addIssue("error", "viewBox", "缺少 viewBox，缩放时会失真")
		}
		rep.OK = false
		return rep
	}

	rootName, attrs, ok := svgRootAttrs(text)
	if !ok {
		addIssue("error", "root", "文本中没有 <svg> 根元素")
		return rep
	}
	rep.Root = rootName
	if !strings.EqualFold(rootName, "svg") {
		addIssue("error", "root", "根元素不是 <svg>，实际为 <"+rootName+">")
	}

	rep.ViewBox = strings.TrimSpace(attrValue(attrs, "viewBox"))
	rep.Width = strings.TrimSpace(attrValue(attrs, "width"))
	rep.Height = strings.TrimSpace(attrValue(attrs, "height"))

	countTokens(text, &rep)

	if rep.ViewBox == "" {
		addIssue("error", "viewBox", "缺少 viewBox，缩放时会失真")
	}

	// warn：viewBox 与 width/height 比例不一致
	if rep.ViewBox != "" {
		if _, _, vw, vh, ok := parseViewBox(rep.ViewBox); ok {
			w, wok := parseLength(rep.Width)
			h, hok := parseLength(rep.Height)
			if wok && hok && !ratioClose(vw/vh, w/h) {
				addIssue("warn", "ratioMismatch",
					"viewBox 比例与 width/height 不一致，可能被拉伸")
			}
		} else {
			addIssue("warn", "viewBox", "viewBox 格式不合法，应为 4 个数字")
		}
	}

	rep.OK = true
	for _, is := range rep.Issues {
		if is.Level == "error" {
			rep.OK = false
			break
		}
	}
	return rep
}

// checkCanvasSize 把交付内容与画布尺寸规范逐一对齐，只报尺寸问题，绝不改写内容。
//
// canvas.Width / canvas.Height 非正数时视为「没有尺寸规范」，返回 nil。
// 能解析出具体数值且与画布不符 → error（必须重新按规范尺寸生成）；
// 缺失或写成百分比等无法核对的形式 → warn（不阻塞交付）。
func checkCanvasSize(s string, canvas Canvas) []Issue {
	if canvas.Width <= 0 || canvas.Height <= 0 {
		return nil
	}
	text := strings.TrimSpace(s)
	if text == "" {
		return nil
	}
	if indexOpenSVG(text, 0) < 0 {
		if r := extract(text); r.Found {
			text = r.SVG
		}
	}
	if body, ok := unwrapSVG(text); ok {
		text = body
	}
	r, ok := findRoot(text)
	if !ok {
		return nil
	}

	wantW := numStr(float64(canvas.Width))
	wantH := numStr(float64(canvas.Height))
	wantVB := strings.TrimSpace(canvas.ViewBox)
	if wantVB == "" {
		wantVB = "0 0 " + wantW + " " + wantH
	}

	var out []Issue
	add := func(level, code, msg string) {
		out = append(out, Issue{Level: level, Code: code, Message: msg})
	}

	if vb := strings.TrimSpace(attrValue(r.attrs, "viewBox")); vb == "" {
		add("error", "canvasViewBox", "缺少 viewBox：必须写成 `"+wantVB+"`")
	} else if _, _, vw, vh, ok := parseViewBox(vb); !ok {
		add("error", "canvasViewBox", "viewBox 格式不合法："+vb+"，应为 4 个数字")
	} else if math.Abs(vw-float64(canvas.Width)) > 0.5 || math.Abs(vh-float64(canvas.Height)) > 0.5 {
		add("error", "canvasViewBox",
			"viewBox 是 "+numStr(vw)+"×"+numStr(vh)+"，画布要求 "+wantVB)
	}

	wv := strings.TrimSpace(attrValue(r.attrs, "width"))
	hv := strings.TrimSpace(attrValue(r.attrs, "height"))
	switch {
	case wv == "" || hv == "":
		add("warn", "canvasSize", "建议显式写 width=\""+wantW+"\" height=\""+wantH+"\"（当前省略）")
	default:
		w, wok := parseLength(wv)
		h, hok := parseLength(hv)
		switch {
		case !wok || !hok:
			add("warn", "canvasSize", "width/height 无法核对："+wv+" / "+hv+"，画布要求 "+wantW+" / "+wantH)
		case math.Abs(w-float64(canvas.Width)) > 0.5 || math.Abs(h-float64(canvas.Height)) > 0.5:
			add("error", "canvasSize",
				"width/height 是 "+wv+" / "+hv+"，画布要求 "+wantW+" / "+wantH)
		}
	}

	return out
}

// countTokens 遍历 token 统计元素数、path/text/渐变数量（仅供前端展示，不作判定）。
func countTokens(s string, rep *Report) {
	dec := xml.NewDecoder(strings.NewReader(s))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		rep.Elements++
		switch strings.ToLower(se.Name.Local) {
		case "path":
			rep.Paths++
		case "text":
			rep.Texts++
		case "lineargradient", "radialgradient":
			rep.Gradients++
		}
	}
}
