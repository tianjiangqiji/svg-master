package agent

// pixelGifStaticHint 附加在静态多层系统提示之后：用户开启了 GIF 开关但工程还是静态形态。
// 模型按动图意识规划画面（构图居中、主体可拆分动作），并引导用户切换到动图形态逐帧制作。
const pixelGifStaticHint = `
【GIF 动图要求】用户希望最终得到可播放的像素动图。当前工程是静态多层形态，本轮先把画面按动图蓝本搭建：主体居中、动作关键姿势明确、背景与主体分层清晰，便于后续逐帧改编。完成本轮后提示用户：在「像素形态」中切换到动图（单层多帧），即可逐帧制作循环动画；画布超过 512×512 时需先缩小尺寸。`

// pixelGifAnimHint 附加在动图系统提示之后：强调循环交付标准，与 GIF 导出对齐。
const pixelGifAnimHint = `
【GIF 交付标准】用户明确要求可播放的 GIF 动图：帧序列必须能无限循环——首尾衔接自然，避免突兀跳变；主体动作幅度明确，静止背景保持逐帧一致以避免闪烁；帧数 6–16 帧、8 fps 起步，动作复杂时可到 24 帧；交付前用 filmstrip 检查首尾两帧的衔接。`

// pixelGifStaticPrompt 返回静态形态下的 GIF 附加提示。
func pixelGifStaticPrompt() string { return pixelGifStaticHint }

// pixelGifAnimPrompt 返回动图形态下的 GIF 附加提示。
func pixelGifAnimPrompt() string { return pixelGifAnimHint }
