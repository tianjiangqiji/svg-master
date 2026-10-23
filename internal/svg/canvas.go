package svg

// presets 返回内置画布预设（前端下拉直接消费）。
// 第一个与 config.DefaultSettings 的默认画布同名，方便前端选中。
//
// 预设只描述「尺寸规范」：宽高与 viewBox 数值。系统不会改写模型交付的 SVG
// （既不 resize，也不包裹 <g transform>），要换比例只能重新生成。
func presets() []Canvas {
	mk := func(name string, w, h int) Canvas {
		return Canvas{
			Name:    name,
			Width:   w,
			Height:  h,
			ViewBox: "0 0 " + numStr(float64(w)) + " " + numStr(float64(h)),
		}
	}
	return []Canvas{
		mk("1:1 方形", 1024, 1024),
		mk("1:1 方形 512", 512, 512),
		mk("16:9 宽屏", 1920, 1080),
		mk("4:3 标准", 1200, 900),
		mk("3:2 横版照片", 1500, 1000),
		mk("2:3 竖版照片", 1000, 1500),
		mk("9:16 竖屏", 1080, 1920),
		mk("Logo 方形 512", 512, 512),
		mk("Logo 横版 800×240", 800, 240),
		mk("图标 24×24", 24, 24),
		mk("图标 64×64", 64, 64),
		mk("A4 打印 1240×1754", 1240, 1754),
		mk("横幅 1584×396", 1584, 396),
		mk("头像 400×400", 400, 400),
		mk("卡片 1200×630", 1200, 630),
	}
}
