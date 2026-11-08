package webview

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"stickynote/internal/application/componentapp"
	"stickynote/internal/domain/component"
	"stickynote/internal/infrastructure/webview2"
)

// Options 由组合根注入，表现层不反向依赖基础设施。
type Options struct {
	// CacheDir 是缓存根目录：内置资源解包与引擎缓存放这里，删掉能自动重建。
	CacheDir string
	// ManagerDir 是管理界面（内核 UI）的资源目录，已解包到磁盘。
	ManagerDir string
	// PluginRoot 是插件安装目录，只用于在界面上显示与"打开文件夹"。
	PluginRoot string
	// OpenManagerOnStart 让管理界面在启动时自动打开（首次运行用）。
	OpenManagerOnStart bool
	// DevMode 打开 DevTools 与默认右键菜单。
	DevMode bool
}

// initEngine 初始化 WebView2 运行时。
//
// 必须在建任何窗口之前调用：环境是进程级的，所有窗口共用一套浏览器进程，
// 晚于第一个窗口创建就得再起一套。
func initEngine(devMode bool, cacheDir string) error {
	// 浏览器自己的缓存与用户数据放这里，删掉能自动重建。
	dir := filepath.Join(cacheDir, "webview2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("建 WebView2 用户数据目录失败: %w", err)
	}
	if err := webview2.Init(dir, devMode); err != nil {
		return err
	}
	log.Printf("WebView2 运行时已就绪：%s（用户数据 %s）", webview2.RuntimeVersion(), dir)
	return nil
}

// running 是一个实例的运行时对象：一个窗口 + 一个视图。
type running struct {
	view componentapp.InstanceView
	win  *Window
	host *Host
}

// App 是内核：它不实现任何具体业务，只负责把组件实例变成窗口。
//
// 组件是数据（样式 + 清单），实例是运行；内核只管窗口、桥接、存储、
// 调度，不碰便签、不碰时钟——那些都在组件自己的前端里。
type App struct {
	svc  *componentapp.Service
	opts Options

	// kernel 是一个隐藏窗口，只用来挂托盘图标和跑消息循环。
	// 它不属于任何组件，所以关掉一个组件不会带走整个程序。
	kernel *Window

	running map[string]*running
	order   []string // 保持创建顺序，托盘菜单按它排列

	// manager 是内核自己的管理界面，不是组件实例。
	manager *managerWindow

	// moveFrames 累计"拖动期间真正挪了几次窗口"。
	// 排查"一跳到位"时和前端报的帧数对一下，就知道断在哪一侧。
	moveFrames int

	// 布局板拖边框这条链的记账（诊断用）。
	//
	// 它和 resizeInstanceQuiet 的成本可能不是一回事：这条链是从
	// WebView2 的消息回调里进来的，SetWindowPos 引发的 put_Bounds
	// 是在引擎正持锁时重入引擎——量出来才知道贵多少。
	bridgeResizeN     int
	bridgeResizeCost  time.Duration
	bridgeResizeWorst time.Duration
	// 消息到达间隔：前端每帧发一条，这里看它到得匀不匀。
	// 如果消息成串到达（隔很久来一批），窗口就会"一会儿不动、一会儿跳"，
	// 那不是几何算错了，是投递本身不匀。
	bridgeResizeLast time.Time
	bridgeGapMin     time.Duration
	bridgeGapMax     time.Duration
	bridgeGapSum     time.Duration
	bridgeGapLate    int
	// bridgeTs 记下每一帧"宿主发出 put_Bounds 的时刻"，用来和页面自己的
	// resize 时刻相减，算出内容落后窗口多少毫秒。
	bridgeTs []int64

	closing bool
}

// Run 装配窗口、启动 WebView2 并进入消息循环。
//
// 调用前应用服务必须已经 Boot 过（目录已载入、实例已确定）。
func Run(svc *componentapp.Service, opts Options) error {
	// WebView2 的 COM 对象必须在同一个 OS 线程上创建和使用。
	runtime.LockOSThread()

	// 验证穿透时要看客户区鼠标消息到底落到哪个窗口上。
	debugInput = opts.DevMode

	if err := initEngine(opts.DevMode, opts.CacheDir); err != nil {
		return err
	}

	kernel, err := createWindow(WindowOptions{
		Title:     "StickyNote",
		Width:     1,
		Height:    1,
		MinWidth:  1,
		MinHeight: 1,
	})
	if err != nil {
		return err
	}

	app := &App{
		svc:     svc,
		opts:    opts,
		kernel:  kernel,
		running: map[string]*running{},
	}
	// 只有内核窗口销毁才退出消息循环。
	kernel.OnDestroy = func() { procPostQuitMessage.Call(0) }
	// 任务栏与 Alt+Tab 都不占位，右下角托盘是唯一入口。
	kernel.EnableTray(TrayHooks{
		Items: app.trayItems,
		// 左键直接开主界面：这是用户最想看到的东西。
		ToggleShow: app.openManager,
	})

	for _, v := range svc.ActiveInstances() {
		if err := app.spawn(v); err != nil {
			log.Printf("启动实例 %s（%s）失败: %v", v.ID, v.ComponentName, err)
		}
	}

	// 一个实例都没有时（所有组件都被停用）必须把管理界面顶上来，
	// 否则进程留在后台却没有任何入口能重新启用组件。
	if len(app.running) == 0 || opts.OpenManagerOnStart {
		app.openManager()
	}
	if len(app.running) == 0 && app.manager == nil {
		log.Printf("没有任何可运行的组件，且管理界面也没起来，退出")
		return nil
	}

	// 光标探针：鼠标压在穿透窗口上时每秒报一次"系统说那儿是谁"。
	// 这是唯一不需要伪造输入的穿透证据来源（见 diag_cursor.go）。
	if opts.DevMode {
		app.startCursorProbe()
	}

	// 改尺寸节奏诊断：只量"改尺寸"这一条链，不跑整串界面探针。
	// 单独一个开关是因为它必须跑在"没有任何界面操作"的干净状态下：
	// 前面那串探针会开抽屉、点方块、改尺寸档位，量出来的数就不干净了。
	if resizeBench() {
		time.AfterFunc(3*time.Second, func() {
			app.deferToUI(func() { probePageFrameRate(app, "note-1") })
		})
		time.AfterFunc(5*time.Second, func() {
			app.deferToUI(func() { probeResizePacing(app) })
		})
		// 后半段要驱动真实的桥接链路，得先把管理界面摆出来。
		time.AfterFunc(14*time.Second, func() {
			app.deferToUI(func() { app.openManager() })
		})
		time.AfterFunc(17*time.Second, func() {
			app.deferToUI(func() { probeBridgeResize(app) })
		})
	}

	kernel.Run()
	return nil
}

// initialPosition 决定一个"还没摆过位置"的实例落在哪里。
//
// free 之外的四项只在第一次出现时生效（之后以存盘的几何为准），
// 因为样式声明的停靠是**初始位置**，不是"永远吸附"——v1 不做贴边吸附。
//
// 这件事不能省：一条横贯屏幕的悬浮提示栏如果居中出现在屏幕正中，
// 又因为点击穿透收不到鼠标事件，用户根本没法把它挪到边上。
// 落位之后一律夹回工作区：窗口落到屏幕外，用户就找不回来了。
func initialPosition(dock component.Dock, width, height int) (int, int) {
	wa := workArea()
	aw := int(wa.Right - wa.Left)
	ah := int(wa.Bottom - wa.Top)
	x := int(wa.Left) + (aw-width)/2
	y := int(wa.Top) + (ah-height)/2
	switch dock {
	case component.DockTop:
		y = int(wa.Top)
	case component.DockBottom:
		y = int(wa.Bottom) - height
	case component.DockLeft:
		x = int(wa.Left)
	case component.DockRight:
		x = int(wa.Right) - width
	}
	if x < int(wa.Left) {
		x = int(wa.Left)
	}
	if y < int(wa.Top) {
		y = int(wa.Top)
	}
	// 错开之后不能跑出工作区，否则窗口落在屏幕外，用户找不回来。
	if maxX := int(wa.Right) - width; x > maxX {
		x = maxX
	}
	if maxY := int(wa.Bottom) - height; y > maxY {
		y = maxY
	}
	return x, y
}

// spawn 为一个实例建窗口与控制器。
func (a *App) spawn(v componentapp.InstanceView) error {
	scale := systemScale()

	// Bounds.Width/Height 是逻辑像素，建窗口要换成物理像素。
	width := int(math.Round(float64(v.Bounds.Width) * scale))
	height := int(math.Round(float64(v.Bounds.Height) * scale))
	if width <= 0 || height <= 0 {
		width = int(math.Round(float64(component.DefaultWidth) * scale))
		height = int(math.Round(float64(component.DefaultHeight) * scale))
	}

	// 落位在建窗口之前就算好：窗口必须**直接建在目标位置**。
	// 分层窗口的命中区域是在显示那一刻按当前矩形登记的，隐藏状态下
	// 挪窗口不会重建它——"先建再挪再显示"会让穿透窗口照样吃鼠标。
	posX, posY := v.Bounds.X, v.Bounds.Y
	if !v.Positioned {
		posX, posY = initialPosition(v.Style.Dock, width, height)
	}

	win, err := createWindow(WindowOptions{
		Title:       v.ComponentName,
		X:           posX,
		Y:           posY,
		HasPosition: true,
		Width:       width,
		Height:      height,
		MinWidth:    v.MinBounds.Width,
		MinHeight:   v.MinBounds.Height,
		Resizable:   v.Style.Resizable,
		NoActivate:  v.Style.Activation == component.NoActivate,
		// 穿透由组里的开关直接决定：勾了就是"点它等于点它背后的东西"，
		// 位置只能靠管理界面的布局板调（窗口收不到鼠标，拖不动自己）。
		ClickThrough: v.Style.ClickThrough,
		Desktop:      v.Style.Layer == component.LayerDesktop,
		// 投影只能由 DWM 画到窗口外面，所以"要不要投影"就是样式里
		// visual.shadow 有没有值——它是这条链上唯一声明过意图的地方。
		Shadow: v.Style.Shadow != "",
	})
	if err != nil {
		return err
	}

	r := &running{view: v, win: win}
	a.running[v.ID] = r
	a.order = append(a.order, v.ID)

	win.OnResize = func() {
		if r.host != nil {
			r.host.Resize()
		}
	}
	// 移动/缩放结束才落盘：WM_MOVE 会连发几百次，每次都写文件没有意义。
	win.OnSettle = func() { a.persistBounds(r) }
	win.OnClose = func() bool {
		a.persistBounds(r)
		return true
	}
	win.OnDestroy = func() {
		a.forget(v.ID)
		// 最后一个窗口关掉后，进程就没有存在的意义了。
		// 留一个没有窗口的进程只会让用户找不到入口，直接退出。
		a.maybeQuit()
	}

	if v.Visible {
		win.Show()
		// 置顶必须在 Show 之后设：隐藏窗口上的 Z 序变更会被系统丢弃。
		// 桌面层是锚在 Progman 正上方的顶层窗口，Z 序已由 Show 定好，
		// 这里不能调 SetTopMost——置顶会把它抬到应用窗口前面，正好相反。
		if v.Style.Layer == component.LayerTopmost {
			win.SetTopMost(true)
		}
	}

	host := NewHost(win, v.AssetsDir, v.Entry, stylePayload{
		Vars:      v.Style.CSSVariables(),
		Titlebar:  v.Style.Titlebar,
		Resizable: v.Style.Resizable,
		Layer:     string(v.Style.Layer),
	})
	r.host = host
	// 探针与穿透验证都必须在**文档就绪之后**跑：WebView2 的导航是异步的，
	// Navigate() 返回时页面还是空的，这时候 eval 只能拿到 null。
	// （Sciter 版的 SciterLoadHtml 是同步的，所以那时可以紧跟 Navigate。）
	host.OnReady = func() {
		a.deferToUI(func() {
			if diagEnabled() {
				logLayout(v.ID, win, "body>*", ".danmaku", ".danmaku__track", ".danmaku__item", ".note__editor")
			}
			win.ReanchorDesktop()
			// 文档就绪后再断言一次缩放：导航完成前设的值有可能被引擎在
			// 首次排版时重算掉。同值重复设置是空操作，代价可以忽略。
			host.SetZoom(v.Zoom)
			// 导航完成后 WebView2 会在浏览器进程里建一个和自己同尺寸的
			// "输入窗口"，盖在宿主正上方吃鼠标（见 inputwin.go）。
			// 这是唯一"它一定已经存在"的时机，必须在这里补透明位。
			win.neutralizeInputWindow()
			// 输入窗口是导航之后才建的，建的那一刻可能还没铺到宿主的尺寸，
			// 所以隔一段时间再补几次。幂等，已经透明了就直接返回。
			for _, d := range []time.Duration{300 * time.Millisecond, 1200 * time.Millisecond, 3 * time.Second} {
				d := d
				time.AfterFunc(d, func() { win.Post(win.neutralizeInputWindow) })
			}
			if win.ClickThrough() && a.opts.DevMode {
				dumpWindowTree(v.ID, win)
				// 引擎的子窗口不一定在文档就绪时就建好，隔一会儿再看一次，
				// 否则会得出"没有子窗口"的错误结论。
				time.AfterFunc(3*time.Second, func() {
					win.Post(func() { dumpWindowTree(v.ID+"(3s)", win) })
				})
			}
		})
	}
	if err := host.Start(func(raw string) { a.onMessage(v.ID, raw) }, a.opts.DevMode); err != nil {
		win.Destroy()
		return err
	}
	// 建 WebView2 控制器会把 Z 序弄回默认位置，锚一次补回来。
	win.ReanchorDesktop()

	// 初始布局到此结束，之后窗口再跨显示器才接受系统的 DPI 建议矩形。
	win.Settle()

	// 样式已在 Start 里随文档创建注入，这里只需要打开入口页。
	host.Navigate()

	// 引擎装载文档时会把视图尺寸按自己的默认值重算（RasterizationScale 也会
	// 被重置，那个在 webview2 包里靠先关掉 ShouldDetectMonitorScaleChanges 解决），
	// 所以落位与尺寸要在 Navigate 之后补一遍。
	//
	// 只补这一次：窗口已经直接建在 posX/posY 上了，再 SetPosition 是个同坐标的
	// 空操作，而"隐藏状态下的空操作"正是穿透失效的来源（见 WindowOptions.X）。
	win.SetPosition(posX, posY)
	win.SetSize(width, height)

	// 内容缩放同样要在 Navigate 之后补一遍：装载文档会把视图参数重置成
	// 引擎自己算的值（见上面那条注释）。窗口尺寸不变，只有页面按倍数重排。
	host.SetZoom(v.Zoom)

	// 首次落位要记账。
	//
	// 不写回 layout.json 的话，存盘里一直是创建时的 0,0 且 positioned=false，
	// 而「布局」页是按存盘画的——全新安装后三个方块会全叠在左上角，
	// 直到用户手动拖一次窗口才对齐。窗口本身位置是对的，只是账没记。
	if !v.Positioned {
		a.persistBounds(r)
	}

	if win.ClickThrough() {
		// 分层属性读回来才算数：WS_EX_LAYERED 只是样式位，属性没设上时
		// 命中测试照样落在本窗口上，而日志看起来"样式全对"。
		log.Printf("实例 %s：穿透已设（分层 %s + WS_EX_TRANSPARENT）", v.ID, win.layeredState())
	}

	if a.opts.DevMode {
		x, y, w, h := win.Bounds()
		cw, ch := win.ClientSize()
		ex := win.ExStyle()
		log.Printf("实例 %s：组件=%s 组=%s outer=(%d,%d) %dx%d client=%dx%d scale=%.3f "+
			"layer=%s resizable=%v noActivate=%v toolwindow=%v topmost=%v "+
			"desktop=%#x(%s,defView=%v) onDesktopLevel=%v transparent=%v degraded=%v",
			v.ID, v.ComponentID, v.Style.GroupID, x, y, w, h, cw, ch, win.Scale(),
			v.Style.Layer, v.Style.Resizable, v.Style.Activation == component.NoActivate,
			ex&wsExToolWindow != 0, ex&0x8 != 0,
			win.DesktopParent(), windowClass(win.DesktopParent()),
			hasChildClass(win.DesktopParent(), "SHELLDLL_DefView"), win.OnDesktopLevel(),
			ex&wsExTransparent != 0, win.DesktopDegraded())

	}

	a.pushState()
	return nil
}

// packPoint 把屏幕坐标打包成 Win32 按值传递的 POINT（两个 int32）。
//
// x64 下 8 字节结构体按值传递时占一个整数寄存器，所以直接塞进 uintptr 即可。
// 注意别传 &POINT —— 那样系统会把它当坐标读，得到的是指针的低位/高位，
// 通常落在屏幕外，函数返回 0，看起来像"这个点没有窗口"（假结论）。
func packPoint(x, y int) uintptr {
	return uintptr(uint32(int32(x))) | uintptr(uint32(int32(y)))<<32
}

// persistBounds 把窗口几何写回应用服务。
//
// X/Y 存物理像素（屏幕坐标本来就是物理的），宽高存逻辑像素，
// 这样换到别的缩放比例下尺寸才对得上。
func (a *App) persistBounds(r *running) {
	x, y, w, h := r.win.Bounds()
	scale := r.win.Scale()
	if scale <= 0 {
		scale = 1
	}
	rect := component.Rect{
		X:      x,
		Y:      y,
		Width:  int(math.Round(float64(w) / scale)),
		Height: int(math.Round(float64(h) / scale)),
	}
	ctx := context.Background()
	if err := a.svc.SetBounds(ctx, r.view.ID, rect); err != nil {
		log.Printf("记录实例几何失败: %v", err)
		return
	}
	if err := a.svc.SaveLayout(ctx); err != nil {
		log.Printf("保存实例布局失败: %v", err)
	}
	// 用户也可能直接拖窗口本身（便签的标题栏），那条路径不经过桥接，
	// 管理界面不知道位置变了。这里补一次刷新，否则布局页的方块停在旧位置。
	// 必须排到 UI 队列：此刻可能还在系统的移动/缩放模态循环里。
	a.deferToUI(a.pushState)
}

func (a *App) forget(id string) {
	delete(a.running, id)
	kept := a.order[:0]
	for _, v := range a.order {
		if v != id {
			kept = append(kept, v)
		}
	}
	a.order = kept
	a.pushState()
}

// maybeQuit 在"没有任何窗口了"的时候退出。
//
// 管理界面也算窗口：它开着就说明用户还在操作，不能把进程收掉。
func (a *App) maybeQuit() {
	if a.closing || len(a.running) > 0 || a.manager != nil {
		return
	}
	a.closing = true
	log.Printf("退出：没有窗口了（实例 0 个，管理界面已关）")
	a.kernel.Close()
}

// onMessage 是前端消息的唯一入口：先解析，再按来源分派。
func (a *App) onMessage(instanceID, raw string) {
	var req request
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		log.Printf("无法解析前端消息（%s）: %v", instanceID, err)
		return
	}
	if a.opts.DevMode {
		log.Printf("bridge <- %s %s (id=%d)", instanceID, req.Method, req.ID)
	}

	// 管理界面用保留的实例 id，走自己的一套方法。
	// 它和组件共用同一个协议，但能力完全不同：组件只能管自己，
	// 管理界面管的是所有组件——所以不能靠组件清单里的权限来区分。
	if instanceID == managerInstanceID {
		a.onManagerMessage(req)
		return
	}

	r := a.running[instanceID]
	if r == nil {
		return
	}
	a.onComponentMessage(r, req)
}

// onComponentMessage 处理某个组件实例的请求。
//
// 分派表按"组件能请求什么"来组织，所有共性能力（存储、通知、日志）
// 都在这里由内核实现，组件自己一件也做不了。
func (a *App) onComponentMessage(r *running, req request) {
	ctx := context.Background()

	switch req.Method {
	case "component.ready":
		// 首屏就绪：一次性把实例信息、解析后的外观、几何、私有存储交给组件，
		// 省掉组件再发三四个请求的往返。
		//
		// 外观字段名叫 style 是沿用旧协议：它的内容是「所属组」解析出来的
		// 最终窗口配置（ResolvedStyle），组件只读不写。
		data, err := a.svc.Storage(ctx, r.view.ID)
		if err != nil {
			replyTo(r.host, req.ID, nil, err)
			return
		}
		// 变量现取而不是用 r.view 里那份：它是建窗时的快照，
		// 用户可能在管理界面里改过，而窗口还没重建。
		vars, err := a.svc.Vars(ctx, r.view.ID)
		if err != nil {
			replyTo(r.host, req.ID, nil, err)
			return
		}
		replyTo(r.host, req.ID, map[string]any{
			"instance": map[string]any{
				"id":          r.view.ID,
				"componentId": r.view.ComponentID,
				"name":        r.view.ComponentName,
			},
			"style":   r.view.Style,
			"bounds":  r.view.Bounds,
			"storage": data,
			"vars":    vars,
		}, nil)

	case "component.close":
		replyTo(r.host, req.ID, nil, nil)
		r.win.Close()

	case "component.setBounds":
		var p struct {
			X      int `json:"x"`
			Y      int `json:"y"`
			Width  int `json:"width"`
			Height int `json:"height"`
		}
		if !params(r.host, req, &p) {
			return
		}
		scale := r.win.Scale()
		if p.Width > 0 && p.Height > 0 {
			r.win.SetSize(int(math.Round(float64(p.Width)*scale)),
				int(math.Round(float64(p.Height)*scale)))
		}
		if p.X != 0 || p.Y != 0 {
			r.win.SetPosition(p.X, p.Y)
		}
		a.persistBounds(r)
		replyTo(r.host, req.ID, nil, nil)

	case "window.beginDrag":
		// 先回包再进模态循环，否则 Promise 要等到用户松手才 resolve。
		replyTo(r.host, req.ID, nil, nil)
		r.win.BeginDrag()

	case "window.beginResize":
		var p struct {
			Edge string `json:"edge"`
		}
		if !params(r.host, req, &p) {
			return
		}
		replyTo(r.host, req.ID, nil, nil)
		r.win.BeginResize(p.Edge)

	case "storage.get":
		var p struct {
			Key string `json:"key"`
		}
		if !params(r.host, req, &p) {
			return
		}
		data, err := a.svc.Storage(ctx, r.view.ID)
		if err != nil {
			replyTo(r.host, req.ID, nil, err)
			return
		}
		// 键不存在时 value 为 null，组件据此判断"首次运行"。
		replyTo(r.host, req.ID, map[string]any{"value": data[p.Key]}, nil)

	case "storage.set":
		var p struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		}
		if !params(r.host, req, &p) {
			return
		}
		replyTo(r.host, req.ID, nil, a.svc.SetStorage(ctx, r.view.ID, p.Key, p.Value))

	case "storage.remove":
		var p struct {
			Key string `json:"key"`
		}
		if !params(r.host, req, &p) {
			return
		}
		replyTo(r.host, req.ID, nil, a.svc.RemoveStorage(ctx, r.view.ID, p.Key))

	case "host.log":
		var p struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		}
		if !params(r.host, req, &p) {
			return
		}
		// 日志由内核统一落盘，组件不自己写文件。
		log.Printf("[%s] %s: %s", r.view.ID, p.Level, p.Message)
		replyTo(r.host, req.ID, nil, nil)

	case "host.notify":
		// 通知是内核能力，不是组件能力。v1 还没有通知浮层，
		// 先落一条日志把链路跑通——组件不该依赖它是否真的弹出来。
		var p struct {
			Text string `json:"text"`
		}
		if !params(r.host, req, &p) {
			return
		}
		log.Printf("[notice] %s: %s", r.view.ID, p.Text)
		replyTo(r.host, req.ID, accepted, nil)

	default:
		replyTo(r.host, req.ID, nil, fmt.Errorf("未知方法: %s", req.Method))
	}
}

// trayItems 每次弹菜单时现算，文案与勾选状态始终反映当前实况。
func (a *App) trayItems() []TrayItem {
	items := []TrayItem{{Label: "管理组件…", OnClick: a.openManager}}
	if len(a.order) > 0 {
		items = append(items, TrayItem{Separator: true})
	}
	for _, id := range a.order {
		r := a.running[id]
		if r == nil {
			continue
		}
		id := id
		label := "隐藏 " + r.view.ComponentName
		if !r.win.Visible() {
			label = "显示 " + r.view.ComponentName
		}
		items = append(items, TrayItem{
			Label:   label,
			OnClick: func() { a.toggleShow(id) },
		})
	}
	return append(items,
		TrayItem{Separator: true},
		TrayItem{Label: "退出", OnClick: a.quit},
	)
}

// moveInstance 把实例窗口挪到屏幕坐标 (x, y)（物理像素）。
//
// 和用户直接拖窗口等价：都走 SetPosition、都写回 layout.json。
// 区别只在触发源——一个是系统模态移动循环，一个是管理界面的布局视图。
//
// 窗口没开着（实例被隐藏/组件停用）时只改存盘几何：等它下次出现，
// 就该落在用户摆的位置，而不是又回到居中。
func (a *App) moveInstance(ctx context.Context, id string, x, y int, commit bool) error {
	if r := a.running[id]; r != nil {
		r.win.SetPosition(x, y)
		if commit {
			a.persistBounds(r)
		}
		return nil
	}

	v, ok := a.svc.Instance(id)
	if !ok {
		return fmt.Errorf("实例 %s 不存在", id)
	}
	// X/Y 存物理像素、宽高存逻辑像素，和 persistBounds 的约定保持一致。
	if err := a.svc.SetBounds(ctx, id, component.Rect{
		X: x, Y: y,
		Width:  v.Bounds.Width,
		Height: v.Bounds.Height,
	}); err != nil {
		return err
	}
	if commit {
		return a.svc.SaveLayout(ctx)
	}
	return nil
}

// moveInstanceQuiet 只挪窗口，不落盘、不发通知，也不派发 WM_WINDOWPOSCHANGING。
//
// 拖动过程中的每一帧走这里：可以当场调用，不用排 UI 队列（排队列会被
// 输入消息饿死，看起来就是"松手才跳过去"）。几何在松手那一下由
// moveInstance 统一落盘。
func (a *App) moveInstanceQuiet(id string, x, y int) error {
	if r := a.running[id]; r != nil {
		r.win.SetPositionQuiet(x, y)
		a.moveFrames++
		return nil
	}
	// 窗口没开着时没有"帧"可言，直接按落盘那条路走。
	return a.moveInstance(context.Background(), id, x, y, false)
}

// resizeInstanceQuiet 只改窗口几何，不落盘、不发通知，也不派发 WM_WINDOWPOSCHANGING。
//
// 拖动过程中的每一帧走这里，理由和 moveInstanceQuiet 完全一样：带
// WM_WINDOWPOSCHANGING 的 SetWindowPos 是同步的，站在引擎回调里发会重入引擎；
// 而排到 UI 队列又会被输入消息饿死，看起来就是"松手才跳一下"。
// WM_SIZE 不受 swpNoSendChanging 影响，所以页面照常重排。
func (a *App) resizeInstanceQuiet(id string, x, y, w, h int) error {
	if r := a.running[id]; r != nil {
		scale := r.win.Scale()
		if scale <= 0 {
			scale = 1
		}
		r.win.SetBoundsQuiet(x, y,
			int(math.Round(float64(w)*scale)), int(math.Round(float64(h)*scale)))
		a.moveFrames++
		return nil
	}
	return a.resizeInstance(context.Background(), id, x, y, w, h, false)
}

// resizeInstance 改实例窗口的位置与尺寸。
//
// 参数口径和 moveInstance 一致：x/y 是物理像素，w/h 是**逻辑像素**
// （layout.json 里存的就是逻辑像素，换到别的缩放比例下才对得上）。
func (a *App) resizeInstance(ctx context.Context, id string, x, y, w, h int, commit bool) error {
	v, ok := a.svc.Instance(id)
	if !ok {
		return fmt.Errorf("实例 %s 不存在", id)
	}
	// 最小尺寸以组件声明的 MinBounds 为准，在这里夹而不是只靠界面：
	// 手写前端脚本或者手改 layout.json 都能绕开界面的滑块。
	//
	// 但下限不能超过当前尺寸——存盘值本来就小于声明下限时（作者改过 manifest，
	// 或者手改过 layout.json），一夹就等于替用户把窗口放大一截。
	if floor := min(v.MinBounds.Width, v.Bounds.Width); floor > 0 && w < floor {
		w = floor
	}
	if floor := min(v.MinBounds.Height, v.Bounds.Height); floor > 0 && h < floor {
		h = floor
	}
	if w <= 0 || h <= 0 {
		return fmt.Errorf("尺寸 %d×%d 不合法", w, h)
	}

	if r := a.running[id]; r != nil {
		scale := r.win.Scale()
		if scale <= 0 {
			scale = 1
		}
		r.win.SetBounds(x, y,
			int(math.Round(float64(w)*scale)), int(math.Round(float64(h)*scale)))
		if commit {
			a.persistBounds(r)
		}
		return nil
	}

	// 窗口没开着（隐藏或组件停用）时只改存盘几何：等它下次出现，
	// 就该是用户摆的那个大小，而不是回到默认值。
	if err := a.svc.SetBounds(ctx, id, component.Rect{X: x, Y: y, Width: w, Height: h}); err != nil {
		return err
	}
	if commit {
		return a.svc.SaveLayout(ctx)
	}
	return nil
}

// setInstanceZoom 改实例的内容缩放（只改页面大小，不动窗口尺寸）。
//
// 窗口开着就当场推给引擎：这一条只碰 COM 属性，不发窗口位置消息，
// 所以不需要像移动那样排 UI 队列躲重入。
func (a *App) setInstanceZoom(ctx context.Context, id string, zoom float64, commit bool) error {
	if err := a.svc.SetZoom(ctx, id, zoom); err != nil {
		return err
	}
	v, ok := a.svc.Instance(id)
	if !ok {
		return fmt.Errorf("实例 %s 不存在", id)
	}
	if r := a.running[id]; r != nil {
		r.view.Zoom = v.Zoom
		if r.host != nil {
			r.host.SetZoom(v.Zoom)
		}
	}
	if commit {
		return a.svc.SaveLayout(ctx)
	}
	return nil
}

// setVisible 统一处理实例显隐：改窗口、记状态、落盘。
func (a *App) setVisible(id string, visible bool) {
	ctx := context.Background()
	if r := a.running[id]; r != nil {
		if visible {
			r.win.ShowRestore()
		} else {
			r.win.Hide()
		}
		r.view.Visible = visible
	}
	// 窗口不在也要记账：侧边栏按实例列出来，用户可能在一个组被停用期间
	// 拨过它的显隐开关；重新启用时应该按记录的状态出现，而不是一律显示。
	if err := a.svc.SetVisible(ctx, id, visible); err != nil {
		log.Printf("记录实例显隐失败: %v", err)
		return
	}
	if err := a.svc.SaveLayout(ctx); err != nil {
		log.Printf("保存实例布局失败: %v", err)
	}
	a.pushState()
}

func (a *App) toggleShow(id string) {
	if r := a.running[id]; r != nil {
		a.setVisible(id, !r.win.Visible())
	}
}

// quit 保存全部几何后退出。
func (a *App) quit() {
	a.closing = true
	ctx := context.Background()

	ids := make([]string, len(a.order))
	copy(ids, a.order)
	for _, id := range ids {
		if r := a.running[id]; r != nil {
			a.persistBounds(r)
			r.win.Destroy()
		}
	}
	if err := a.svc.SaveLayout(ctx); err != nil {
		log.Printf("退出前保存实例布局失败: %v", err)
	}
	if a.manager != nil {
		a.persistManager()
		a.manager.win.Destroy()
	}
	a.kernel.Close()
}

// replyTo 回一条响应。id 为 0 表示前端没有等回复，不需要回。
func replyTo(h *Host, id int, result any, err error) {
	if id == 0 || h == nil {
		return
	}
	if err != nil {
		h.Post(errResponse(id, err))
		return
	}
	h.Post(okResponse(id, result))
}
