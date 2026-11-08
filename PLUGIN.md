# StickyNote · 插件开发说明

插件分两级，区别不是「权限多少」，而是**「是数据还是程序」**：

| 层级 | 名称 | 形态 | 占进程 | 需要权限 |
|---|---|---|---|---|
| **L1** | 样式包 | 纯 JSON（`style.json`） | 否 | 否 |
| **L2** | 组件 | HTML / CSS / JS（`manifest.json` + 入口页） | 每实例一个 WebView2 控件（共享一个浏览器进程） | 是 |

一个包只能是 L1 或 L2，**两个清单同时存在会被拒绝安装**。

---

## L1 样式包

样式包决定**窗口外壳**：窗口在哪一层、能不能被点、能不能拖边框、初始落位、默认视觉值。
它不跑任何代码，装十个样式包进程数不变。

```json
{
  "id": "builtin.style.topmost",
  "name": "置顶便签纸",
  "window": {
    "layer": "topmost",
    "activation": "activate",
    "resizable": true,
    "sizeMode": "user",
    "dock": "free",
    "titlebar": true
  },
  "visual": { "padding": 18, "radius": 12, "background": "#fffbf0" },
  "cssVars": { "--sn-ink": "#2e2a22" }
}
```

### 窗口词表

`window` 里的取值只能是这些，样式只能组合，不能发明：

| 字段 | 取值 |
|---|---|
| `layer` | `topmost` \| `normal` \| `desktop` \| `toast`（`toast` 只允许内置样式） |
| `activation` | `activate` \| `noActivate` |
| `resizable` | `true` \| `false`（缺省 false） |
| `sizeMode` | `fixed` \| `user` \| `fitContent` |
| `dock` | `free` \| `left` \| `right` \| `top` \| `bottom` |
| `titlebar` | `true` \| `false`（缺省 false） |

### 三种预设样式（只能引用，不能自建）

内核只提供三条窗口路径，各对应一个内置样式。**`layer` 就是这三条路径的名字**：

| 样式 id | 形态 | 鼠标 | 被应用窗口覆盖 | Win+D | 示例组件 |
|---|---|---|---|---|---|
| `builtin.style.desktop` | 桌面子窗口（图标之上） | **穿透** | **是** | 跟随桌面隐藏 | 数字时钟 |
| `builtin.style.topmost` | `HWND_TOPMOST` | 可交互 | 否 | 不跟随 | 便签 |
| `builtin.style.toast` | 置顶 + 点击穿透 | **穿透** | 否（最上层） | 不跟随 | 测试弹幕 |

`desktop` 与 `toast` 都是穿透的，但理由不同：`toast` 是"没有点击事件的提示栏"，
`desktop` 是**贴在桌面上的装饰**——鼠标落在表盘上应该直接落到下面的桌面图标上，
否则它会把整个图标区域挡成一块死区。两者的位置都由管理界面的「布局」页调，
不靠窗口自己接拖动。

`toast` 是唯一带穿透的形态，所以**只允许内置样式声明**（`Style.Validate` 会拒绝
第三方 id）。词表里因此没有 `clickThrough` 字段——能力只在层级这一层出现，
插件无法自己造一个"看不见又点不掉"的窗口。

`dock` 只在**第一次出现**时决定落位（`top` 就是贴屏幕顶边），之后以存盘的几何为准。
v1 不做拖拽时的贴边吸附。对 `toast` 来说这条尤其重要：它收不到鼠标事件，
用户挪不动它，落位错了就再也回不来。

---

## L2 组件

组件是一段网页，被宿主装进一个无边框窗口。它拿到实例信息、样式变量、自己的私有存储，
通过桥接调宿主能力。

```json
{
  "id": "note",
  "name": "便签",
  "style": "builtin.style.topmost",
  "entry": "index.html",
  "permissions": ["storage", "window.drag", "window.resize"],
  "defaultBounds": { "width": 420, "height": 360 },
  "minBounds": { "width": 260, "height": 200 },
  "visual": {},
  "cssVars": {}
}
```

`manifest.json` 里**不允许**出现 `window` / `type` / `customLayer` / `customFlags`：
窗口语义只能由样式声明。出现了直接判为不兼容，不会被静默忽略。

- `id` 不能占用 `builtin.` 前缀——那是内置资源的保留前缀，内置资源每次启动重新解包，
  装了也会被还原。
- `style` 必须指向一个**已安装的** L1 样式包或内置样式，否则安装被拒。
- 前端是原生 HTML/CSS/**ES Module**，没有构建步骤。Chromium 支持什么就能用什么。

### 组（样式载体）

窗口语义挂在**组**上，不挂在插件上：`data/groups.json` 存组定义，`Instance.GroupID`
记录每个实例归属哪个组。同一个样式可以在主界面里开多个组，各自调参数；
组件只是"被放进某个组里跑"。

---

## 样式解析：三层合并

```
builtin 兜底默认
   ↓ 覆盖
L1 样式包   window / visual / cssVars
   ↓ 覆盖
L2 组件     visual / cssVars      ← 只能覆盖视觉，不能覆盖语义
   ↓
ResolvedStyle
```

**语义字段锁在 L1**（`layer` / `activation` / `resizable` / `sizeMode` / `dock` / `titlebar`）。
`Definition.Visual` 里根本没有这些字段，所以「分级」是类型层面就成立的，
不靠运行时检查。如果 L2 能改 `layer`，样式就形同虚设。

**视觉字段 L2 可覆盖**（`padding` / `radius` / `background` / `shadow` / `cssVars`）。
合并顺序是**「组永远赢」**——组是用户在界面上亲手调的那一层，不该被插件默认值盖掉。

---

## L1 与 L2 的契约：`--sn-*` 变量

样式管窗口外壳，不管组件内部。合并结果摊平成 CSS 变量注入页面：

| 变量 | 来源 |
|---|---|
| `--sn-padding` / `--sn-radius` / `--sn-background` / `--sn-shadow` | `visual` |
| 其余（如 `--sn-ink`） | 样式与组件声明的 `cssVars` |

外加三个开关，写在 `<html>` 的 `data-*` 上：`data-sn-titlebar`、`data-sn-resizable`、
`data-sn-layer`。

**组件必须用 `var(--sn-xxx)` 消费它们**，否则样式对它无效。硬编码颜色等于把组件钉死在
一种配色上。

这些变量在文档创建时注入（`AddScriptToExecuteOnDocumentCreated`），不是等页面就绪后
`Eval`——否则首屏会先按兜底配色画一遍再跳色，肉眼可见地闪。

---

## 桥接协议

极简 JSON-RPC over web message：`{id, method, params}` → `{from:"host", id, result|error}`。
`from:"host"` 用来区分真正的回复和底层库的消息回显。

```js
const call = (method, params) =>
  new Promise((resolve, reject) => {
    const id = ++seq;
    pending[id] = { resolve, reject };
    window.chrome.webview.postMessage({ id, method, params });  // 传对象，不要 stringify
  });
```

**`componentId` 由宿主按消息来源控制器注入，前端不能自己声明**——否则组件能冒充别的组件。

### 组件能调用的（`onComponentMessage`）

| method | 作用 |
|---|---|
| `component.ready` | 首屏就绪，一次性返回实例信息、样式、几何、置顶状态、全部私有存储 |
| `component.close` | 关闭本实例 |
| `component.setPinned` | 置顶开关（仅 `topmost` 层生效） |
| `component.setBounds` | 改自己的位置尺寸 |
| `window.beginDrag` | 进入系统移动循环 |
| `window.beginResize` | 进入系统缩放循环，`{edge}` |
| `storage.get` / `storage.set` / `storage.remove` | 本实例私有 KV |
| `host.log` | 写日志，由内核统一落盘 |
| `host.notify` | 请求内核弹提示（v1 只落日志，浮层未实现） |

宿主 → 前端事件：`host:pin`（`{pinned}`）。

新增能力 = 在分派表加一个 case，协议本身不动。

### 变量与存储是两件事

- **变量**（`data/vars/<组件>/<实例>.json`）：作者声明，主界面渲染成编辑控件，
  用户在这里调颜色、文字、开关。**默认值不落盘**（用 `SameJSON` 判断），
  所以改了插件默认值能立刻生效。变量不走 `Storage`。
- **存储**（`storage.*`）：组件自己的运行时数据，用户看不见。

**读变量必须在补实例之后**——实例还没建就读，拿到的是空。

---

## 拖动与缩放

**拖动不能靠前端算位移**。正解：

1. 前端 `mousedown` 判断落点（标题栏？边框？）
2. 调 `window.beginDrag` / `window.beginResize {edge}`
3. 宿主 `ReleaseCapture()` + 伪造 `WM_NCLBUTTONDOWN`（`HTCAPTION` / `HTLEFT` …）
4. 交给 `DefWindowProc` 的模态循环——Aero Snap、双击最大化都是系统原生行为

前端只报"按在哪儿"，位移由系统算。自己算位移会丢掉 Snap、丢掉多屏边界处理。

改自己的尺寸/位置用 `component.setBounds`；拖动过程中宿主走
`SetBoundsQuiet` / `SetPositionQuiet`（`SWP_NOSENDCHANGING`），避免重入引擎。

---

## 安装与校验

「安装插件…」选一个目录即可，目录里必须有 `style.json` 或 `manifest.json`。
安装时做四类检测，全部通过才复制进 `plugins\`：

1. 目录里两个清单都有 → 拒绝（一个包只能是 L1 或 L2）
2. id 已存在 → 拒绝（不覆盖，先卸载旧版本）
3. id 用了 `builtin.` 前缀 → 拒绝（内置资源每次启动重新解包，装了也会被还原）
4. L2 依赖的样式不在目录里 → 拒绝，并提示先装那个样式包

复制中途失败会回滚整个目录：半成品留着会占住 id，用户既装不进去也删不掉。

卸载只删 `plugins\` 下的文件，**不动 `storage\`**：重装回来正文还在。
要彻底清数据是另一件事，不该藏在「卸载」按钮里顺手做掉。
**有实例的组件不能在目录里消失**——`Reload` 会拒绝，否则实例会变成指向空气的记录。

---

## 调试

- `STICKYNOTE_DEV=1` 启动：开 DevTools、自动打开主界面、记录桥接调用与窗口几何。
- 日志在 `<安装目录>\data\app.log`；`host.log` 也落这里。
- 前端 `handleMessage` 要同时兼容对象与字符串：宿主传的是对象字面量，不是 JSON 字符串。
- **`app.js` 是 module 脚本**：模块作用域里的变量 `ExecuteScript` 取不到，诊断要么碰 DOM
  要么走宿主侧。

---

## 三个示例

| 目录 | 组件 | 绑定的样式 | 看点 |
|---|---|---|---|
| `bundles/note/` | 便签 | `builtin.style.topmost` | 可交互窗口、标题栏、拖动、私有存储 |
| `bundles/clock/` | 数字时钟 | `builtin.style.desktop` | 桌面层、点击穿透、无标题栏 |
| `bundles/danmaku/` | 测试弹幕 | `builtin.style.toast` | 置顶穿透、落位由 `dock` 决定 |

这三个就是模板：拷一个目录、改 `id` 与 `name`、换掉页面内容，就是一个新组件。
