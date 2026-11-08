# 实现级陷阱清单
> 由 `MEMORY.md` 引用：**改引擎/窗口/光标/样式前必读**。这些都是踩过的坑，每条都曾让功能静默失效。

## 引擎（WebView2 Composition）
- 必须 `CreateCoreWebView2CompositionController`（在 `ICoreWebView2Environment3` 上）；用普通
  `CreateCoreWebView2Controller` 建出来的对象 QI 不到 CompositionController（`E_NOINTERFACE`）。
- DComp 要周期 `Commit()`：不提交则内容完全不出现，**且无任何错误码**。
- 先 `ShouldDetectMonitorScaleChanges(FALSE)` 再 `RasterizationScale`；反了会被重置回引擎自算的值。
- `put_DefaultBackgroundColor` 的 alpha 只能 0 或 255（不支持整窗半透明磨砂）。
- Composition 模式不接管输入：鼠标/键盘要宿主 `SendMouseInput` 自己转发。

## 窗口与穿透
- 穿透 = `WS_EX_LAYERED|WS_EX_TRANSPARENT` + `SetLayeredWindowAttributes(…,255,LWA_ALPHA)`；
  `NOREDIRECTIONBITMAP` 与 `LAYERED` **不互斥**（与常见说法相反，实测可共存且合成正常）。
- 桌面层 = 顶层窗口 + Z 序锚在 Progman 正上方。`SetWindowPos` 的 `hWndInsertAfter` 是"排在它**之下**"；
  Z 序会被多处重置 → `ReanchorDesktop()` 幂等多次调。
- 一个组件是三个窗口，吃鼠标的是 `Chrome_WidgetWin_1`，只能跨进程加 `WS_EX_TRANSPARENT`。
- **验证穿透的现成 API 全会骗人**（`WindowFromPoint` 不跳过 `WS_EX_TRANSPARENT`），唯一真凭据是鼠标
  消息有没有进窗口过程。
- `layer=topmost` 只是配置，**真凭据是 `WS_EX_TOPMOST` 位**；spawn 末尾那行 DEV 日志读得太早。

## 视觉
- **基座样式必须 `insertBefore(s, head.firstChild)`**：`appendChild` 会让基座排在组件 `<link>` 后面，
  同特异度时基座赢 → `html,body{background:transparent}` 盖掉组件底色。**底色画在内层元素**。
- Win11 投影与圆角同一个开关 `DWMWA_WINDOW_CORNER_PREFERENCE(33)`；**磨砂做不到**，别写。
- **`[hidden]` 必须显式写 CSS**（作者样式会盖掉 UA 的 `[hidden]`）；flex 项要 `min-width:0` 才出省略号。
- 显隐用 class（`.is-bad` 之类），不要依赖 `[hidden]` 的动态切换。

## 光标与拖拽
- 光标：`WM_SETCURSOR` 里用引擎给的 HCURSOR 自己 `SetCursor`；缩放边框交给 `WM_NCHITTEST`（系统自己
  设光标、自己跑缩放循环）。
- **拖动不能靠前端算位移**：前端判落点 → 宿主 `ReleaseCapture()` + 伪造 `WM_NCLBUTTONDOWN`（HTCAPTION
  等）→ `DefWindowProc` 模态循环（Aero Snap 原生）。必须 Post 到 UI 线程。
- 拖动中挪窗口用 `SetPositionQuiet`（`SWP_NOSENDCHANGING`）：同步 `SetWindowPos` 会重入引擎；排 UI 队列
  又会饿死（`WM_MOUSEMOVE` 优先级高于 `PostMessage` 的 `WM_APP`，拖得越连续越排不上）。

## 已废弃：Sciter（仅存档，别复活）
插件作者无法复用 npm/React/Vue，生态起不来，用户否决。Sciter 时代的 CSS/JS 结论（不支持 `dataset`、
`matchMedia`、`ResizeObserver`、`display:grid`、`position:fixed`、Pointer Events 等一大串）**一律不适用**，
别写 polyfill。WebView2 是 Chromium，按标准写即可。
