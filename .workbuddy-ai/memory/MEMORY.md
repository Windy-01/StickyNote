# StickyNote 项目约定
> 细节见 `docs/component-framework.md`；引擎/窗口/光标/样式的实现级坑见 `memory/TRAPS.md`；
> 过程见 `memory/2026-*.md`。本文件只留铁律与索引。

## 协作（铁律）
- **手感类验证全交用户**（光标、拖拽、动画、输入法、对齐、真实点击）；我只做编译/单测/DOM 与几何断言/
  日志读数。**不伪造输入**（合成事件、`SendInput` 常静默失效）；**不靠试错代替测量**；采像素前先确认
  自己真在最上层。不替用户提交 git；不动 Windows 环境；不返回截图。

## 架构
「桌面视觉组件基础服务 + 插件生态」（对标 foobar2000），宿主 Go。边界：**「所有组件都可能要用、且必须
统一口径」的进内核**。DDD 四层单向向内；`builtin/`（`go:embed`）与 `bundles/`（不进 exe）分开。
- **组是唯一样式载体**（`data/groups.json`，不是插件）；**归属在实例上**（`Instance.GroupID`）。
- **窗口语义是正交开关**：`Layer` 三选一 + 独立 `ClickThrough`；`ClickThrough`/`desktop` 都强制
  `NoActivate`。**开关变更必须重建窗口**，只改外观走 `restyleGroup()`；**不能在 WebView2 回调里建/拆
  窗口**，`deferToUI` 是唯一出口。**合并顺序「组永远赢」**。契约 = `--sn-*` + `data-sn-*`。
- **变量 = 作者声明 + 实例持有值**：`data/vars/<组件>/<实例>.json`，一律 `json.RawMessage`。标量
  `text`/`textarea`/`select`/`number`/`bool`，集合 `list`/`group`。**不让用户写 JSON**；**默认值不落盘**
  （用 `SameJSON` 判断）；**变量不走 `Storage`**；**读变量必须在补实例之后**。

## 实例几何与内容缩放（两个正交维度）
- `Instance.Bounds`：**x/y 物理像素、w/h 逻辑像素**（layout.json 口径）；`Instance.Zoom`：内容缩放
  0.5~3.0，**不改窗口尺寸**。合成一个值会丢掉"便签拉长但字不放大"这类正当需求。
- 缩放 = `RasterizationScale(dpi × zoom)`，页面真重排。**不用 `put_ZoomFactor`**：它按 origin 持久化，
  所有组件页共用虚拟主机前缀，会漏到别的实例上。
- 布局板改尺寸**只看 `sizeMode`（「固定」才挡），不看 `resizable`**——后者是"用户能不能直接拖窗口
  边框"，穿透窗口/桌面层永远 false，拿它挡等于让那两个组没有尺寸入口。下限取
  `min(MinBounds, 当前尺寸)`，别把小于下限的存盘值一夹放大。
- 拖动中改几何一律 `SetBoundsQuiet`/`SetPositionQuiet`（`SWP_NOSENDCHANGING`）：同步 `SetWindowPos`
  会重入引擎，排 UI 队列又会被输入消息饿死。
- **成本在 `SetWindowPos`，不在 `put_Bounds`**（实测：挪 0.89ms / 改尺寸 1.2~1.4ms / `put_Bounds`
  仅 0.16ms）→ 省的是"发多少次 SetWindowPos"。节流按**显示器刷新率**（`GetDeviceCaps(VREFRESH)`，
  夹 6~20ms）——本机 165Hz，写死 16ms 等于压到 60Hz。几何定了（`SetBounds`/`WM_EXITSIZEMOVE`/
  `WM_DPICHANGED`）必须强制同步。`STICKYNOTE_RESIZE_BENCH=1` 单跑这条链，和 `DIAG` 分开。

## 主界面与插件
**主界面管组件，所以它不能是组件**（组里停用就点不回来）。资源 `builtin/manager/`，桥接 `manager.*`，
路由用保留 id `kernel.manager`；只有它占任务栏；只有 `firstRun || STICKYNOTE_DEV` 才自动打开。
- **有实例的组件不能在目录里消失**（`Reload` 拒绝）；**卸载只删 `plugins` 不动 `storage`**；第三方组件
  默认停用。桥接里改实例集合的写操作**必须自己 `SaveLayout`**。**`fail()` 必须 `log.Printf`**。
- **状态推送会重渲染抽屉**：`refreshDrawer()` 要跳过 `document.activeElement` 落在 `[data-var]` /
  `[data-rec-field]` 的那一轮，否则"每敲一个字就跳出输入框"。
- **预置包只在首次发放时安装**（跳过 `bundles.json` 已发放的 id）→ 改了 `bundles/` 要手动拷进
  `data/plugins/components/<id>/`；删 `data/plugins` 重装会**重排窗口位置**。便签 id 是 `note`，
  改名要动**四处**，漏一处不报错。

## 构建与诊断
- `go build -ldflags "-H windowsgui -s -w" -o bin/StickyNote.exe ./cmd/stickynote`。**必须带 `-s -w`**
  （否则 mingw ld 写出非法镜像，双击没反应）。重建前先 `taskkill //F //IM StickyNote.exe`；
  `build.bat` 必须 GBK + CRLF。前端原生 HTML/CSS/**ES Module**，无构建步骤。
- **`app.js` 是 module 脚本**：`state`/`findInstance`/`call` 都是模块作用域，`ExecuteScript` 里取不到；
  诊断探针只能碰 DOM 或走宿主侧 Go，否则整段 eval 抛 ReferenceError、读数是空的。
- **`postMessage` 传对象不传字符串**（字符串会被当 JSON 字符串值，宿主只报"无法解析前端消息"）；
  **`Eval` 不等 Promise**（拿到 `{}`），要异步结果就挂 `window.__x` 隔会儿再读。
- 桥接 JSON-RPC（**componentId 由宿主按来源注入**）；存储便携（exe 旁 `data`），原子写，**写权限判定
  必须试写文件**。`gofmt -w` 与 CRLF 冲突：先转 LF 到仓库内临时目录再 `gofmt -l`。

## 待办
- 内核通知服务（`host.notify` 只落日志）；zip 导入；设置页；休眠回收；组件视觉优化。
- **改尺寸的"卡顿/滞后"只量到链路是干净的**（宿主 1.2ms/帧、内容 5ms 内重排、投递 16.0ms 均匀、
  页面 240fps），剩下怀疑在合成/呈现侧，待用户实测再定方向。
- 待用户人工验：中文 IME、真实点击/拖拽、**布局板拖边框手感**、安装插件模态框、多实例内存。
