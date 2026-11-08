package webview

import (
	"errors"
	"fmt"
	"log"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 桌面层（layer=desktop）的实现。
//
// 桌面不是"一个普通窗口"，它是 Explorer 画的一张壁纸，上面还压着图标视图。
// 想让组件**贴在桌面上、被任何应用窗口覆盖、Win+D 时跟着桌面显隐**，
// 直觉是挂成桌面窗口的子窗口——**这条路做不到穿透**，已否决：子窗口的命中
// 测试由父窗口的窗口过程做，而 Progman 找子窗口时不看 WS_EX_TRANSPARENT，
// 设了也白设（表现是点在组件上被吃掉，下面的图标一动不动）。
//
// 现在的做法是"顶层窗口 + Z 序锚在 Progman 正上方"：
// 在图标之上（图标是 Progman 的子窗口，顶层窗口天然盖住它），
// 又在所有普通窗口之下（新窗口一来就压住我们）。
//
// 桌面附近有两个容易搞混的容器：
//
//	壁纸层（空 WorkerW）   → 在图标**之下**，动态壁纸用，点不到
//	图标宿主（含 DefView） → 在图标**之上**，桌面组件该待的地方
//
// 组件是装饰，压在上面但**不接鼠标**：窗口带 WS_EX_LAYERED|TRANSPARENT，
// 点击落回下面的图标。

// hasChildClass 判断窗口是否有指定类名的直接子窗口。
func hasChildClass(hwnd uintptr, class string) bool {
	v, _, _ := procFindWindowExW.Call(
		hwnd, 0, uintptr(unsafe.Pointer(utf16Ptr(class))), 0,
	)
	return v != 0
}

// attachToDesktop 解析出桌面宿主（Progman）并返回，供之后锚定 Z 序用。
//
// 这里**不动 Z 序**：隐藏窗口上的 Z 序变更会被系统丢弃，
// 真正落层在 Show 之后做（placeAtDesktopLevel）。
func attachToDesktop() (uintptr, error) {
	progman, _, _ := procFindWindowExW.Call(
		0, 0, uintptr(unsafe.Pointer(utf16Ptr("Progman"))), 0,
	)
	if progman == 0 {
		return 0, errors.New("找不到 Progman")
	}
	return progman, nil
}

// placeAtDesktopLevel 把窗口的 Z 序锚到 Progman 正上方。
//
// hWndInsertAfter 的语义是"排在这个窗口**之下**"，所以锚点要取 Progman
// 上面那个窗口（GW_HWNDPREV）；传 Progman 本身会沉到它下面去（壁纸之后，看不见）。
//
// 必须在 Show 之后调用：隐藏窗口上的 Z 序变更会被系统丢弃。
func placeAtDesktopLevel(hwnd, progman uintptr) error {
	anchor, _, _ := procGetWindow.Call(progman, gwHwndPrev)
	usedBottom := false
	if anchor == 0 {
		// Progman 已经是最上层（Win+D 之后常见）：没有比它更高的锚点，
		// 退一步用 HWND_BOTTOM，至少保证在所有普通窗口之下。
		anchor = hwndBottom
		usedBottom = true
	}
	r, _, err := procSetWindowPos.Call(hwnd, anchor, 0, 0, 0, 0,
		swpNoMove|swpNoSize|swpNoActivate)
	// 锚点取错会让窗口沉到壁纸后面（看不见）或者浮到应用窗口前面（挡路），
	// 两种都是静默的，所以把实际结果打出来。
	below, _, _ := procGetWindow.Call(hwnd, gwHwndNext)
	log.Printf("落桌面层：progman=%#x 锚点=%#x(用了BOTTOM=%v) 调用成功=%v 下方=%#x(%s)",
		progman, anchor, usedBottom, r != 0, below, windowClass(below))
	if r == 0 {
		return fmt.Errorf("调整桌面层 Z 序失败: %w", err)
	}
	return nil
}

// windowClass 返回窗口的类名，自检日志用它证明"真的挂到了桌面上"。
func windowClass(hwnd uintptr) string {
	if hwnd == 0 {
		return ""
	}
	var buf [64]uint16
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// workArea 返回主显示器去掉任务栏后的可用区域（物理像素）。
func workArea() rect {
	var r rect
	if ret, _, _ := procSystemParametersInfoW.Call(
		spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0,
	); ret == 0 {
		sw, _, _ := procGetSystemMetrics.Call(smCXScreen)
		sh, _, _ := procGetSystemMetrics.Call(smCYScreen)
		return rect{Left: 0, Top: 0, Right: int32(sw), Bottom: int32(sh)}
	}
	return r
}

// ---------------------------------------------------------------------------
// 穿透
//
// 只有一条可靠写法：WS_EX_LAYERED | WS_EX_TRANSPARENT + SetLayeredWindowAttributes。
// 系统在**命中测试阶段**按这个扩展样式把点让给下面的窗口，与窗口过程无关。
//
// 两个常见误区：
//   - 以为 WS_EX_TRANSPARENT 只管绘制顺序 —— 它确实也影响绘制，但让点是它的本职；
//   - 以为要在 WM_NCHITTEST 里回 HTTRANSPARENT —— 那条路只对同线程窗口链有效，
//     实测点击会被直接丢弃（既不到自己也不到下面）。
//
// 验证也别问窗口自己（它愿意怎么回答都行），更别用 WindowFromPoint
// （不看这个标志，一律返回最上层），ChildWindowFromPointEx 也不行
// （它不看 DWM cloak，会返回没被合成的 UWP 窗口）。唯一可信的读数是
// "鼠标消息有没有进窗口过程"——见 diag_cursor.go 的光标探针与 cmd/winprobe。
// ---------------------------------------------------------------------------
