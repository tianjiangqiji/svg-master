# SVG Master · SVG 生成器

本地 SVG 生成工作台：Go 后端 + 原生 HTML 前端，Windows 主程序为单个 exe。支持四种 API 协议、ChatGPT 订阅接入、Agent 工具循环、图转 SVG、多图层编辑、动图预览与 GIF 导出。

模仿「鹈鹕 SVG 检测器（Pelican SVG Tester）」的形态，但做成真正能用的生成器。

| 连接方式 | 如何连接 | 额外依赖 |
|---|---|---|
| API Key | 配置供应商的协议、Base URL 和 API Key | 无需 Codex CLI |
| ChatGPT 订阅（Codex） | 在应用内登录一次，生成、模型列表与额度查询共用同一账号 | 官方 Codex CLI |

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

双击 `svg-master.exe` 即可。主程序和 `data` 文件夹可一起迁移；Codex 登录凭据由系统凭据管理器保存，迁移后可能需要重新登录。

### 方式二：源码运行

```bat
go run .                 :: 弹 WebView2 窗口
go run . -web            :: 不弹窗口，打印地址用浏览器打开
go run . -port 18080     :: 固定端口
go run . -data .\mydata  :: 指定数据目录
go run . -debug          :: WebView2 调试模式
```

### 依赖

- Go 1.27+（构建时，以 `go.mod` 为准）
- Microsoft Edge WebView2 运行时（运行 Windows 窗口时；Win10/11 一般已内置，缺失时自动降级为系统浏览器）
- 纯 Go 依赖，`CGO_ENABLED=0` 可交叉编译
- 官方 Codex CLI（仅 ChatGPT 订阅模式需要，当前验证版本为 **0.162.1**）

## ChatGPT 订阅接入

### 安装与首次登录

按[官方 Codex CLI 安装说明](https://learn.chatgpt.com/docs/codex/cli)安装 CLI。安装完成后，在新终端中确认命令可用：

```powershell
codex --version
```

重新打开 SVG Master，让程序读取更新后的 PATH，然后完成以下步骤：

1. 打开「管理供应商」，点击「新建」并填写名称。
2. 将「连接方式」设为 **ChatGPT 订阅（Codex）**。
3. 点击「登录 Codex」。程序会保存此供应商，并打开系统浏览器。
4. 在官方页面完成账号授权，返回应用等待显示「已登录 Codex」。无需填写 API Key。
5. 登录成功后会自动获取模型列表，也可点击「获取模型列表」，然后保存。
6. 从顶部模型选择器选中该供应商和模型，输入提示词并开始生成。

**同一供应商只需登录一次。** 生成 SVG、获取模型列表和显示额度使用同一 Codex 账号。可用模型以该账号返回的列表为准。

SVG Master 为每个供应商维护独立的登录，不自动使用其他 Codex 终端或桌面应用的账号。取消登录会结束当前授权流程；退出账号会同时断开该供应商的生成和额度查询，不影响其他供应商。

### 订阅额度

选择订阅供应商后，页面底部底栏显示「订阅额度」，单行排布套餐、周期剩余、进度条与重置时间。API Key 供应商不显示此面板。

| 账号类型 | 显示周期 | 显示内容 |
|---|---|---|
| Plus | **5h** | 剩余百分比、进度条、重置时间 |
| Pro，包括服务端返回的 `prolite`、`pro_*` 类型 | **7day** | 剩余百分比、进度条、重置时间 |
| 其他类型或缺少对应周期 | 显示状态说明 | 不推算额度，不把缺失数据当作 0% |

额度来自当前登录账号的 Codex 用量接口。剩余百分比为 `100 − 已用百分比`，重置时间按本机时区显示。页面可见时每分钟刷新，并在生成结束、返回页面时刷新；也可手动点击「刷新」。「管理额度」会打开 ChatGPT 用量设置页。

这里选择展示一个周期，并不表示账号只受这一项额度约束；实际可用额度和限制以服务端为准。额度耗尽不会自动改用 API Key 计费。

### Fast 加速

左侧 **Fast 加速** 默认关闭，开启状态会保存。此开关适用于 ChatGPT 订阅模式和 OpenAI Responses 协议，与「快速 / 多图层 / 动图」生成方式及思考等级分别设置。

| 连接方式 | 开启后发送的设置 |
|---|---|
| ChatGPT 订阅（Codex） | app-server 的 `serviceTier: "priority"` |
| OpenAI Responses（API Key） | Responses 请求的 `service_tier: "priority"` |

界面与日志根据服务端反馈显示结果：

- 返回 `priority` 或 `fast`：本轮已使用 Fast 加速。
- 返回 `default`：已请求 Fast，本轮按标准档位处理。
- 未返回档位：已请求 Fast，服务端未确认加速档位。

目前 Codex 适配能够提交 Fast 设置，但没有取得实际服务档位的确认，因此按第三种状态显示。开启开关不等于加速已经生效；额度或费用消耗取决于服务端执行结果。

### 网络与代理

程序不会写入或修改系统代理配置。HTTP 客户端优先采用 `HTTP_PROXY` / `HTTPS_PROXY` 环境变量；未配置这两个变量时，在 Windows 上读取已启用的系统手动代理，并处理 `NO_PROXY` 与系统绕过列表。仅设置 `NO_PROXY` 不会关闭系统代理回退。Codex 子进程会继承选定的代理及环境变量，不包含固定代理地址或端口。

当前不解析 PAC 自动代理脚本。调整代理或 PATH 后，请重启 SVG Master，以便已启动的 Codex 子进程使用新设置。

### 常见问题

| 现象 | 处理方式 |
|---|---|
| 提示需要安装 Codex CLI | 在新终端运行 `codex --version`，确认 CLI 已加入 PATH，再重启 SVG Master。 |
| 已在 Codex 终端登录，应用仍显示未登录 | 在 SVG Master 中为该供应商登录一次；两者使用独立的账号存储。 |
| 已登录但没有模型，或原模型不可用 | 点击「获取模型列表」，保存并重新选择当前账号可用的模型。 |
| 额度显示「服务端暂未返回」 | 点击刷新；只在服务端提供对应周期和有效用量时显示百分比。 |
| 授权或生成遇到超时、HTTP 403 | 检查程序所用的网络出口、代理环境变量和 Windows 系统代理。更换配置后重启应用；代理启用本身不保证服务端接受请求。 |

登录回调指向 `127.0.0.1` 是本机接收授权结果的正常方式。回调地址直连不会使发往 OpenAI 的请求自动绕过代理，两类连接应分别检查。

### 兼容范围

- 已在 Windows 和 Codex CLI **0.162.1** 上验证；尚未完成 macOS、Linux 的端到端验证。
- 接入启用了 app-server 实验能力，使用动态工具与历史注入。CLI 升级后需重新验证兼容性，相关协议见[官方 app-server 文档](https://learn.chatgpt.com/docs/app-server)。
- 订阅模式使用流式输出，「非流式兜底」仅适用于原有 API Key 协议。
- 单次生成内的推理上下文由 Codex 管理，SVG Master 保存可见对话及工具结果供下一次请求续接。原有 ACP 投影不会在每次工具调用后替换正在运行的 Codex 上下文。
- 绘图、图层和校验仍由 SVG Master 的工具执行；接入配置禁用 Codex 终端、网页搜索和子代理工具。

---

## 界面

顶栏只有品牌名 `SVG Master`（无副标题），右侧依次是：**模型徽标**（点击弹出该供应商下的模型菜单，可直接切换、也能一键跳去「管理供应商」）、**主题按钮**、**清理**、复制 SVG、保存 SVG；动图模式下「导出 GIF」插在保存按钮左边。标签页只有两个：`生成` / `日志流`。**原始输出不再是独立页签**，而是常驻右栏底部的分段面板（正文 / 思考 / 错误）。

左栏只有「生成」组：**生成方式三级开关**（快速 / 多图层 / 动图）、提示词、比例预设、思考等级、图片输入（可选）、**开始生成 / 停止生成** 单按钮（运行中同一按钮变「停止生成」），旁边是 **代码** 按钮用于在画布与代码视图之间切换；底部是「管理供应商」与「提示词库」。**连接信息不在主界面出现**，URL / Key / 协议全部只在供应商弹窗里维护。

右栏包括上下文用量（used/max、压缩块、TPS、tokens、模型轮/工具）、图层、AGENT 参数及**原始输出**。订阅模式会在页面底部底栏显示额度条。原始输出分为正文 / 思考 / 错误，支持单独清空。

**动图模式**下画布上方出现动图工具条（播放、帧率、帧位置），「导出 GIF」按钮则移到右上角、紧挨保存按钮；此时「保存」按钮变成「保存代码」，导出的是 SVG 源码文件。

- **日志流只显示摘要**：`TEXT` / `THINK` 连续事件合并进同一行，正文与思维链截到 90 字 + `…`，行尾标 `· N 字`；**全文一律去右栏「原始输出」看**，日志流不再被流式碎片刷屏。
- **代码视图**：点「代码」把画布换成源码面板——按标签自动换行与缩进、行号、`▾` 折叠（点父标签可整段收起，工具栏有「全部折叠 / 全部展开」）、「自动换行」、「复制代码」，底部同样带校验问题条。快捷键 `Ctrl + \``。
- **清理**：顶栏「清理」弹确认框，一次清空画板、日志、原始输出、已载入图片与当前会话上下文（供应商与设置不受影响）。

- **双主题**：深色 / 浅色，默认跟随系统（`prefers-color-scheme`），点击主题按钮按 `跟随系统 → 深色 → 浅色` 循环，选择记在 `localStorage`，首帧由内联脚本落定避免闪白。
- **画布**：棋盘格底，鼠标左键拖动平移、滚轮以光标为锚点缩放（0.1×–8×）、双击复位，工具栏有「适应窗口」和「1:1」。拖动与缩放走 `transform` + `requestAnimationFrame` 合并，只改合成层，不触发重排。
- **自定义下拉**：比例预设与思考等级都是自绘下拉（`position:fixed` 定位，不受左栏滚动裁切），带 130ms 展开动画与方向键/Esc 键盘操作。
- **思考等级**：默认 4 档 `off / low / medium / high`；在供应商面板勾选「扩展思考」后解锁 `xhigh — 极致` 与 `max — 最大`。未勾选时即使配置里写了 `xhigh`/`max`，落盘也会自动降级为 `high`。
- **流式文本合并**：连续 `TEXT` / `THINK` 事件合并进同一行日志，不刷屏；全文进右栏「原始输出」。

---

## API Key 模式的四种协议

| 协议 | 端点 | 鉴权头 |
|---|---|---|
| OpenAI 兼容 `openai-completions` | `POST {base}/chat/completions` | `Authorization: Bearer` |
| OpenAI Responses `openai-responses` | `POST {base}/responses` | `Authorization: Bearer` |
| Anthropic Messages `anthropic-messages` | `POST {base}/v1/messages` | `x-api-key` + `anthropic-version` |
| Google Gemini `google-generative-ai` | `POST {base}/v1beta/models/{model}:streamGenerateContent?alt=sse` | `x-goog-api-key` |

`base` 自动归一：Gemini 缺 `/v1beta` 自动补；Anthropic 保留原样；OpenAI 系不强制加 `/v1`。
所有协议统一映射为内部 `StreamEvent`（`text` / `thinking` / `tool_start` / `tool_delta` / `usage` / `done` / `error`），上层无感知。

## 模型选择器

- 「获取模型列表」在 API Key 模式下按协议拉取远端模型（OpenAI 系 `GET /models`、Anthropic `GET /v1/models`、Gemini `GET /v1beta/models`）；订阅模式使用当前 Codex 账号的模型列表。失败不影响手填，但手填模型也必须是账号实际可用的模型。
- 每个模型 id 自动推断能力：`contextWindow` / `maxTokens` / `reasoning` / `vision`（有序前缀规则，覆盖 gemini / claude / gpt / o 系 / deepseek / kimi / qwen / glm / grok / mistral / llama 等）。
- 能力值可在供应商面板里逐模型覆盖，覆盖优先于推断。
- 多供应商并存，可随时切换；API Key 落盘做本机混淆（`enc:` 前缀），界面回显打码。

## Agent 与上下文

- 多步工具循环：`emit_svg` / `validate_svg` / `compress` / `decompress` / `search_context` / `acp_status` / `ask_user`。会改写 SVG 或注入风格偏好的工具（`apply_canvas` / `list_prompts` / `search_prompts`）已按需求移除。
- 熔断与重试：流式重试 3 次、空回复重试 2 次、步数上限可配（默认 40）、同错重复 3 次熔断、5 分钟无事件 watchdog。
- **自动校验**：开启后 `emit_svg` 若校验出 error 级问题，会把问题清单作为工具错误回灌给模型，逼它继续修而不是带着错误收工。
- **非流式兜底**：API Key 模式下，部分网关不支持 SSE，勾选后改走一次性 JSON 响应，四协议均已实现完整响应解码，事件序列不变。
- **ACP 上下文压缩**：消息按 `mNNNNN` 编号，模型通过 `compress` 工具写 T1/T2/T3 三层摘要块；API Key 模式中投影折叠「发送视图」，原始历史完整保留，`decompress` 天然可逆；用量到阈值（默认 75%）自动 nudge；`contextWindow < 16000` 时退化为仅提示。Codex 模式的上下文行为见上方「兼容范围」。

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
- 画布下方出现动图工具条：**播放 / 暂停**、**帧率**（1–30，仅对多帧分组动画生效）、帧位置显示；**导出 GIF** 在右上角保存按钮旁边，此时保存按钮为「保存代码」，导出 SVG 源码。
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
│  ├─ codex/                   Codex 进程、账号登录、模型生成与额度查询
│  ├─ netproxy/                环境变量与 Windows 系统代理适配
│  ├─ agent/                   Agent 循环、工具、系统提示词、ACP 压缩
│  ├─ svg/                     提取 / 只读校验 / 画布尺寸规范 / 提示词库（不改写 SVG）
│  └─ api/                     HTTP + SSE 服务、go:embed 前端资源
│     └─ webdist/              index.html / app.js / style.css
├─ data/                       运行时数据（settings.json、会话 SVG）
└─ dist/svg-master/            打包产物
```

## Codex 接入实现

`internal/codex` 通过标准输入输出与官方 app-server 通信，提供符合现有 `llm.Client` 接口的适配器。账号授权和凭据刷新交给 CLI；SVG Master 接收模型事件并执行原有绘图工具。

| 操作 | 使用的 app-server 方法 |
|---|---|
| 登录、取消、退出、状态 | `account/login/start`、`account/login/cancel`、`account/logout`、`account/read` |
| 订阅额度 | `account/rateLimits/read` |
| 模型列表 | `model/list` |
| 开始生成与载入已有历史 | `thread/start`、`thread/inject_items`、`turn/start` |
| 绘图工具调用及结果回传 | `item/tool/call` 请求与对应响应 |
| 停止与释放订阅 | `turn/interrupt`、`thread/unsubscribe` |

每次用户发起生成时创建临时 Codex 线程，载入工程中保存的历史。工具调用交回 SVG Master 执行，将校验或执行结果返回同一 Codex 轮次，直到完成或用户停止。对已经发起的有状态请求，不使用普通 HTTP 流的自动重放重试。

供应商使用 `authMode: "chatgpt"` 选择 Codex；为空时沿用 API Key 模式。`fastMode` 是默认关闭的全局设置。旧设置未包含这两个字段时继续按原有方式工作。

每个供应商的 Codex 数据目录位于 `data/codex-quota/<供应商 ID 的 SHA-256>/`，作为该子进程的独立 `CODEX_HOME`。目录名称保持稳定，以便复用已创建的系统凭据条目；它同时服务于生成与额度查询。凭据由 CLI 的 `keyring` 存储管理，不写入供应商的 API Key 字段。迁移目录或更换系统用户时可能需要重新授权。

## 验证

在仓库根目录运行以下检查；Go 测试使用模拟服务或本地辅助进程，不需要登录真实账号：

```powershell
go test ./...
node --check internal/api/webdist/app.js
git diff --check
```

Node.js 仅用于 JavaScript 语法检查，不是主程序运行依赖。

| 验证项 | 覆盖内容 |
|---|---|
| Codex 连接测试 | 工具调用与错误结果回传、历史注入、用量累计、停止与取消订阅、结束后的请求禁止重放 |
| 账号与额度测试 | 供应商账号隔离、退出状态、Plus 5h、Pro / prolite / pro_* 7day、周期顺序变化、缺失及无效用量 |
| Fast 测试 | 默认关闭、设置持久化、单次请求覆盖、参数透传、按返回值显示实际档位 |
| 代理测试 | 环境代理优先、系统代理回退、仅有 NO_PROXY 时的处理、本机地址和绕过规则 |

Windows 本地联调已验证一次 Codex 登录后获取模型、生成蓝色圆形、在后续请求中改成绿色，并查询同一账号的 7day 额度。浏览器联调覆盖登录与退出、额度面板、浅色 / 深色主题、Fast 状态及 API Key 模式；这些浏览器检查使用本地辅助脚本，未作为仓库内自动化测试发布。Plus 5h 的显示已通过模拟测试，尚未用真实 Plus 账号验证。

## HTTP 接口

全部需带令牌（URL `?k=` 或头 `X-App-Token`），`Origin` 仅允许 `http://127.0.0.1` / `http://localhost`。

`GET /api/state`、`PUT /api/settings`、`GET|POST|DELETE /api/providers`、`GET /api/protocols`、`GET /api/prompts`、`GET|POST /api/canvas`、`POST /api/models`、`POST /api/validate`、`POST /api/export`、`POST /api/chat`（SSE）、`POST /api/chat/abort`、`POST /api/render-result`。

订阅模式新增以下接口，均需带已保存供应商的 `providerId` 查询参数，并遵循相同的本地令牌校验：

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/api/chatgpt/status` | 当前 Codex 登录状态 |
| GET | `/api/chatgpt/quota` | 当前账号的订阅额度 |
| POST | `/api/chatgpt/login` | 发起官方登录并打开浏览器 |
| POST | `/api/chatgpt/cancel` | 取消尚未完成的登录 |
| POST | `/api/chatgpt/logout` | 退出当前供应商账号 |
| POST | `/api/chatgpt/usage` | 打开 ChatGPT 用量设置页 |

详见 [INTERFACES.md](INTERFACES.md)。

## 安全边界

程序只监听 `127.0.0.1` 随机端口，带一次性令牌校验，不对外暴露；存储、渲染源与导出的 SVG 一律是模型交付的原文，程序不做净化或改写，**净化只发生在浏览器预览这一步**（`internal/api/webdist/app.js` 的 `sanitizeSVG()`，剥 `script` / `on*` / `foreignObject` / 外部 `href` / `javascript:`，只作用于 innerHTML 渲染副本）——导出文件保留原文，请勿在不受信任的浏览器环境中直接打开；API Key 仅在本机以混淆形式存储，不上传任何第三方。

## 许可

本项目代码 MIT。`internal/svg/prompts.json` 中的提示词模板来自公开仓库，遵循各自原始许可，已在 `source` 字段标注。
