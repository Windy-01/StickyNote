// winprobe 是只读的窗口栈探针：回答"这个屏幕点上，谁在吃鼠标"。
//
// 为什么需要它：排查穿透时我们手上的三个读数都会骗人。
//   - WindowFromPoint 不看 WS_EX_TRANSPARENT，返回的可能是自己；
//   - ChildWindowFromPointEx 不看 DWM cloak，会返回一个根本没被合成的
//     UWP 窗口（StartMenuExperienceHost 之类），看起来像"有东西压着我们"；
//   - 问窗口自己的 WM_NCHITTEST 只能得到它愿意回答的值。
//
// 唯一可信的办法是把 Z 序从最上层往下走一遍，逐层打出类名、矩形、
// 扩展样式、是否可见、是否被 DWM cloak。压在我们上面的到底是谁，
// 一眼就能看出来。全程只读，不动任何窗口。
//
// 用法：
//
//	winprobe -x 175 -y 667            # 看点 (175,667) 上的窗口栈
//	winprobe -class StickyNoteHostWindow   # 看我们的窗口在哪一层
package main

import (
	"flag"
	"fmt"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	dwmapi  = windows.NewLazySystemDLL("dwmapi.dll")
	kernel3 = windows.NewLazySystemDLL("kernel32.dll")

	procGetTopWindow       = user32.NewProc("GetTopWindow")
	procGetWindow          = user32.NewProc("GetWindow")
	procGetWindowRect      = user32.NewProc("GetWindowRect")
	procGetClassNameW      = user32.NewProc("GetClassNameW")
	procGetWindowTextW     = user32.NewProc("GetWindowTextW")
	procGetWindowLongPtrW  = user32.NewProc("GetWindowLongPtrW")
	procIsWindowVisible    = user32.NewProc("IsWindowVisible")
	procGetWindowThreadPID = user32.NewProc("GetWindowThreadProcessId")
	procWindowFromPoint    = user32.NewProc("WindowFromPoint")
	procChildWindowFromPt  = user32.NewProc("ChildWindowFromPointEx")
	procGetLayeredAttrs    = user32.NewProc("GetLayeredWindowAttributes")
	procSetWindowLongPtrW  = user32.NewProc("SetWindowLongPtrW")
	procSetWindowPos       = user32.NewProc("SetWindowPos")
	procGetParent          = user32.NewProc("GetParent")
	procEnumWindows        = user32.NewProc("EnumWindows")
	procGetDesktopWindow   = user32.NewProc("GetDesktopWindow")
	procFindWindowExW      = user32.NewProc("FindWindowExW")
	procGetCursorPos       = user32.NewProc("GetCursorPos")

	procDwmGetWindowAttribute = dwmapi.NewProc("DwmGetWindowAttribute")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
	// 未文档化导出，Win10 起做 acrylic 的常用入口。比 DWM 的 system backdrop
	// 多一个好处：tint 颜色和透明度由调用方给，不跟系统主题走。
	procSetWindowCompositionAttribute = user32.NewProc("SetWindowCompositionAttribute")

	// DPI 感知必须在任何窗口/坐标调用之前设：不设的话进程是 96 DPI 虚拟化的，
	// GetWindowRect 和 WindowFromPoint 拿到的是被 1.25 除过的逻辑坐标，
	// 与宿主日志里的物理像素对不上，量出来的位置全是错的。
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
)

const (
	gwHwndNext = 2
	gwHwndPrev = 3
	gwOwner    = 4

	wsExToolWindow          = 0x00000080
	wsExTransparent         = 0x00000020
	wsExLayered             = 0x00080000
	wsExNoRedirectionBitmap = 0x00200000
	wsExNoActivate          = 0x08000000
	wsExTopmost             = 0x00000008

	dwmwaCloaked = 14
	// DWMWA_EXTENDED_FRAME_BOUNDS：DWM 那边的窗口矩形。
	// 它和 GetWindowRect 不一致，就说明窗口在 user32 里已经挪了、
	// DWM 还没跟上——"新位置正常、旧位置残留"的典型成因。
	dwmwaExtendedFrameBounds = 9
)

type rect struct{ Left, Top, Right, Bottom int32 }
type point struct{ X, Y int32 }

func className(hwnd uintptr) string {
	var buf [128]uint16
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:n])
}

func windowText(hwnd uintptr) string {
	var buf [128]uint16
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:n])
}

func exStyle(hwnd uintptr) uint32 {
	idx := int32(-20)
	ret, _, _ := procGetWindowLongPtrW.Call(hwnd, uintptr(idx))
	return uint32(ret)
}

func bounds(hwnd uintptr) rect {
	var r rect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r
}

func pidOf(hwnd uintptr) uint32 {
	var pid uint32
	procGetWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}

// cloaked 返回 DWM 是否把这个窗口藏起来了。
//
// 这个读数很关键：UWP 挂起、虚拟桌面切换都会 cloak，被 cloak 的窗口
// IsWindowVisible 仍然是 true、矩形也还在，但它根本不在屏幕上，
// 也不可能收到鼠标。不看这一位就会把 StartMenuExperienceHost 当成"挡路的"。
func cloaked(hwnd uintptr) int32 {
	var v int32
	procDwmGetWindowAttribute.Call(hwnd, dwmwaCloaked,
		uintptr(unsafe.Pointer(&v)), uintptr(unsafe.Sizeof(v)))
	return v
}

// exFlags 把扩展样式翻成人读的短名。
func exFlags(ex uint32) string {
	var s []string
	if ex&wsExTopmost != 0 {
		s = append(s, "TOPMOST")
	}
	if ex&wsExNoActivate != 0 {
		s = append(s, "NOACTIVE")
	}
	if ex&wsExLayered != 0 {
		s = append(s, "LAYERED")
	}
	if ex&wsExTransparent != 0 {
		s = append(s, "TRANSPARENT")
	}
	if ex&wsExToolWindow != 0 {
		s = append(s, "TOOLWIN")
	}
	if ex&wsExNoRedirectionBitmap != 0 {
		s = append(s, "NOREDIR")
	}
	if len(s) == 0 {
		return "-"
	}
	return strings.Join(s, "|")
}

func contains(r rect, p point) bool {
	return p.X >= r.Left && p.X < r.Right && p.Y >= r.Top && p.Y < r.Bottom
}

// stackAt 返回"这个点上，从最上层往下"的窗口清单。
//
// 顶层窗口的 Z 序就是 GetTopWindow + GW_HWNDNEXT 这条链，
// 第一个可见、没被 cloak、矩形包含该点的窗口就是系统会去问的那个。
func stackAt(p point, limit int) {
	hwnd, _, _ := procGetTopWindow.Call(0)
	n := 0
	fmt.Printf("=== 点 (%d,%d) 上的顶层窗口栈（从上往下）===\n", p.X, p.Y)
	for hwnd != 0 && n < limit {
		r := bounds(hwnd)
		vis, _, _ := procIsWindowVisible.Call(hwnd)
		cl := cloaked(hwnd)
		if contains(r, p) {
			mark := "  "
			if vis != 0 && cl == 0 {
				mark = ">>" // 这一层是系统真正会命中的
			}
			fmt.Printf("%s %#010x %-32s pid=%-6d (%d,%d)-(%d,%d) vis=%v cloak=%d ex=%s\n",
				mark, hwnd, className(hwnd), pidOf(hwnd),
				r.Left, r.Top, r.Right, r.Bottom, vis != 0, cl, exFlags(exStyle(hwnd)))
			n++
		}
		hwnd, _, _ = procGetWindow.Call(hwnd, gwHwndNext)
	}
	if n == 0 {
		fmt.Println("  （这个点上没有任何顶层窗口）")
	}
}

// layeredAttrs 读回分层窗口的 alpha/key/flags。
//
// 这是"到底是不是分层窗口"的唯一权威读数：WS_EX_LAYERED 只是一个样式位，
// 它有没有真的生效要看分层属性有没有设上。返回 false 就说明这个窗口
// 压根不是分层的——这种情况下 WS_EX_TRANSPARENT 不会让鼠标穿透，
// 但样式位读起来"全对"，是最容易骗过自己的地方。
func layeredAttrs(hwnd uintptr) string {
	var key, alpha, flags uint32
	r, _, err := procGetLayeredAttrs.Call(hwnd,
		uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&alpha)),
		uintptr(unsafe.Pointer(&flags)))
	if r == 0 {
		return fmt.Sprintf("未生效(GetLayeredWindowAttributes 失败: %v)", err)
	}
	mode := "LWA_COLORKEY"
	if flags&2 != 0 {
		mode = "LWA_ALPHA"
	}
	return fmt.Sprintf("%s alpha=%d key=%#x", mode, alpha, key)
}

// dwmBounds 读 DWM 侧的窗口矩形。
func dwmBounds(hwnd uintptr) string {
	var r rect
	hr, _, _ := procDwmGetWindowAttribute.Call(hwnd, dwmwaExtendedFrameBounds,
		uintptr(unsafe.Pointer(&r)), uintptr(unsafe.Sizeof(r)))
	if hr != 0 {
		return fmt.Sprintf("读不到(hr=%#x)", hr)
	}
	return fmt.Sprintf("(%d,%d)-(%d,%d) %dx%d", r.Left, r.Top, r.Right, r.Bottom,
		r.Right-r.Left, r.Bottom-r.Top)
}

// setTransparent 试着给**别的进程**的窗口加 WS_EX_TRANSPARENT。
//
// 这是全项目唯一一个会改动别人窗口的地方，所以放在探针里先单独验证：
// SetWindowLongPtr 对跨进程窗口到底允不允许、改完会不会被对方改回去。
// 改的是 msedgewebview2 那个"输入窗口"——它盖在我们窗口正上方、
// 分层却不透明，鼠标全被它吃掉（见 -class 的输出）。
func setTransparent(hwnd uintptr) {
	idx := int32(-20) // GWL_EXSTYLE
	before, _, _ := procGetWindowLongPtrW.Call(hwnd, uintptr(idx))
	after, _, err := procSetWindowLongPtrW.Call(hwnd, uintptr(idx), before|wsExTransparent)
	back, _, _ := procGetWindowLongPtrW.Call(hwnd, uintptr(idx))
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		0x0001|0x0002|0x0004|0x0010|0x0020) // NOSIZE|NOMOVE|NOZORDER|NOACTIVATE|FRAMECHANGED
	fmt.Printf("给 %#010x %s 加 WS_EX_TRANSPARENT：\n  前=%s\n  SetWindowLongPtr 返回=%#x err=%v\n  读回=%s\n",
		hwnd, className(hwnd), exFlags(uint32(before)), after, err, exFlags(uint32(back)))
}

// dwmAttr 读写一个 DWM 窗口属性（int32 值），先读、再写、再读回。
//
// 为什么要有这个：DWM 投影不是我们画的东西，改 CSS 一辈子也去不掉。
// 能不能去掉只有试一次才知道，所以把它做成一次可复现的开关，
// 让"改完到底有没有变"变成一个能当场量到的读数，而不是靠猜。
func dwmAttr(hwnd uintptr, attr int32, val int32) {
	var before int32
	hr, _, _ := procDwmGetWindowAttribute.Call(hwnd, uintptr(attr),
		uintptr(unsafe.Pointer(&before)), uintptr(unsafe.Sizeof(before)))
	fmt.Printf("DWM 属性 %d 写 %#010x %s：读回前 hr=%#x 值=%d\n",
		attr, hwnd, className(hwnd), hr, before)

	hr, _, err := procDwmSetWindowAttribute.Call(hwnd, uintptr(attr),
		uintptr(unsafe.Pointer(&val)), uintptr(unsafe.Sizeof(val)))
	fmt.Printf("  Set 返回 hr=%#x err=%v\n", hr, err)

	var after int32
	hr2, _, _ := procDwmGetWindowAttribute.Call(hwnd, uintptr(attr),
		uintptr(unsafe.Pointer(&after)), uintptr(unsafe.Sizeof(after)))
	fmt.Printf("  读回后 hr=%#x 值=%d\n", hr2, after)
}

// accentPolicy 对应 ACCENT_POLICY。
type accentPolicy struct {
	State         uint32
	Flags         uint32
	GradientColor uint32 // ABGR
	AnimationID   uint32
}

// winCompAttrData 对应 WINDOWCOMPOSITIONATTRIBDATA。
type winCompAttrData struct {
	Attrib uint32
	Data   unsafe.Pointer
	Size   uintptr
}

// setAccent 试 SetWindowCompositionAttribute 的 ACCENT_POLICY。
//
// state: 0=关 3=blurbehind 4=acrylicblurbehind 5=hostbackdrop
// color 是 ABGR（0xAABBGGRR）。tint 的 alpha 决定磨砂层的不透明度，
// 所以"颜色由 CSS 给、模糊由系统给"这条设计要靠它成立。
func setAccent(hwnd uintptr, state uint32, color uint32) {
	const wcaAccentPolicy = 19
	p := accentPolicy{State: state, GradientColor: color}
	d := winCompAttrData{Attrib: wcaAccentPolicy, Data: unsafe.Pointer(&p), Size: unsafe.Sizeof(p)}
	r, _, err := procSetWindowCompositionAttribute.Call(hwnd, uintptr(unsafe.Pointer(&d)))
	fmt.Printf("ACCENT 写 %#010x：state=%d color=%#08x 返回=%d err=%v\n", hwnd, state, color, r, err)
}

// setExStyle 读-改-写扩展样式位，并把改前改后都打出来。
//
// 用来做"某个样式位是不是磨砂的拦路虎"这类对照实验：比如把 WS_EX_LAYERED
// 拿掉再开 DWM 材质，看背后内容有没有被真正模糊。
func setExStyle(hwnd uintptr, mask uint32, val uint32) {
	idx := int32(-20)
	before, _, _ := procGetWindowLongPtrW.Call(hwnd, uintptr(idx))
	want := (uint32(before) &^ mask) | val
	procSetWindowLongPtrW.Call(hwnd, uintptr(idx), uintptr(want))
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		0x0001|0x0002|0x0004|0x0010|0x0020) // NOSIZE|NOMOVE|NOZORDER|NOACTIVATE|FRAMECHANGED
	after, _, _ := procGetWindowLongPtrW.Call(hwnd, uintptr(idx))
	fmt.Printf("扩展样式 %#010x：前=%s\n  后=%s\n", hwnd, exFlags(uint32(before)), exFlags(uint32(after)))
}

// findClass 枚举所有指定类名的顶层窗口。
//
// 不能用 FindWindowExW：它只返回第一个。时钟、弹幕、便签、内核窗口
// 是同一个类，只看第一个会把结论建在错的那个窗口上。
func findClass(class string) []uintptr {
	var out []uintptr
	cb := windows.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		if className(hwnd) == class {
			out = append(out, hwnd)
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	return out
}

// neighbors 打出一个窗口在 Z 序里的上下邻居。
//
// "我们下面是不是 Progman"就是靠这个判断的——比读日志可靠，
// 因为可以在任意时刻跑，包括用户拖动之后。
func neighbors(hwnd uintptr) {
	fmt.Printf("=== 窗口 %#010x %s 「%s」 ex=%s ===\n", hwnd, className(hwnd),
		windowText(hwnd), exFlags(exStyle(hwnd)))
	r := bounds(hwnd)
	fmt.Printf("  矩形 (%d,%d)-(%d,%d) %dx%d pid=%d\n", r.Left, r.Top, r.Right, r.Bottom,
		r.Right-r.Left, r.Bottom-r.Top, pidOf(hwnd))
	fmt.Printf("  分层属性：%s\n", layeredAttrs(hwnd))
	fmt.Printf("  DWM：%s cloak=%d\n", dwmBounds(hwnd), cloaked(hwnd))

	// 标题必须一起打：我们的窗口和 WebView2 的子窗口类名相同，
	// 只靠类名分不清"这条是谁"，标题是唯一的人话标识。
	fmt.Println("  上方（更靠前）：")
	h, _, _ := procGetWindow.Call(hwnd, gwHwndPrev)
	for i := 0; i < 5 && h != 0; i++ {
		fmt.Printf("    %#010x %-32s %-12s %s\n", h, className(h), windowText(h), exFlags(exStyle(h)))
		h, _, _ = procGetWindow.Call(h, gwHwndPrev)
	}
	fmt.Println("  下方（更靠后）：")
	h, _, _ = procGetWindow.Call(hwnd, gwHwndNext)
	for i := 0; i < 5 && h != 0; i++ {
		fmt.Printf("    %#010x %-32s %-12s %s\n", h, className(h), windowText(h), exFlags(exStyle(h)))
		h, _, _ = procGetWindow.Call(h, gwHwndNext)
	}
}

func main() {
	x := flag.Int("x", -1<<30, "屏幕 x")
	y := flag.Int("y", -1<<30, "屏幕 y")
	class := flag.String("class", "", "按类名找窗口，打印它的 Z 序邻居")
	limit := flag.Int("limit", 40, "最多列出多少层")
	setTrans := flag.String("transparent", "", "给指定句柄（十六进制）的窗口加 WS_EX_TRANSPARENT")
	dwm := flag.String("dwm", "", "读写 DWM 属性：<句柄十六进制>,<属性号>,<值>")
	accent := flag.String("accent", "", "设 ACCENT_POLICY：<句柄十六进制>,<state>,<ABGR>")
	ex := flag.String("ex", "", "改扩展样式：<句柄十六进制>,<掩码>,<值>（先清掩码位再置值位）")
	flag.Parse()

	// PER_MONITOR_AWARE_V2 = -4。失败也无所谓（老系统没有这个 API），
	// 但失败就说明下面的坐标是虚拟化过的，得在输出里讲清楚。
	if r, _, _ := procSetProcessDpiAwarenessContext.Call(^uintptr(3)); r == 0 {
		fmt.Println("⚠ 设置 DPI 感知失败，下面的坐标是 96 DPI 虚拟化过的")
	}

	usePoint := *x != -1<<30 && *y != -1<<30

	if *setTrans != "" {
		var h uint64
		if _, err := fmt.Sscanf(*setTrans, "%x", &h); err != nil {
			fmt.Printf("句柄格式不对：%v\n", err)
			return
		}
		setTransparent(uintptr(h))
		return
	}

	if *dwm != "" {
		parts := strings.Split(*dwm, ",")
		if len(parts) != 3 {
			fmt.Println("格式应为 <句柄十六进制>,<属性号>,<值>")
			return
		}
		h, err1 := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(parts[0]), "0x"), 16, 64)
		attr, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		val, err3 := strconv.Atoi(strings.TrimSpace(parts[2]))
		if err1 != nil || err2 != nil || err3 != nil {
			fmt.Printf("参数解析失败：%v %v %v\n", err1, err2, err3)
			return
		}
		dwmAttr(uintptr(h), int32(attr), int32(val))
		return
	}

	if *accent != "" {
		parts := strings.Split(*accent, ",")
		if len(parts) != 3 {
			fmt.Println("格式应为 <句柄十六进制>,<state>,<ABGR>")
			return
		}
		h, err1 := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(parts[0]), "0x"), 16, 64)
		st, err2 := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 32)
		col, err3 := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(parts[2]), "0x"), 16, 32)
		if err1 != nil || err2 != nil || err3 != nil {
			fmt.Printf("参数解析失败：%v %v %v\n", err1, err2, err3)
			return
		}
		setAccent(uintptr(h), uint32(st), uint32(col))
		return
	}

	if *ex != "" {
		parts := strings.Split(*ex, ",")
		if len(parts) != 3 {
			fmt.Println("格式应为 <句柄十六进制>,<掩码>,<值>")
			return
		}
		h, err1 := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(parts[0]), "0x"), 16, 64)
		m, err2 := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(parts[1]), "0x"), 16, 32)
		v, err3 := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(parts[2]), "0x"), 16, 32)
		if err1 != nil || err2 != nil || err3 != nil {
			fmt.Printf("参数解析失败：%v %v %v\n", err1, err2, err3)
			return
		}
		setExStyle(uintptr(h), uint32(m), uint32(v))
		return
	}

	if *class != "" {
		all := findClass(*class)
		if len(all) == 0 {
			fmt.Printf("找不到类名为 %q 的窗口\n", *class)
			return
		}
		for i, hwnd := range all {
			if i > 0 {
				fmt.Println()
			}
			neighbors(hwnd)
		}
		if usePoint {
			fmt.Println()
			stackAt(point{int32(*x), int32(*y)}, *limit)
		}
		return
	}

	if !usePoint {
		var p point
		procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
		fmt.Printf("没给坐标，用当前光标位置 (%d,%d)\n", p.X, p.Y)
		*x, *y = int(p.X), int(p.Y)
	}
	p := point{int32(*x), int32(*y)}

	stackAt(p, *limit)

	// 再补三条系统自己的读数，和上面的栈对照着看。
	pt := uintptr(uint32(p.X)) | uintptr(uint32(p.Y))<<32
	top, _, _ := procWindowFromPoint.Call(pt)
	fmt.Printf("\nWindowFromPoint  = %#010x %s ex=%s\n", top, className(top), exFlags(exStyle(top)))

	// 命中窗口的祖先链：WindowFromPoint 只给最深的那个，
	// 是不是我们自己的子窗口，要看它挂在谁下面。
	fmt.Print("  祖先链：")
	for h := top; h != 0; {
		fmt.Printf("%s(%#x) ", className(h), h)
		h, _, _ = procGetParent.Call(h)
	}
	fmt.Println()

	desk, _, _ := procGetDesktopWindow.Call()
	for _, f := range []struct {
		name string
		flag uintptr
	}{
		{"跳可见禁用", 0x0002 | 0x0004},
		{"跳透明+可见禁用", 0x0001 | 0x0002 | 0x0004},
	} {
		h, _, _ := procChildWindowFromPt.Call(desk, pt, f.flag)
		fmt.Printf("ChildWindowFromPointEx(%-16s) = %#010x %s cloak=%d\n",
			f.name, h, className(h), cloaked(h))
	}
}

func utf16Ptr(s string) *uint16 {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return nil
	}
	return p
}
