# 桌面组件框架 · 设计文档（下一阶段）

> 状态：草案 v2（已并入第一轮决策）
> 基线：`main` @ `a8a8fce`（无边框 WebView2 便签 + 托盘）
> 本文档回答：做成什么样、怎么分层、插件怎么写、先做什么。

---

## 0. 决策记录

第一轮已经定下来的事，后面所有设计都以这张表为准：

| # | 问题 | 决定 |
|---|---|---|
| 1 | 插件怎么分发 | **设置窗口里手动导入**（本地 zip 或目录）。宿主在容器内解包 → 做**冲突与兼容性检测** → 落到指定插件目录。**不做签名校验、不做版本号校验** |
| 2 | 多显示器 | **只做指定屏幕**，在设置里可细调（全局默认 + 单组件覆盖） |
| 3 | 提示与日志 | **内核能力，不是组件**。通知、日志、存储等共性行为全部在内核里实现，组件只能通过 API 请求 |
| 4 | 组件并发规模 | **暂不考虑大量组件同时存在**，不做 renderer 池复用，每个可见组件一个控制器 |
| 5 | 本期范围 | **只做技术框架**，不做组件市场、评级、下载量等运营字段 |
| 6 | 插件分级 | **两级**：L1 = 样式包（纯 JSON 声明，无程序），L2 = 组件（HTML/CSS/JS）。L2 只能复用 L1 与内置样式，不能自己声明窗口语义 |
| 7 | 样式覆盖 | **L1 与 L2 都可以带样式，L2 默认覆盖 L1**。但只允许覆盖视觉字段，语义字段 L2 动不了 |
| 8 | 依赖失效 | 引用的 L1 被禁用/卸载时，L2 实例**一并停用**，不做静默降级 |

由决策 3 派生出两个结构调整，都是**收缩**而非扩张：

- **「悬浮提示栏」不再是插件的一种形态**，而是内核通知服务的专用窗口形态（详见 §3.3）。
- **内核与组件的边界按「共性 / 个性」划分**，凡是所有组件都可能要用、且必须统一口径的
  能力都收进内核（详见 §3.1）。

由决策 6/7 派生出的结构变化：**窗口配置从「组件的字段」变成「三层合并的结果」**
（兜底默认 → L1 样式 → L2 视觉覆盖），详见 §3.4。

另有一条后补的视觉结论建议先知道：**真磨砂做不到**——`backdrop-filter` 在这里不是"没效果"，
而是把背后抹成一片纯色，比不写更糟。半透明和不透明都是真的，详见 §24.3 / §24.4。

---

## 1. 一句话定义

把现在的「一个便签程序」改造成 **组件宿主**：宿主只负责窗口、进程、存储、权限与调度；
所有可见的东西——包括便签本身——都是**组件**，用同一套清单、同一套 API、同一套生命周期。

内置组件与第三方插件的唯一区别是**来源与默认启用状态**，不是能力。这条决定了后面所有设计。

---

## 2. 概念模型

| 名词 | 含义 | 类比 |
|---|---|---|
| **组件（Component）** | 一份静态定义：清单 + 前端资源 + 可选资源文件。**会跑代码** | foobar2000 的 component 包 |
| **组（Group）** | 一组组件的共享外观与窗口行为。**内核运行时数据，不是插件包**，用户在管理界面里直接编辑 | 一个「模具」，同时决定模具里那批窗口怎么摆在桌面上 |
| **实例（Instance）** | 组件的一次运行，有自己的位置、尺寸、私有状态 | 同时开两个便签，是两个实例 |
| **宿主（Host）** | 内核：窗口管理、桥接、存储、权限、通知、调度 | 播放器本体 |
| **解析后样式（ResolvedStyle）** | 兜底默认 + 组件建议 + 组覆盖，合并后的最终窗口配置 | 合并后的成品模具 |
| **皮肤（Theme）** | 一组命名的 `cssVars` 覆盖，属于组件内部资源。换皮肤 = 换一组变量，不动窗口语义 | 同一模具换一套配色 |

关键约束：**组件不能直接操作窗口**。它只能通过桥接 API 请求宿主做动作，宿主决定允不允许。
这是能做权限、能做点击穿透、能统一调度的前提。

**组 → 实例是一对多**：一个组管着一批窗口，这些窗口共用这套外观与窗口行为。
归属记在实例上（`Instance.GroupID`），不记在组件上——同一个组件可以开两个窗口，
一个贴桌面层、一个浮在最上面。组的定义见 §25，归属为什么下移见 §26。

---

## 3. 内核与组件的职责边界

这是整份文档最重要的一节。边界划错了，后面所有 API 都会跟着歪。

### 3.1 什么是内核，什么是组件

| 能力 | 归属 | 理由 |
|---|---|---|
| 通知（入队、去重、时长、等级、渲染） | **内核** | 全局行为，必须唯一出口 |
| 日志（收集、落盘、轮转） | **内核** | 组件只写不落盘，避免各写各的 |
| 存储（文件、原子写、防抖） | **内核** | 组件只有自己的命名空间 |
| 窗口（创建、层级、点击穿透、显隐） | **内核** | 组件不能直接操作窗口 |
| 样式解析（三层合并、字段合法性、词表校验） | **内核** | L1 只是数据，解释权必须唯一 |
| 主题下发、设置、开机自启 | **内核** | 全局状态 |
| 权限判定 | **内核** | 组件无法自己授权 |
| 具体界面与业务逻辑 | 组件 | 这才是组件该干的事 |

判断标准很简单：**凡是「所有组件都可能要用、且必须统一口径」的，都进内核；
凡是「只有某类组件才需要」的，才是组件。**

通知是最典型的例子——如果允许每个组件自己弹浮层，「谁在骚扰用户」就没有唯一答案了，
日志也对不上账。

### 3.2 窗口形态（内核能力，共三种）

窗口只有三条代码路径，由内核实现，不随插件数量膨胀：

| 形态 | 层级 | 鼠标 | 任务栏/Alt+Tab | 被普通窗口覆盖 | 谁在用 |
|---|---|---|---|---|---|
| `desktop` | 挂到 `WorkerW`（桌面层） | 可交互 | 无 | **是** | 桌面类组件 |
| `topmost` | `HWND_TOPMOST` | 可交互 | 无 | 否 | 置顶类组件 |
| `toast` | 非激活 + 点击穿透 | **穿透** | 无 | 否（最上层） | **只有内核通知服务** |

实现要点：

- **`desktop`**：通过 `SendMessageTimeout(Progman, 0x052C, ...)` 让 Explorer 生成 `WorkerW`，
  找到持有 `SHELLDLL_DefView` 的那个 `WorkerW`，取其下一个兄弟窗口作为父窗口 `SetParent`。
  窗口因此成为桌面的子窗口：被所有应用覆盖、`Win+D` 跟随桌面显隐。
  降级方案：`SetWindowPos(HWND_BOTTOM)` + `WS_EX_NOACTIVATE`（简单，但 `Win+D` 会最小化）。
- **`topmost`**：现有便签的行为，原样保留。
- **`toast`**：`WS_EX_TRANSPARENT | WS_EX_NOACTIVATE | WS_EX_TOOLWINDOW`，
  用 `SW_SHOWNOACTIVATE` 显示，永不抢焦点。点击穿透靠 `WS_EX_TRANSPARENT`，
  不依赖前端 `pointer-events`——鼠标事件根本不会进 WebView。
- 三者都带 `WS_EX_TOOLWINDOW`，入口统一走托盘，不占任务栏。

### 3.3 插件分级

插件分两级。区别不是「权限多少」，而是**「是数据还是程序」**：

| 层级 | 名称 | 形态 | 能做什么 | 不能做什么 |
|---|---|---|---|---|
| **L1** | 样式包 | **纯 JSON 声明**，无 HTML、无 JS | 定义一种窗口模板：层级、激活、可缩放、停靠、留白、圆角、CSS 变量 | 没有行为逻辑；不创建 WebView；不需要权限；不占 renderer |
| **L2** | 组件 | HTML / CSS / JS | 提供内容与交互；可覆盖所绑样式的**视觉**参数 | 不能自己声明窗口语义；不能绕过宿主建窗口 |

**三种内置样式（`builtin.style.desktop` / `builtin.style.topmost` /
`builtin.style.toast`）与 L1 完全同级**，只是随 exe 打包、默认启用、不可卸载。
示例组件（便签、时钟、弹幕）都是普通 L2 插件，各自绑一个内置样式——
与第三方走完全相同的加载路径。

这条分级的实际意义：

- **L1 的价值是「被复用」**：当多个 L2 想共享同一套窗口定义时，L1 才有存在意义。
  如果只有一个组件用某个样式，它就该直接写在 L2 里，不必单独做一个 L1。
- **L2 的价值是「被运行」**：只有 L2 会创建窗口、跑 JS、持有存储、申请权限。
- 窗口形态仍只有三条代码路径（§3.2），L1 只是在参数空间里取值，
  **不会随插件数量膨胀**。

**`toast` 不对任何插件开放**，包括 L1。它是内核通知服务的专用形态。
想要「滚动字幕」这类效果，组件只能通过 `host.notify` 请求内核去显示。

### 3.4 样式解析与覆盖规则

一个实例最终生效的窗口配置，由三层合并而成，后者覆盖前者：

```
builtin 兜底默认（宿主内建，任何字段缺失都由它补齐）
   ↓ 覆盖
L1 样式包  .window / .visual / .cssVars
   ↓ 覆盖
L2 组件    .visual / .cssVars      ← 只允许覆盖视觉，不能覆盖语义
   ↓
ResolvedStyle  →  宿主据此建窗口 + 把 cssVars 注入组件页面
```

**语义字段 L2 不可覆盖**：`layer` / `activation` / `resizable` / `sizeMode` / `dock` / `titlebar`。

理由很直接：如果 L2 能改 `layer`，L1 就形同虚设，分级退化成「每个组件自己声明窗口」，
§3.2 那句「只有三条代码路径」也就不成立了。**分级要成立，语义必须锁在 L1。**

**视觉字段 L2 可覆盖**：`padding` / `radius` / `background` / `shadow` / `cssVars`。
这就是「二级样式默认覆盖一级样式」——组件想换配色、换留白，不必另写一个 L1。

**边界要划清：L1 管的是「窗口外壳」，不是「组件内部」。**
窗口的留白、圆角、背景、阴影、是否带拖动条，由 L1 决定；组件内部长什么样
（标题栏放哪、按钮怎么排、字号多少）永远归 L2 自己的 HTML/CSS。
想让便签内部换配色，那是 L2 的 `themes`，不是 L1——否则 L1 就得知道每个组件的 DOM 结构，
它就不再是声明式数据了。

**依赖失效**（决策 8）：L1 被禁用或卸载时，引用它的 L2 实例**一并停用**——
窗口销毁、状态置 `suspended`、设置窗口里标红「样式缺失」；重新启用该 L1 后自动恢复。
不做静默降级：一个便签突然换了副样子，比它消失更让人困惑。

**L1 的表达边界**（诚实说明）：纯 JSON 能描述「静态模板」，描述不了「行为」。
像「贴边自动隐藏」「内容高度自适应」这类需要测量的效果，JSON 表达不了。两条出路：

1. 该行为足够通用 → 由内核做成词表里的一个取值（如 `dock: rightAutoHide`），所有 L1 共享；
2. 该行为只此一家 → 说明它不该是 L1，直接写在 L2 组件里。

v1 不提供「L1 带 JS」的形态。一旦 L1 能跑代码，它就等于 L2，分级也就没有意义了。

---

## 4. 架构分层

延续现有 DDD 四层，依赖仍然单向向内：

```
cmd/stickynote/             组合根：装配依赖、启动宿主
internal/
├── domain/                 领域层：不含框架、不含 Win32
│   ├── component/          组件定义、实例、样式、ResolvedStyle、权限、生命周期状态
│   ├── notice/             通知模型（等级、时长、队列规则）
│   └── setting/            全局设置（含指定屏幕）
├── application/            应用层：用例编排
│   ├── componentapp/       安装/启用/停用/移动/改尺寸/休眠/恢复
│   ├── noticeapp/          通知入队、去重、过期
│   └── settingsapp/
├── infrastructure/         基础设施：实现领域定义的端口
│   ├── persistence/        JSON 仓储（components / layout / settings）
│   └── plugin/             插件包解析、解包、校验、安装
└── interfaces/             表现层
    ├── webview/            宿主窗口、控制器管理、桥接、托盘、通知窗口
    └── settings/           设置窗口
builtin/                    内置资源（以插件包形式提供，与第三方同构）
├── styles/
│   ├── desktop/            内置桌面样式（L1 级 JSON）
│   └── topmost/            内置置顶样式（L1 级 JSON）
├── note/                   便签（L2 组件，绑定 builtin.style.topmost）
└── clock/                  时钟（L2 示例组件）
web/                        设置窗口前端（编进 exe）
docs/                       本目录
```

**与现在的对应关系**：

| 现在 | 之后 |
|---|---|
| `domain/note` | **移除**，下沉为 `domain/component` 的通用实例状态 |
| `application/noteapp` | 拆进 `componentapp`，`note.*` 变成通用 `storage.*` |
| `interfaces/webview/app.go` 分派表 | 变成桥接路由，按 `componentId` 分区 |
| `web/static/`（便签界面） | 移到 `builtin/note/`，成为组件资源 |
| `web/` | 改成设置窗口前端 |

> 便签从 `domain` 层退到 `builtin/note/` 是一次**降级**，不是丢失：
> 它的业务规则（正文长度上限、尺寸下限）本来就简单，改成组件后由通用能力覆盖，
> 换来的是「一条便签 = 一个实例」，同时开多个便签变成零成本。

---

## 5. 运行时模型

### 5.1 一种运行时

**v1 只做一种运行时：WebView2。** 组件的行为逻辑（定时器、数据获取、状态机）就在它自己的
前端 JS 里跑，不引入 goja、不引入独立进程。

**L1 样式包不参与运行时**：它只是一段启动时被解析的 JSON，不创建控制器、不占进程、不需要权限。
这也是分级能省资源的原因——样式再多，进程数只跟 L2 实例数有关。

理由：要求「行为上保持一致」。两套运行时会立刻分裂成两套 API、两套调试链路、两套权限模型。
一种运行时的代价是每个组件多一份 renderer 进程，这是可以量化的；两套运行时的代价不可量化。

### 5.2 进程与内存

- 所有 WebView 控制器**共享同一个 `ICoreWebView2Environment`**（同一 user data 目录）。
  浏览器进程、GPU 进程、网络进程全局只有一份，每多一个组件只多一个 renderer 进程。
- 实测基线：单控制器时 WebView2 侧工作集 349 MB / 私有 150 MB（含跨应用共享页）。
  每增加一个 renderer 约 +50–80 MB 工作集。**这是本方案最大的成本，必须写进预期。**
- **不做控制器池化**（决策 4）：每个可见组件一个控制器，不可见即销毁，不做复用。
  池化在组件数量少时只会增加状态复杂度，等真有几十个组件的需求再说。
- **休眠**：不可见超过 N 秒（默认 300s，`desktop` 类型不参与）后销毁控制器，
  保留实例元数据；唤出时重建。这是唯一的省内存手段。

### 5.3 线程模型

沿用现状：`runtime.LockOSThread()` 固定 UI 线程，所有触碰 COM 的操作走 `Window.Post` 排队。
新增约束：**控制器的创建/销毁也必须在 UI 线程**，不能从插件回调里直接建窗口。

### 5.4 屏幕

决策 2：只支持**一块指定屏幕**。

- 全局设置 `display.target`：记录显示器 ID（而非索引，避免热插拔后错位）与当时的缩放。
- 该屏不存在时降级到主屏，并在设置窗口里提示。
- 组件坐标仍用**屏幕物理像素**存储，与现在的 `note.json` 一致，避免逻辑/物理换算歧义。
- 单组件可覆盖全局设置（`instance.display`），但默认继承。

---

## 6. 插件包格式

两级插件是两种东西：**L1 是数据，L2 是程序**。包结构完全不同，连校验流程都分两路。

### 6.1 L1 样式包（纯声明，无程序）

```
styles/sidebar/
├── style.json           必需，样式定义
├── preview.svg          可选，设置窗口里的预览图
└── tokens.css           可选，纯 CSS 变量（宿主注入到组件页面）
```

```json
{
  "id": "vendor.sidebar",
  "name": "贴边侧栏",
  "author": "someone",
  "description": "停靠在屏幕右缘、内容自适应高度的竖条",
  "window": {
    "layer": "topmost",
    "activation": "noActivate",
    "resizable": false,
    "sizeMode": "fitContent",
    "dock": "right",
    "titlebar": false
  },
  "visual": {
    "padding": 12,
    "radius": 14,
    "background": "rgba(28,28,30,0.92)",
    "shadow": "0 8px 24px rgba(0,0,0,0.35)"
  },
  "cssVars": {
    "--fg": "#f2f2f7",
    "--accent": "#c99700"
  }
}
```

- `window`：**窗口语义**，L2 不可覆盖。取值全部来自宿主词表，L1 只能组合，不能发明。
- `visual`：**视觉默认值**，L2 可逐项覆盖。
- `cssVars`：注入组件页面的 CSS 变量，L2 可覆盖同名项。
- **没有 `entry`**：L1 不带页面，所以它不创建 WebView、不占 renderer、不需要权限。

窗口词表（v1，只增不改）：

| 字段 | 取值 | 含义 |
|---|---|---|
| `layer` | `desktop` \| `topmost` \| `normal` \| `toast` | 窗口层级。`toast` 带穿透，只允许内置样式 |
| `activation` | `activate` \| `noActivate` | 点击是否抢焦点（装饰类用 `noActivate`，可输入的必须 `activate`） |
| `resizable` | `true` \| `false` | 用户能否拖边缩放 |
| `sizeMode` | `fixed` \| `user` \| `fitContent` | 尺寸由谁决定 |
| `dock` | `free` \| `left` \| `right` \| `top` \| `bottom` | 停靠位置 |
| `titlebar` | `true` \| `false` | 是否保留自绘拖动条 |

`clickThrough` 不在这张表里，理由见 §3.3。

### 6.2 L2 组件包（程序）

```
note/
├── manifest.json        必需，组件清单
├── index.html           必需，入口页面
├── assets/              可选，CSS/JS/图片
│   ├── style.css
│   └── app.js
├── themes/              可选，皮肤（一组命名的 cssVars）
│   ├── paper/theme.css
│   └── dark/theme.css
└── icon.svg             可选，设置窗口里显示
```

```json
{
  "id": "note",
  "name": "便签",
  "style": "builtin.style.topmost",
  "entry": "index.html",
  "author": "stickynote",
  "description": "固定置顶的文字便签",
  "icon": "icon.svg",
  "permissions": ["storage", "window.drag", "window.resize"],
  "defaultBounds": { "width": 420, "height": 360 },
  "minBounds": { "width": 260, "height": 200 },
  "singleton": false,
  "variables": [
    { "key": "title", "label": "标题", "type": "text", "default": "便签" },
    { "key": "fontSize", "label": "字号", "type": "select", "default": "m",
      "options": [{ "value": "s", "label": "小" }, { "value": "m", "label": "中" }] }
  ],
  "themes": ["paper", "dark"],
  "defaultTheme": "paper",
  "visual": { "background": "#fffbf0", "radius": 12 },
  "cssVars": { "--ink": "#2e2a22" }
}
```

字段约定：

- `id`：反向域名风格，全局唯一，**安装后不可改**（改了就是另一个组件）。
- `style`：**必填**，指向一个已安装的 L1 样式或内置样式。缺失或未安装则不可启用（见 §3.4）。
- `visual` / `cssVars`：可选，**只允许覆盖视觉字段**。写了语义字段（`layer` 等）直接判为不兼容。
- `themes` / `defaultTheme`：可选，皮肤只是 `cssVars` 的命名预设，不动窗口语义。
- `singleton`：`true` 时全局只能有一个实例（时钟通常是，便签不是）。
- `variables`：可选，作者声明的**用户可填参数**——要哪些、叫什么、怎么填（见 §27）。
  这里只有**声明**；值是实例的，存在 `data/vars/<组件>/<实例>.json`。
- `permissions`：见 §9，未声明即无权调用。
- **没有 `version` 字段**（决策 1：不做版本校验）。需要人类可读的标识就写进 `description`。

---

## 7. 插件导入流程

决策 1 定下的完整链路。入口：设置窗口「组件 → 导入…」，或把 zip 拖进设置窗口。

```
选择 zip / 目录
   ↓
解包到临时目录 %AppData%\StickyNote\.staging\<随机>
   ↓
① 识别层级      有 style.json 且无 index.html        → L1 样式包
                有 manifest.json 且含 style 字段      → L2 组件
                两者都有 / 两者都没有                 → 拒绝，「包结构不明确」
   ↓
② 冲突检测      同 id 已存在？→ 提示「覆盖 / 取消」
                （L1 与 L2 各自独立命名空间，样式 id 与组件 id 不会互撞）
   ↓
③ 兼容性检测    —— L1 ——
                style.json 能否解析；id / name / window 必填
                window 每个字段是否都在词表内（出现 clickThrough 一律拒绝）
                visual / cssVars 是否只含已知键
                —— L2 ——
                manifest.json 能否解析；id / name / style / entry / permissions 必填
                style 指向的样式是否已安装 → 未安装则允许落盘，标记 missingStyle，不可启用
                visual / cssVars 是否只含视觉字段（出现语义字段即拒绝）
                entry 指向的文件是否真实存在
                permissions 是否都在宿主支持列表内（有未知权限即拒绝）
   ↓
④ 落盘          通过 → plugins\styles\<id>\  或  plugins\components\<id>\
                失败 → 删除临时目录，返回具体失败原因
   ↓
⑤ 注册          写入 components.json，默认 enabled=false，等待用户启用
   ↓
⑥ 启用          L2 分配实例 → 解析样式（§3.4）→ 创建窗口
```

明确**不做**的（决策 1）：

- 不做签名校验、不做证书链验证
- 不做版本号比对（没有 `version` 字段）
- 不做在线拉取、不做自动更新
- 不做代码静态审计

明确**要做**的：

- 冲突检测：同 `id` 覆盖前必须让用户确认，不允许静默替换
- 层级识别：包结构不明确的直接拒绝，不猜——猜错会把样式包装成组件运行
- 兼容性检测：结构性校验，失败信息要指到具体字段
- 路径安全：解包时拒绝 `..` 与绝对路径，防止写到插件目录外（这是唯一必须做的「安全」检查，
  因为它保护的是用户机器而不是插件作者）

> 注意：不做签名校验意味着**安装插件等于运行任意前端代码**。这个风险由用户在导入时承担，
> 文档和 UI 都要说清楚，不能假装它是安全的。

---

## 8. 宿主 API（桥接协议）

沿用现有 JSON-RPC 形状，`from:"host"` 区分回显。新增 `componentId` 由宿主按**消息来源控制器**
注入，**前端不能自己声明**——否则组件可以冒充别的组件读写它的存储。

这张表就是 §3.1 那张边界表的接口形态：**所有共性行为都在这里，组件一件也做不了**。
表里没有的能力（弹自定义浮层、直接写文件、直接建窗口），组件就是没有。

### 8.1 前端 → 宿主

| method | 权限 | 作用 |
|---|---|---|
| `component.ready` | — | 首屏就绪，宿主回推 `ResolvedStyle`（含 `cssVars`）、尺寸、状态 |
| `component.close` | — | 关闭本实例 |
| `component.setBounds` | `window.resize` | 改自己的位置尺寸 |
| `window.beginDrag` | `window.drag` | 进入系统移动循环 |
| `window.beginResize` | `window.resize` | 进入系统缩放循环，`{edge}` |
| `storage.get` | `storage` | 读本组件私有 KV，`{key}` |
| `storage.set` | `storage` | 写本组件私有 KV，`{key,value}`，宿主防抖落盘 |
| `storage.remove` | `storage` | 删键 |
| `host.notify` | `notify` | **请求内核**弹提示，`{text, level}`。只给内容，样式/位置/时长由内核决定 |
| `host.log` | — | 写组件日志，`{level, message}`，由内核统一落盘与轮转 |
| `host.openSettings` | `settings.open` | 打开设置窗口，可带 `{page}` |
| `http.fetch` | `http` | 经内核代理发请求，绕过 CORS 并记录域名 |

`host.notify` 的语义要特别说明：**它不是「显示一个窗口」，而是「提交一条通知」**。
内核可能合并同类、可能限流、可能在用户开了「勿扰」时直接丢弃并只记日志。
组件无权知道也不该依赖最终呈现形态。

### 8.2 宿主 → 前端（事件）

| event | 数据 | 时机 |
|---|---|---|
| `host:theme` | `{name, vars}` | 主题切换 |
| `host:style` | `{window, visual, cssVars}` | 绑定的样式被更新（L1 覆盖安装、用户改绑定） |
| `host:visibility` | `{visible}` | 被隐藏/唤出（休眠前后） |
| `host:bounds` | `{x,y,width,height}` | 窗口几何变化 |
| `host:settings` | `{...}` | 全局设置变化 |

> `host:style` 只在**视觉字段**变化时下发；`window` 语义变更需要重建窗口，
> 走的是「实例重启」而不是热更新。
>
> 现有 `host:pin` 会被 `host:settings` 或组件自己的状态取代。

---

## 9. 权限模型

清单声明 + 首次启用时用户确认。设置窗口里可随时撤销。

**L1 样式包没有权限可言**——它不跑代码、不创建窗口、不碰存储，只是一段被解析的 JSON。
所以「安装一个样式包」的风险等级与「安装一个组件」完全不同，导入 UI 上应当区分表述。

| 权限 | 含义 | 风险 |
|---|---|---|
| `storage` | 读写自己的私有 KV | 低 |
| `window.drag` / `window.resize` | 拖动/缩放自己 | 低 |
| `notify` | 请求宿主弹提示 | 中（可骚扰） |
| `settings.open` | 打开设置窗口 | 低 |
| `http` | 经宿主代理发网络请求 | 高（数据外泄） |

**v1 不提供**：`fs.*`、`clipboard.*`、任意窗口创建、任意进程启动。

组件**不能**开新窗口。「新窗口」由宿主以「新建某组件的实例」提供，
插件只能请求「再开一个我的实例」，不能请求「开一个任意窗口」。
这条是安全边界的关键：**插件无法创建未经声明的窗口形态**。

---

## 10. 设置窗口

> 已实现一版（只有「组件 / 样式」两页），实际形态与偏差见 §18。
> 下面保留的是完整设想，未做的页签按 §18.8 的顺序继续。

独立窗口（第二个 WebView2 控制器），**保留标准标题栏与任务栏按钮**——
它是管理界面不是组件，需要能被正常找到和关闭。

页面结构：

1. **组件列表**：已安装组件，显示绑定样式、来源（内置/导入）、运行状态、实例数；启用/停用开关。
   引用缺失样式的条目标红「样式缺失」。
2. **组件详情**：权限清单（可撤销）、默认尺寸、**样式绑定与视觉覆盖**、皮肤选择、日志、卸载。
3. **样式库**：内置样式与已安装 L1 样式，含预览图、被哪些组件引用、启用/停用、卸载。
   停用或卸载被引用的样式时，必须先列出「会因此停用的实例」让用户确认（决策 8）。
4. **实例管理**：每个实例的位置尺寸、层级、显隐、休眠状态、定位到屏幕。
5. **导入**：选择 zip / 拖入 / 选目录，自动识别 L1 还是 L2，走 §7 流程，
   失败时显示具体字段级原因。
6. **全局设置**：开机自启、主题、休眠超时、**指定屏幕**、日志级别。
7. **通知**（内核能力，不是组件）：勿扰开关、默认停留时长、浮层停靠位置、
   是否允许组件请求通知的全局开关、以及**按组件查看谁弹得最多**。
8. **开发者**：API 版本、调试端口、日志目录。

第 7 页是决策 3 的直接体现：通知既然归内核，就必须有一个地方让用户管住它。
没有这一页，「统一由内核处理」对用户来说等于没有控制权。

入口：托盘右键菜单「设置…」。

---

## 11. 存储布局

> 已实现版本与下面这张图有偏差：数据不再是 `%AppData%\StickyNote`，而是
> **exe 旁边的 `data\`**（便携模式），缓存单独收进 `cache\`。见 §19。
> 另外「内置组件」已经不存在了：便签是随包分发的插件，走完整导入流程。见 §20。

```
%AppData%\StickyNote\
├── settings.json          全局设置（含 display.target）
├── components.json        注册表：已安装的 L1 样式与 L2 组件、启用状态、授权、来源
├── layout.json            实例清单与几何（含层级、休眠状态）
├── plugins\styles\<id>\       已安装的 L1 样式包
├── plugins\components\<id>\   已安装的 L2 组件
├── builtin\<hash>\        内置资源（内置样式 + 内置组件，随 exe 解包，内容哈希）
├── web\<hash>\            设置窗口前端资源
├── logs\<id>.log          分组件日志，按大小轮转
├── .staging\              导入临时目录，启动时清空
└── webview\               WebView2 user data（所有控制器共享）
```

**内置组件不走导入流程**：`builtin.*` 随 exe 打包，首次启动自动注册到 `components.json`，
用户只能启用/停用，不能卸载。

现有 `note.json` 的迁移：首次启动检测到它存在 → 自动创建 `builtin.note` 的一个实例，
正文写入该实例的 `storage`（`content` 键），几何写入 `layout.json`，
然后把 `note.json` 重命名为 `note.json.migrated` 保留。
**不删除用户数据。**

---

## 12. 迁移路径

分三步，每步结束时程序都能跑、行为不回退：

1. **抽层**：`domain/component`（含 `Style` 与 `ResolvedStyle`）+ `componentapp` + 三个 JSON 仓储就位；
   内置样式 `builtin.style.topmost` 以 JSON 落地，便签绑定它、行为与现在完全一致，
   `app.go` 的 `note.*` 暂时保留为兼容别名。
2. **切换**：便签界面移到 `builtin/note/`，桥接换成 `storage.*`，
   删掉 `domain/note` 与 `noteapp`，跑迁移逻辑处理 `note.json`。
3. **扩容**：窗口层支持 `desktop` / `toast`，L1 样式包导入与样式库上线，
   通知服务 + 设置窗口上线。

---

## 13. 里程碑

| 里程碑 | 内容 | 验收 |
|---|---|---|
| **M0 抽层** | 组件/样式领域模型、三层样式解析、仓储、注册表；便签绑定内置样式但不改行为 | 便签功能与现在完全一致，`ResolvedStyle` 单测覆盖覆盖规则，测试全绿 |
| **M1 内核** | 三种窗口形态（含 toast 浮层）、桌面层挂载、指定屏幕、**通知服务** | 通知能从内核弹出并排队；手动注册的组件各就各位 |
| **M2 插件** | L1/L2 双路解析、层级识别、导入流程、动态加载/卸载、权限确认、依赖失效停用 | 先导入一个 L1 样式包，再导入一个引用它的 L2 组件，启用后能跑；停用 L1 时实例随之停用 |
| **M3 设置窗口** | 列表/详情/实例/导入/全局/通知/开发者七页 | 全部操作不依赖手改 JSON |
| **M4 开发者体验** | API 文档、插件脚手架、两个示例组件（时钟、桌面便签） | 外部开发者照着文档能做出一个时钟 |
| **M5 打磨** | 休眠/恢复、主题、崩溃隔离、日志轮转 | 开关组件不泄漏句柄与进程 |

建议 M0/M1 一起做——只抽层不换窗口，收益看不出来。

M1 里**通知服务优先于组件加载**：先把「内核怎么通知」跑通（哪怕只有一个硬编码的测试入口），
再谈组件接入。顺序反了的话，后面每个组件都会忍不住自己弹一个浮层，
边界一旦破了就收不回来。

---

## 14. 风险清单

| 风险 | 影响 | 应对 |
|---|---|---|
| **WebView2 透明背景在 Win32 下不稳** | toast 的圆角/半透明做不出来 | v1 先做不透明；透明作为独立任务验证 `put_DefaultBackgroundColor` + DWM |
| **`WorkerW` 挂载依赖 Explorer** | Explorer 重启后桌面组件消失 | 监听 `TaskbarCreated` 消息重建；降级到 `HWND_BOTTOM` |
| **每组件一个 renderer 的内存** | 组件越多内存越高 | 决策 4 已接受此代价；靠休眠兜底，设置里显示实时占用 |
| **插件是任意前端代码** | 恶意组件读数据、弹广告 | 权限白名单 + 消息来源注入 `componentId` + `http` 走代理记录；**明确告知不校验签名** |
| **窗口词表覆盖不到真实需求** | 有人要的样式用 JSON 表达不出来，被迫写 L2，分级形同虚设 | 词表按真实需求逐个加值（只增不改）；加值的工作量在内核，要克制。文档 §3.4 已说明「表达不了就写进 L2」是合法出路，不是失败 |
| **`devicePixelRatio` 1.425 vs 系统 125%** | 所有组件 UI 比设计值大约 14% | 在 M1 用 `put_ZoomFactor` 统一校正，否则每个组件都要自己补偿 |
| **卸载时控制器仍在跑** | 句柄/进程泄漏 | 卸载 = 先销毁控制器 → 等 `Destroyed` 回调 → 再删文件 |

---

## 15. 仍然待定

只剩三个小的，不阻塞 M0：

1. **组件的私有存储要不要给容量上限**（比如单组件 1 MB），防止插件把磁盘写满。
2. **导入时的「覆盖」语义**：同 id 覆盖后，原有实例的 `storage` 与 `layout` 保留还是清空？
   倾向保留（用户数据不该因为换版本丢失），但插件换了数据结构可能读到脏数据。
3. **托盘菜单在组件很多时怎么组织**：目前只有「显示便签 / 置顶 / 退出」，
   加上设置与组件清单后需要分组。先按「设置… / 分隔 / 组件子菜单 / 分隔 / 退出」做。
4. **窗口词表扩展节奏**：`dock` 要不要支持「自动隐藏」「吸附到其他窗口」这类带行为的值？
   建议 v1 只做静态停靠，行为型取值等真实需求出现再加——每加一个都是内核工作量。
5. **L1 能否带 `tokens.css`**：允许它能表达 `visual` 字段装不下的东西（如 hover 态、滚动条样式），
   但它离「样式包可以塞任意 CSS」只差一步。倾向允许，但限定只作用于 `:root` 的自定义属性，
   不允许写选择器。

---

## 16. 术语表

- **WorkerW**：Explorer 里承载桌面图标层的一个隐藏窗口，`SetParent` 到它下面即成为桌面子窗口。
- **控制器（Controller）**：WebView2 的一个实例，对应一个窗口里的一块渲染区域。
- **renderer 进程**：Chromium 里负责跑 JS 和排版的进程，每个控制器至少一个。
- **点击穿透**：鼠标事件不进入该窗口，直接落到下层窗口，靠 `WS_EX_TRANSPARENT` 实现。
- **L1 样式包**：纯 JSON 的窗口模板声明，不跑代码、不占进程、无权限。
- **L2 组件**：真正会运行的前端包，必须绑定一个 L1 或内置样式。
- **ResolvedStyle**：兜底默认 → L1 → L2 三层合并后的最终窗口配置，宿主只认这个结果。
- **内置样式 / 内置组件**：随 exe 打包、`id` 以 `builtin.` 开头、不可卸载；格式与第三方完全同构。

---

## 17. 实现进度（第一版内核）

已完成（对应 M0 + M2 的一部分）：

| 项 | 落地位置 | 备注 |
|---|---|---|
| 样式 / 组件 / 实例模型 | `internal/domain/component/` | 语义字段锁在 L1，靠类型系统保证 |
| 三层样式解析 | `component.Resolve` | 兜底 → L1 → L2，视觉可覆盖、语义不可 |
| JSON 仓储 | `internal/infrastructure/persistence/` | `components.json` / `layout.json` / `storage/` |
| 插件包加载与校验 | `internal/infrastructure/plugin/` | 坏包只记日志；含语义字段直接拒绝 |
| 多实例窗口管理 | `internal/interfaces/webview/app.go` | 每实例一个窗口 + 一个控制器 |
| 按样式建窗 | `window.go` 的 `WindowOptions` | `layer` / `activation` / `resizable` 已生效 |
| 每实例独立桥接 | `host.go` 的 `VirtualHost` | `https://<组件id>.stickynote`，天然隔离 |
| 便签改造成 L2 组件 | `builtin/components/note/` | 绑定 `builtin.style.topmost` |
| 旧数据迁移 | `cmd/stickynote/main.go` | `note.json` → 实例存储，原文件保留 |

与文档的偏差，都是**有意的收缩**：

1. ~~**`layer: desktop` 暂不实现**，声明即校验失败。~~
   **已实现**（见 §22.3）：`desktop` / `topmost` / `toast` 三条路径都可用，
   三种预设样式各配了一个示例组件。当初"无法在无人值守下验证"的判断是错的——
   `GetClassName(GetParent(hwnd))` 与 `GetWindowLongPtr(GWL_EXSTYLE)` 就够当证据。
2. **插件只支持「目录即插件」**，没有 zip 导入。§7 的解包、冲突检测、staging 目录
   留到 M2 后半段——先把加载路径跑通，导入只是它外面套一层。
3. **通知服务未实现**，`host.notify` 只落日志。M1 的顺序不变：先有内核通知，
   才允许组件接入，否则每个组件都会忍不住自己弹浮层。
4. **样式没有独立的启用开关**。`enabled` 只到组件粒度；L1 样式只能整体卸载，
   且被引用时不许卸。真正的「停用某个样式」得先回答"停用后它的组件怎么办"，
   答案会牵动实例状态机，不适合塞进这一版。

下一步建议顺序：通知服务（M1 剩余）→ zip 导入（M2 剩余）→ 桌面层。

设置窗口（§10 / M3）已经做完了一版，见 §18——它原本排在最前，
是因为它让「装了什么、开了什么、样式被谁引用」第一次可见。
这条可观测性通道现在已经在用了，后面的调试都建立在它上面。

---

## 18. 实现进度（主界面）

对应 §10，但只做了「一张主界面」，没有分 8 页。

### 18.1 定位：内核 UI，不是组件

这是这一版最重要的一个判断。主界面管组件，所以它**不能是组件**：

- 如果它是 L2 组件，用户停用它之后就再也没有入口把它点回来了（循环依赖）。
- 如果它是 L1 样式，样式是数据，本来就跑不了界面。

所以它走的是和内核通知浮层同一条边界：**内核自己的能力，不能交给被自己管理的东西去实现**。
落地表现：

| 项 | 组件 | 主界面 |
|---|---|---|
| 资源位置 | `builtin/components/<id>/` | `builtin/manager/`（同级，不参与插件扫描） |
| 有 `manifest.json` | 有 | 没有 |
| 绑定 L1 样式 | 必须 | 不绑定，用内置的 `managerStyle` |
| 可被卸载 | 是 | 否 |
| 桥接命名空间 | `component.*` / `storage.*` / `window.*` | `manager.*` |
| 消息路由 | 按实例 id | 保留 id `kernel.manager` |

但它**复用**了组件的全部基础设施：同一个 `Window`、同一个 `Host`、
同一套 `--sn-*` 变量契约、同一套拖动/缩放做法。所以它不是"另写一个程序"，
只是换了一个入口和一套权限。

### 18.2 入口

- 托盘左键 → 打开主界面（原来左键是切换第一个实例的显隐，改了）。
- 托盘右键 → 「管理组件…」。
- 首次运行自动打开；`STICKYNOTE_DEV=1` 时也自动打开，省得每次点托盘。

主界面窗口与组件窗口的差别只有一处：**它在任务栏占位**（`ShowInTaskbar`）。
组件窗口一律 `WS_EX_TOOLWINDOW`，因为它们的入口是托盘；
但主界面是用户会切走再切回来的窗口，不占任务栏就找不到了。
也因此只有它提供「最小化」按钮——toolwindow 最小化后无法找回，这是老的坑。

### 18.3 能力

组件页（L2）每张卡片：启用开关、新建实例、显示/隐藏、关闭实例、打开目录、卸载。
样式页（L1）每张卡片：窗口语义（`layer` / `activation` / `sizeMode` / `dock` /
`resizable` / `titlebar`）、被哪些组件引用、打开目录、卸载。

「启用/停用」的语义是：**停用只关窗口，不删实例**。实例记录、几何、私有数据都留着，
重新启用时原样回来。删除是另一个动作（关闭实例），要用户明确点。

### 18.4 安装的四类检测

按顺序，任何一条不过就拒绝，报错写清楚下一步做什么：

1. 目录里同时有 `style.json` 与 `manifest.json` → 拒绝。一个包只能是一级。
2. id 已被占用 → 拒绝。v1 不做覆盖安装，先卸载旧版本。
3. id 用了 `builtin.` 前缀 → 拒绝。内置资源每次启动按内容哈希重新解包，
   覆盖了也会被还原，用户会以为"装了没生效"。
4. L2 的 `style` 不在当前目录里 → 拒绝，并提示先装那个样式包。

复制中途失败会 `RemoveAll` 掉目标目录：半成品留着会占住 id，
用户既装不进去也删不掉。

### 18.5 重扫目录的两条拒绝条件

`Service.Reload` 用新目录整体替换旧目录，但有两个前提：

1. **有实例的组件不能在目录里消失**。实例带着几何与私有数据，组件定义一没，
   它们就成了孤儿——数据还在，界面上再也找不到。
2. **有实例的组件，它绑定的样式不能在目录里消失**。样式没了就解析不出窗口语义，
   会留下"有窗口但没定义"的悬空状态。

两条都是拒绝而不是"顺手清理"：清理是静默的数据损失，用户不会知道发生了什么。

### 18.6 卸载的守卫与边界

- 组件还有实例 → 拒绝（先关实例）。
- 样式还被组件引用 → 拒绝（先卸组件）。
- 内置的 → 按钮直接禁用。
- **只删 `plugins\` 下的文件，不动 `storage\`**。重装回来正文还在。
  彻底清数据是另一件事，不该藏在「卸载」按钮里顺手做掉。

### 18.7 落地位置

| 项 | 位置 |
|---|---|
| 主界面窗口与 `manager.*` 分派 | `internal/interfaces/webview/manager.go` |
| 选文件夹 / 打开资源管理器 | `internal/interfaces/webview/picker.go`（`SHBrowseForFolderW` + `ShellExecuteW`） |
| 安装 / 卸载 / 复制 | `internal/infrastructure/plugin/install.go` |
| 安装卸载的应用规则（守卫、重扫、回滚） | `componentapp.Service.InstallPackage` / `UninstallPackage` / `Reload` |
| 主界面窗口几何 | `%AppData%\StickyNote\ui.json`（`persistence/ui.go`） |
| 前端 | `builtin/manager/{index.html,style.css,app.js}` |

`SHBrowseForFolderW` 用的是老 API 而不是 `IFileOpenDialog`：
前者是一次调用加一个结构体，后者要自己实现 COM 接口。
代价是对话框长得旧一点，但它能直接在地址栏输路径，够用。

选文件夹是模态的，所以 `manager.install` 先回包、再用 `Window.Post` 排到 UI 队列上执行——
和拖动/缩放同一条规矩：不能在 COM 回调里进嵌套消息循环。

### 18.8 这一版没做的

- §10 里的「组件详情」「权限清单可撤销」「皮肤选择」「日志查看」「全局设置」
  「通知管理」「开发者」这些页都没有。一张界面只解决"看得见 + 能开关 + 能装"。
- 拖入安装、zip 安装都没有，只有选目录。
- 样式没有启用/停用，只有卸载。
- 主界面不跟随系统主题（没有深色模式）。

---

## 19. 便携模式：数据跟着 exe 走

用户要求：「用户只需要管理它安装所在的文件夹即可，不要让用户注意到这个文件夹」
（指 `%AppData%\StickyNote`）。

### 19.1 决定

数据根目录从 `%AppData%\StickyNote` 改成 **`<exe 所在目录>\data\`**。
把整个安装目录拷走就是迁移，用户配置目录里不留任何东西。

```
<安装目录>\
├── StickyNote.exe
└── data\
    ├── components.json / layout.json / ui.json
    ├── storage\<组件id>\<实例id>.json
    ├── plugins\styles\<id>\  plugins\components\<id>\
    ├── cache\            ← 整块可删
    │   ├── builtin\<hash>\
    │   └── webview\
    └── app.log
```

### 19.2 为什么把 cache 单拎出来

`builtin\<hash>\` 和 `webview\` 加起来是几十 MB 的浏览器缓存与解包产物，
但它们和"用户的东西"性质完全不同：**删了不丢任何数据，只是下次启动慢一点**。

混在数据目录里会让用户面对一堆哈希命名的文件夹。单独收进 `cache\`，
语义就变成"这一整块你可以忽略，也可以随时删"。

顺带解决了一个界面问题：内置组件的资源住在 `cache\builtin\<hash>\components\<id>\`，
那个路径不该出现在任何按钮上。所以**内置项不再提供「打开目录」**，
Go 侧 `manager.reveal` 也加了一道拦截，防止有人绕过界面直接调。

### 19.3 写不进去怎么办

`Program Files` 下没有管理员权限时，exe 旁边的目录不可写。判定不能只看
`MkdirAll` 成不成功——目录可能建得出来但文件写不进去（只读介质、ACL 拒绝、
受控文件夹访问）。所以探针是**试写一个文件再删掉**。

不可写才退回 `%AppData%\StickyNote`，并在日志里写明原因。那种情况下没得选，
总不能起不来。这条路径是兜底，不是常态。

### 19.4 老用户怎么办

旧版本的数据在 `%AppData%\StickyNote`，新版本第一次启动时：

1. 只搬 `components.json` / `layout.json` / `ui.json` / `storage` / `plugins`。
2. **不搬缓存**——它会自动重建，搬 70MB 浏览器缓存只会拖慢首次启动。
3. 搬完把旧目录改名为 `StickyNote.migrated`，**不删除**：万一搬错，用户手上还有原件。
4. 新位置已经有 `layout.json` 就跳过（防止每次启动都覆盖用户后来的改动）。

`os.Rename` 在跨卷时会失败（AppData 和安装目录常常不在一个盘）。
这时日志会提示"旧目录仍在某处，确认无误后可手动删除"，但**数据已经搬完**，
程序照常工作——先复制再改名，顺序保证了这一点。

### 19.5 界面上的连带改动

- 状态栏的「打开数据目录」改成「打开插件目录」：用户唯一有理由打开的只有这个。
- 内置项（组件与样式）不再显示「打开目录」。
- 界面上不再出现"数据目录"这个概念，只出现"插件目录"。

### 19.6 测试

`persistence/migrate_test.go` 三个用例，靠 `t.Setenv("APPDATA", tmp)` 把
`os.UserConfigDir()` 指到临时目录，所以能真实跑一遍搬迁：

- 用户数据搬走、缓存不搬、旧目录改名为 `.migrated`
- 已有新数据时跳过（不覆盖）
- 没有旧目录时是空操作

### 19.7 代价

- 便携模式不支持"多用户共用一份安装目录"：每个人拷一份自己的。
  这和"用户只管一个文件夹"是同一件事的两面，接受。
- 装在 `Program Files` 时会退回 AppData，此时"一个文件夹"不成立。
  真要装到那里，得让安装包把 `data\` 建到用户可写位置，或申请提权——不在这一版范围。

---

## 20. 便签从内核分离：改成预置插件

用户要求：「把便签从内核里分离出来，以插件形式安装」。

### 20.1 决定

便签不再编进 exe。它变成一个**普通插件**，放在 exe 旁边的 `bundles\note\`，
首次启动时被复制进 `data\plugins\components\note\`。

分发形态因此从「单个 exe」变成「exe + `bundles\` 文件夹」。这是明知代价的选择：
换来的是「换掉/删掉/顶替便签都不需要动内核」。如果打进 exe，
「便签是可替换的」就只是句口号——用户根本拿不到那个目录。

`builtin\` 里现在只剩两样东西：`styles\`（L1 样式包）与 `manager\`（主界面）。
两者都不是组件，一个是数据，一个是内核 UI。

### 20.2 为什么 id 必须从 `builtin.note` 改成 `note`

`builtin.` 是**保留前缀**，`Installer.Install` 会直接拒绝占用它的包——
理由是这个前缀的资源由二进制内嵌、每次启动重新解包，允许覆盖也会被还原，
让用户以为「装了但没生效」。

便签既然走插件路径，就不能再占这个前缀，否则它和第三方之间那条
「谁都不能冒充内置资源」的界线就破了。所以改名为 `note`。

### 20.3 改名要动四处，少一处就出事

| 位置 | 内容 |
|---|---|
| `layout.json` | `instances[].componentId` |
| `layout.json` | `instances[].id`（`builtin.note-1` → `note-1`） |
| `components.json` | `enabled` 的键 |
| `storage\<组件id>\<实例id>.json` | 目录名**与**文件名都带 id |

漏任何一处都不会报错，只会变成「组件还在、数据找不到」——这比直接崩掉更难查，
因为界面看起来一切正常，只是正文空了。所以 `MigrateBuiltinNote` 是一处
**幂等**的整体迁移：`layout.json` 里已经有 `note` 就直接返回，不做第二次。

它必须跑在 `Boot` 之前：`Boot` 读到指向已消失组件的实例会直接丢弃
（`丢弃实例 xxx：组件 xxx 已不存在`），那等于把用户数据删了。

### 20.4 发放账本：`bundles.json`

```json
{ "issued": ["note"] }
```

「发过」和「装着」是两件不同的事。判断要不要装，依据必须是这份只增不减的记录，
不能是「插件目录里有没有」——否则用户卸载便签之后，下次启动它又长回来了。
**用户删掉的东西不该自己回来。**

反过来，`InstallBundles` 遇到「目录里已经装着、但账本里没记」的情况（用户手动
放进去的、或账本丢了），只补一条记录，不重新复制、不覆盖用户改过的文件。

### 20.5 首次发放默认启用

普通第三方插件默认停用，等用户去主界面打开。但预置包是开箱就该能用的东西，
让用户面对一个空桌面说不过去。

做法不是给它加 `Builtin` 标记（那是「随内核打包」的语义，它已经不是了），
而是在 `Boot` 之前告诉应用层：`svc.SetBootstrapEnabled("note")`。

- 只对**注册表里没有记录**的 id 生效。用户后来关掉它，下次启动仍然是关的。
- 没声明过的第三方组件仍然默认停用（`TestNonBootstrapPluginStaysDisabled`）。

### 20.6 启动顺序

```
Root()                    → data\（不可写则退回 %AppData%）
MigrateFromAppData()      → 旧版数据搬进来
MkdirAll(plugins\)
Materialize(builtin)      → 内置样式与主界面解包到 cache\builtin\<hash>\
loadCatalog()             → 只为拿样式 id，给预置包做依赖检测
InstallBundles()          → 复制预置包（跳过已发放的）
MarkBundledIssued()
MigrateBuiltinNote()      → 把旧 id 的实例数据改到新 id
Boot()                    → 读注册表与布局，建首个实例
```

两个「必须早于 `Boot`」的理由不一样：

- `InstallBundles` 要早于 `Boot`，否则 `Boot` 扫不到便签，会以「没有组件」为前提
  建不出实例，还会把启用状态提前写死。
- `MigrateBuiltinNote` 要早于 `Boot`，否则实例会因为「组件不存在」被丢掉。

第一次 `loadCatalog()` 只取样式、**不 Boot**，正是为了满足这个顺序：
预置包装进去之前不能有「以目录为准」的决策发生。

### 20.7 升级怎么办

`bundles\` 只在**首次启动**被读一次。之后便签住在 `data\plugins\` 里，
把 `bundles\note\` 换成新版也不会自动覆盖已装的旧版——
插件是用户的，不该被静默替换。要更新就手动卸载再安装。

这一版刻意没做「预置包自动升级」：它需要版本比较、需要处理「用户改过插件」的
冲突，属于插件市场那一层的事（决策 5：只做技术框架）。

### 20.8 测试

- `plugin/bundles_test.go`：装新包、跳过已发放、已装着只记账不覆盖、
  坏包不影响好包、样式依赖缺失时拒装。
- `persistence/migrate_note_test.go`：四处一起改、幂等、无布局文件时是空操作。
- `componentapp/service_test.go`：bootstrap 首次启用并建实例、不覆盖用户停用状态、
  未声明的插件仍默认停用。

---

## 21. 修一个卡死：不能在桥接回调里动窗口

### 21.1 症状

在管理界面里把一个组件关掉、再打开，整个程序完全卡死：
窗口不刷新、托盘点不动、日志停在最后一行，但进程还在任务管理器里活着。

### 21.2 定位

没有可点的按钮可自动化，于是在 `builtin/manager/app.js` 末尾临时塞了一段
自动开关（验证完删掉），每步都往 `app.log` 打一行：

```
autotest: 开始停用
bridge <- kernel.manager manager.setEnabled (id=2)
autotest: 停用已回包          ← 停用没事
autotest: 开始启用
bridge <- kernel.manager manager.setEnabled (id=3)
                              ← 到此为止，进程还在，日志不再增长
```

**停用能回包，启用必死**。启用走 `spawnMissing` → `spawn` → `Host.Start`
→ `edge.Chromium.Embed`；而 `Embed` 内部是这样的：

```go
for {
    if atomic.LoadUintptr(&e.inited) != 0 { break }
    GetMessageW(&msg, 0, 0, 0)
    TranslateMessage(&msg)
    DispatchMessageW(&msg)
}
```

它跑一段**嵌套消息循环**等控制器就绪。而这个回调本身就是在
`DispatchMessageW` 派发进 `ICoreWebView2WebMessageReceivedEventHandler::Invoke`
的——在 WebView2 的事件回调里再进一层嵌套循环，运行时的内部锁再也拿不到，
于是静默死锁。这正是 `BeginDrag`（内部 `Post`）与 `beginInstall`
（先回包再 `Post`）早就守过的那条规矩，只是这次漏在了"启用组件"上。

### 21.3 顺带修的另一半

停用在实测里侥幸没卡，但 `stopInstances` 里的 `DestroyWindow` 同样是
"在回调里动窗口"：`DestroyWindow` 会**同步**发 `WM_DESTROY` 到承载控制器的
子窗口，走到运行时内部去。属于同一类隐患，一起挪出回调。

### 21.4 修法

`manager.setEnabled` / `createInstance` / `removeInstance` 三个改成两段式：

1. 立刻回 `{"accepted": true}`；
2. 真正的动作排进 `Window.Post` 队列，跑完用 `manager:state` 事件推新状态。

```go
replyTo(h, req.ID, accepted, nil)
a.deferToUI(func() {
    if err := a.svc.SetEnabled(ctx, p.ComponentID, p.Enabled); err != nil {
        a.pushManager("manager:toast", toast{Text: err.Error()})
        a.pushManager("manager:state", a.managerState())
        return
    }
    if p.Enabled { a.spawnMissing(p.ComponentID) } else { a.stopInstances(p.ComponentID) }
    a.pushManager("manager:state", a.managerState())
})
```

**协议因此变了**：这三个方法的返回值不再是状态快照。前端不能再
`apply(await call(...))`，只能 `await call(...)` 然后等 `manager:state` 事件。
失败也走事件（`manager:toast`），所以 `guard()` 仍然兜得住异常。

### 21.5 验证

同样的自动开关跑 5 轮停用/启用，全部回包、每轮都重新建出 `note-1`、
进程存活、界面正常。句柄 323 → 383（约 +12/轮），内存 29.4 → 32.8 MB。

### 21.6 遗留

那 +12 句柄/轮是**控制器没有显式关闭**留下的：`go-webview2` 的
`ICoreWebView2Controller` 只暴露了 `PutIsVisible` 之类的方法，没有导出
`Close`（vtable 里有，字段是私有的）。按微软的文档，宿主应当自己
`Close` 控制器，否则环境与渲染进程不会立刻回收。

现在没做，是因为要绕过私有字段就得手写 vtable 索引（`unsafe`），
而这条路没法在无人工点击的环境里充分验证——宁可在文档里留一条明确的账。
真要修，正确做法是给 `Host` 加 `Close()`，并在 `stopInstances` /
`removeInstance` / `quit` 里调用；或者干脆换掉这个绑定。

---
---

## 22. 三种预设样式，各配一个示例组件

> 本节第一版写错了：把「另外两个默认样式」当成了 `builtin/styles` 目录里当时存在的包
> （`topmost` + `hud`），于是时钟挂 `hud`、弹幕新造了一个 `style.danmaku`。
> 用户指正后重写。**错在把"目录里有什么"当成了"设计里定了什么"。**

### 22.1 三种预设样式是窗口形态，不是配色

§3.2 定了三条窗口路径，用户最初的需求里对应三种默认组件形态：

> 一种为桌面组件，只会在桌面上置顶，会被任何窗口覆盖；
> 二为固定置顶窗口，用于固定展示一个窗口（比如便签）；
> 三为悬浮提示栏（类似于弹幕，没有点击事件）。

所以预设样式就是三个，`layer` 直接取三条路径的名字：

| 样式 id | 形态 | 鼠标 | 被覆盖 | 示例 |
|---|---|---|---|---|
| `builtin.style.desktop` | WorkerW 子窗口 | 可交互 | **是** | 数字时钟 |
| `builtin.style.topmost` | `HWND_TOPMOST` | 可交互 | 否 | 便签 |
| `builtin.style.toast` | 置顶 + 穿透 | **穿透** | 否 | 测试弹幕 |

`hud` 从来不在这个列表里——它是"深色卡片"这种**配色**，不是一种窗口形态。
配色应该由组件自己（或第三方 L1）决定，不该占一个预设名额。
上一版把它当预设，才逼出"再新造一个 `style.danmaku`"这种越补越歪的结果。

### 22.2 穿透为什么写成层级，而不是字段

`toast` 需要 `WS_EX_TRANSPARENT`。但 §3.3 明确定了「`clickThrough` 不给插件」——
一个能造出"看不见又点不掉、还盖在最上层"的窗口的能力，不该出现在样式词表里。

两条约束同时成立的办法：**把能力收进层级**。

- 词表里仍然没有 `clickThrough` 字段，第三方 L1 无法声明穿透；
- `layer: toast` 是唯一带穿透的取值，而 `Style.Validate` 要求它必须以
  `builtin.` 开头（`ReservedIDPrefix`）——**只有内置样式能用**；
- 用户始终有撤销权：组件在管理界面里看得见、关得掉，窗口跟着消失。

`builtin.` 这个前缀本来只是"安装器不许写"的标记（§20.2），现在多了一重含义：
**它同时是"这条能力只给内核"的凭据**。两处必须同一个常量，
所以常量挪进了领域层（`component.ReservedIDPrefix`）。

### 22.3 桌面层：挂"图标宿主"，不是壁纸层

`layer: desktop` 之前是"声明即校验失败"。要真的实现只有一条路：**成为桌面的子窗口**。
但桌面附近有两个容器，语义正好相反，挂错了整个功能就是废的：

| 容器 | 位置 | 能不能点 | 谁该用 |
|---|---|---|---|
| 空 `WorkerW` | 图标**之下**（壁纸层） | **不能** | 动态壁纸 |
| 含 `SHELLDLL_DefView` 的窗口（Progman 或某个 WorkerW） | 图标**之上** | 能 | 桌面组件 |

第一版挂的是空 WorkerW——那套做法是给 Wallpaper Engine 这类动态壁纸用的：
动画必须被图标压住。但桌面组件不是壁纸：`SHELLDLL_DefView` 铺满整个桌面，
会把鼠标事件全吃掉，组件既被图标压着、也点不动拖不动。
**"桌面上置顶"指的是图标之上，不是图标之下。**

现在找的是**含 `SHELLDLL_DefView` 子窗口的那个顶层窗口**（Win10 之前是 Progman，
之后可能是某个 WorkerW，所以按子窗口找、不按类名找），挂上去之后
`SetWindowPos(HWND_TOP)` 排到同级最上层——父窗口里铺着图标视图，我们必须压在它上面。
这一步不会跑到应用窗口前面去：父窗口本身就在 Z 序最底。

**怎么确认挂对了**（不靠肉眼，DEV 日志里三行字）：

```
desktopParent=0x40438(Progman,defView=true) aboveIcons=true
```

- `defView=true`：`FindWindowEx(parent, 0, "SHELLDLL_DefView", 0)` 有结果，
  证明父窗口确实是图标宿主；
- `aboveIcons=true`：`GetWindow(hwnd, GW_HWNDPREV) == 0`，证明我们在同级里排第一、
  压在图标视图上面。

坐标系也跟着变：`SetWindowPos` 收父客户坐标，`SetPosition` 里按父窗口客户区原点折算；
`GetWindowRect` 仍返回屏幕坐标，存盘几何不受影响。

**降级**：`SetParent` 失败就退回普通顶层窗口，日志留一行
`挂桌面层失败，降级为普通窗口`。这里**不能**用 `HWND_BOTTOM` 兜底——
顶层窗口沉到最底会落到 Progman 之下，也就是壁纸后面，窗口直接看不见，
比不挂还糟。降级必须留痕，不能静默。

**已知代价**：Explorer 重启时 Progman / WorkerW 会重建，挂在上面的窗口跟着一起没，
要等下一次 `spawn` 才回来。这一版没做重挂。

**桌面层也是穿透的**（`WS_EX_TRANSPARENT | WS_EX_NOACTIVATE`）。这条第一版漏了：
时钟挂在图标之上、却仍然吃掉鼠标，等于把桌面的一块区域变成死区——图标点不到、
框选框不过去。`desktop` 和 `toast` 穿透的理由不同，但结论一样：

| 层级 | 为什么穿透 |
|---|---|
| `toast` | 它是"没有点击事件的提示栏"，按定义不接鼠标 |
| `desktop` | 它是贴在桌面上的**装饰**，鼠标应该落到下面的图标上 |

代价是组件自己接不到任何鼠标事件，所以：

- 组件里不能有交互（时钟的"拖表盘挪位置"已删掉，`window.drag` 权限也一起去掉）；
- 位置只能在管理界面的**布局页**调（§23）——这是穿透窗口唯一的移动途径。

判定写在一处：`spawn` 里 `ClickThrough: layer == toast || layer == desktop`。
`Style.Validate` 只管"`toast` 只给内置样式"，`desktop` 不设这个限制——
第三方也可以做桌面装饰，它照样是穿透的，位置照样走布局页。

### 22.3.1 穿透必须设到 WebView2 的子窗口上

只给宿主窗口设 `WS_EX_TRANSPARENT` **不管用**。WebView2 会在宿主窗口里再套三层：

```
StickyNoteHostWindow            ← 我们建的
  Chrome_WidgetWin_0
    Chrome_WidgetWin_1
      Chrome_RenderWidgetHostHWND   ← 真正接鼠标的是它
      Intermediate D3D Window
```

扩展样式不继承。鼠标命中的是 `Chrome_RenderWidgetHostHWND`，它没有那个位，
于是点击照样被吃掉——第一版就卡在这里：宿主日志里 `transparent=true` 一切正常，
用户点下去却毫无反应。

现在的做法是 `applyClickThrough`：`EnumChildWindows` 把宿主连同全部子孙一起加上
`WS_EX_TRANSPARENT | WS_EX_NOACTIVATE`，日志打一行

```
实例 clock-1：已给 3 个窗口设上穿透（宿主 + WebView2 子窗口）
```

**还要设两遍**。渲染子窗口是导航时才建出来的，`Start()` 之后立刻设只能覆盖到当时
存在的几个；页面导航完成时 `Chrome_RenderWidgetHostHWND` 可能已经换了一个新的，
新窗口不带样式，穿透就在那一刻失效。所以 `Host.OnReady`（每次导航完成）里
再补一次，走 `deferToUI` 排队，不站在 WebView2 的回调里动窗口。

### 22.3.2 怎么验证穿透（别用 WindowFromPoint）

`WindowFromPoint` **不跳过** `WS_EX_TRANSPARENT` 的窗口——它只跳过隐藏和禁用的。
拿它当证据会得到"命中的是我们自己"的假结论，然后去改一个本来没坏的地方。

系统给鼠标找目标窗口时用的是带跳过标志的那套命中测试，对应的 API 是：

```go
ChildWindowFromPointEx(parent, pt,
    CWP_SKIPTRANSPARENT|CWP_SKIPINVISIBLE|CWP_SKIPDISABLED)
```

返回的才是"点击会落到谁身上"。DEV 日志里两个实例的实测：

```
穿透探针 clock-1：屏幕(1359,162) 在 Progman 下命中 0x40460(SHELLDLL_DefView)
                 命中本窗口树=false  祖先链=SHELLDLL_DefView<Progman
穿透探针 danmaku-1：屏幕(1280,35) 在 #32769 下命中 0x2081a(Chrome_WidgetWin_1)
                 命中本窗口树=false
```

时钟那一条命中 `SHELLDLL_DefView`——正好是桌面图标视图，说明点击会落到图标上；
弹幕那一条命中的是它下面的应用窗口（顶层 Chromium 窗口），说明这条提示栏对鼠标是空的。

另外 `#32769` 是桌面窗口的类名（`GetDesktopWindow` 的类），顶层窗口的命中测试要在它下面做。

### 22.4 不抢焦点的窗口不能用 SW_SHOW

顺手修掉的一个老问题：`Show()` 一直是 `SW_SHOW` + `SetForegroundWindow`。
`noActivate` 的组件（时钟、弹幕）本该"点击不抢焦点"，但启动那一下照样抢了焦点。
现在按语义分叉：`noActivate` / 穿透用 `SW_SHOWNOACTIVATE`，且不调 `SetForegroundWindow`。

### 22.5 dock 只在第一次出现时生效

`dock` 之前只是"记录一下，不做贴边吸附"。加了 `toast` 之后这条不能继续糊弄：
一条横贯屏幕的提示栏如果居中出现在屏幕正中，**用户既点不到也拖不动**
（穿透窗口收不到鼠标事件），只能去管理界面里删掉它。

所以补了 `initialPosition(dock, w, h)`：没摆过位置的实例按 `top/bottom/left/right`
贴边、`free` 居中。之后以存盘的几何为准，不做拖拽吸附。

### 22.6 时钟为什么不能留圆角

窗口是不透明的：窗口类带实心米色画刷，`WS_POPUP` 也没加 `WS_EX_LAYERED`，
WebView2 控制器的 `DefaultBackgroundColor` 默认不透明。

`hud` 那版用的是 `rgba(20,20,22,0.88)` + `radius:16`，圆角缺口里露出来的是
**窗口画刷的米色**，`rgba` 也只会和页面底色混合、不会和桌面混合。
所以两个深色样式都改成 `radius: 0` + 实色底。看起来是"一块深色面板"，
而不是"一块想半透明却发灰、四角还有米色缺口的卡片"。

真透明要窗口层配合（控制器背景 alpha=0 + 分层窗口或 DirectComposition），
不是样式能表达的，留给以后单独做。

### 22.7 保留的两条教训

1. **`InstallBundles` 要先装 L1 再装 L2**（§22.8）。虽然现在三个组件都绑内置样式、
   不再需要这条，但第三方预置包会需要，测试仍然钉着它。
2. **`openInitialInstances`**：`Boot` 里"给首个启用组件建一个实例"按 id 排序会挑到
   `clock`，全新安装就只剩一个时钟。改成"本次刚发放的组件各开一个"。

### 22.8 一个坑：同一批预置包要先装 L1

`InstallBundles` 原本按目录名排序、一轮装完。加入 L1 与 L2 混装的场景后，
组件可能排在样式前面，于是因为"缺少样式"被拒——而它依赖的样式包就在同一个
`bundles\` 里。改成**两轮**：先落地全部 `PackageStyle`，再装 `PackageComponent`，
并把装成功的样式 id 补进 `knownStyles`。`TestInstallBundlesInstallsStylesFirst`
把这个顺序钉死了（用例里故意让组件目录名排在样式前面）。

### 22.9 验证

在临时目录做全新安装，三个包各建一个实例，自检日志（DEV 模式）：

```
实例 clock-1：样式=builtin.style.desktop outer=(1105,615) 350x150
              layer=desktop noActivate=true topmost=false transparent=true
              desktopParent=0x40438(Progman,defView=true) aboveIcons=true
实例 danmaku-1：样式=builtin.style.toast outer=(655,0) 1250x70
              layer=toast noActivate=true topmost=true transparent=true
实例 note-1：样式=builtin.style.topmost layer=topmost resizable=true topmost=true
[clock-1]   数字时钟就绪：16:40:20 ... 样式=builtin.style.desktop 底色=rgb(20, 20, 22)
[danmaku-1] 测试弹幕就绪：位移 -849px · 速度=18s · 文字 45 字 · 样式=builtin.style.toast
```

三处硬证据，都是 Win32 查出来的、不是"我以为"：

- `desktopParent=0x40438(Progman,defView=true)`：父窗口有 `SHELLDLL_DefView` 子窗口，
  确实是图标宿主（不是壁纸层）；
- `aboveIcons=true`：`GW_HWNDPREV` 为 0，我们在同级里压着图标视图；
- `transparent=true`：`GetWindowLongPtr(GWL_EXSTYLE)` 里真有 `WS_EX_TRANSPARENT`；
- `outer=(655,0)`：`dock: top` 真的落到了屏幕顶边（宽 1250 = 1000 × 1.25 缩放）。

`底色=rgb(20, 20, 22)` 是 `desktop` 样式声明的实色底（不是组件的兜底值），
`速度=18s` 是 `toast` 样式声明的 `--sn-toast-speed`——说明三层解析与
样式 `cssVars` 都真的传到了组件里。

> 后续变更：`desktop` 与 `toast` 的底色已改成 `transparent`（桌面摆件直接压在壁纸上，
> 一块实色底反而多余）。这行读数的意义不变——它证明 `--sn-background` 来自样式包而不是
> 组件兜底；现在读数是 `底色=rgba(0, 0, 0, 0)` 配 `varBg=transparent`。

---

## 23. 主界面的布局页：拖方块改窗口位置

### 23.1 为什么必须在内核里做这件事

三种窗口形态里，有两种用户根本拖不动：

- `toast`（悬浮提示栏）点击穿透，收不到鼠标事件；
- `desktop`（桌面组件）**也是穿透的**（§22.3），而且贴着图标、藏在应用窗口下面，
  就算能点也找不到它的边缘。

`topmost` 便签有标题栏，能拖，但那是它自己的 UI 给的。内核不能指望每个组件
都记得给自己做拖动入口。

所以"改位置"这件事必须有一个**统一的、与组件无关的入口**——放在管理界面里，
用一个按比例的桌面缩略图来摆。这也顺带回答了"组件摆哪去了"这个问题：
以前只能靠肉眼在桌面上找，现在有一张图。

### 23.2 坐标一律用物理像素

`layout.json` 里存的是混合单位：`x/y` 是**物理像素**（屏幕坐标本来就是物理的），
`width/height` 是**逻辑像素**（换缩放比例后尺寸才对得上）。这个不对称是故意的。

布局视图只做等比映射，所以：

- 后端把 `width/height × scale` 换成物理像素再给前端，前端拿到的是**一套单位**；
- 拖拽换算只需要一个比值 `物理像素 / CSS 像素 = screen.w / board.clientWidth`；
- 前端完全不需要知道 DPI 是多少。

如果让前端自己乘 scale，125% 缩放下方块会和真实窗口差出肉眼可见的一截，
而且这种错在 100% 缩放的机器上永远复现不出来。

### 23.3 交互

- **拖方块**：`pointerdown` + `setPointerCapture`，`pointermove` 里换算坐标、
  夹到桌面范围内（窗口不能被拖到屏幕外，否则用户再也找不回来），
  直接改 DOM 不等宿主回包——否则手感会滞后一帧以上。
- **双击居中**：给一个确定的落点。穿透窗口和桌面层窗口都不好手动摆，
  双击比"精确拖到中间"现实得多。
- **拖边角改大小**：方块八个方向都有手柄，只看组的 `sizeMode`（「固定」时不给）。
  这一节只讲位置，尺寸与内容缩放的完整口径见 §28。
- **虚线方块**：实例存在但窗口不在（隐藏 / 组件停用）。记录还在，
  用户得能看见它，否则会以为丢了。

拖动中不重渲染：`render()` 里判断 `dragging` 非空就跳过布局，
否则刚抓起来的元素会被换成新节点，`setPointerCapture` 也就断了。

### 23.4 桥接：`manager.moveInstance` + `commit`

```json
{ "instanceId": "clock-1", "x": 240, "y": 160, "commit": false }
```

一次拖拽会产生几十上百个 `pointermove`。所以：

- 前端用 `requestAnimationFrame` 合并，一帧最多发一条；
- 拖动过程中 `commit=false`：只挪窗口，**不写文件**；
- 松手时 `commit=true`：落盘一次。

后端仍是两段式（§21）：先回 `{accepted:true}`，动作排进 UI 队列。
`SetPosition` 会同步给窗口发 `WM_WINDOWPOSCHANGING`，而我们正站在
WebView2 的消息回调里——和建窗、销毁窗口是同一条规矩。

窗口没开着（隐藏 / 停用）时只改存盘几何，不建窗口：等它下次出现，
就该落在用户摆的位置，而不是又回到居中。

### 23.5 一个容易漏的同步点

用户也可能**直接拖窗口本身**（便签的标题栏、时钟的表盘）。那条路径走的是
`WM_EXITSIZEMOVE` → `persistBounds`，原来不会通知管理界面，
于是布局页的方块会停在旧位置。

现在 `persistBounds` 末尾补一次 `deferToUI(refreshManager)`：
排到 UI 队列是因为此刻可能还在系统的移动循环里。

### 23.6 验证

没有可点的按钮可以自动化，所以临时在 `app.js` 末尾塞了一段自检
（验证完即删），走的是和拖拽完全相同的 `call("manager.moveInstance")`：

```
autotest 板子=699x377 方块 left=51.4062% top=34.4203% 尺寸=13.6719%x10.8696%
autotest 移动后 left=9.375% top=11.5942%
autotest 已复位
```

对一遍数就知道对不对（屏幕 2560×1380 物理像素）：

| 项 | 计算 | 日志 |
|---|---|---|
| 板子宽高比 | 2560/1380 = 1.855 | 699/377 = 1.854 ✓ |
| 方块 left | 1316/2560 = 51.41% | 51.4062% ✓ |
| 方块 top | 475/1380 = 34.42% | 34.4203% ✓ |
| 方块尺寸 | 350/2560, 150/1380 | 13.6719% × 10.8696% ✓ |
| 移动后 left | 240/2560 = 9.375% | 9.375% ✓ |

"移动后"那一行同时证明了回推链路是通的：`commit` 之后宿主推
`manager:state`，前端重渲染，方块才落到新位置。最后一步复位，
`layout.json` 里 `clock-1` 回到 `x=1316 y=475`。

第二轮自检（补页签 bug 之后重做，量在 `requestAnimationFrame` 之后）：

```
autotest: components→[components] | styles→[styles] | layout→[layout]
          board=698x376 box=95
```

三个页签各自只剩一个 pane 可见，板子有真实尺寸、方块 95px 宽。
第一版自检是**同步**量的，所以打印 `board=0x0`——那是测量时机问题，
不是板子真没尺寸；但正因为这个 `0x0` 差点被当成第二个 bug，所以这一轮
改成等两帧再量。

### 23.7 页签"点了没反应"：`[hidden]` 被作者样式盖掉

加布局页时顺手踩的坑，值得单独记：三个 pane 靠 `hidden` 属性切换，
但 `hidden` 生效靠的是 UA 样式表里的 `[hidden]{display:none}`，
而作者样式里写着 `.pane{display:flex}`。两条规则**特异性相同、作者优先**，
于是三个面板同时铺开、上下排成一列——点页签只是换了高亮，
第三个面板早被挤出可视区，看起来就是"布局页打不开"。

修法只有一条显式规则：

```css
.pane[hidden] { display: none; }
```

启动自检里加了永久探针「**可见页签 N 个**」（`getComputedStyle(p).display !== "none"`），
正常恒为 1。这类"看着像没反应"的问题截图也判断不出来，一个数字就够了。

## 24. 背景与 CSS 声明一致：哪条能成立，哪条不能

需求原话是「背景要与 css 样式的设计保持一致，css 为磨砂，显示也是磨砂；css 为透明，
背景也是透明」。这句话拆成两条能力，一条成立、一条不成立，而且不成立的那条是**引擎级**
的，不是我们没写对。

### 24.1 底色不能画在 `body` 上

`html` 透明时，`body` 的背景会**传播到画布**——画布是方的，`body{border-radius}` 管不住它，
圆角处露直角。所以底色一律画在内层元素上，由它自己裁：

```css
body { background: transparent; overflow: hidden; }
.note { height: 100%; background: var(--sn-background);
        border-radius: var(--sn-radius); overflow: hidden; }
```

这条也解释了为什么诊断探针必须**连第一个子元素一起读**：只看 `body` 的
`backgroundColor` 会得到 `rgba(0,0,0,0)`，误判成"组件没画底色"。

### 24.2 半透明成立：页面 alpha 直通合成器

CSS 写 `rgba(...)`，窗口就真的半透明，背后内容原样透出——实测背景区
「相邻像素差均值 53.66」，背后浏览器窗口的文字边缘清晰可辨。这条不需要任何额外开关，
因为 DComp 直接合成页面的 alpha。

### 24.3 磨砂不成立：四条路，全部否决

判据统一用「同一区域内**相邻像素差均值**」——真模糊会抹平高频细节，数值应该显著低于基线；
若降到 0，那不是模糊，是背后被抹成一片纯色。

| 方案 | 相邻差 | 结论 |
|---|---|---|
| 无材质（基线） | 53.66 | 背后内容原样透出 |
| CSS `backdrop-filter: blur(20px)` | **0.00** | 不是模糊，是把背后**抹成纯色** |
| `DWMWA_SYSTEMBACKDROP_TYPE`=2 Mica | 0.00，纯 `#AFAFAF` | 只画系统主题色，不采样背后 |
| 同上 =3 Acrylic | 0.00，纯 `#989898` | 同上 |
| 同上 =4 Tabbed | 0.00，纯 `#A7A7A7` | 同上 |
| `SetWindowCompositionAttribute` acrylic | 51.17（≈基线） | 完全无效 |
| DComp `CreateBackdropBrush` | — | **API 不存在**：`IDCompositionDevice3` 方法表里只有 effect 工厂，只有 WinRT `Compositor.CreateBackdropBrush` 有，而它要独占 HWND 的 desktop window target，与 WebView2 冲突 |

实验设计值得抄：**左右对照**。同一页面左半开 `backdrop-filter`、右半不开，取同一帧比较，
排除"背后内容在两轮之间变了"这个变量。结果左半 0.00 / 右半 33.24，结论无可争议。

也排除了「是不是 `WS_EX_LAYERED` 挡的」：把弹幕窗口的分层位临时清掉再开 acrylic，
读数仍是均匀 152。**根因是 `NOREDIRECTIONBITMAP` + DComp 这条合成路径拿不到窗口背后的像素**，
与穿透/分层无关。

### 24.4 给插件作者的硬规则

- **不要写 `backdrop-filter`**。它在这里不是"没效果"，是**把背后抹成一块纯色**——
  比不写更糟，而且没有任何报错。
- 想要磨砂观感只能用 CSS 近似：**半透明底色 + 噪点颗粒**（不采样背后，纯前端合成）。
- 想要"透出桌面"就写 `rgba()`；想要不透明就写实色。这两种都真。

真磨砂只剩一条路：定期抓取窗口背后的屏幕区域 → D2D 模糊 → 当背景图喂给页面。
代价是要处理"抓到自己"和"背后内容变了要重抓"，本期不做。

### 24.5 顺带定下的一件事：窗口投影

`visual.shadow` 非空才给窗口投影，实现是 `DWMWA_WINDOW_CORNER_PREFERENCE(33)`：
`ROUND` = 圆角 + 一圈投影，`DONOTROUND` = 两者都没有。**Win11 上这两个是同一个开关**，
`DWMWA_NCRENDERING_POLICY=DWMNCRP_DISABLED` 无效（Set 返 S_OK，投影纹丝不动）。
属性 33 可读，设完必须读回复核——否则"设了没生效"会静默通过。

这条同时修掉一个隐性错误：以前所有窗口一律 `ROUND`，`--sn-radius: 0` 的时钟和弹幕
被系统偷偷裁成 8px 圆角。现在形状全交给 CSS。

---

## 25. 组取代样式包：窗口语义变成正交开关

这一节记录一次**破坏性重构**：删掉 L1 样式包，改成内核里的「组」。
旧模型（§6.1 的 `style.json`、§3.3 的两级插件）在本节之后**作废**。

### 25.1 为什么要改

旧模型里「样式包」是个插件包：用户想调个圆角，得先写一份 `style.json`、
装进 `plugins/styles/`、再让组件去 `"style": "xxx"` 绑定它。三个问题：

1. **调外观需要会写 JSON 并会装包**。这是用户自己的桌面，不该有这种门槛。
2. **绑定是一对多但方向反了**：组件指向样式，于是"两个组件共用一套外观"
   要靠两份清单里写同一个 id 来维持，改一处漏一处。
3. **窗口形态被做成了枚举**。`desktop` / `topmost` / `toast` 三选一，
   而"要不要穿透""能不能缩放""有没有标题栏"其实是**互相独立**的开关，
   硬塞进枚举就只能靠加形态来组合，加不过来。

### 25.2 新模型

**组（Group）**是内核的运行时数据，存 `data/groups.json`，不随插件分发。

```go
type Group struct {
    ID         string
    Name       string
    Color      string            // 识别色 #rrggbb，只用来区分组
    Window     WindowSpec        // 窗口开关
    Visual     Visual            // 外观：留白/圆角/底色/阴影
    CSSVars    map[string]string
    Components []string          // 旧模型遗留，仅用于迁移，迁完为空（见 §26）
}
```

- **组 → 实例一对多**：一个组管着一批窗口，组内所有实例一起变。
- 归属只存一份（在实例的 `GroupID` 上），组不记录自己有哪些实例——
  避免两处数据打架。`Group.Components` 只是老数据的迁移入口。
- 用户在主界面左侧的组列表里直接编辑，改的是**组本身**，组里所有窗口一起变。

### 25.3 窗口语义：三选一 + 一堆正交开关

```go
type WindowSpec struct {
    Layer        Layer      // normal | topmost | desktop（三选一，互斥）
    ClickThrough bool       // 独立开关
    Activation   Activation // 由 Normalize 推导，界面不暴露
    Resizable    bool
    SizeMode     SizeMode   // fixed | user | fitContent
    Titlebar     bool
}
```

`toast` 从词表里删掉了：它当年是"置顶 + 穿透 + 不可缩放 + 无标题栏"的打包，
现在这四条各自是开关，`toast` 就只是其中一种组合，没有单独存在的必要。

**推导项由 `Normalize()` 补齐**，不让用户在界面上再点一次：

```go
if w.ClickThrough { w.Activation = NoActivate }
if w.Layer == LayerDesktop { w.Activation = NoActivate }
```

理由：穿透窗口永远收不到鼠标，也就永远不可能被激活；桌面层贴在图标之上、
应用窗口之下，抢了焦点就会从"桌面的一部分"变成一个被盖住的窗口。
允许这种组合存在于数据里只会在运行时自相矛盾，所以在归一化这一步抹平。

### 25.4 合并顺序：组永远赢

```go
merged := over.Merge(g.Visual)   // over = 组件的建议
CSSVars: MergeVars(overVars, g.CSSVars)
```

**组压组件**，和旧模型里"L2 覆盖 L1"正好相反。理由很直接：组是用户亲手调的，
必须生效；组件清单里写的是作者的出厂建议，用户改了组就该盖过去。

组没设的字段仍然留给组件：`Visual` 的字段是指针，`nil` 表示"我没意见"，
此时组件的值存活，最后才落到兜底值。**0 是合法取值**（`radius: 0` = 直角），
所以不能用零值判断"没设"。

### 25.5 改设置：重建窗口 vs 只推样式

窗口的层级、穿透、可缩放、标题栏都是 `CreateWindow` 时的选项，
**没有"就地改"这回事**，只能拆了重建。但改留白、圆角、底色、阴影是纯 CSS，
重建会闪一下，拖动颜色滑杆时尤其明显。

所以 `manager.updateGroup` 分两条路：

```go
if before.Window != next.Window.Normalize() {
    a.rebuildGroup(p.ID)   // 拆窗口重建，位置与显隐从实例记录恢复
} else {
    a.restyleGroup(p.ID)   // Host.ApplyStyle 推一次 CSS 变量，不动窗口
}
```

`rebuild` 必须在 UI 队列上跑（`deferToUI`）：不能在 WebView2 的回调里
建或拆窗口，见 §21。

### 25.6 启动时的一致性修复：`ensureGroups`

每次启动都跑，处理几种"用户可能在程序关着的时候干过"的事：

1. **组列表为空**（首次启动）→ 用组合根给的种子生成默认组。
2. **老数据把归属记在组上**（`Group.Components`）→ 搬到实例的 `GroupID` 上，
   然后把组上那份清空。两处都留着迟早会打架。
3. **实例指向一个已不存在的组** → 改放兜底组 `builtin.group.default`。
4. **组没有识别色** → 补一个当前用得最少的颜色。

注意顺序：`Boot` 必须**先读实例再整理分组**。归属落在实例上，
而迁移老数据必须先有实例清单才谈得上"把归属搬过去"。

兜底组由 `ensureDefaultGroup()` 按需补出来，`CreateInstance` 传空组时也走它——
新建实例总得有个落脚点，报错让用户自己建组太不友好。

### 25.7 插件侧的变化

- `PackageKind` 只剩 `PackageComponent`；`Package` 不再有 `Style` 字段。
- 组件清单里的 `"style"` 字段被**静默忽略**（不进 `forbiddenComponentKeys`），
  老插件不改也能继续加载。
- 但 `window` / `type` / `customLayer` / `customFlags` 仍然显式拒绝：
  这些是想绕过组直接改窗口语义，静默忽略会让人以为改成功了。
- 安装时只认 `manifest.json`；只有 `style.json` 的目录会得到一句明确报错
  ——「这是旧版的样式包……新版本里窗口外观由「组」决定」，而不是"文件坏了"。

### 25.8 主界面

- 左：组列表（折叠箭头 + 色点 + 组名 + 层级/穿透徽标 + **实例行** + 「⋯」进组设置）
- 右：桌面布局板，**范围按组切换**
- 右抽屉：组设置（识别色、层级三选一、穿透/可缩放/标题栏开关、留白/圆角滑杆、底色/阴影）
- 组件设置放在另一张抽屉里：启用开关、所属组下拉、实例列表、打开目录/卸载

窗口形态的徽标只有三种（常规/置顶/置底）加一个「穿透」，正好对应
"三选一 + 独立开关"这个结构，界面上不会出现无法组合的选项。

**侧边栏按实例列，不按组件类型列。** 用户面前摆的是一个个窗口，
"便签有 2 个窗口"和"有 2 个便签组件"是两件事，前者才是他要管的：

```
桌面层  ▾  ① 数字时钟  clock-1  [开关]
置顶    ▾  ① 便签      note-1   [开关]
          ② 便签      note-2   [开关]
```

- 每行右侧的开关是**这个实例的显隐**（`manager.setVisible`），不是组件的启停。
- 组件级启停挪到组件设置抽屉里：停用会关掉它的所有窗口，但实例记录与私有数据都留着。
  组件停用时，它那些实例行的开关是 disabled 的——让它能拨却什么都不发生最糟。
- 没有实例的组件也留一行（灰字「无实例」+ 一个「+」直接建实例），
  否则它在这个组里就彻底消失了，用户既看不到也点不进设置去建实例。

**组可以折叠**：箭头按钮管折叠（只存内存，属于"我现在想看什么"的临时状态，
不是配置），组头其余部分管"只看这个组"。折叠时保留实例数，一眼能看出里面有几个窗口。

### 25.8.1 布局板按组工作

位置是**绝对屏幕坐标**，所以"按组调整布局"只能在只显示那个组的前提下做：
全都摊在一起时，拖一个方块到底改的是哪个组会变得含糊。因此布局板有两种范围：

| 范围 | 显示 | 能否拖 |
|---|---|---|
| 全部组 | 所有方块，按组着色，带图例 | **不能**（只读总览） |
| 某一个组 | 只有这个组的方块 | 能，双击居中也在这一档 |

- 进入某个组：点侧边栏组头、点图例、点全部视图里的方块，或者点组头的「⋯」进设置。
- 退出：点同一个组头（再点一次），或点标题右侧的「显示全部」。
- 只读视图里方块可点，点它就进到它所属的组——这比"点了没反应"清楚。

**组色**是 `Group.Color`（`#rrggbb`），纯标记，不参与任何窗口或样式计算：

- 新组按 `GroupPalette` 自动分配，取"当前用得最少"的颜色（不是按顺序取下一个——
  删过组之后按顺序会撞上还在用的颜色）。
- 老数据（字段出现之前生成的组）在 `ensureGroups` 里补上并落盘。
- 界面把颜色清空时 `UpdateGroup` 保留原色：没有"不设颜色"这个状态，
  真留空会让那个组的方块在布局板上失去归属。
- 只收 `#rrggbb`：这个值要同时喂给 `<input type="color">` 和 CSS 变量，
  形状不统一就会一半能用一半不能用。

方块用 `--g`（边框）和 `--g-soft`（`#rrggbb2e` 淡底）上色，侧边栏的色点、
图例的圆点共用同一组变量。没显示（隐藏或未运行）的方块用虚线 + 降透明度，
但**保留组色**——否则"哪个组的窗口关着"就看不出来了。

### 25.9 旧数据

- `data/styles/` 与 `builtin/styles/` 都不再被读，`bundles.json` 里的样式包记录
  只增不减，留着无害。
- `layout.json` 里的 `pinned` 字段不再迁移：置顶现在是组的属性，
  单个实例没有"自己置顶"这回事。
- `groups.json` 不存在时按种子生成，所以老用户升级上来会得到四个默认组。
- **`groups.json` 里的 `components` 数组会被搬空**：归属迁到
  `layout.json` 的 `instances[].groupId` 上（见 §26）。迁移是幂等的，
  迁完组上那份就清掉，之后它一直是空的。

## 26. 归属下移到实例：一个组管的是一批窗口

### 26.1 为什么组不能管组件

25 版把归属放在组件上（`Group.Components []string`），默认一个组件只属于一个组。
第一次真正用起来就撞到墙：

> 我想要两个便签，一个贴在桌面上（跟图标一起、Win+D 时消失），
> 另一个浮在所有窗口最上面。

组件级归属表达不了这件事——同一个组件的两个窗口必须在同一个组里。
**组管的是窗口，不是组件。** 于是归属从 `Group.Components` 移到
`Instance.GroupID`，`Group.Components` 降级成纯迁移字段（`json` 里还要留着，
删掉就等于迁移前先把数据丢了）。

带来的直接结果：

- `ComponentView` 不再有 `GroupID`/`GroupName`——组件是静态定义，没有窗口也就没有组。
- `InstanceView` 多了 `GroupID`/`GroupName`。
- `CreateInstance(ctx, componentID, groupID)`；`SetComponentGroup` 换成
  `SetInstanceGroup(ctx, instanceID, groupID)`。
- `DeleteGroup` 的拒绝条件从"组里还有组件"变成"组里还有实例"。
- 改组设置时**不动归属**：那是实例的事。

### 26.2 界面：两个视角看同一批实例

实例成了唯一的管理单位，侧边栏就得分两个视角，顶部用一组页签切换：

- **「组」视角**（默认）：按窗口形态归类。组卡片里列的是**这个组里的实例**，
  点组头的 `+` 打开「选组件」抽屉，挑一个组件就在这个组里开一个新窗口。
- **「组件」视角**：按来源归类。一个组件一张卡，卡里是它名下**全部实例**，
  每行带一个"属于哪个组"的下拉——这是实例级改组的入口。

两个视角的数据源是同一份 `instances` 扁平清单（`managerState.Instances`），
不是从组件的嵌套结构里拼出来的。宿主同时下发嵌套版（给组件设置抽屉用）和
扁平版（给侧边栏与布局板用），是同一批数据的两种切法。

### 26.3 抽屉的四种

| kind | 主体 | 用途 |
|---|---|---|
| `group` | 组 | 名称/识别色/层级/穿透/可缩放/标题栏/尺寸/留白/圆角/底色/阴影 + 实例清单 |
| `component` | 组件 | 说明/启用 + 实例清单（每行带组下拉）+ 选组新建 |
| `instance` | 实例 | 所属组、状态、几何、隐藏/关闭 |
| `pick` | 组 | 往这个组里挑组件建实例（组固定，挑组件） |

「组」抽屉和「pick」抽屉是一对：前者管已有的窗口，后者往里加新的。

### 26.4 单例组件不能提供"再建一个"

`clock`、`danmaku` 的清单里 `singleton: true`。原来界面照样把"新建实例"
摆在用户面前，点下去才收到一句「组件 数字时钟 是单例，已经有一个实例了」——
白点一次，而且提示三秒就没了，事后翻日志什么也没有。

现在 `ComponentView.Singleton` 一路下发到界面：选组件抽屉里单例组件直接置灰
（meta 写「单例，只能有一个」），组件设置抽屉里"新建"按钮也是关的。

### 26.5 两条踩过的坑

- **删除实例必须自己落盘。** `manager.removeInstance` 走的是直接 `Destroy`
  （不经过 `OnClose`，因为那条路会给一个注定消失的记录写一遍几何），
  于是没有任何地方替这次删除写 `layout.json`——**下次启动那个实例又回来了**。
  现在删除后显式 `SaveLayout`。
- **`fail()` 必须写日志。** 原来只推一条三秒消失的浮层提示，而失败往往发生在
  「点了但窗口没出来」这种时候，事后只能翻 `app.log`，结果什么都没有。
  现在 `fail()` 先 `log.Printf` 再推提示。

---

## 27. 变量：作者声明，实例持有

### 27.1 为什么要有这一层

组件想让人配点东西——便签的标题与字号、弹幕的文字与速度。之前只有两条路，
都不合适：

- **让组件自己在界面里画设置**：那就要给组件开一整套设置窗口的权限，
  而且"用户在哪填"散落在每个组件里，内核管不到，导出/迁移也没法统一。
- **塞进 storage**：storage 是组件自己写的运行时数据（便签正文），
  和"用户在管理界面里填的配置"是两种东西。混在一个文件里，组件一次
  `setStorage` 就可能把用户填的东西盖掉；"删实例时哪些该留"也会变得含糊。

所以变量单独一层：**作者在清单里声明要什么，用户在管理界面里按实例填，
内核负责校验与落盘。**

### 27.2 声明：`Definition.Variables`

```json
"variables": [
  { "key": "title", "label": "标题", "type": "text", "default": "便签",
    "placeholder": "顶栏显示的名字", "description": "留空就用组件名" },
  { "key": "body", "label": "正文", "type": "textarea", "rows": 6 },
  { "key": "size", "label": "字号", "type": "select", "default": "m",
    "options": [{ "value": "s", "label": "小" }, { "value": "m", "label": "中" }] },
  { "key": "lines", "label": "多行弹幕", "type": "list", "item": "text",
    "default": ["第一条", "第二条"] },
  { "key": "cards", "label": "卡片", "type": "group",
    "fields": [
      { "key": "title", "label": "标题", "type": "text" },
      { "key": "context", "label": "内容", "type": "textarea", "rows": 3 },
      { "key": "order", "label": "序号", "type": "number", "default": 0 },
      { "key": "pinned", "label": "置顶", "type": "bool" }
    ],
    "default": [ { "title": "第一张", "context": "…", "order": 1 } ] }
]
```

| 字段 | 说明 |
|---|---|
| `key` | 标识，组件代码用它取值。`[A-Za-z_][A-Za-z0-9_.-]*`，**安装后不可改** |
| `label` | 界面标题，留空用 `key` |
| `description` | 标题下那行说明 |
| `type` | `text` / `textarea` / `select` / `number` / `bool` / `list` / `group`，留空按 `text` |
| `default` | 没填过时的兜底值，**不落盘**（见 27.4）。**形状跟着 `type` 走**：文本类是字符串，`number` 是数字，`bool` 是布尔，`list` / `group` 是数组 |
| `placeholder` | 输入框灰字 |
| `item` | 只在 `list` 用：每一条落到对象里的字段名，留空按 `text` |
| `fields` | 只在 `group` 用：一条记录由哪几个字段组成，见 27.10 |
| `options` | 只在 `select` 用。`"小"` 或 `{"value":"s","label":"小"}` 两种写法都行 |
| `required` | 只影响界面上的"必填"角标，**不阻止保存** |
| `rows` | `textarea` / `list` 编辑框的高度，默认 4（`list` 按 6），上限 20 |

七种类型分两类。**标量**：`text` / `textarea` / `select` 是字符串，`number`
是数字，`bool` 是布尔——它们的意义不是"多一种控件"，而是**让值保持 JSON
的原生类型**。字号存成 `13` 而不是 `"13"`，组件那边就不用 parseInt。
**集合**：`list` 一条一个字段，`group` 一条几个字段，都落盘成数组。

刻意不做"颜色""日期"这类类型：它们要么能用一个 `text` 加说明表达，
要么该由组件自己解释——多一种类型就多一条"清单写了但内核没实现"的路。

**没有任何一种类型要求用户写 JSON。** `group` 那条看起来像 JSON，
但界面上是一张张卡片、每个字段一个普通输入框，序列化是控件自己做的
（见 27.10）。

**声明写错了在加载清单时就报**：`Definition.Validate()` 会跑
`ValidateVariables`——key 不合法、key 重复、`text` 带了 options、
`select` 没 options 或选项重复、默认值不在选项里、`number` 的默认值写成
`"13"`、`list` 的默认值不是数组或某一项缺 `item` 字段（或多写了别的字段）、
`group` 没有 `fields` 或字段重名/字段类型是 `list`、`group`、
`group` 的默认值里有未声明的字段、超过 32 个……
这些都会让插件**装不上**，而不是等用户打开管理界面才发现有个填不了的控件。

### 27.3 值：`data/vars/<组件id>/<实例id>.json`

```
data/vars/note/note-1.json   {"title":"工作","fontSize":"l"}
data/vars/note/note-2.json   {"title":"生活"}
data/vars/danmaku/danmaku-1.json
  {"text":"…","speed":"fast","lines":[{"text":"第一条"},{"text":"第二条"}]}
data/vars/cards/cards-1.json
  {"cards":[{"title":"第一张","context":"…","order":1}]}
```

一个实例一个文件，和 `storage/` 同构但**分开**。分开的理由就是 27.1 说的那两条：
组件怎么写都碰不到用户填的东西，删实例时也不用去分辨一份文件里哪些是用户的。

**值的类型跟着声明走**：文本类是一个 JSON 字符串，`number` 是数字，
`bool` 是布尔，`list` / `group` 是一个对象数组。
`lines` 与 `cards` 那两段存的都是真正的数组，不是"数组的字符串"——组件拿到手
直接 `Array.isArray(v.cards) === true`，不需要 `JSON.parse`，也不需要自己按
换行拆、自己定转义规则。

- **归属在实例上**，和窗口几何一样。同一个组件的两个实例各持一份值——
  一个便签用大字号、另一个用小字号，这正是"变量按实例存"的意义。
- **删除实例连带删掉它的变量文件**：窗口没了，留着配置只会在下次
  装回来时塞给一个同名的新实例。
- **空表等于删文件**：所有值都改回默认值之后，磁盘上不留一个空壳。

### 27.4 默认值不落盘

文件里**只存用户改过的值**。读取时由 `Variable.Value(values)` 兜底：
没有这个 key 就用 `default`。

好处是作者日后调整清单里的默认值，用户没动过的实例会自动跟着变。
反过来说，一旦把默认值也写进文件，"用户没改"和"用户改成了和默认值一样"
就再也分不出来了。`SetVars` 里因此有一条：**coerce 之后与 `default` 等价的
值直接从表里删掉**，而不是存成同值。

"等价"用 `SameJSON` 判断（解析后深比较），所以把默认值原样抄一遍、
只是缩进不同或对象键顺序不同，也算没改过。`list` 类型同理：清单里写
`["第一条"]`、用户填一行"第一条"，两者归一之后都是
`[{"text":"第一条"}]`，判为"没改"，不落盘。

代价是显式的空串与"没填过"确实分不开——但 `text` 类型的默认值本来就是空串，
两者行为完全一样，没有实际差别。

### 27.5 写入口：`SetVars` 的三条规则

```go
SetVars(ctx, instanceID string, values map[string]string) error
```

入参是界面上控件里的**原始文本**：标量是框里的字，`list` 是那个多行框里的
若干行文字，`group` 是卡片表单自己序列化好的 JSON 数组（**用户不写 JSON**，
是那些输入框拼出来的）。界面这一侧永远不需要"理解"值的结构，`group` 除外——
多字段没法用一段文本表达，只能由控件自己拼，见 27.10。

1. **没提到的变量保持原样。** 表单可以只提交动过的那一个，不会被顺手清空。
2. **提到的都过 `Coerce`**：`select` 必须在选项里（否则报错、整次写入放弃），
   `text` 去首尾空白，`textarea` 原样保留（换行和缩进可能就是内容本身），
   `number` 必须是数字字面量（`"13"` 这种带引号的会被拒），
   `bool` 只认 `true`/`false`/`1`/`0`，
   `list` 按行切开、去掉空行与首尾空白、包成 `[{"text":"…"}]`，
   `group` 逐条逐字段校验类型、丢掉空记录、未声明的字段直接报错。
3. **声明里已经没有的 key 直接丢掉。** 从旧版本升上来的文件里可能留着
   作者后来删掉的变量，留着既没人读，又会在作者重新用同一个 key 时
   冒出一个"用户从没填过"的值。

`list` 与 `group` 的 `Coerce` 返回 `nil` 表示"清空这一项"：一条都没有时
把 key 从文件里删掉，默认值重新生效。返回 `[]` 就不行了——`[]` 是一个明确的
取值（"就是要空列表"），而清空的意思是"我不要这个自定义值了"。

### 27.6 怎么送到两边

- **管理界面**：`managerState.components[].variables` 给声明（补齐兜底值之后
  的具体值），`managerState.instances[].vars` 给**最终值**。声明挂在组件上、
  值挂在实例上——这正是两者的归属关系，也省得同一份声明在推送里重复 N 遍。
- **组件**：`component.ready` 的返回体里带 `vars`；用户在管理界面改完之后
  宿主推一条 `{from:"host", event:"vars", data:{…}}`，前端按
  `document.addEventListener("host:vars", …)` 接。

  ```js
  const ready = await call("component.ready", {});
  applyVars(ready.vars);
  document.addEventListener("host:vars", (e) => applyVars(e.detail));
  ```

  注意 `component.ready` 里的 `vars` 是**现取**的，不是建窗那一刻的快照：
  用户可能在管理界面改过值，而窗口还没重建。

### 27.7 界面

- **实例抽屉**（`instance`）：按声明渲染表单，七种类型对应五种控件——
  `text`/`number` 是单行（数字框），`select` 是下拉，`bool` 是勾选框，
  `textarea`/`list` 是多行框，`group` 是一摞卡片。
  `list` 的多行框用等宽字体、下面多一行"一行一条，空行会被忽略"。
  边打边存，合并 400ms，失焦立刻落盘。必填但没填的变量在顶部提示一行。
- **组件抽屉**（`component`）：只列声明（名称/key/类型），不提供输入框——
  值不是组件的。旁边一句"值按实例保存，打开某个实例的设置就能填"。

**界面上没有 JSON 输入框，这是有意的。** 早先试过让用户直接写
`[{"text":"第一条"}]`，问题不在于难，而在于**边打边校验没法用**：
`[{` 到 `[{"a":1}]` 之间必然有一瞬间是非法 JSON，于是要么每敲一个字弹一条
"JSON 不合法"，要么加一层"打完了才校验"的时序逻辑。而且作者真正要表达的
往往只是"若干行"或"几个字段"，让他去背括号和引号是多余的负担。

所以 `list` 就是一个普通多行文本框：一行一条，拆成对象数组是宿主
`Coerce` 的事。用户在框里敲什么都是合法输入——一行里出现 `[{` 也只是
那一行的文字，不会被当成语法错误（这条在诊断里专门验过）。

一条必须记住的坑：**正在往变量输入框里打字时不要重渲染抽屉**。
`refreshDrawer()` 原来每次都重建 DOM，重建就会丢焦点和光标位置，
表现是"每敲一个字就跳出输入框"。现在检测焦点是否落在 `[data-var]`
**或 `[data-rec-field]`** 上，是就跳过这一轮刷新——记录表里的输入框带的是
后者，漏了它的话每敲一个字卡片就会重建一次。值本来就在框里，
等失焦之后的下一次推送再刷。

### 27.8 预置包里的示例

- `note`：`title`（text）+ `fontSize`（select，小/中/大/特大）。正文 `font-size`
  由变量决定，标题留空退回组件名。
- `danmaku`：`text`（textarea）+ `speed`（select，慢/中/快 → 28s/18s/10s）+
  `lines`（list，`item: "text"`，默认 `[]`）。改文字时**两份都要改**——
  无缝循环靠的是轨道里两份一模一样的文字，只改第一份的话接缝处会突然换内容。
  `lines` 非空时**优先于** `text`：每项取 `text` 字段，拼成 ` · ` 一行。
  这就是"同一个实例返回多组变量"的实际用法——想放十条弹幕，在「多行弹幕」
  框里一行写一条就行，不用碰 JSON。

### 27.9 列表式变量：`list` 类型

`text`/`textarea` 只能表达"一个字符串"。但作者经常需要一组结构化的东西：
弹幕的一串条目、待办的几条事项、轮播的几张卡片。把它们塞进一个字符串
（换行分隔、逗号分隔）之后，组件那边还得自己解析、自己定义转义规则，
换个作者就换一套格式——这正是内核该统一的口径。

所以第四种类型 `list`。它**在界面上就是一个多行文本框**（一行一条），
落到磁盘与组件手里是对象数组 `[{"text":"第一条"}]`，
`Array.isArray(v.lines) === true`，不需要 `JSON.parse`。

为什么不让用户直接写 JSON：`[{` 到 `[{"a":1}]` 之间必然有一瞬间是非法
JSON，边打边报错没法用；而且"若干行"本来就不该要求用户会写括号。
结构化留在内核这侧做，代价只是列表项**只有一个字段**。

四条约定：

1. **一行一条，空行忽略**。每行的首尾空白去掉——一行弹幕末尾多两个空格
   没有意义；两条之间多敲一个回车本意是留白，不是"加一条空的"。
2. **字段名由 `item` 决定，默认 `text`**。清单写 `"item": "title"`，
   组件拿到的就是 `[{"title":"买菜"}]`，代码里读 `o.title` 更清楚。
3. **列表项只有一个字段**。默认值里多写的键（`{"text":"a","color":"red"}`）
   会在加载清单时直接报错——用户编辑之后只能产出一个字段，多写的那个
   迟早会无声消失，与其让它悄悄丢，不如当场指出来。真需要多个字段时用
   27.10 的 `group`。
4. **空输入 = 清空这一项**，`Coerce` 返回 `nil`，`SetVars` 把 key 删掉，
   默认值重新生效。这里刻意**不**返回 `[]`——`[]` 是一个明确的取值
   （"就是要空列表"），而清空输入框的意思是"我不要这个自定义值了"。

默认值可以写省事形式 `["第一条","第二条"]`，加载时归一成
`[{"text":"第一条"}]`。归一不是洁癖：不归一的话"用户填的和默认值一样"
永远比不出来（见 27.4），默认值会被白白落盘一次，作者日后改默认值
用户就跟不上了。

### 27.10 记录表：`group` 类型

一条记录要几个字段时（`[{title, context}, …]`），用 `group`：

```json
{ "key": "cards", "label": "卡片", "type": "group",
  "fields": [
    { "key": "title",   "label": "标题", "type": "text" },
    { "key": "context", "label": "内容", "type": "textarea", "rows": 3 },
    { "key": "order",   "label": "序号", "type": "number", "default": 0 },
    { "key": "pinned",  "label": "置顶", "type": "bool" }
  ],
  "default": [ { "title": "第一张", "context": "…", "order": 1 } ] }
```

组件拿到的就是 `[{title, context, order, pinned}, …]`——和 `list` 同构，
只是每条更宽。**字段类型只能是标量**：`text` / `textarea` / `select` /
`number` / `bool`。列表里再套列表在界面上没法用，组件那边也会拿到一个
嵌套深度不定的值，所以 `list` 与 `group` 做字段时会在加载清单时被拒。

**界面是一条一块的纵向卡片**：每张卡片上方一行"第 N 条 / 删除"，下面
字段自上而下排（小标题 + 控件），底部一个「+ 添加一条」。

五条约定：

1. **空卡片不留痕**。点「+ 添加一条」只是往 DOM 里插一张空卡片，**不提交**；
   一个字段都没填的卡片在提交时被丢掉，磁盘上不会出现 `{}`。
2. **没填的字段不写进值里**。界面上空着的单行框 / 多行框 / 下拉不进对象，
   组件读到的是 `undefined`，跟"作者没声明这个字段"一个语义。
   布尔是例外：勾选框**总是**写 `true` / `false`，因为"关掉了"本身是一个
   明确的取值，不写就分不清它和"没有这个字段"。
3. **值保持 JSON 原生类型**。`order` 存成数字 `3`，不是 `"3"`；
   `pinned` 存成布尔 `true`，不是 `"true"`。宿主 `Coerce` 会逐字段验类型，
   类型对不上直接报错、整次写入放弃——不会把一个字符串偷偷塞进数字字段。
4. **未声明的字段当场拒掉**。界面上根本没有它的控件，留着只会在用户
   一改就消失。默认值里写错了同理，加载清单时就报。
5. **一条都没有 = 清空这一项**，和 `list` 一样把 key 删掉、默认值重新生效。

**唯一一处"界面拼 JSON"**：`group` 的提交值是控件自己序列化好的数组
（`readRecords()` 读卡片 → `JSON.stringify`）。用户看到的始终是一张张
卡片和普通输入框，不碰 JSON；宿主那边照旧只认"值"，不认"用户输入了什么
文本"。这是多字段没法用一段文本表达的必然结果，`list` 那条"一行一条"
的纯文本路径不受影响。

**字段的 `default` 只是新建卡片时的预填值**，不进落盘：用户新建一条、
什么都不改就保存，存下来的仍是"没填的字段不写"。想给整张表一个默认内容，
写变量级的 `default`（数组），不是字段级的。

## 28. 改尺寸与内容缩放：两个正交的维度

### 28.1 为什么必须拆成两件事

"把这个便签放大一点"在用户嘴里是一句话，在实现上是两件互不相干的事：

| | 改的是什么 | 存的是 | 效果 |
|---|---|---|---|
| **改尺寸** | 窗口矩形 | `Instance.Bounds`（x/y 物理像素，w/h 逻辑像素） | 窗口变大，里面的字大小不变，可用的排版宽度变大 |
| **内容缩放** | 页面排版比例 | `Instance.Zoom`（0.5 ~ 3.0） | 窗口不变，里面的字和间距一起变大，排版宽度变小 |

合成一个值就会丢掉一个正当需求：便签拉长时不该顺便把字撑大，
而"字太小看不清"也不该逼着用户把窗口撑到半个屏幕。

两条都落在**实例**上，不是组上：同一个便签组里，一张可以是 200×150 @100%，
另一张 400×300 @150%。

### 28.2 内容缩放怎么实现

用 `ICoreWebView2Controller3::put_RasterizationScale(dpi × zoom)`。

这个接口原本是给 DPI 用的，但它同时决定**一个 CSS 像素占几个物理像素**，
所以乘上去就等于整页缩放：页面是真的按倍数重新排版，不是把位图拉大。
实测（125% DPI 的机器，`note-1` 窗口 325×250 物理像素）：

| | `documentElement.clientWidth` | `devicePixelRatio` |
|---|---|---|
| 100% | 260 | 1.25 |
| 150% | 174（=260/1.5） | 1.875（=1.25×1.5） |

排版视口跟着变小，这是"真的缩放"和"放大镜"的分界。

三条约束：

1. **必须先 `put_ShouldDetectMonitorScaleChanges(FALSE)`**，否则引擎在排版 /
   导航后会把它重置回自己算的值。这条和 DPI 那条是同一个坑，见 §22。
2. **改窗口尺寸不会重置它**（实测：改尺寸后 `clientWidth` 仍是
   400/1.875≈214，`dpr` 仍是 1.875）。所以不需要在每次改尺寸后补设，
   只要在建窗和导航完成后各设一次。
3. **只碰 COM 属性，不发窗口位置消息**，所以在桥接回调里直接调是安全的，
   不像 `SetPosition` 那样要排 UI 队列躲重入。

不用 `put_ZoomFactor`（浏览器的 Ctrl+滚轮缩放）：它是**按 origin 持久化**的，
而所有组件页共用同一个虚拟主机前缀，一个实例调过缩放会漏到别的实例上。

### 28.3 布局板上拖边框

`#board` 里每个方块在组视图下带八个手柄（四角 + 四边），
`data-edge` 取值 `n/s/e/w/ne/nw/se/sw`。用绝对定位的手柄而不是
"在 mousedown 里算离边几像素"：方块缩到很小时后者会误判。

- **组视图才有手柄**，全部组视图是只读总览（和拖位置同一条规则）。
- **只跟 `sizeMode` 走，不看 `resizable`**。这一条是踩过才定下来的：
  `resizable` 说的是"用户能不能直接拖窗口边框"，而穿透窗口和桌面层窗口
  根本收不到鼠标，它们永远是 `false`——三个内置组里有两个就是这种情况。
  拿它去挡布局板，那两个组就再也没有尺寸入口了，而布局板恰恰是它们唯一的
  入口。`sizeMode = "fixed"` 才是"谁都不能改"，它挡得住，界面上不给手柄、
  提示里写明原因，`manager.setInstanceBounds` 也拒绝。
- **坐标全在物理像素里算**，和拖位置一致；只在提交时除以 `scale` 转成逻辑像素。
- **先按"对边固定"夹尺寸，再由尺寸反推位置**。拖左边时宽度和 x 一起变，
  顺序反了会出现"宽度被夹小、左边还在往外跑"的错位。
- **下限取组件声明的 `MinBounds`**（`managerState` 里已乘过 DPI 转成物理像素），
  但**跟当前尺寸取小**：存盘值本来就小于声明下限时（作者改过 manifest 或手改过
  `layout.json`），一夹就等于替用户把窗口放大一截。宁可先拖不动，也不要替用户改尺寸。

### 28.4 桥接

```json
// 改尺寸：x/y 物理像素，w/h 逻辑像素（和 layout.json 一个口径）
{ "instanceId": "note-1", "x": 2205, "y": 201, "w": 320, "h": 240, "commit": true }

// 内容缩放
{ "instanceId": "note-1", "zoom": 1.5, "commit": true }
```

- `commit = false` 走 `SetBoundsQuiet`（压掉 `WM_WINDOWPOSCHANGING`）：
  带这条消息的 `SetWindowPos` 是同步的，站在引擎回调里发会重入引擎；
  而排到 UI 队列又会被输入消息饿死（见 §23.4）。
  `WM_SIZE` 不受影响，页面照常重排。
- `commit = true` 排到 UI 队列，用非静默版本补一次分层合成再落盘。
- 缩放**不需要**静默版：它不发窗口位置消息。滑杆拖动中每格发一次
  （前端 40ms 防抖），松手才 `commit`。

### 28.5 缩放取值的归一

`Instance.Zoom` 的 0 表示"没设置"（老数据没有这个字段），
`EffectiveZoom()` 负责三件事：0/负数/NaN → 1，正数越界 → 夹到 `[0.5, 3.0]`。

放在领域层而不是界面上，是因为**手改 `layout.json` 是明摆着的用法**，
界面上的滑块管不到那种输入。`SetZoom` 也走同一个函数，调用方给什么
都不会把数据写坏。

### 28.6 验证

诊断探针（`STICKYNOTE_DIAG=1`）里的读数和它们各自回答的问题：

| 探针 | 读数 | 证明了什么 |
|---|---|---|
| 布局板手柄(各组) | `桌面层 块1 柄8 可拖 \| 置顶 块1 柄8 可拖 \| 浮层 块1 柄8 可拖` | 三个组都有手柄，**含两个穿透组** |
| 布局板手柄(边值) | `标题=浮层 边=nw/n/ne/e/se/s/sw/w 光标=nwse-resize` | 八个方向都在，光标对 |
| 尺寸档位=固定 | `板=浮层 块1 柄0 提示=说固定` | 该挡的时候挡住了，且说明了原因 |
| 尺寸档位已还原 | `块1 柄8` | 改回「可调」后手柄回来 |
| 实例抽屉(缩放) | `滑杆=range min=50 max=300 step=5 复位=有` | 控件在，范围对 |
| 页面尺寸(缩放前/后) | `260x200 dpr=1.25` → `174x134 dpr=1.875` | 页面真的重排了 |
| 改尺寸(桥接) | 请求 `320×240` 逻辑 → 窗口 `400×300` 物理 | 逻辑/物理换算通 |
| 改尺寸(界面) | `方块=400x300 方块样式=127px/96px` | 板子按缩放比重画 |
| 页面尺寸(改尺寸后) | `client=214 dpr=1.875` | 改尺寸没有重置缩放 |

**真正的鼠标拖拽不在这里测**：合成 `MouseEvent` 不会进到拖动逻辑，
拿它测等于自欺。手柄在不在、边值齐不齐、桥接解得开、窗口跟不跟得上，
这些是确定性的；"跟不跟手"交给人在真机上看。

## 29. 改尺寸的节流：先量清楚贵在哪，再决定省什么

用户报了两件事：拖便签窗口边框时内容"一顿一顿地变"，从布局板拖方块边框时
"方块跟手、组件窗口滞后"。两条链的触发源不同，但汇合在同一个地方：

```
WM_SIZE → Host.Resize() → put_Bounds() → DComp Commit()
```

差别只在谁发的 `SetWindowPos`：系统模态循环（拖窗口边框），还是桥接（拖方块）。

### 29.1 量法

新增 `STICKYNOTE_RESIZE_BENCH=1`：不打开管理界面、不动任何界面状态，
只对 `note-1` 跑几段确定的改尺寸，把读数打进日志。
和 `STICKYNOTE_DIAG` 分开是有意的——整串界面探针会开抽屉、点方块、改档位，
跑完之后窗口已经不是用户摆的样子，量出来的成本不干净。

| 段 | 做了什么 | 回答什么 |
|---|---|---|
| 只挪位置 | 60 帧 `SetPositionQuiet` | `SetWindowPos` 本身多贵 |
| 吞吐 | 60 帧 `SetBoundsQuiet` 背靠背 | 每帧成本 + 节流比 |
| 跟手 | 60 帧按 16ms 走，每 10 帧读页面 `clientWidth` | 引擎有没有跟上窗口 |
| 高频 | 250 帧按 8ms 走 | 上游比屏幕快时节流省多少 |
| 页面出帧 | 页面里数 `requestAnimationFrame` | 渲染端能画多快 |
| 拖边框链路 | 页面 `postMessage` 驱动真实桥接 60 帧 | 真入口的成本、投递匀不匀、内容延迟 |

### 29.2 读数（本机 165Hz/240Hz 屏，scale=1.25）

```
只挪位置：60 帧，平均 887µs，最慢 1.86ms，WM_SIZE=0 次
改尺寸：  60 帧背靠背，WM_SIZE=1 次（被节流合成 1 次）
页面出帧：145 帧 / 604ms = 240fps
拖边框落定：60 帧，宿主平均 1.22ms，最慢 3.39ms
拖边框投递：间隔 最小 13.4ms 平均 16.0ms 最大 18.9ms，超 25ms 的 0 次
内容延迟：  宿主 60 帧，页面 resize 60 次，延迟 中位 5ms 最慢 16ms 最快 3ms
```

四条结论：

1. **成本几乎全在 `SetWindowPos` 上，不在 `put_Bounds`**。挪一下 0.89ms，
   改尺寸 1.2~1.4ms，而 `put_Bounds + Commit` 只有约 0.16ms。
   所以要省的是"发多少次 SetWindowPos"，不是"告诉引擎多少次"。
2. **引擎跟得上**：60 次尺寸变更对应 60 次页面 resize，中位延迟 5ms。
   内容落后窗口的量级是毫秒，不是几十毫秒。
3. **投递是匀的**：16.0ms 平均、最大 18.9ms，没有成串到达。
   所以"滞后"不是消息被攒批。
4. **页面能画 240fps**，渲染端不是瓶颈。

### 29.3 于是改了什么

| 改动 | 位置 | 依据 |
|---|---|---|
| 几何没变就不发 `SetWindowPos` | `SetBoundsQuiet` | 拖动时相当比例的帧换算回来是同一个矩形 |
| `SetBoundsQuiet` 一帧最多发一次 | 同上 | 1.2ms/次 × 125~250Hz 会吃掉 15~30% UI 线程 |
| `put_Bounds` 一帧最多同步一次 | `syncContent` | 引擎最快也只能跟屏幕一样快，多出来的同步只会让帧间隔不齐 |
| 几何定了强制同步 | `SetBounds` / `WM_EXITSIZEMOVE` / `WM_DPICHANGED` | 中间帧被跳过的尺寸必须补上，否则画面停在旧尺寸 |
| 被跳过的帧排一次补帧 | `flushPendingResize` | 拖动结束在 `WM_SIZE` 之外也要收口 |

一帧的时长读显示器刷新率（`GetDeviceCaps(VREFRESH)`），夹在 6~20ms；
读不到按 60Hz。**不能写死 16ms**——本机是 165Hz，写死就等于把内容压到 60Hz。

### 29.4 没量出来的部分

节流之后，宿主这一侧每帧 1.2ms、内容 5ms 内重排、投递均匀，
**链路上没有任何一处能解释"肉眼可见的卡顿"**。剩下的可能在合成/呈现侧
（DComp 提交与 DWM 出帧的配合），那一层拿不到不靠截图就能读的读数。
所以这一节只记"确定的部分"，"跟不跟手"仍然交给人看。
