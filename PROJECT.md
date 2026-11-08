# StickyNote · 项目说明

一个 Windows 桌面组件宿主：宿主只负责窗口、桥接、存储、权限与调度，
**所有可见的东西都是组件**——包括便签本身。

便签和第三方插件走完全相同的加载、安装、卸载路径，区别只有「来源」（随包预置）
与「默认启用」两点。

渲染层是 WebView2，宿主是 Go，产物是 exe + `WebView2Loader.dll` + 一个 `bundles\` 文件夹。
唯一的系统依赖是 Edge WebView2 Runtime（Win11 预装，Win10 由 Edge 自动带上）。

> 想写插件（样式包 / 组件）请看 [`PLUGIN.md`](PLUGIN.md)。

---

## 目录结构

```
cmd/stickynote/                 组合根：装配依赖 + 程序入口
├── main.go                     载入目录、迁移旧数据、启动内核
├── app.manifest                PerMonitorV2 DPI 感知 + 通用控件 v6
├── app.rc / app.ico / rsrc.syso 图标与清单资源（rsrc.syso 由 app.rc 编译）

internal/
├── domain/component/           领域层：样式、组件定义、实例、三层解析
│   ├── component.go            Definition / Instance / Package / 校验
│   ├── group.go                组（样式的实际载体）
│   ├── variable.go             变量声明与取值
│   └── repository.go           仓储端口（Layout / Registry / Storage / UI / Package）
├── application/componentapp/   应用层：目录装配、实例生命周期、安装卸载、存储转发
│   ├── service.go
│   └── view.go                 扁平 DTO，表现层只认它
├── infrastructure/
│   ├── persistence/            JSON 仓储：groups.json / layout.json / ui.json / vars/ / storage/
│   ├── plugin/                 插件包解析（loader）与安装卸载（install）
│   ├── webview2/               WebView2 Composition 适配层（C++ 包装 + cgo 绑定）
│   └── screen/                 屏幕/窗口像素抓取，纯诊断用（与引擎无关）
└── interfaces/webview/         表现层：Win32 宿主 + 托盘
    ├── win32.go                Win32 裸绑定与常量
    ├── window.go               无边框窗口、消息循环、DPI、拖动缩放
    ├── host.go                 单个实例的 WebView2 控制器
    ├── bridge.go               前后端协议
    ├── manager.go              主界面（内核 UI）与 manager.* 分派
    ├── picker.go               选文件夹 / 打开资源管理器
    ├── tray.go                 托盘图标与动态菜单
    ├── icon.go                 按 DPI 取图标资源
    └── app.go                  内核：多实例管理、消息分派

builtin/                        编进 exe 的内置资源（三种预设样式 + 主界面）
├── styles/topmost/             置顶窗口：便签纸
├── styles/desktop/             桌面组件：图标之上、应用窗口之下
├── styles/toast/               悬浮提示栏：置顶 + 点击穿透
└── manager/                    主界面（内核 UI，不参与插件扫描）

bundles/                        随包分发的预置插件（不打进 exe）
├── note/                       便签（L2，绑 builtin.style.topmost）
├── clock/                      数字时钟（L2，绑 builtin.style.desktop）
└── danmaku/                    测试弹幕（L2，绑 builtin.style.toast）
```

**三个预设样式在 `builtin\` 里，三个示例组件在 `bundles\` 里**。
前者是数据（窗口语义 + 视觉默认值），后者是程序；删掉任何一个组件都不需要动内核。

---

## 三种窗口形态

内核只提供三条窗口路径，各对应一个内置样式。**`layer` 就是这三条路径的名字**，
样式只能在参数空间里取值，不能发明新层级：

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

穿透要**设到 WebView2 的子窗口上**：真正接鼠标的是 `Chrome_RenderWidgetHostHWND`，
它不继承宿主的扩展样式；而且渲染子窗口是导航时才建的，所以每次导航完成都要补一遍
（`Host.OnReady` → `applyClickThrough`）。验证别用 `WindowFromPoint`（它不看
`WS_EX_TRANSPARENT`），要用 `ChildWindowFromPointEx` 加 `CWP_SKIPTRANSPARENT`，
DEV 日志里的「穿透探针」打的就是这个结果。

---

## 主界面

托盘左键、或右键菜单里的「管理组件…」都会打开主界面。
它有三个页签：**组件**（L2）、**样式**（L1）与**布局**。

组件页每张卡片能做：

- 开关启用（停用只关窗口，实例记录与几何都留着，重新启用原样回来）
- 新建 / 显示 / 隐藏 / 关闭实例（关闭会连带删除该实例的私有数据）
- 打开插件目录、卸载（只有 `builtin.` 前缀的资源不可卸载；便签是可卸载的）

样式页展示窗口语义与「被哪些组件引用」；被引用的样式不能卸载。

布局页把桌面按比例画成一块板子，每个实例是一个方块：**拖方块就是拖窗口**，
双击方块居中。虚线方块表示"记录在、窗口不在"（隐藏或组件停用）。
坐标一律用物理像素，前端只做等比换算，所以 125% 缩放下方块和真实窗口严丝合缝。
拖动时每帧只发一条 `manager.moveInstance`（`commit:false`），松手才发 `commit:true` 落盘。

页签切换靠 `hidden` 属性，而 `.pane{display:flex}` 会盖掉 UA 样式表里的
`[hidden]{display:none}`（同特异性、作者样式优先）——必须显式写一条
`.pane[hidden]{display:none}`，否则三个面板同时铺开、页签看着像"点了没反应"。
启动自检里的「可见页签 N 个」就是这条规则的探针，正常恒为 1。

**主界面不是组件**。组件由它管理；如果它本身是一个组件，那么「停用管理界面」
之后就再也没有入口把它点回来了。这条边界和内核通知浮层一样——
内核自己的能力不能交给被自己管理的东西去实现。所以它不走 L1 样式、
不在 `components/` 里、`manifest.json` 也没有。

### 只有主界面能调用的桥接方法（`onManagerMessage`）

| method | 作用 |
|---|---|
| `manager.state` | 整个目录 + 全部实例 + 运行状态，一次给全 |
| `manager.setEnabled` | 启用/停用组件（停用只关窗口，保留实例）※ |
| `manager.createInstance` / `manager.removeInstance` | 新建 / 删除实例 ※ |
| `manager.setVisible` | 显示 / 隐藏实例窗口 |
| `manager.moveInstance` | 把实例挪到屏幕坐标 `{x,y}`（物理像素），`commit` 为真时落盘 ※ |
| `manager.install` | 弹文件夹选择框，装完重扫目录 |
| `manager.uninstall` | 卸载插件（有实例或被引用时拒绝） |
| `manager.reveal` / `manager.openDataDir` | 在资源管理器里打开 |
| `manager.reload` | 重新扫描插件目录 |
| `manager.close` / `window.minimize` | 收窗口 |

※ 这几个会建/拆/移动窗口，**不能同步执行**：先回 `{accepted:true}`，
动作排到 UI 队列，真正的新状态用 `manager:state` 事件推回来。
前端因此不能 `apply(await call(...))`，只能等事件。

入口按实例 id 分两条路：普通实例走 `onComponentMessage`，保留 id `kernel.manager`
走 `onManagerMessage`。组件就算伪造 `manager.*` 方法也进不到管理分支。
协议骨架与组件可调用的方法见 [`PLUGIN.md`](PLUGIN.md#桥接协议)。

---

## 预置包（`bundles\`）

随包分发的插件放在 exe 旁边的 `bundles\` 里，启动时被复制进 `data\plugins\`。
复制完之后它和用户自己装的插件**没有任何区别**：同样的目录、同样的加载器、
同样的卸载规则。区别只在两处：

- **首次出现时默认启用**（`SetBootstrapEnabled`），并**各开一个实例**。
  普通第三方插件默认停用，但开箱只有一个空桌面说不过去。只对注册表里没有记录的
  id 生效——用户后来关掉它，下次启动仍然是关的；已经有实例的也不再补。
- **只发放一次**。`data\bundles.json` 记着"发过哪些 id"：

  ```json
  { "issued": ["clock", "danmaku", "note"] }
  ```

  判断依据必须是这份记录，不能是「插件目录里有没有」。否则用户卸载便签之后，
  下次启动它又长回来了——用户删掉的东西不该自己回来。

安装用的是和手动安装完全相同的 `Installer.Install`，所以四类检测一条不落。
**同一批预置包里先装 L1 再装 L2**：目录名排序不可靠，一轮装下来组件可能因为
「缺少样式」被拒，而它依赖的样式包就在同一个 `bundles\` 里。

单个预置包坏了只记日志，不影响其他包，也不影响程序启动。

---

## 构建

```bash
go build -ldflags "-H windowsgui -s -w" -o bin/StickyNote.exe ./cmd/stickynote
```

或直接 `build.bat`。

改了 `app.manifest` 或 `app.rc` 后必须重新生成资源对象，否则 DPI 感知与图标不生效：

```bash
cd cmd/stickynote
windres -i app.rc -o rsrc.syso -O coff --target=pe-x86-64
```

前端没有构建步骤：`builtin\` 下的样式与主界面用 `go:embed` 编进 exe，
启动时按内容哈希解包到 `<安装目录>\data\cache\builtin\<hash>\`；
`bundles\` 下的预置插件不打进 exe，是运行时从磁盘复制的。

调试：`STICKYNOTE_DEV=1` 启动可开 DevTools、自动打开主界面，并记录桥接调用与窗口几何。
日志在 `<安装目录>\data\app.log`。

测试：`go test ./...`（domain / persistence / plugin / componentapp / 结构体布局）。

---

## 数据布局（便携模式）

**所有数据都在 exe 旁边的 `data\` 里**，用户只需要管一个文件夹：
把整个安装目录拷走就是迁移，用户配置目录里不留任何东西。

```
<安装目录>\
├── StickyNote.exe
├── bundles\                   ← 预置插件的来源，可以整个删掉（只是装不了预置插件）
│   ├── note\                  便签
│   ├── clock\                 数字时钟
│   └── danmaku\               测试弹幕
└── data\
    ├── groups.json            组（样式载体）的定义
    ├── layout.json            实例清单与几何
    ├── ui.json                主界面窗口几何
    ├── bundles.json           已发放过的预置插件 id（只增不减）
    ├── vars\<组件id>\<实例id>.json      实例持有的变量值
    ├── storage\<组件id>\<实例id>.json   组件私有数据
    ├── plugins\
    │   ├── styles\<id>\       第三方样式包（style.json）
    │   └── components\<id>\   已装组件（manifest.json + 入口页），含 note\ clock\ danmaku\
    ├── cache\                 ← 整块可随时删，删了自动重建
    │   ├── builtin\<hash>\    内置样式与主界面（随 exe 解包）
    │   └── webview\          WebView2 用户数据（浏览器缓存）
    └── app.log                运行日志
```

`cache\` 和其余部分刻意分开命名：它是唯一"删了不丢东西"的目录。
内置资源按内容哈希解包，换版本自动换目录、旧的自动清掉。

`bundles\` 只在**首次启动**被读一次。之后插件就住在 `data\plugins\` 里，
升级程序时把 `bundles\clock\` 换成新版也不会自动覆盖已装的旧版——
插件是用户的，不该被静默替换。要更新就手动卸载再安装。

**只有 exe 所在目录不可写时**（装在 `Program Files` 又没有管理员权限）才退回
`%AppData%\StickyNote`，并在日志里写明原因——那种情况下没得选，总不能起不来。

从旧版本升级上来时，启动会把 `%AppData%\StickyNote` 里的用户数据搬进 `data\`
（只搬配置、布局、私有数据、插件，**不搬缓存**），然后把旧目录改名为
`StickyNote.migrated` 留着做备份，不删除。

实例几何的口径是分开的：**X/Y 存物理像素**（屏幕坐标本来就是物理的），
**宽高存逻辑像素**（96 DPI 基准），换到别的缩放比例下尺寸才对得上。
`ui.json` 用同一套口径。

卸载插件只删 `plugins\` 下的文件，**不动 `storage\`**：重装回来正文还在。
要彻底清数据是另一件事，不该藏在「卸载」按钮里顺手做掉。

首次启动会把旧版单便签的 `note.json` 迁进便签插件的实例存储，
原文件改名为 `note.json.migrated` 保留，**不删除用户数据**。

便签从内置组件变成插件时改过一次 id（`builtin.note` → `note`）：新 id 不再占用
`builtin.` 保留前缀，否则第三方永远不能顶替它。改名要动**四处**——`layout.json` 的
`componentId`、实例 id、`components.json` 的键、`storage\` 下的目录与文件名，
漏一处就会变成「组件还在、数据找不到」，比直接报错更难查。迁移在 `Boot` 之前完成，
因为 `Boot` 读到指向已消失组件的实例会直接丢弃。

---

## 关键实现决策

| 决策 | 理由 |
|---|---|
| 自写 Win32 消息循环，不用窗口框架 | 无边框、多窗口、每窗口一个 WebView，框架反而是束缚 |
| 只做一种运行时（WebView2） | 两套运行时会分裂成两套 API、两套调试、两套权限 |
| 样式包不跑代码 | 样式是数据，装十个样式包进程数不变 |
| 一个隐藏的内核窗口挂托盘 | 关掉一个组件不该带走整个程序；托盘是唯一入口 |
| 每个组件一个独立虚拟主机 | `https://<组件id>.stickynote`，天然隔离，读不到别人的资源 |
| 主界面不进插件目录 | 它管理组件；如果它自己是组件，停用它就再也点不回来了 |
| 便签不进内核，走 `bundles\` | 想换掉/删掉/顶替它都不必动内核；代价是分发从单 exe 变成 exe + 文件夹 |
| 卸载不删组件数据 | 卸载是可逆的，删数据应该是单独一件事 |

## 必守的坑

- **WebView2 控制器初始 bounds 是 0×0**，`Embed` 后必须主动 `Resize()` 一次，
  否则首屏空白、要用户拉伸边框才渲染。
- **拖动/缩放不能靠宿主 `WM_NCHITTEST`**：WebView2 子窗口铺满客户区，命中测试落在子窗口上。
  正解是前端 mousedown 判断落点 → 宿主 `ReleaseCapture()` + 伪造 `WM_NCLBUTTONDOWN`，
  交给 DefWindowProc 的模态循环。必须 Post 到 UI 线程再进，不能在 COM 回调里进嵌套消息循环。
- **置顶必须在 `Show()` 之后设**，隐藏窗口上的 Z 序变更会被系统丢弃。
- **启动阶段忽略 `WM_DPICHANGED` 的建议矩形**（`settled` 标记），否则存盘的尺寸会被改掉。
- **`NOTIFYICONDATAW` 在 x64 下是 976 字节**，对齐填充必须显式写，`cbSize` 错一位就静默失败。
  `tray_test.go` 把字段偏移钉死了。
- **前端 `handleMessage` 要同时兼容对象与字符串**：宿主传的是对象字面量，不是 JSON 字符串。
- 本机 Go 代理 `goproxy.io` 不通，用 `GOPROXY=https://goproxy.cn,direct`。
- **不能在桥接回调里建/拆窗口**。`Host.Start` 内部的 `Embed` 会跑一段嵌套
  `GetMessage` 循环等 WebView2 控制器就绪，而这个回调本身就是消息循环派发进来的；
  再套一层嵌套循环会撞上运行时的内部锁，进程**静默卡死**——没有崩溃、没有日志，
  任务管理器里还活着。症状就是「在管理界面关掉再打开组件，整个程序不动了」。
  所以 `manager.setEnabled` / `createInstance` / `removeInstance` 都是先回包、
  再 `Window.Post` 到 UI 队列执行。销毁窗口同理（`DestroyWindow` 会同步走到承载
  控制器的子窗口），`stopInstances` 也必须在队列里跑。
- **数据目录的判定不能只看 `MkdirAll`**：目录可能建得出来但文件写不进去
  （只读介质、ACL 拒绝、受控文件夹访问）。探针要真的试写一个文件再删掉。

---

## 当前进度

已完成：

- 组件领域模型（组、定义、实例、变量）、三层样式解析、JSON 仓储
- 插件包加载与结构性校验（坏包只记日志，不影响其他包）
- 内核：多实例窗口管理、按样式建窗、每实例独立桥接、托盘动态菜单
- 便签改造成 L2 组件，绑定 `builtin.style.topmost`
- 旧 `note.json` 自动迁移
- **主界面**：组件/样式两页、启用开关、实例增删显隐、安装/卸载、目录重扫
- **便携模式**：数据跟着 exe 走（`data\`），从旧版 AppData 自动搬迁
- **便签从内核分离**：改成 `bundles\note\` 预置插件，id 从 `builtin.note` 改为 `note`，
  旧数据四处一并迁移；`bundles.json` 记录发放状态，卸载后不会自己长回来
- **三种预设样式各配一个示例组件**：便签（`topmost` 置顶窗口）、数字时钟
  （`desktop` 桌面层）、测试弹幕（`toast` 悬浮提示栏），三种窗口形态全部落地
- **布局页**：按比例画桌面，拖方块即改窗口位置（双击居中），松手才落盘
- **变量系统**：作者声明 + 实例持有值，主界面按声明类型渲染编辑控件
- **内容缩放**：`Instance.Zoom` 与窗口尺寸正交，走 `RasterizationScale` 真重排

尚未实现：

- 内核通知浮层（`toast` 形态已经有了，但通知服务本身还没做）
- 插件 zip 导入（目前只支持「目录即插件」）
- 样式级别的启用/停用（样式只能整体卸载，且被引用时不许卸）
- 休眠与控制器回收
- `devicePixelRatio` 与系统缩放不一致导致 UI 偏大约 14% 的老问题

---

## 许可证

本项目采用 **MIT License**，全文见 [`LICENSE`](LICENSE)。

二进制里还包含别人的代码，各自的许可证与版权声明集中在
[`THIRD-PARTY-NOTICES.md`](THIRD-PARTY-NOTICES.md)：Go 运行时与 `golang.org/x/sys`
（BSD-3-Clause，The Go Authors）、Microsoft Edge WebView2 SDK
（BSD-3-Clause，Microsoft Corporation，原文在 `third_party/webview2/LICENSE`）。

**发布 exe 时要把 `LICENSE`、`THIRD-PARTY-NOTICES.md`、`third_party/webview2/LICENSE`
三个文件一起带上**——这是上述三个许可证共同的再分发义务，不要求开源，但要求附声明。
`build.bat` 会自动把它们拷进 `bin\`，不用手动处理。
