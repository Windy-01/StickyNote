package webview

import (
	"log"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WebView2 在 Composition 模式下会自己建一个"输入窗口"，它会吃掉鼠标。
//
// 一个组件其实是三个窗口，只有一个是我们的：
//
//	StickyNoteHostWindow   我们的宿主窗口（穿透位设在这里）
//	Chrome_WidgetWin_0     引擎建在宿主下的子窗口
//	Chrome_WidgetWin_1     引擎在**浏览器进程**里建的顶层"输入窗口"
//
// 第三个是罪魁：矩形与宿主窗口重合，扩展样式是
// `NOACTIVE|LAYERED|TOOLWIN|NOREDIR`——**没有 WS_EX_TRANSPARENT**，
// 于是它把鼠标全吃掉了：
//
//	光标探针 "测试弹幕"：命中=Chrome_RenderWidgetHostHWND < Chrome_WidgetWin_1
//	                     是我们=false 宿主 mousemove=0 nchittest=0
//
// 宿主的 mousemove 和 WM_NCHITTEST 都是 0：鼠标压根没到我们的窗口过程。
// 所以宿主窗口的 `WS_EX_LAYERED|WS_EX_TRANSPARENT` 设得再对也没用——
// 穿透规则是"跳过这个窗口"，可鼠标根本没问到这个窗口。
//
// 这也解释了"必须拖一下才穿透、旧位置依旧不正常"：拖动之前它一直盖在原位
// 吃鼠标，宿主挪走之后新位置自然就"正常"了。
//
// 为什么不能删掉它：它是浏览器进程的窗口，DestroyWindow 只能销毁本线程的窗口，
// 跨进程做不到；而且它承担 Composition 模式下真正需要 HWND 的那部分工作
// （键盘焦点、IME 候选窗定位、无障碍/自动化）。删了会连带断掉输入法。
// 能做的只有让它别再挡鼠标：`SetWindowLongPtr` 改扩展样式**跨进程是允许的**
// （AutoHotkey 一类工具一直这么做，本项目实测返回成功、读回生效），
// 补上透明位之后它就和宿主一样被命中测试跳过，键盘/IME 照常。

// inputSearch 保存一次"找输入窗口"的中间状态。
//
// EnumWindows 的回调是 Win32 调进来的，带不了闭包参数，只能走包级变量；
// 而且 windows.NewCallback 的槽位有限（约 2000 个），绝不能每次调用都新建，
// 否则每挪一次窗口漏一个槽，迟早耗尽。
var inputSearch struct {
	self  rect
	best  uintptr
	area  int32
	exact bool
	byPid bool
}

var enumInputProc = windows.NewCallback(func(hwnd, _ uintptr) uintptr {
	if !strings.HasPrefix(windowClass(hwnd), "Chrome_WidgetWin") {
		return 1
	}
	// 三个位一起认：Composition 的输入窗口是
	// NOACTIVATE|LAYERED|TOOLWIN|NOREDIR。少认一位就会误伤——实测有一次
	// 挑中了一个 `0x00200100`（只有 NOREDIR|WINDOWEDGE）的瞬时 Chromium
	// 窗口，真正的输入窗口反而漏掉了。
	if !hasInputStyle(exStyleOf(hwnd)) {
		return 1
	}
	// 优先要"矩形完全重合"的那个：输入窗口是照着宿主铺的，尺寸一致。
	// 尺寸还没铺准的时候（刚导航完是整块内容区）退一步按重叠面积挑，
	// 那时再多要求一条"不是本进程"，避免挑到自己的窗口。
	if inputSearch.exact {
		if windowRect(hwnd) == inputSearch.self {
			inputSearch.best = hwnd
			return 0 // 找到就停
		}
		return 1
	}
	if pidOfWindow(hwnd) == uint32(os.Getpid()) {
		return 1
	}
	if a := overlapArea(inputSearch.self, windowRect(hwnd)); a > inputSearch.area {
		inputSearch.area = a
		inputSearch.best = hwnd
	}
	return 1
})

// neutralizeInputWindow 给 WebView2 的输入窗口补上 WS_EX_TRANSPARENT。
//
// 只在穿透窗口上调用：不穿透的窗口（便签、主界面）需要它正常接输入。
// 幂等——已经透明了就直接返回，可以反复调。
func (w *Window) neutralizeInputWindow() {
	if !w.clickThrough || w.hwnd == 0 {
		return
	}
	h, ok := w.findInputWindow()
	if !ok {
		return
	}

	ex := exStyleOf(h)
	if ex&wsExTransparent != 0 {
		return
	}
	idx := int32(-20) // GWL_EXSTYLE
	procSetWindowLongPtrW.Call(h, uintptr(idx), uintptr(ex)|uintptr(wsExTransparent))
	// 改完必须让系统重算一次窗口框，否则新的样式位不参与命中测试。
	procSetWindowPos.Call(h, 0, 0, 0, 0, 0,
		swpNoSize|swpNoMove|swpNoZOrder|swpNoActivate|swpFrameChanged)

	how := "Z 序邻近"
	if inputSearch.byPid {
		how = "全屏枚举"
	}
	log.Printf("窗口 %q：WebView2 输入窗口 %#x 补上透明位（%s，0x%08x → 0x%08x）",
		w.title, h, how, ex, exStyleOf(h))
}

// findInputWindow 找到宿主对应的那个输入窗口。
//
// 两条路，缺一不可：
//
//  1. **Z 序邻近**：矩形与宿主完全一致、紧挨着宿主。多数情况一次就中，最便宜。
//  2. **按浏览器进程号 + 矩形重叠**：置顶窗口（弹幕）的输入窗口**不在置顶层**，
//     排在所有置顶窗口下面，Z 序上离宿主很远（实测隔了十几层），邻近那条路
//     根本看不到它。它的矩形仍与宿主重合，所以按进程筛 + 取重叠面积最大的最稳。
//
// 输入窗口的矩形要过一会儿才铺到宿主尺寸（实测刚导航完时还是整块内容区），
// 所以调用方要隔一段时间重试几次。
func (w *Window) findInputWindow() (uintptr, bool) {
	self := windowRect(w.hwnd)
	if self == (rect{}) {
		return 0, false
	}

	// 一、Z 序邻近。
	for _, dir := range []uintptr{gwHwndPrev, gwHwndNext} {
		h, _, _ := procGetWindow.Call(w.hwnd, dir)
		for i := 0; i < 4 && h != 0; i++ {
			if strings.HasPrefix(windowClass(h), "Chrome_WidgetWin") &&
				hasInputStyle(exStyleOf(h)) &&
				windowRect(h) == self {
				inputSearch.byPid = false
				return h, true
			}
			h, _, _ = procGetWindow.Call(h, dir)
		}
	}

	// 二、全屏枚举，先要矩形完全重合的。
	//
	// 为什么不能只靠 pid：引擎建在宿主下的子窗口（Chrome_WidgetWin_0）
	// **在本进程里**，拿不到浏览器进程号；而输入窗口恰恰在浏览器进程。
	// 所以这里按"类名 + NOREDIR + 矩形完全重合"认，这一组条件已经足够窄。
	inputSearch.self = self
	inputSearch.best = 0
	inputSearch.area = 0
	inputSearch.exact = true
	procEnumWindows.Call(enumInputProc, 0)
	if inputSearch.best != 0 {
		inputSearch.byPid = true
		return inputSearch.best, true
	}

	// 三、退一步：重叠面积最大的那个（输入窗口还没铺到宿主尺寸时）。
	inputSearch.exact = false
	procEnumWindows.Call(enumInputProc, 0)
	if inputSearch.best == 0 {
		return 0, false
	}
	inputSearch.byPid = true
	return inputSearch.best, true
}

// hasInputStyle 判断扩展样式是不是"WebView2 Composition 输入窗口"那一组。
//
// 必须同时有 LAYERED、NOACTIVATE、NOREDIRECTIONBITMAP 三位：
//   - LAYERED + 无 TRANSPARENT 正是它吃鼠标的原因；
//   - NOREDIR 是 Composition 的标志，普通 Electron 窗口没有；
//   - NOACTIVATE 说明它不抢焦点，就是个输入目标。
//
// 三个一起用，误伤概率基本为零。
func hasInputStyle(ex uint32) bool {
	const want = wsExLayered | wsExNoActivate | wsExNoRedirectionBitmap
	return ex&want == want
}

// pidOfWindow 取窗口所属进程号。
func pidOfWindow(hwnd uintptr) uint32 {
	var pid uint32
	procGetWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}

// overlapArea 返回两个矩形相交的面积，不相交为 0。
func overlapArea(a, b rect) int32 {
	l := max(a.Left, b.Left)
	t := max(a.Top, b.Top)
	r := min(a.Right, b.Right)
	bt := min(a.Bottom, b.Bottom)
	if r <= l || bt <= t {
		return 0
	}
	return (r - l) * (bt - t)
}

// windowRect 取窗口矩形，用来比"两个窗口是不是完全重合"。
func windowRect(hwnd uintptr) rect {
	var r rect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r
}
