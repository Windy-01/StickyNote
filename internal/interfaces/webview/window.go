package webview

import (
	"fmt"
	"log"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"stickynote/internal/infrastructure/webview2"
)

// WindowOptions 是一个宿主窗口的创建参数。
//
// 它是 ResolvedStyle 在表现层的投影：应用层把样式翻译成这几个开关，
// 表现层不需要认识 domain 里的枚举。
type WindowOptions struct {
	Title string
	// Width/Height 是物理像素；调用方已经按 DPI 换算过。
	Width  int
	Height int
	// X/Y 是屏幕物理像素坐标，只在 HasPosition 为 true 时生效。
	//
	// 一定要在建窗口时就给对，别建完再 SetWindowPos 挪过去：
	// 分层窗口的命中区域是在**显示那一刻**按当前矩形登记的，
	// 隐藏状态下的位移不会重建它。启动时"先 CW_USEDEFAULT 建、再挪到
	// 目标位置、最后才显示"的写法，会让窗口顶着一整套正确的穿透样式
	// 却照样吃鼠标——实测表现就是"必须手动拖一下位置才能穿透"。
	X, Y        int
	HasPosition bool
	// MinWidth/MinHeight 是逻辑像素，命中测试时再乘当前缩放。
	MinWidth  int
	MinHeight int

	// Resizable 决定是否带 WS_THICKFRAME（能不能拖边缩放）。
	Resizable bool
	// NoActivate 对应 WS_EX_NOACTIVATE：点击不抢焦点。
	NoActivate bool
	// ClickThrough 对应 WS_EX_TRANSPARENT：鼠标消息穿透到下面的窗口。
	// 只有 layer=toast 的内置悬浮提示栏用它。
	ClickThrough bool
	// Desktop 表示放到桌面层：顶层窗口，Z 序锚在 Progman 正上方，
	// 于是在图标之上、被任何应用窗口覆盖。
	Desktop bool
	// ShowInTaskbar 为 true 时不用 WS_EX_TOOLWINDOW，窗口会占一个任务栏按钮。
	// 组件窗口一律不占（入口走托盘）；管理窗口要占，否则用户切走就找不回来了。
	ShowInTaskbar bool
	// Shadow 表示样式声明了窗口投影（visual.shadow 非空）。
	//
	// 投影不是我们画的东西：页面在窗口里面，CSS 的 box-shadow 只能画在
	// 窗口矩形以内，画不到外面去。能画到外面的只有 DWM，而 DWM 的投影
	// 和圆角是同一个开关（见 win32.go 里 dwmwcpDoNotRound 的注释）。
	// 所以这里只能二选一：要投影就连带接受 DWM 的圆角裁剪，
	// 不要投影就整圈干净、圆角交给 CSS 的 --sn-radius。
	Shadow bool
}

// Window 是一个无边框 Win32 宿主窗口。
//
// 客户区内容由 WebView2 Composition 直接合成，窗口自己什么都不画
// （所以带着 WS_EX_NOREDIRECTIONBITMAP，不参与 DWM 重定向）。
// 非客户区被 WM_NCCALCSIZE 抹掉；拖动与缩放由前端 mousedown 发起，
// 这里转成系统原生的移动/缩放循环。
type Window struct {
	hwnd  uintptr
	title string  // 只用于日志
	scale float64 // 当前 DPI 缩放，1.0 = 96 DPI
	minW  int     // 最小尺寸（逻辑像素）
	minH  int
	// settled 为 true 后才接受系统建议的 DPI 变更矩形。
	// 启动阶段窗口会从默认位置挪到目标显示器，那一次 WM_DPICHANGED 会把
	// 已经按存盘尺寸设好的窗口又改一遍，导致尺寸对不上。
	settled bool

	// tray 为 nil 表示没有启用通知区域图标。
	tray *tray

	// clickThrough / noActivate 影响显示方式：都不能用 SW_SHOW，
	// 那会顺手把焦点抢过来。
	clickThrough bool
	noActivate   bool
	// resizable 决定要不要在 WM_NCHITTEST 里回答缩放边框。
	resizable bool
	// ctrl 是这个窗口上的页面。为 nil 表示窗口还没挂页面
	// （内核窗口就是这样，它只负责托盘与消息循环）。
	ctrl *webview2.Controller
	// hcursor 是引擎算好的系统光标句柄，CursorChanged 带过来。
	// 0 表示还没收到过。
	hcursor uintptr
	// cursorEvents 是 CursorChanged 收到的次数，纯诊断。
	cursorEvents int
	// moveMsgs 是收到的 WM_MOUSEMOVE 条数，诊断用。
	moveMsgs int
	// hitTests 是收到的 WM_NCHITTEST 条数，诊断用。
	// 系统在问"这一点归谁"时才会发它，所以它涨了就等于
	// "命中测试把我们算进去了"——穿透窗口不该有这条消息。
	hitTests int
	// tracking 表示已经请求过 WM_MOUSELEAVE，避免重复申请。
	tracking bool
	// 改尺寸记账（只在 STICKYNOTE_DIAG 下开）。
	//
	// 拖窗口边框时这条链路每帧都在跑：WM_SIZE → put_Bounds → DComp Commit。
	// 一旦某一帧慢下来，观感就是"内容跟不上手"，而窗口本身照样跟手——
	// 光看观感分不清是消息没到、引擎没跟上、还是某次调用太贵，只能靠读数。
	resizeTrace bool
	resizeN     int
	resizeCost  time.Duration
	resizeWorst time.Duration
	resizeLast  time.Time
	// sizeMsgs 是收到的 WM_SIZE 条数，syncCount 是真正告诉引擎的次数。
	// 两者之比就是节流省掉了多少——拖动的成本全落在这个比例上。
	sizeMsgs  int
	syncCount int
	// resizePending 表示"有被节流跳过的尺寸还没告诉引擎"，
	// resizeArmed 表示已经排过一次补帧，避免每帧都排。
	resizePending bool
	resizeArmed   bool
	// boundsLast 是上一次真正发出去的 SetBoundsQuiet 的时刻（拖动中限速用）。
	boundsLast time.Time
	// leftDown/rightDown 记录按键状态：WebView2 的 SendMouseInput 要求
	// 每次事件都带上当前按住的键，否则拖拽选不中。
	leftDown  bool
	rightDown bool
	// desktopParent 是桌面层锚定的参照窗口（Progman），0 表示不是桌面层。
	// 窗口本身始终是顶层窗口——挂成 Progman 的子窗口做不到穿透。
	desktopParent uintptr
	// desktopDegraded 表示"落桌面层失败"：窗口退回成普通顶层窗口。
	// 不去动 Z 序——顶层窗口的 HWND_BOTTOM 会沉到 Progman 之下，
	// 也就是壁纸后面，窗口直接看不见了，那比不挂还糟。
	desktopDegraded bool

	// 以下回调都在 UI 线程上被调用。
	OnResize func()
	OnClose  func() bool
	// OnSettle 在移动/缩放循环结束时触发，用来记录几何。
	OnSettle func()
	// OnDestroy 在窗口真正销毁时触发。消息循环的退出由调用方决定，
	// 不能在这里无条件 PostQuitMessage——多实例时关一个窗口不该退出程序。
	OnDestroy func()

	mu    sync.Mutex
	queue []func()
}

var (
	windowMu       sync.RWMutex
	windowRegistry = map[uintptr]*Window{}

	// debugInput 打开后记录客户区鼠标消息，只在验证穿透时用。
	debugInput bool
)

func registerWindow(w *Window) {
	windowMu.Lock()
	windowRegistry[w.hwnd] = w
	windowMu.Unlock()
}

func unregisterWindow(hwnd uintptr) {
	windowMu.Lock()
	delete(windowRegistry, hwnd)
	windowMu.Unlock()
}

func lookupWindow(hwnd uintptr) *Window {
	windowMu.RLock()
	defer windowMu.RUnlock()
	return windowRegistry[hwnd]
}

// setController 挂上这个窗口的页面。
func (w *Window) setController(c *webview2.Controller) { w.ctrl = c }

// Controller 返回窗口上的页面，可能为 nil（内核窗口）。
func (w *Window) Controller() *webview2.Controller { return w.ctrl }

// onCursor 收下引擎算好的光标句柄并立刻设一次。
//
// Composition 模式不自己调 SetCursor：引擎通过 CursorChanged 告诉我们
// "指针现在该长这样"，设不设是宿主的事。系统在每条鼠标消息之前都会发
// WM_SETCURSOR，那条才是权威时机，这里只是保证指针不滞后一帧。
func (w *Window) onCursor(hcursor uintptr) {
	w.cursorEvents++
	if hcursor == 0 {
		return
	}
	w.hcursor = hcursor
	procSetCursor.Call(hcursor)
}

// applyCursor 按缓存的光标句柄设一次。
func (w *Window) applyCursor() {
	if w.hcursor == 0 {
		return
	}
	procSetCursor.Call(w.hcursor)
}

// CursorStats 返回光标变更次数与当前句柄，诊断用。
// 次数恒为 0 说明 CursorChanged 根本没送到宿主。
func (w *Window) CursorStats() (events int, hcursor uintptr) {
	return w.cursorEvents, w.hcursor
}

// MouseMoveMsgs 返回窗口收到的 WM_MOUSEMOVE 条数，诊断用。
func (w *Window) MouseMoveMsgs() int { return w.moveMsgs }

// HitTestCount 返回窗口被问过多少次 WM_NCHITTEST，诊断用。
func (w *Window) HitTestCount() int { return w.hitTests }

// forwardMouse 把一条窗口鼠标消息转给页面。
//
// Composition 模式不接管输入：宿主是唯一的输入入口，不转发的话
// 页面里点不动、hover 也没有，看起来像"渲染出来了但死的"。
func (w *Window) forwardMouse(msg uint32, wParam, lParam uintptr) bool {
	if w.ctrl == nil {
		return false
	}

	x := int32(int16(lParam & 0xFFFF))
	y := int32(int16((lParam >> 16) & 0xFFFF))

	var kind int32
	switch msg {
	case wmMouseMove:
		kind = webview2.MouseMove
	case wmLButtonDown, wmLButtonDblClk:
		kind = webview2.MouseLeftDown
		w.leftDown = true
	case wmLButtonUp:
		kind = webview2.MouseLeftUp
		w.leftDown = false
	case wmRButtonDown:
		kind = webview2.MouseRightDown
		w.rightDown = true
	case wmRButtonUp:
		kind = webview2.MouseRightUp
		w.rightDown = false
	case wmMButtonDown:
		kind = webview2.MouseMiddleDown
	case wmMButtonUp:
		kind = webview2.MouseMiddleUp
	case wmMouseWheel:
		kind = webview2.MouseWheel
		// 滚轮的 lParam 是屏幕坐标，其余鼠标消息是客户区坐标
		pt := point{X: x, Y: y}
		procScreenToClient.Call(w.hwnd, uintptr(unsafe.Pointer(&pt)))
		x, y = pt.X, pt.Y
	default:
		return false
	}

	// 按键状态要按位带上，否则引擎认为"没有键按着"，拖拽选不中。
	var vk int32
	if w.leftDown || wParam&0x0001 != 0 {
		vk |= webview2.VkLeftButton
	}
	if w.rightDown || wParam&0x0002 != 0 {
		vk |= webview2.VkRightButton
	}
	if wParam&0x0010 != 0 {
		vk |= webview2.VkMiddleButton
	}

	var data int32
	if kind == webview2.MouseWheel {
		data = int32(int16((wParam >> 16) & 0xFFFF))
	}

	w.ctrl.SendMouse(kind, vk, data, x, y)
	return true
}

// trackLeave 申请一次 WM_MOUSELEAVE。
//
// 不申请的话指针移出窗口时页面收不到 leave，hover 状态会一直挂着。
func (w *Window) trackLeave() {
	if w.tracking {
		return
	}
	tme := trackMouseEvent{Size: uint32(unsafe.Sizeof(trackMouseEvent{})), Flags: tmeLeave}
	if ret, _, _ := procTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tme))); ret != 0 {
		w.tracking = true
	}
}

// resizeHit 判断屏幕坐标是否落在缩放边框带上，是就返回对应的命中值。
//
// 边框宽度按 DPI 放大：125% 下 5 个逻辑像素只有 4 个物理像素，太难点到。
func (w *Window) resizeHit(lParam uintptr) (uintptr, bool) {
	if !w.resizable || w.hwnd == 0 {
		return 0, false
	}
	pt := point{
		X: int32(int16(lParam & 0xFFFF)),
		Y: int32(int16((lParam >> 16) & 0xFFFF)),
	}
	procScreenToClient.Call(w.hwnd, uintptr(unsafe.Pointer(&pt)))

	var rc rect
	procGetClientRect.Call(w.hwnd, uintptr(unsafe.Pointer(&rc)))
	cw, ch := rc.Right-rc.Left, rc.Bottom-rc.Top
	if cw <= 0 || ch <= 0 {
		return 0, false
	}

	band := int32(5 * w.scale)
	if band < 4 {
		band = 4
	}
	return hitTestBand(cw, ch, pt.X, pt.Y, band)
}

// hitTestBand 是缩放边框的纯几何判定，单独拆出来是为了能测。
//
// 坐标是客户区坐标。落在带上返回对应的 HT*，否则 false（交给引擎/系统）。
func hitTestBand(cw, ch, x, y, band int32) (uintptr, bool) {
	if cw <= 0 || ch <= 0 {
		return 0, false
	}
	// 窗口不比两倍边框宽时不要判边框，否则整个窗口都成了边框，
	// 用户点哪儿都在缩放。用 <= 而不是 <：正好两倍时左右两条带会
	// 无缝铺满整个客户区，一个像素都不剩。
	if cw <= band*2 || ch <= band*2 {
		return 0, false
	}

	l := x < band
	r := x >= cw-band
	t := y < band
	b := y >= ch-band

	switch {
	case t && l:
		return htTopLeft, true
	case t && r:
		return htTopRight, true
	case b && l:
		return htBottomLeft, true
	case b && r:
		return htBottomRight, true
	case l:
		return htLeft, true
	case r:
		return htRight, true
	case t:
		return htTop, true
	case b:
		return htBottom, true
	}
	return 0, false
}

// systemScale 返回主显示器缩放比例，用于把逻辑尺寸换算成物理像素。
func systemScale() float64 {
	dpi, _, _ := procGetDpiForSystem.Call()
	if dpi == 0 {
		dpi = 96
	}
	return float64(dpi) / 96.0
}

// SystemScale 暴露给组合根：迁移旧数据时要把物理像素折回逻辑像素。
func SystemScale() float64 { return systemScale() }

// createWindow 创建无边框窗口。
//
// 因为 WM_NCCALCSIZE 把非客户区抹成 0，外框尺寸就等于客户区尺寸，
// 存盘值与实际显示一致，不需要再做边框换算。
func createWindow(opts WindowOptions) (*Window, error) {
	hInst, _, _ := procGetModuleHandleW.Call(0)
	className := utf16Ptr("StickyNoteHostWindow")

	brush, _, _ := procCreateSolidBrush.Call(uintptr(paperColor))

	wc := wndClassExW{
		Size:       uint32(unsafe.Sizeof(wndClassExW{})),
		WndProc:    windows.NewCallback(windowProc),
		Instance:   windows.Handle(hInst),
		Icon:       windows.Handle(loadIcon(systemIconSize(false))),
		IconSm:     windows.Handle(loadIcon(systemIconSize(true))),
		Background: windows.Handle(brush),
		ClassName:  className,
	}
	// 类重复注册会返回 0 并置 ERROR_CLASS_ALREADY_EXISTS，属正常情况。
	_, _, _ = procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	style := uintptr(wsPopup | wsClipChildren)
	if opts.Resizable {
		// 无边框仍然保留 WS_THICKFRAME：系统的模态缩放循环要靠它才成立。
		style |= wsThickFrame | wsMinimizeBox
	}

	exStyle := uintptr(wsExToolWindow) // 任务栏与 Alt+Tab 都不占位，入口统一走托盘
	// 内容由 DirectComposition 合成，窗口不参与 DWM 重定向。
	// 这是 WebView2 Composition 的硬性前提，建窗口时就得带上（事后加不可靠）。
	exStyle |= wsExNoRedirectionBitmap
	if opts.NoActivate || opts.ClickThrough {
		exStyle |= wsExNoActivate
	}
	if opts.ClickThrough {
		// 穿透必须写成"分层 + 透明"两个位：只有透明位时系统只跳过绘制，
		// 命中测试照样落在本窗口上。加了分层之后窗口形状被忽略，
		// 鼠标事件直接传给下面的窗口。
		exStyle |= wsExTransparent | wsExLayered
	}
	if opts.ShowInTaskbar {
		exStyle &^= wsExToolWindow
	}

	w := &Window{
		title:        opts.Title,
		scale:        systemScale(),
		minW:         opts.MinWidth,
		minH:         opts.MinHeight,
		clickThrough: opts.ClickThrough,
		noActivate:   opts.NoActivate || opts.ClickThrough,
		resizable:    opts.Resizable,
		resizeTrace:  diagEnabled() || resizeBench(),
	}

	// 建窗口时就给最终坐标，不要建完再挪。
	// 分层窗口的命中区域是在显示那一刻按当前矩形登记的，隐藏状态下的
	// 位移不会重建它（见 WindowOptions.X 的注释）。
	x, y := uintptr(cwUseDefault), uintptr(cwUseDefault)
	if opts.HasPosition {
		x, y = uintptr(opts.X), uintptr(opts.Y)
	}

	hwnd, _, err := procCreateWindowExW.Call(
		exStyle,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16Ptr(opts.Title))),
		style,
		x, y,
		uintptr(opts.Width), uintptr(opts.Height),
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		return nil, fmt.Errorf("创建窗口失败: %w", err)
	}
	w.hwnd = hwnd

	if opts.ClickThrough {
		// 常量 alpha=255：观感不变，只是把窗口变成分层窗口。
		procSetLayeredWindowAttrs.Call(hwnd, 0, 255, lwaAlpha)
	}

	// GetDpiForSystem 给的是主显示器缩放；窗口可能落在别的显示器上，
	// 以窗口自身的 DPI 为准。
	if dpi, _, _ := procGetDpiForWindow.Call(hwnd); dpi > 0 {
		w.scale = float64(dpi) / 96.0
	}

	// 圆角与投影。无边框窗口系统不会自动加圆角，要显式声明；
	// 而这一位在 Win11 上同时决定"有没有 DWM 投影"，两者绑在一起：
	//   ROUND      → DWM 裁圆角 + 画一圈投影（默认的窗口观感）
	//   DONOTROUND → 两者都不画，形状完全由页面自己决定
	// 样式没声明 shadow 的窗口一律 DONOTROUND：一个 radius=0 的桌面时钟
	// 被系统偷偷裁成 8px 圆角、还带一圈投影，那不是样式要的样子。
	pref := int32(dwmwcpDoNotRound)
	if opts.Shadow {
		pref = dwmwcpRound
	}
	hr, _, _ := procDwmSetWindowAttribute.Call(
		hwnd,
		dwmwaWindowCornerPreference,
		uintptr(unsafe.Pointer(&pref)),
		uintptr(unsafe.Sizeof(pref)),
	)
	if hr != 0 {
		// 老系统没有这个属性，圆角/投影就只能保持系统默认，留痕不静默。
		log.Printf("窗口 %q：设置圆角偏好失败 hr=%#x（投影开关不生效）", opts.Title, hr)
	} else {
		// 读回一次：这个属性是可读的（DwmGetWindowAttribute 返回 S_OK），
		// 所以"设了但没生效"能被当场抓到，不用靠肉眼比。
		var back int32
		g, _, _ := procDwmGetWindowAttribute.Call(hwnd,
			dwmwaWindowCornerPreference,
			uintptr(unsafe.Pointer(&back)), uintptr(unsafe.Sizeof(back)))
		if g != 0 || back != pref {
			log.Printf("窗口 %q：圆角偏好没设上（想 %d，读回 %d，hr=%#x）",
				opts.Title, pref, back, g)
		}
	}

	// 桌面层：锚到 Progman 正上方（顶层窗口，不是子窗口）。
	// 锚不上就降级成普通顶层窗口——它会被所有窗口盖住，但至少看得见。
	// 降级要留痕，不能静默。
	//
	// 这里只记下 progman 句柄；真正的 Z 序调整在 Show 之后做，
	// 因为隐藏窗口上的 Z 序变更会被系统丢弃。
	if opts.Desktop {
		if host, err := attachToDesktop(); err != nil {
			log.Printf("窗口 %q 落桌面层失败，降级为普通窗口: %v", opts.Title, err)
			w.desktopDegraded = true
		} else {
			w.desktopParent = host
		}
	}

	registerWindow(w)
	return w, nil
}

// Settle 标记窗口已完成初始布局，之后才响应系统的 DPI 变更建议。
func (w *Window) Settle() { w.settled = true }

// resizeCodes 把前端的边名映射成命中测试值。
var resizeCodes = map[string]uintptr{
	"left":        htLeft,
	"right":       htRight,
	"top":         htTop,
	"bottom":      htBottom,
	"topLeft":     htTopLeft,
	"topRight":    htTopRight,
	"bottomLeft":  htBottomLeft,
	"bottomRight": htBottomRight,
}

// BeginDrag 让系统进入窗口移动循环。
//
// 前端在标题栏上按下鼠标时调用。这里不自己算鼠标位移，而是伪造一次
// "标题栏上的左键按下"，把后续全部交给 DefWindowProc 的模态移动循环，
// 这样 Aero Snap、贴边吸附、多显示器拖拽都是系统原生行为。
func (w *Window) BeginDrag() {
	w.Post(func() { w.beginSystemMove(htCaption) })
}

// BeginResize 让系统进入窗口缩放循环。edge 取值 left/right/top/bottom/
// topLeft/topRight/bottomLeft/bottomRight。
func (w *Window) BeginResize(edge string) {
	code, ok := resizeCodes[edge]
	if !ok {
		return
	}
	w.Post(func() { w.beginSystemMove(code) })
}

// beginSystemMove 必须在 UI 线程的消息循环里调用，不能直接在 WebView2
// 的事件回调里调：模态循环会继续抽消息，在 COM 回调内部进入嵌套消息循环
// 有重入风险。所以上面两个方法都走 Post。
func (w *Window) beginSystemMove(ht uintptr) {
	// WebView2 的子窗口会捕获鼠标，不先释放，系统的移动循环拿不到输入。
	procReleaseCapture.Call()
	// SendMessage 会一直阻塞到用户松开鼠标，这是预期行为：
	// 移动/缩放本身就是模态的。
	procSendMessageW.Call(w.hwnd, wmNCLButtonDown, ht, 0)
}

// Handle 返回 HWND，供 WebView2 嵌入。
func (w *Window) Handle() uintptr { return w.hwnd }

// Scale 返回窗口当前所在显示器的缩放比例。
func (w *Window) Scale() float64 { return w.scale }

// Show 显示并激活窗口。
func (w *Window) Show() {
	// 不抢焦点的窗口不能用 SW_SHOW：它会把焦点抢过来，
	// 而 noActivate / clickThrough 的语义正好是"别碰焦点"。
	if w.noActivate || w.clickThrough {
		procShowWindow.Call(w.hwnd, swShowNoActivate)
	} else {
		procShowWindow.Call(w.hwnd, swShow)
		procSetForegroundWindow.Call(w.hwnd)
	}
	procUpdateWindow.Call(w.hwnd)

	// 桌面层的 Z 序必须在这之后落：Show 之前设的会被系统丢掉。
	// 放在这里而不是调用方，是因为"显示"和"落在哪一层"对桌面组件是同一件事。
	if w.desktopParent != 0 {
		if err := placeAtDesktopLevel(w.hwnd, w.desktopParent); err != nil {
			log.Printf("窗口 %q 落桌面层失败: %v", w.title, err)
		}
	}
}

// Close 请求关闭窗口，会走 OnClose 回调。
func (w *Window) Close() {
	procPostMessageW.Call(w.hwnd, wmClose, 0, 0)
}

// Destroy 立即销毁窗口，不触发 OnClose。
//
// 程序退出时用：此时不需要再走"保存并确认"的交互。
func (w *Window) Destroy() {
	procDestroyWindow.Call(w.hwnd)
}

// Minimize 最小化窗口。
func (w *Window) Minimize() {
	procPostMessageW.Call(w.hwnd, wmSysCommand, scMinimize, 0)
}

// EnableTray 在通知区域挂一个图标，并在窗口销毁时自动撤掉。
func (w *Window) EnableTray(hooks TrayHooks) {
	if w.tray != nil {
		return
	}
	w.tray = newTray(w, hooks)
	w.tray.install()
}

// Visible 报告窗口当前是否可见。
func (w *Window) Visible() bool {
	ret, _, _ := procIsWindowVisible.Call(w.hwnd)
	return ret != 0
}

// Hide 把窗口藏起来，任务栏与 Alt+Tab 都不留痕迹。
func (w *Window) Hide() {
	procShowWindow.Call(w.hwnd, swHide)
}

// ShowRestore 显示并还原窗口（从隐藏或最小化状态都适用）。
func (w *Window) ShowRestore() {
	procShowWindow.Call(w.hwnd, swRestore)
	procSetForegroundWindow.Call(w.hwnd)
}

// layeredState 读回分层属性，用来证明"它真的登记成分层窗口了"。
//
// WS_EX_LAYERED 只是一个样式位，它有没有生效要看分层属性有没有设上。
// 这两件事会不一致，而样式位读起来"全对"，是最容易骗过自己的地方。
func (w *Window) layeredState() string {
	var key, alpha, flags uint32
	r, _, err := procGetLayeredAttrs.Call(w.hwnd,
		uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&alpha)),
		uintptr(unsafe.Pointer(&flags)))
	if r == 0 {
		return fmt.Sprintf("未生效(%v)", err)
	}
	return fmt.Sprintf("alpha=%d key=%#x flags=%#x", alpha, key, flags)
}

// refreshLayered 让分层窗口的合成结果和命中区域跟上新位置。
//
// 带 WS_EX_NOREDIRECTIONBITMAP 的窗口移动之后，DWM 那边可能还留着旧位置的
// 合成结果：新位置一切正常，旧位置照样接鼠标、还留着残影。所以移动完主动
// 推它一次。
//
// 两个细节：
//   - **alpha 要改两次**。设成和当前一样的值，系统多半当空操作丢掉，
//     而我们要的正是"真的变了一次"——只有真的变了才会重算分层表面。
//   - 只在不频繁的路径上调。DwmFlush 是阻塞的，拖动的每一帧都调会直接卡住。
func (w *Window) refreshLayered() {
	if !w.clickThrough || w.hwnd == 0 {
		return
	}
	procSetLayeredWindowAttrs.Call(w.hwnd, 0, 254, lwaAlpha)
	procSetLayeredWindowAttrs.Call(w.hwnd, 0, 255, lwaAlpha)
	procDwmFlush.Call()
	// 顺手把 WebView2 的输入窗口再查一遍：它可能在导航/重建时被换成新的，
	// 而新那个默认是不透明的，会重新开始吃鼠标（见 inputwin.go）。
	w.neutralizeInputWindow()
}

// SetTopMost 设置/取消置顶。
func (w *Window) SetTopMost(on bool) {
	insertAfter := hwndNoTopMost
	if on {
		insertAfter = hwndTopMost
	}
	procSetWindowPos.Call(w.hwnd, insertAfter, 0, 0, 0, 0,
		swpNoMove|swpNoSize|swpNoActivate)
}

// SetPosition 只移动窗口，不改尺寸。
//
// 参数是屏幕物理像素。桌面层也是顶层窗口（锚在 Progman 上方），
// 所以这里不需要任何父子坐标折算。
func (w *Window) SetPosition(x, y int) {
	procSetWindowPos.Call(w.hwnd, 0, uintptr(x), uintptr(y), 0, 0,
		swpNoSize|swpNoZOrder|swpNoActivate)
	w.refreshLayered()
}

// SetPositionQuiet 挪窗口，但不派发 WM_WINDOWPOSCHANGING。
//
// 专给"拖动中每帧挪一次"用。带 WM_WINDOWPOSCHANGING 的 SetWindowPos 是同步的，
// 站在引擎回调里发它会重入引擎；而排到 UI 队列里又会饿死——WM_MOUSEMOVE 是
// 输入消息，优先级高于 PostMessage 的 WM_APP，拖得越连续队列越排不上，
// 结果只在松手时挪一次，看起来就是"从起点直接跳到终点"。
func (w *Window) SetPositionQuiet(x, y int) {
	procSetWindowPos.Call(w.hwnd, 0, uintptr(x), uintptr(y), 0, 0,
		swpNoSize|swpNoZOrder|swpNoActivate|swpNoSendChanging)
}

// resizeSyncGap 兜底的同步间隔：读不到刷新率时按 60Hz 算。
//
// 依据是实测（STICKYNOTE_RESIZE_BENCH 的读数）：
//   - 一次 SetWindowPos（只挪位置）约 0.9ms，改尺寸约 1.4ms；
//   - 其中真正告诉引擎的那一下 put_Bounds 只有约 0.16ms；
//   - 也就是说成本几乎全在"窗口自己改几何"上，改多少次就付多少次。
//
// 鼠标报点是 125~250Hz，而画面最快也只能跟显示器刷新一样快。每来一条
// WM_SIZE 就同步一次，多出来的那些同步既换不来更快的画面，还会让渲染端
// 收到一串间隔不齐的尺寸变更——表现就是内容一顿一顿地跳。
const resizeSyncGap = 16 * time.Millisecond

// vRefresh 是 GetDeviceCaps 的"显示器刷新率"索引。
const vRefresh = 116

// screenFrameInterval 返回一帧的时长，用来给"改尺寸同步"定节拍。
//
// 按显示器刷新率算，夹在 6~20ms：节拍再小画面也出不了那么快，
// 再大内容就会明显落后于窗口。读不到就按 60Hz。
func screenFrameInterval() time.Duration {
	frameOnce.Do(func() {
		frameGap = resizeSyncGap
		hdc, _, _ := procGetDC.Call(0) // 0 = 整个屏幕
		if hdc != 0 {
			if hz, _, _ := procGetDeviceCaps.Call(hdc, vRefresh); hz >= 24 && hz <= 500 {
				frameGap = time.Second / time.Duration(hz)
			}
			procReleaseDC.Call(0, hdc)
		}
		if frameGap < 6*time.Millisecond {
			frameGap = 6 * time.Millisecond
		}
		if frameGap > 20*time.Millisecond {
			frameGap = 20 * time.Millisecond
		}
	})
	return frameGap
}

var (
	frameOnce sync.Once
	frameGap  time.Duration
)

// needSync 判断这一帧要不要真的把新尺寸告诉引擎。
//
// 三条：强制（几何已定）、第一次（还没同步过）、距上次够久（≥ 一帧）。
// 抽成纯函数是为了能单测——节流一旦写错，表现是"画面停在旧尺寸"，不报错。
func needSync(now, last time.Time, gap time.Duration, force bool) bool {
	if force || last.IsZero() {
		return true
	}
	return now.Sub(last) >= gap
}

// syncContent 把新客户区告诉引擎，并按需节流。
//
// force 为 true 时一定同步：几何已经不会再变了（缩放循环结束、落盘那一下），
// 这时候省掉一次同步，画面就会停在旧尺寸上直到下次改尺寸——那是最不该省的。
//
// 节流只跳过"中间帧"，跳过的那些帧不需要记账：put_Bounds 每次都从
// GetClientRect 取当前值，跳过的帧会被下一次同步自动带上。
func (w *Window) syncContent(force bool) {
	if w.OnResize == nil {
		return
	}
	now := time.Now()
	gap := screenFrameInterval()
	if !needSync(now, w.resizeLast, gap, force) {
		w.resizePending = true
		if !w.resizeArmed {
			// 只排一次补帧。补帧走 Post：拖动中 WM_MOUSEMOVE 优先级更高，
			// 排队的消息可能晚一点才轮到，但"几何已经定了"这种情况
			// （SetBounds 落盘、WM_EXITSIZEMOVE）另有强制同步兜底。
			w.resizeArmed = true
			time.AfterFunc(gap, func() { w.Post(w.flushPendingResize) })
		}
		return
	}

	since := now.Sub(w.resizeLast)
	first := w.resizeLast.IsZero()
	// 上一段拖动结束得比 400ms 还早，说明这是新的一段，先把上一段结算掉。
	if !first && since > 400*time.Millisecond {
		w.flushResizeStats()
	}
	w.resizeLast = now
	w.resizePending = false
	w.resizeArmed = false
	w.syncCount++

	if !w.resizeTrace {
		w.OnResize()
		return
	}

	t0 := now
	w.OnResize()
	d := time.Since(t0)

	w.resizeN++
	w.resizeCost += d
	if d > w.resizeWorst {
		w.resizeWorst = d
	}
	// 只打"第一帧"和"明显慢的帧"，否则一次拖动会刷几百行把日志淹掉。
	if w.resizeN == 1 || d > 16*time.Millisecond {
		cw, ch := w.ClientSize()
		how := "首帧"
		if !first {
			how = "距上帧 " + since.Round(time.Millisecond).String()
		}
		log.Printf("窗口 %q 改尺寸 #%d 客户区 %d×%d 引擎耗时 %v %s",
			w.title, w.resizeN, cw, ch, d.Round(time.Microsecond), how)
	}
}

// flushPendingResize 补上被节流跳掉的那一次同步。
func (w *Window) flushPendingResize() {
	if !w.resizePending {
		return
	}
	w.syncContent(true)
}

// flushResizeStats 结算一轮改尺寸，打一行汇总。
func (w *Window) flushResizeStats() {
	if !w.resizeTrace || w.resizeN == 0 {
		return
	}
	log.Printf("窗口 %q 一轮改尺寸：%d 帧，引擎总耗时 %v，平均 %v，最慢 %v",
		w.title, w.resizeN, w.resizeCost.Round(time.Microsecond),
		(w.resizeCost / time.Duration(w.resizeN)).Round(time.Microsecond),
		w.resizeWorst.Round(time.Microsecond))
	w.resizeN, w.resizeCost, w.resizeWorst = 0, 0, 0
}

// SetSize 只改尺寸，不动位置与 Z 序。
func (w *Window) SetSize(width, height int) {
	procSetWindowPos.Call(w.hwnd, 0, 0, 0, uintptr(width), uintptr(height),
		swpNoMove|swpNoZOrder|swpNoActivate)
}

// SetBounds 同时改位置与尺寸（都是屏幕物理像素）。
//
// 和 SetSize 的区别是要不要补一次分层窗口合成：位置变了而 DWM 没被推过，
// 旧位置的残影会一直留着，还照样接鼠标。
func (w *Window) SetBounds(x, y, width, height int) {
	procSetWindowPos.Call(w.hwnd, 0, uintptr(x), uintptr(y),
		uintptr(width), uintptr(height), swpNoZOrder|swpNoActivate)
	w.refreshLayered()
	// 这是"几何定了"的那一下（拖动的最后一帧、启动落位、DPI 迁移），
	// 必须强制同步一次：拖动中间帧被节流跳过的尺寸，靠这一次补上。
	w.syncContent(true)
}

// SetBoundsQuiet 改位置与尺寸，但不派发 WM_WINDOWPOSCHANGING。
//
// 专给"在管理界面的布局板上拖边框改大小"用，理由和 SetPositionQuiet 一样：
// 带 WM_WINDOWPOSCHANGING 的 SetWindowPos 是同步的，站在引擎的消息回调里
// 发它会重入引擎。WM_SIZE 不受 swpNoSendChanging 影响，所以页面照常重排。
func (w *Window) SetBoundsQuiet(x, y, width, height int) {
	// 几何没变就别发：拖动时鼠标动一格、换算成逻辑像素再乘回物理像素，
	// 有相当比例的帧算出来是同一个矩形，而每次 SetWindowPos 都会走一遍
	// WM_SIZE → 命中测试登记，纯属白花。
	if cx, cy, cw, ch := w.Bounds(); cx == x && cy == y && cw == width && ch == height {
		return
	}
	// 一帧最多改一次：实测这一下要 1.4ms，而它换来的画面最快也只能
	// 跟显示器刷新一样快。被跳掉的中间帧不用补——松手那一下走 SetBounds，
	// 那是不节流的，最终几何一定落得上。
	now := time.Now()
	if !w.boundsLast.IsZero() && now.Sub(w.boundsLast) < screenFrameInterval() {
		return
	}
	w.boundsLast = now
	procSetWindowPos.Call(w.hwnd, 0, uintptr(x), uintptr(y),
		uintptr(width), uintptr(height),
		swpNoZOrder|swpNoActivate|swpNoSendChanging)
}

// Center 把窗口放到主显示器中央（首次启动时用）。
func (w *Window) Center() {
	sw, _, _ := procGetSystemMetrics.Call(smCXScreen)
	sh, _, _ := procGetSystemMetrics.Call(smCYScreen)
	_, _, width, height := w.Bounds()
	x := (int(sw) - width) / 2
	y := (int(sh) - height) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	w.SetPosition(x, y)
}

// ReanchorDesktop 重新把窗口锚回 Progman 正上方。
//
// 必须能被反复调用：Show 之后建 WebView2 控制器、导航、以及引擎自己
// 调整内部窗口，都会顺手改动 Z 序（实测 CreateCoreWebView2CompositionController
// 之后窗口就不再贴在 Progman 上方了）。这一步幂等，多调几次没有副作用。
func (w *Window) ReanchorDesktop() {
	if w.desktopParent == 0 {
		return
	}
	if err := placeAtDesktopLevel(w.hwnd, w.desktopParent); err != nil {
		log.Printf("窗口 %q 重新落桌面层失败: %v", w.title, err)
	}
}

// DesktopParent 返回窗口挂到的桌面窗口句柄，0 表示没挂（含降级）。
func (w *Window) DesktopParent() uintptr { return w.desktopParent }

// OnDesktopLevel 表示窗口真的贴在 Progman 正上方。
//
// 顶层窗口的 Z 序里，"下面是 Progman"就说明它落在桌面这一层：
// 在图标之上（图标是 Progman 的子窗口，而我们是顶层窗口），
// 又在所有普通窗口之下。这个断言成立，穿透与"被应用窗口覆盖"才都靠得住。
func (w *Window) OnDesktopLevel() bool {
	if w.desktopParent == 0 {
		return false
	}
	below, _, _ := procGetWindow.Call(w.hwnd, gwHwndNext)
	return below == w.desktopParent
}

// DesktopDegraded 表示挂桌面层失败、已退回普通顶层窗口。
func (w *Window) DesktopDegraded() bool { return w.desktopDegraded }

// ClickThrough 返回窗口是否点击穿透。
func (w *Window) ClickThrough() bool { return w.clickThrough }

// Bounds 返回窗口在屏幕上的物理像素矩形。无边框下等同客户区。
func (w *Window) Bounds() (x, y, width, height int) {
	r := windowRect(w.hwnd)
	return int(r.Left), int(r.Top), int(r.Right - r.Left), int(r.Bottom - r.Top)
}

// SizeMsgs / SyncCount 是改尺寸这条链的"进来多少、出去多少"。
//
// 差值就是节流省掉的同步次数。诊断用，不参与任何决策。
func (w *Window) SizeMsgs() int  { return w.sizeMsgs }
func (w *Window) SyncCount() int { return w.syncCount }

// ClientSize 返回客户区尺寸（物理像素），WebView2 铺满的就是这块。
func (w *Window) ClientSize() (int, int) {
	var r rect
	if ret, _, _ := procGetClientRect.Call(w.hwnd, uintptr(unsafe.Pointer(&r))); ret == 0 {
		return 0, 0
	}
	return int(r.Right - r.Left), int(r.Bottom - r.Top)
}

// ExStyle 返回窗口的扩展样式位，诊断用（自检日志靠它证明穿透真的生效）。
func (w *Window) ExStyle() uint32 {
	// GWL_EXSTYLE = -20。nIndex 是 LONG，要按有符号传进去。
	idx := int32(-20)
	ret, _, _ := procGetWindowLongPtrW.Call(w.hwnd, uintptr(idx))
	return uint32(ret)
}

// Post 把一段代码排到 UI 线程执行。
//
// 所有触碰 WebView2 的操作都必须走这里：COM 对象不能跨线程用，
// 而消息处理、定时器、其他 goroutine 都可能在不同线程上。
func (w *Window) Post(f func()) {
	w.mu.Lock()
	w.queue = append(w.queue, f)
	w.mu.Unlock()
	procPostMessageW.Call(w.hwnd, wmApp, 0, 0)
}

func (w *Window) drain() {
	w.mu.Lock()
	q := w.queue
	w.queue = nil
	w.mu.Unlock()
	for _, f := range q {
		f()
	}
}

// Run 进入消息循环，窗口销毁后返回。
func (w *Window) Run() {
	var m msgT
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// lparam 把 Win32 消息里的 LPARAM 还原成结构体指针。
//
// LPARAM 里的地址由系统提供，不是 Go 堆指针，所以不存在被 GC 移动的问题。
// 多绕一层指针转换只是为了让 go vet 不误报 unsafe.Pointer(uintptr)。
func lparam(p uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&p))
}

// windowProc 是所有窗口消息的入口。
//
// 与上一版（Sciter）的「引擎优先」相反，这里是**宿主优先**：
// Composition 模式不接管输入，窗口收到什么消息引擎根本不知道——
// 没有子窗口，也没有引擎窗口过程可插。所以宿主必须自己把输入转过去
// （鼠标 SendMouseInput，焦点 MoveFocus），再处理窗口形态。
//
// 顺序：宿主独占命令 → 输入转发 → 窗口形态 → DefWindowProc。
// 前三段都在"引擎之前"，因为引擎压根不在消息链上。
func windowProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	w := lookupWindow(hwnd)
	if w == nil {
		ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
		return ret
	}

	// ---- 一、宿主独占的命令消息（引擎不参与） ----
	switch uint32(msg) {
	case wmClose:
		if w.OnClose != nil && !w.OnClose() {
			return 0
		}
		procDestroyWindow.Call(hwnd)
		return 0

	case wmTray:
		// 通知区域的鼠标回调。
		if w.tray != nil {
			w.tray.onMessage(lParam)
		}
		return 0

	case wmCommand:
		// 目前只有托盘菜单会发 WM_COMMAND。
		if w.tray != nil {
			w.tray.onCommand(uint32(wParam & 0xFFFF))
		}
		return 0

	case wmApp:
		w.drain()
		return 0

	case wmDestroy:
		// 先收页面，再收窗口：控制器关掉之后引擎会把它的视觉树摘掉，
		// 顺序反了会在已经销毁的窗口上操作合成目标。
		if w.ctrl != nil {
			w.ctrl.Close()
			w.ctrl = nil
		}
		if w.tray != nil {
			w.tray.remove()
		}
		unregisterWindow(hwnd)
		if w.OnDestroy != nil {
			w.OnDestroy()
		}
		return 0
	}

	// ---- 二、输入转发 ----
	// Composition 模式没有子窗口替我们收消息，页面里的 hover / 点击 /
	// 拖拽全靠这里转过去。穿透窗口收不到这些消息，天然就不用管。
	switch uint32(msg) {
	case wmMouseMove:
		// 只记数：拖拽"跟不跟手"要看窗口收了几条移动、前端又算了几帧，
		// 两个数一比就知道丢在哪一层。
		w.moveMsgs++
		if debugInput && w.clickThrough && (w.moveMsgs <= 5 || w.moveMsgs%50 == 0) {
			log.Printf("窗口 %q 收到第 %d 条 WM_MOUSEMOVE（穿透窗口不该收到）", w.title, w.moveMsgs)
		}
		w.trackLeave()
		w.forwardMouse(uint32(msg), wParam, lParam)

	case wmMouseLeave:
		w.tracking = false
		if w.ctrl != nil {
			w.ctrl.SendMouse(webview2.MouseLeave, 0, 0, 0, 0)
		}

	case wmLButtonDown, wmLButtonUp, wmLButtonDblClk, wmRButtonDown,
		wmRButtonUp, wmMButtonDown, wmMButtonUp, wmMouseWheel:
		if debugInput && w.clickThrough {
			log.Printf("窗口 %q 收到 %s（客户区 %d,%d）", w.title, mouseMsgName(uint32(msg)),
				int32(int16(lParam&0xFFFF)), int32(int16((lParam>>16)&0xFFFF)))
		}
		w.forwardMouse(uint32(msg), wParam, lParam)
	}

	// ---- 三、窗口形态与几何记账 ----
	switch uint32(msg) {
	case wmNCCalcSize:
		// 返回 0 表示"客户区 = 整个窗口"，把标题栏和边框全部抹掉。
		if wParam != 0 {
			return 0
		}

	case wmNCHitTest:
		w.hitTests++
		// 穿透的正路是 WS_EX_LAYERED|WS_EX_TRANSPARENT：系统在命中测试
		// **之前**就把点让出去了，这个分支根本不会被问到。
		// 留着纯属兜底——万一分层没设上，至少别让点落进 Chromium 里。
		// 它不能当主手段：HTTRANSPARENT 只对同线程窗口链有效，
		// 跨进程时点击会被直接丢弃（既不到自己也不到下面）。
		if w.clickThrough {
			return ^uintptr(0) // (LRESULT)-1 == HTTRANSPARENT
		}
		// 缩放边框交给系统，不由前端算：回答 HTLEFT 之类的值之后，
		// 光标由系统自己设、缩放循环由系统自己跑，前端一行都不用管。
		if code, ok := w.resizeHit(lParam); ok {
			return code
		}

	case wmSetCursor:
		// 客户区里的光标归宿主：引擎通过 CursorChanged 给了句柄，
		// 但不会自己 SetCursor；DefWindowProc 又只认得窗口类的箭头。
		// 非客户区（缩放边框）必须放行，让系统设缩放箭头。
		if uint32(lParam&0xFFFF) == htClient && w.hcursor != 0 {
			w.applyCursor()
			return 1
		}

	case wmSetFocus:
		// 页面要拿到键盘焦点才能输入中文。Composition 模式没有子窗口，
		// 焦点得显式交给引擎。
		if w.ctrl != nil {
			w.ctrl.Focus()
		}

	case wmGetMinMaxInfo:
		mmi := (*minMaxInfo)(lparam(lParam))
		mmi.MinTrackSize.X = int32(float64(w.minW) * w.scale)
		mmi.MinTrackSize.Y = int32(float64(w.minH) * w.scale)
		return 0

	case wmSize:
		// 最小化时客户区为 0，跳过。
		if wParam != sizeMinimized {
			w.sizeMsgs++
			w.syncContent(false)
		}

	case wmExitSizeMove:
		// 移动/缩放结束：几何稳定了，可以落盘。
		// 先把最后那一帧尺寸补上（拖动中可能被节流跳过），再结算读数。
		w.syncContent(true)
		w.flushResizeStats()
		// 顺手刷新分层窗口的合成——拖动过程中为了不卡帧，走的是
		// SetPositionQuiet，DWM 那边没被推过，松手后必须补一次，
		// 否则旧位置会一直留着残影、还照样接鼠标。
		w.refreshLayered()
		if w.OnSettle != nil {
			w.OnSettle()
		}

	case wmDpiChanged:
		w.scale = float64(uint32(wParam)&0xFFFF) / 96.0
		// 启动阶段的这次变更来自"把窗口挪到目标显示器"，此时尺寸刚按存盘值设好，
		// 再套用系统建议矩形会让窗口偏离记录值，所以只在稳定后才接受。
		if !w.settled {
			return 0
		}
		if r := (*rect)(lparam(lParam)); r != nil {
			procSetWindowPos.Call(hwnd, 0,
				uintptr(int(r.Left)), uintptr(int(r.Top)),
				uintptr(int(r.Right-r.Left)), uintptr(int(r.Bottom-r.Top)),
				swpNoZOrder|swpNoActivate)
		}
		w.syncContent(true)
		return 0
	}

	ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return ret
}

// mouseMsgName 给日志用的鼠标消息名。
//
// 穿透验证要看"到底哪条消息进了窗口过程"，日志里出现中文名比数字好认。
func mouseMsgName(msg uint32) string {
	switch msg {
	case wmLButtonDown:
		return "WM_LBUTTONDOWN"
	case wmLButtonUp:
		return "WM_LBUTTONUP"
	case wmLButtonDblClk:
		return "WM_LBUTTONDBLCLK"
	case wmRButtonDown:
		return "WM_RBUTTONDOWN"
	case wmRButtonUp:
		return "WM_RBUTTONUP"
	case wmMButtonDown:
		return "WM_MBUTTONDOWN"
	case wmMButtonUp:
		return "WM_MBUTTONUP"
	case wmMouseWheel:
		return "WM_MOUSEWHEEL"
	}
	return "?"
}
