package webview

import (
	"golang.org/x/sys/windows"
)

// 本文件只放 Win32 的裸绑定与常量，不含业务逻辑。
// 之所以不用第三方窗口库：宿主窗口将来要支持无边框、多面板、每面板一个
// WebView，需要完全掌控消息循环，框架反而是负担。

var (
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")
	modGdi32    = windows.NewLazySystemDLL("gdi32.dll")
	modDwmapi   = windows.NewLazySystemDLL("dwmapi.dll")
	modShell32  = windows.NewLazySystemDLL("shell32.dll")
	modOle32    = windows.NewLazySystemDLL("ole32.dll")

	procRegisterClassExW      = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW       = modUser32.NewProc("CreateWindowExW")
	procDefWindowProcW        = modUser32.NewProc("DefWindowProcW")
	procGetMessageW           = modUser32.NewProc("GetMessageW")
	procTranslateMessage      = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW      = modUser32.NewProc("DispatchMessageW")
	procPostQuitMessage       = modUser32.NewProc("PostQuitMessage")
	procPostMessageW          = modUser32.NewProc("PostMessageW")
	procShowWindow            = modUser32.NewProc("ShowWindow")
	procSetLayeredWindowAttrs = modUser32.NewProc("SetLayeredWindowAttributes")
	procUpdateWindow          = modUser32.NewProc("UpdateWindow")
	procSetWindowPos          = modUser32.NewProc("SetWindowPos")
	procEnumWindows           = modUser32.NewProc("EnumWindows")
	procFindWindowExW         = modUser32.NewProc("FindWindowExW")
	procGetWindow             = modUser32.NewProc("GetWindow")
	procGetClassNameW         = modUser32.NewProc("GetClassNameW")
	procSystemParametersInfoW = modUser32.NewProc("SystemParametersInfoW")
	procGetWindowRect         = modUser32.NewProc("GetWindowRect")
	procGetClientRect         = modUser32.NewProc("GetClientRect")
	procScreenToClient        = modUser32.NewProc("ScreenToClient")
	procTrackMouseEvent       = modUser32.NewProc("TrackMouseEvent")
	procGetDpiForWindow       = modUser32.NewProc("GetDpiForWindow")
	procGetDpiForSystem       = modUser32.NewProc("GetDpiForSystem")
	procSetForegroundWindow   = modUser32.NewProc("SetForegroundWindow")
	procDestroyWindow         = modUser32.NewProc("DestroyWindow")
	procGetSystemMetrics      = modUser32.NewProc("GetSystemMetrics")
	procGetWindowLongPtrW     = modUser32.NewProc("GetWindowLongPtrW")
	// GetLayeredWindowAttributes 是"到底是不是分层窗口"的唯一权威读数：
	// WS_EX_LAYERED 只是样式位，属性没设上就只是个摆设。
	procGetLayeredAttrs = modUser32.NewProc("GetLayeredWindowAttributes")
	procReleaseCapture  = modUser32.NewProc("ReleaseCapture")
	procSendMessageW    = modUser32.NewProc("SendMessageW")
	procGetCursorPos    = modUser32.NewProc("GetCursorPos")
	procSetCursor       = modUser32.NewProc("SetCursor")
	procIsWindowVisible = modUser32.NewProc("IsWindowVisible")
	procLoadIconW       = modUser32.NewProc("LoadIconW")
	procLoadImageW      = modUser32.NewProc("LoadImageW")
	procCreatePopupMenu = modUser32.NewProc("CreatePopupMenu")
	procAppendMenuW     = modUser32.NewProc("AppendMenuW")
	procTrackPopupMenu  = modUser32.NewProc("TrackPopupMenu")
	procDestroyMenu     = modUser32.NewProc("DestroyMenu")

	procSetWindowLongPtrW = modUser32.NewProc("SetWindowLongPtrW")

	// 穿透验证用：WindowFromPoint 是**顶层**路由（不看 WS_EX_TRANSPARENT），
	// 用来判断子窗口归属会得出反向结论，所以两个都只当参考，不当凭据。
	procGetDesktopWindow = modUser32.NewProc("GetDesktopWindow")
	procWindowFromPoint  = modUser32.NewProc("WindowFromPoint")

	// GetParent 用来还原"谁接住了鼠标"：WebView2 会在宿主下建
	// Chrome_WidgetWin_0，只有沿祖先链走到宿主才能确定那一点是我们的。
	procGetParent = modUser32.NewProc("GetParent")
	// GetWindowThreadProcessId 用来认领"这个窗口是哪个进程的"：
	// WebView2 的输入窗口在浏览器进程里，靠 pid 才能把它和别的
	// Chromium 窗口（Electron 应用之类）分开。
	procGetWindowThreadPID = modUser32.NewProc("GetWindowThreadProcessId")

	procShellNotifyIconW = modShell32.NewProc("Shell_NotifyIconW")
	// 选文件夹用 SHBrowseForFolderW 而不是 IFileOpenDialog：
	// 前者是一次调用加一个结构体，不需要自己实现 COM 接口。
	procSHBrowseForFolderW  = modShell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDList = modShell32.NewProc("SHGetPathFromIDListW")
	procShellExecuteW       = modShell32.NewProc("ShellExecuteW")

	procCoTaskMemFree = modOle32.NewProc("CoTaskMemFree")

	procGetModuleHandleW = modKernel32.NewProc("GetModuleHandleW")
	procCreateSolidBrush = modGdi32.NewProc("CreateSolidBrush")
	// 读显示器刷新率：拖动时"多久同步一次尺寸"要按屏幕的节拍来定。
	procGetDC         = modUser32.NewProc("GetDC")
	procReleaseDC     = modUser32.NewProc("ReleaseDC")
	procGetDeviceCaps = modGdi32.NewProc("GetDeviceCaps")

	procDwmSetWindowAttribute = modDwmapi.NewProc("DwmSetWindowAttribute")
	// 只用来读回圆角偏好做断言：这个属性是少数几个可读的 DWM 属性之一。
	procDwmGetWindowAttribute = modDwmapi.NewProc("DwmGetWindowAttribute")
	procDwmFlush              = modDwmapi.NewProc("DwmFlush")
)

const (
	wsPopup        = 0x80000000
	wsThickFrame   = 0x00040000
	wsMinimizeBox  = 0x00020000
	wsClipChildren = 0x02000000

	wsExToolWindow = 0x00000080
	// WS_EX_TOPMOST：窗口在置顶层。它**只能读**——写要靠 SetWindowPos
	// 传 HWND_TOPMOST，直接改扩展样式位不会真的把窗口抬上去。
	// 读它是判断"置顶到底生没生效"唯一靠得住的凭据。
	wsExTopmost = 0x00000008
	// WS_EX_NOACTIVATE：点击不抢焦点，样式声明 activation=noActivate 时用。
	wsExNoActivate = 0x08000000
	// WS_EX_TRANSPARENT：鼠标消息穿透到下面的窗口。
	// 只给 layer=toast（内置悬浮提示栏）用，词表里没有对应字段。
	wsExTransparent = 0x00000020
	// WS_EX_LAYERED 必须和 WS_EX_TRANSPARENT 一起用：单独设透明位只影响
	// 绘制顺序，命中测试照样命中；分层窗口带透明位时系统才会跳过它。
	wsExLayered = 0x00080000
	// WS_EX_NOREDIRECTIONBITMAP：窗口不参与 DWM 重定向，内容由
	// DirectComposition 自己合成。WebView2 Composition 的硬性前提。
	// 实测它和 WS_EX_LAYERED 并不互斥（与常见说法相反）。
	wsExNoRedirectionBitmap = 0x00200000
	// LWA_ALPHA：用常量 alpha 合成，255 等于"看不出区别"，
	// 目的只是把窗口变成分层窗口。
	lwaAlpha = 0x00000002

	cwUseDefault = 0x80000000

	swShow = 5
	// SW_SHOWNOACTIVATE：显示但不激活。不抢焦点的窗口必须用它，
	// 否则 SW_SHOW 会顺手把焦点抢过来。
	swShowNoActivate = 4

	// HWND_TOPMOST / HWND_NOTOPMOST / HWND_BOTTOM 是特殊句柄值。
	hwndTopMost   = ^uintptr(0)
	hwndNoTopMost = ^uintptr(0) - 1
	hwndBottom    = 1

	// GetWindow 的取值：GW_HWNDNEXT 取 Z 序下一个兄弟窗口，
	// GW_HWNDPREV 取上一个（返回 0 说明自己就是同级里最上面那个）。
	gwHwndNext = 2
	gwHwndPrev = 3
	gwChild    = 5

	// SPI_GETWORKAREA：主显示器去掉任务栏后的可用区域。
	spiGetWorkArea = 0x0030

	swpNoSize     = 0x0001
	swpNoMove     = 0x0002
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010
	// swpFrameChanged 让系统重算窗口框（包括命中测试的登记）。
	swpFrameChanged = 0x0020
	// swpNoSendChanging 压掉 WM_WINDOWPOSCHANGING。
	// 拖动时每一帧都要挪窗口，而这条通知是同步派发的——站在引擎回调里
	// 发它就等于重入引擎。压掉之后可以当场挪，不用排 UI 队列。
	swpNoSendChanging = 0x0400

	wmDestroy       = 0x0002
	wmSize          = 0x0005
	wmClose         = 0x0010
	wmGetMinMaxInfo = 0x0024
	wmNCCalcSize    = 0x0083
	wmNCHitTest     = 0x0084
	wmNCLButtonDown = 0x00A1
	wmSysCommand    = 0x0112
	// 客户区鼠标消息。穿透窗口正常情况下永远收不到它们，
	// 收到了就说明点击被吃掉了——验证穿透时拿它当证据。
	wmLButtonDown = 0x0201
	// wmSetCursor 的低字是命中测试码。客户区里必须自己拿住，
	// 否则 DefWindowProc 会用窗口类的箭头光标盖掉引擎刚设好的那个。
	wmSetCursor  = 0x0020
	wmDpiChanged = 0x02E0
	// WM_EXITSIZEMOVE 在用户松开鼠标、移动/缩放循环结束时到达，
	// 是记录窗口几何最合适的时机（WM_MOVE 会连发几百次）。
	wmExitSizeMove = 0x0232
	wmApp          = 0x8000

	scMinimize = 0xF020

	// 命中测试返回值。无边框窗口要自己判断拖动区和缩放边框。
	htClient = 1

	htCaption     = 2
	htLeft        = 10
	htRight       = 11
	htTop         = 12
	htTopLeft     = 13
	htTopRight    = 14
	htBottom      = 15
	htBottomLeft  = 16
	htBottomRight = 17

	// DWMWA_WINDOW_CORNER_PREFERENCE / DWMWCP_*
	//
	// 这一个属性同时管两件事，实测过：设成 ROUND 时 DWM 会给窗口加
	// 圆角**和一圈投影**；设成 DONOTROUND 时两者一起消失（实测同一条
	// 扫描线上的像素从 31→26 的渐变直接变成全 31 的平值）。
	// 所以"要不要窗口投影"没有单独的开关，只能靠这一位表达。
	dwmwaWindowCornerPreference = 33
	dwmwcpDoNotRound            = 1
	dwmwcpRound                 = 2

	sizeMinimized = 1

	// 托盘图标。wmTray 是 Shell_NotifyIcon 的回调消息，取 WM_APP+1，
	// 因为 WM_APP 本身已经被 Post 队列占用。
	nimAdd     = 0
	nimDelete  = 2
	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	wmTray        = 0x8001
	wmCommand     = 0x0111
	wmContextMenu = 0x007B
	wmLButtonUp   = 0x0202
	wmRButtonUp   = 0x0205
	wmMouseMove   = 0x0200

	// Composition 模式不接管输入：客户区里的鼠标消息要由宿主转给引擎。
	// 下面几个是转发时要区分的种类。
	wmMouseLeave    = 0x02A3
	wmLButtonDblClk = 0x0203
	wmRButtonDown   = 0x0204
	wmMButtonDown   = 0x0207
	wmMButtonUp     = 0x0208
	wmMouseWheel    = 0x020A
	wmSetFocus      = 0x0007

	mfString    = 0x00000000
	mfChecked   = 0x00000008
	mfSeparator = 0x00000800

	tpmRightButton = 0x0002
	tpmBottomAlign = 0x0020

	// 图标资源。app.rc 里以 id 1 编进 exe，同时是 Explorer 显示的文件图标。
	iconResourceID = 1
	imageIcon      = 1
	// lrShared 让系统缓存图标句柄，调用方不必（也不能）DestroyIcon。
	lrShared = 0x8000

	// SHBrowseForFolderW 的标志位。
	bifReturnOnlyFSDirs = 0x0001
	bifEditBox          = 0x0010
	bifNewDialogStyle   = 0x0040

	swShownormal = 1

	// 资源缺失时的兜底：IDI_APPLICATION。
	idiApplication = 32512

	swHide    = 0
	swRestore = 9

	// 图标尺寸。图标是正方形的，宽高取同一个值即可。
	smCXScreen = 0
	smCYScreen = 1
	smCXIcon   = 11
	smCXSmIcon = 49

	// paperColor 是窗口类背景刷的颜色（COLORREF = 0x00BBGGRR），
	// 与前端 --paper 保持一致，避免 WebView2 就绪前的白闪。
	paperColor = 0x00F0FBFF
)

// wndClassExW 对应 WNDCLASSEXW。字段顺序与对齐必须与 C 结构体一致。
type wndClassExW struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type rect struct {
	Left, Top, Right, Bottom int32
}

type point struct {
	X, Y int32
}

// msgT 对应 MSG。x64 下总长 48 字节。
type msgT struct {
	Hwnd    windows.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

type minMaxInfo struct {
	Reserved     point
	MaxSize      point
	MaxPosition  point
	MinTrackSize point
	MaxTrackSize point
}

// trackMouseEvent 对应 TRACKMOUSEEVENT。申请一次之后系统才会在指针
// 离开客户区时发 WM_MOUSELEAVE——不申请的话 hover 状态会一直挂着。
type trackMouseEvent struct {
	Size      uint32
	Flags     uint32
	HwndTrack uintptr
	HoverTime uint32
}

const tmeLeave = 0x00000002

// browseInfoW 对应 BROWSEINFOW（x64 下 64 字节）。
//
// 字段偏移：hwndOwner 0 / pidlRoot 8 / pszDisplayName 16 / lpszTitle 24 /
// ulFlags 32 / lpfn 40 / lParam 48 / iImage 56。ulFlags 后面的 4 字节与
// iImage 后面的 4 字节都是对齐填充，必须显式写出来。
type browseInfoW struct {
	Owner       uintptr
	Root        uintptr
	DisplayName *uint16
	Title       *uint16
	Flags       uint32
	_           uint32
	Callback    uintptr
	LParam      uintptr
	Image       int32
	_           int32
}

// notifyIconDataW 对应 NOTIFYICONDATAW（x64 下 976 字节）。
//
// 字段偏移必须与 C 结构体一致，所以对齐填充是显式写出来的：
// cbSize 0 / hWnd 8 / uID 16 / uFlags 20 / uCallbackMessage 24 / hIcon 32 /
// szTip 40 / dwState 296 / dwStateMask 300 / szInfo 304 / uTimeout 816 /
// szInfoTitle 820 / dwInfoFlags 948 / guidItem 952 / hBalloonIcon 968。
type notifyIconDataW struct {
	CbSize           uint32
	_                uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	_                uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UTimeout         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     uintptr
}

func utf16Ptr(s string) *uint16 {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return nil
	}
	return p
}
