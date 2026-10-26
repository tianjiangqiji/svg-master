# GIF 导出回归测试

`gif-export.cjs` 直接加载仓库中的前端页面，点击「导出 GIF」，再解码下载的文件检查实际像素。测试不启动后端，也不读取账号配置或修改用户工程。

运行需要 Node.js、已安装且可被 Node.js 解析的 Playwright，以及本机 Edge / Chromium。Node.js 和 Playwright 仅供开发测试，程序运行与 GIF 导出不依赖它们。

在仓库根目录执行：

```powershell
# 也支持安装在 .tools/ui-test/node_modules 中的 Playwright。
# 按本机安装位置修改；省略时使用 Playwright 的 msedge 通道。
$env:GIF_BROWSER_PATH = 'C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe'
node test/pw/gif-export.cjs
```

覆盖内容：

- 十二帧分组同时带 SMIL 透明度动画时，每一帧都能显示，导出后的预览继续播放。
- 按 `data-frame` 数值排序，覆盖内联样式、展示属性和 CSS 中的隐藏设置。
- 退出分组播放时恢复原有样式；暂停状态下导出不会自动开始播放。
- SMIL 的 `begin="0s"`、延迟开始、多个开始时间和结束时间能正确采样。
- 透明背景逐帧清除，移动物体不会留下上一帧的残影。
- 编码失败后恢复按钮状态并保留画布；静态 SVG 可导出单帧 GIF。

还可用自己的十二帧 SVG 验证完整导出流程：

```powershell
$env:GIF_OUTPUT_PATH = 'C:\Temp\example.gif'
node test/pw/gif-export.cjs 'C:\Temp\example.svg'
```

这个可选检查会确认十二帧均非空、后续帧与首帧不同、源码保持不变，且导出后预览继续切帧。省略 `GIF_OUTPUT_PATH` 时不保存导出文件。仓库内置的是最小复现样例，不包含用户工程。
