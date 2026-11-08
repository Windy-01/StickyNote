package webview

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// trayItemBase 是动态菜单项的起始命令 ID。
// 0 以下留给系统，1..99 预留给将来的固定项。
const trayItemBase = 100

// TrayItem 是托盘菜单里的一项。
//
// 菜单每次弹出前现算，所以"显示/隐藏"这类文案能反映当前状态，
// 组件数量变化也不需要重建菜单对象。
type TrayItem struct {
	Label     string
	Checked   bool
	Separator bool
	OnClick   func()
}

// TrayHooks 由内核实现。
//
// 托盘只负责"用户点了什么"，要显示什么、点了做什么全部交出去。
type TrayHooks struct {
	// Items 每次弹菜单时调用，返回当前应有的菜单项。
	Items func() []TrayItem
	// ToggleShow 响应左键单击：通常切换第一个实例的显隐。
	ToggleShow func()
}

// tray 封装 Shell_NotifyIcon。
//
// 图标挂在宿主窗口上：通知区域的消息会发给宿主 HWND，
// 由 windowProc 的 wmTray 分支转进 onMessage。
type tray struct {
	win   *Window
	hooks TrayHooks
	nid   notifyIconDataW
	added bool

	// items 是上一次弹出的菜单项，用于把命令 ID 映射回动作。
	items []TrayItem
}

func newTray(win *Window, hooks TrayHooks) *tray {
	return &tray{win: win, hooks: hooks}
}

// install 把图标加到通知区域。
func (t *tray) install() {
	t.nid = notifyIconDataW{
		HWnd:             t.win.hwnd,
		UID:              1,
		UFlags:           nifMessage | nifIcon | nifTip,
		UCallbackMessage: wmTray,
		HIcon:            loadIcon(systemIconSize(true)),
	}
	t.nid.CbSize = uint32(unsafe.Sizeof(t.nid))
	copy(t.nid.SzTip[:], windows.StringToUTF16("StickyNote 组件宿主"))

	ret, _, _ := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&t.nid)))
	t.added = ret != 0
}

// remove 撤掉图标。进程退出前必须调，否则通知区域会留一个幽灵图标，
// 要等鼠标划过才消失。
func (t *tray) remove() {
	if !t.added {
		return
	}
	t.added = false
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&t.nid)))
}

// onMessage 处理通知区域回传的鼠标消息。
// lParam 低 16 位是鼠标消息号，高 16 位是图标 ID。
func (t *tray) onMessage(lParam uintptr) {
	switch uint32(lParam & 0xFFFF) {
	case wmLButtonUp:
		if t.hooks.ToggleShow != nil {
			t.hooks.ToggleShow()
		}
	case wmRButtonUp, wmContextMenu:
		t.showMenu()
	}
}

// onCommand 处理菜单项点击，把命令 ID 映射回上一次菜单里的动作。
func (t *tray) onCommand(id uint32) {
	idx := int(id) - trayItemBase
	if idx < 0 || idx >= len(t.items) {
		return
	}
	if fn := t.items[idx].OnClick; fn != nil {
		fn()
	}
}

func (t *tray) showMenu() {
	if t.hooks.Items == nil {
		return
	}
	items := t.hooks.Items()
	if len(items) == 0 {
		return
	}
	t.items = items

	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer procDestroyMenu.Call(hMenu)

	for i, it := range items {
		if it.Separator {
			procAppendMenuW.Call(hMenu, mfSeparator, 0, 0)
			continue
		}
		appendMenuItem(hMenu, trayItemBase+i, it.Label, it.Checked)
	}

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))

	// 弹菜单前必须把窗口设为前台，否则点菜单外面菜单不会消失，
	// 会一直粘在屏幕上。
	procSetForegroundWindow.Call(t.win.hwnd)
	procTrackPopupMenu.Call(
		hMenu,
		tpmRightButton|tpmBottomAlign,
		// 坐标按有符号 int 传，只取低 32 位，多显示器负坐标才不会溢出。
		uintptr(uint32(pt.X)),
		uintptr(uint32(pt.Y)),
		0, t.win.hwnd, 0,
	)
	// 这条空消息是配套的收尾动作，否则菜单偶尔会残影。
	procPostMessageW.Call(t.win.hwnd, 0, 0, 0)
}

func appendMenuItem(hMenu uintptr, id int, text string, checked bool) {
	flags := uintptr(mfString)
	if checked {
		flags |= mfChecked
	}
	procAppendMenuW.Call(hMenu, flags, uintptr(id),
		uintptr(unsafe.Pointer(utf16Ptr(text))))
}
