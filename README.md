# SVG Master · SVG 生成器

本地 SVG 生成工作台：Go 后端 + 原生 HTML 前端，单 exe 绿色运行。四协议直连、内置模型选择器、Agent 工具循环、ACP 上下文压缩、图转 SVG、实时渲染、比例预设、内置提示词库。

模仿「鹈鹕 SVG 检测器（Pelican SVG Tester）」的形态，但做成真正能用的生成器。

---

## 快速开始

### 方式一：绿色版（推荐）

```bat
build.bat
```

产物在 `dist\svg-master\`：

```
dist\svg-master\
├─ svg-master.exe      主程序（无控制台窗口）
├─ data\               运行时生成：settings.json / 会话 SVG
├─ README-FIRST.txt    中文使用说明
└─ README.md
```

双击 `svg-master.exe` 即可。整个文件夹可任意拷走，无注册表、无安装。

### 方式二：源码运行

```bat
go run .                 :: 弹 WebView2 窗口
go run . -web            :: 不弹窗口，打印地址用浏览器打开
go run . -port 18080     :: 固定端口
go run . -data .\mydata  :: 指定数据目录
go run . -debug          :: WebView2 调试模式
```

### 依赖

- Go 1.21+（构建时）
- Microsoft Edge WebView2 运行时（运行 Windows 窗口时；Win10/11 一般已内置，缺失时自动降级为系统浏览器）
- 纯 Go 依赖，`CGO_ENABLED=0` 可交叉编译

---

## 界面

顶栏只有品牌名 `SVG Master`（无副标题），右侧依次是：**模型徽标**（点击弹出该供应商下的模型菜单，可直接切换、也能一键跳去「管理供应商」）、**主题按钮**、**清理**、复制 SVG、保存 SVG。标签页只有两个：`生成` / `日志流`。**原始输出不再是独立页签**，而是常驻右栏底部的分段面板（正文 / 思考 / 错误）。

左栏只有「生成」组：**生成方式三级开关**（快速 / 多图层 / 动图）、提示词、比例预设、思考等级、图片输入（可选）、**开始生成 / 停止生成** 单按钮（运行中同一按钮变「停止生成」），旁边是 **代码** 按钮用于在画布与代码视图之间切换；底部是「管理供应商」与「提示词库」。**连接信息不在主界面出现**，URL / Key / 协议全部只在供应商弹窗里维护。

右栏从上到下三块：上下文用量（used/max、压缩块、TPS、tokens、模型轮/工具）→ AGENT 参数（最大步数、自动校验、上下文压缩、非流式兜底、压缩阈值）→ **原始输出**（正文 / 思考 / 错误三段切换，底部显示 `正文 N 字 · 思考 N 字`，可单独清空当前段）。

- **日志流只显示摘要**：`TEXT` / `THINK` 连续事件合并进同一行，正文与思维链截到 90 字 + `…`，行尾标 `· N 字`；**全文一律去右栏「原始输出」看**，日志流不再被流式碎片刷屏。
- **代码视图**：点「代码」把画布换成源码面板——按标签自动换行与缩进、行号、`▾` 折叠（点父标签可整段收起，工具栏有「全部折叠 / 全部展开」）、「自动换行」、「复制代码」，底部同样带校验问题条。快捷键 `Ctrl + \``。
- **清理**：顶栏「清理」弹确认框，一次清空画板、日志、原始输出、已载入图片与当前会话上下文（供应商与设置不受影响）。

- **双主题**：深色 / 浅色，默认跟随系统（`prefers-color-scheme`），点击主题按钮按 `跟随系统 → 深色 → 浅色` 循环，选择记在 `localStorage`，首帧由内联脚本落定避免闪白。
- **画布**：棋盘格底，鼠标左键拖动平移、滚轮以光标为锚点缩放（0.1×–8×）、双击复位，工具栏有「适应窗口」和「1:1」。拖动与缩放走 `transform` + `requestAnimationFrame` 合并，只改合成层，不触发重排。
- **自定义下拉**：比例预设与思考等级都是自绘下拉（`position:fixed` 定位，不受左栏滚动裁切），带 130ms 展开动画与方向键/Esc 键盘操作。
- **思考等级**：默认 4 档 `off / low / medium / high`；在供应商面板勾选「扩展思考」后解锁 `xhigh — 极致` 与 `max — 最大`。未勾选时即使配置里写了 `xhigh`/`max`，落盘也会自动降级为 `high`。
- **流式文本合并**：连续 `TEXT` / `THINK` 事件合并进同一行日志，不刷屏；全文进右栏「原始输出」。

---

## 四种协议

| 协议 | 端点 | 鉴权头 |
|---|---|---|
| OpenAI 兼容 `openai-completions` | `POST {base}/chat/completions` | `Authorization: Bearer` |
| OpenAI Responses `openai-responses` | `POST {base}/responses` | `Authorization: Bearer` |
| Anthropic Messages `anthropic-messages` | `POST {base}/v1/messages` | `x-api-key` + `anthropic-version` |
| Google Gemini `google-generative-ai` | `POST {base}/v1beta/models/{model}:streamGenerateContent?alt=sse` | `x-goog-api-key` |

`base` 自动归一：Gemini 缺 `/v1beta` 自动补；Anthropic 保留原样；OpenAI 系不强制加 `/v1`。
所有协议统一映射为内部 `StreamEvent`（`text` / `thinking` / `tool_start` / `tool_delta` / `usage` / `done` / `error`），上层无感知。

## 模型选择器

- 「获取模型列表」按协议拉取远端模型（OpenAI 系 `GET /models`、Anthropic `GET /v1/models`、Gemini `GET /v1beta/models`），失败不影响手填。
- 每个模型 id 自动推断能力：`contextWindow` / `maxTokens` / `reasoning` / `vision`（有序前缀规则，覆盖 gemini / claude / gpt / o 系 / deepseek / kimi / qwen / glm / grok / mistral / llama 等）。
- 能力值可在供应商面板里逐模型覆盖，覆盖优先于推断。
- 多供应商并存，可随时切换；API Key 落盘做本机混淆（`enc:` 前缀），界面回显打码。

## Agent 与上下文

- 多步工具循环：`emit_svg` / `validate_svg` / `compress` / `decompress` / `search_context` / `acp_status` / `ask_user`。会改写 SVG 或注入风格偏好的工具（`apply_canvas` / `list_prompts` / `search_prompts`）已按需求移除。
- 熔断与重试：流式重试 3 次、空回复重试 2 次、步数上限可配（默认 40）、同错重复 3 次熔断、5 分钟无事件 watchdog。
- **自动校验**：开启后 `emit_svg` 若校验出 error 级问题，会把问题清单作为工具错误回灌给模型，逼它继续修而不是带着错误收工。
- **非流式兜底**：部分网关不支持 SSE，勾选后改走一次性 JSON 响应，四协议均已实现完整响应解码，事件序列不变。
- **ACP 上下文压缩**：消息按 `mNNNNN` 编号，模型通过 `compress` 工具写 T1/T2/T3 三层摘要块；投影只折叠「发送视图」，原始历史完整保留，`decompress` 天然可逆；用量到阈值（默认 75%）自动 nudge；`contextWindow < 16000` 时退化为仅提示。

## 生成方式（三级开关）

左栏顶部是**可点击、也可按住拖动的三档开关**，档位即模型的工作方式：

| 档位 | 交付形态 | 工具 | 适合 |
|---|---|---|---|
| **快速模式** | 一次性整幅 SVG | 无图层工具，最快 | 单图、图标、一次性成图 |
| **多图层模式** | 分步、按图层创建与改造 | 图层增删改、逐层渲染 | 复杂画面、局部返工 |
| **动图模式** | 仍是**单个图层**，但交付可播放动图 | 同快速模式 + 动画规范 | 需要动效的图标 / 加载动画 / 示意 |

- **切换资格**：**没点过「开始生成」之前，三档可以随便来回切**（动图 ↔ 快速 ↔ 多图层都不受限，刷新页面也不锁）。**点过「开始生成」之后才锁**——只有仍停在快速模式时还留**最后一次**切换机会，切走之后三档全部置灰、拖动也被拒绝。
- **动图是单向门，但只在开始之后**：在动图模式下点了「开始生成」，就再也切不回快速 / 多图层。出口是工程弹窗里的**「快速生成」**——它本来就是清空画板重开，等同于换一个快速模式的新会话；「新建项目 / 打开项目」不会解除这道门。
- **空画板不多问一句**：当前快速工程本来就空（所有图层无内容且没有对话历史）时，点「快速生成」直接进入，不弹「将清空临时工程的图层与上下文」的确认框。
- **图层数约束**：从多图层切回快速或动图时，若当前仍有多个图层，会提示先把图层删到 1 个（否则单图层交付无处落地）。

### 动图预览与 GIF 导出

- 动图模式下的 SVG 照常渲染在画布上，**SMIL（`<animate>` / `<animateTransform>` / `<animateMotion>` / `<set>`）由浏览器原生播放**，不需要额外动画库。
- 画布下方出现动图工具条：**播放 / 暂停**、**帧率**（1–30，仅对多帧分组动画生效）、帧位置显示、**导出 GIF**。
- **导出 GIF 是纯前端实现**：从当前 SVG 的固定快照逐帧序列化 → `Image` 载入 → `drawImage` 到 canvas → 自写 GIF89a 编码器（中位切分量化 + LZW + 透明背景逐帧清除）打包成 Blob 下载。导出保持画布原有播放 / 暂停状态，零第三方依赖、不经过后端、不需要联网。
- 多帧分组动画（多个 `data-frame` 分组）按编号排序，预览和导出统一控制分组的 `display`、`opacity`、`visibility`，兼容同时带 SMIL 显隐动画的 SVG；纯 SMIL 动画走「按帧率平移时间采样」，两条路径共用同一个编码器。原始 SVG 源码保持不变。
- GIF 导出的浏览器回归测试及运行方式见 [test/pw/README.md](test/pw/README.md)。

## 生成能力

- **文生 SVG**：默认提示词就是主题本身 `A pelican riding a bicycle.`，改这几个字即可换任意主题。
- **输出规范写死**：「只交付 SVG 代码、只走 `emit_svg` 通道」属于系统提示词，与用户提示词解耦——提示词库里的模板只描述「画什么」，不重复「怎么交」。
- **图转 SVG**：拖拽 / 粘贴 / 选择图片 → 前端缩放到长边 ≤1568px → 以 base64 送入 vision 模型，要求输出可编辑矢量。
- **实时渲染**：Agent 每交付一版 SVG 立即推给前端重绘，不等整轮结束。
- **比例预设**：1:1 / 16:9 / 4:3 / 3:2 / 2:3 / 9:16 / Logo 方形 / Logo 横版 / 图标 24 与 64 / A4 / 横幅 / 头像 / 卡片等。切换预设只更新「尺寸规范」，已生成的 SVG 保持原样、不做重排。
- **只校验、不改写**：模型交付什么就存什么。校验器只读地报告根元素、XML 合法性、viewBox 与画布尺寸是否一致（viewBox/width/height 偏离画布为 error，可回灌逼模型自修）；不再做白名单净化、归一补属性、apply_canvas 包 transform。

## 提示词库

40+ 条英文提示词模板，8 个分类（Logo/Brand、Icon、Illustration、Diagram、UI Mock、Chart、Character、Pattern/Decorative），每条带 `{{subject}}` 占位符，点击即按当前主题替换填入。来源标注在 `internal/svg/prompts.json` 的 `source` 字段（LLM4SVG、awesome-ai-logo-generator-prompts、Simon Willison pelican benchmark、builtin）。

---

## 目录结构

```
svg-master/
├─ main.go                     入口与命令行参数
├─ build.bat / build.sh        绿色版打包
├─ INTERFACES.md               内部接口契约
├─ internal/
│  ├─ app/                     进程装配、WebView2 窗口 / 浏览器降级
│  ├─ config/                  Settings 模型、落盘、密钥混淆
│  ├─ llm/                     四协议流式客户端、模型列表、能力推断
│  ├─ agent/                   Agent 循环、工具、系统提示词、ACP 压缩
│  ├─ svg/                     提取 / 只读校验 / 画布尺寸规范 / 提示词库（不改写 SVG）
│  └─ api/                     HTTP + SSE 服务、go:embed 前端资源
│     └─ webdist/              index.html / app.js / style.css
├─ data/                       运行时数据（settings.json、会话 SVG）
└─ dist/svg-master/            打包产物
```

## HTTP 接口

全部需带令牌（URL `?k=` 或头 `X-App-Token`），`Origin` 仅允许 `http://127.0.0.1` / `http://localhost`。

`GET /api/state`、`PUT /api/settings`、`GET|POST|DELETE /api/providers`、`GET /api/protocols`、`GET /api/prompts`、`GET|POST /api/canvas`、`POST /api/models`、`POST /api/validate`、`POST /api/export`、`POST /api/chat`（SSE）、`POST /api/chat/abort`、`POST /api/render-result`。

详见 [INTERFACES.md](INTERFACES.md)。

## 安全边界

程序只监听 `127.0.0.1` 随机端口，带一次性令牌校验，不对外暴露；存储、渲染源与导出的 SVG 一律是模型交付的原文，程序不做净化或改写，**净化只发生在浏览器预览这一步**（`internal/api/webdist/app.js` 的 `sanitizeSVG()`，剥 `script` / `on*` / `foreignObject` / 外部 `href` / `javascript:`，只作用于 innerHTML 渲染副本）——导出文件保留原文，请勿在不受信任的浏览器环境中直接打开；API Key 仅在本机以混淆形式存储，不上传任何第三方。

## 许可

本项目代码 MIT。`internal/svg/prompts.json` 中的提示词模板来自公开仓库，遵循各自原始许可，已在 `source` 字段标注。
