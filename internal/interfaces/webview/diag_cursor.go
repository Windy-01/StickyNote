package webview

import (
	"fmt"
	"log"
	"strings"
	"time"
	"unsafe"
)

// 光标探针：只要鼠标压在一个穿透窗口上，就每秒报一次"系统说那儿是谁"。
//
// 为什么非要有它：判断穿透成立不成立，手上那几个 API 全会骗人。
//   - WindowFromPoint 不看 WS_EX_TRANSPARENT，返回最上面那个，看起来永远"命中自己"；
//   - ChildWindowFromPointEx 不看 DWM cloak，会返回一个根本没被合成的 UWP 窗口；
//   - 问窗口自己的 WM_NCHITTEST 只能得到它愿意回答的值，改变不了系统的决定。
//
// 唯一可信的读数是"鼠标消息到底进没进窗口过程"。可那要求鼠标真的压在窗口上，
// 而合成事件（PostMessage/SendInput）在引擎层经常静默失效，测出来的结论是假的。
//
// 折中：这里不改动输入，只在光标**自己**落在窗口上时报一次现场——光标位置、
// WindowFromPoint、那条窗口的祖先链、以及宿主累计收到的 mousemove 条数。
// 鼠标在窗口上停两秒，就能看出两件事：
//   - 命中窗口是不是我们自己（祖先链里有没有 StickyNoteHostWindow）；
//   - mousemove 计数有没有涨（涨了就是鼠标真被我们吃了）。
func (a *App) startCursorProbe() {
	go func() {
		tick := time.NewTicker(time.Second)
		for range tick.C {
			a.probeCursor()
		}
	}()
}

func (a *App) probeCursor() {
	var p point
	if r, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&p))); r == 0 {
		return
	}
	// 走 windowRegistry 而不是 a.running：探针在自己的 goroutine 上跑，
	// 而 a.running 只在 UI 线程上被增删，直接读是数据竞争。
	// 窗口注册表自带读写锁，且里面正好都是"活着的窗口"。
	windowMu.RLock()
	wins := make([]*Window, 0, len(windowRegistry))
	for _, w := range windowRegistry {
		wins = append(wins, w)
	}
	windowMu.RUnlock()

	for _, win := range wins {
		if !win.ClickThrough() {
			continue
		}
		x, y, w, h := win.Bounds()
		if int(p.X) < x || int(p.X) >= x+w || int(p.Y) < y || int(p.Y) >= y+h {
			continue
		}

		pt := packPoint(int(p.X), int(p.Y))
		hit, _, _ := procWindowFromPoint.Call(pt)
		chain := ancestorChain(hit)
		// 祖先链里出现宿主的句柄，才说明这一点真的被我们接住了；
		// 只看类名不行——四个组件窗口是同一个类。
		ours := strings.Contains(chain, fmt.Sprintf("%#x", win.Handle()))
		log.Printf("光标探针 %q：pos=(%d,%d) 命中=%#x(%s) 是我们=%v 宿主 mousemove=%d nchittest=%d 祖先=%s",
			win.title, p.X, p.Y, hit, windowClass(hit), ours,
			win.MouseMoveMsgs(), win.HitTestCount(), chain)
	}
}

// ancestorChain 把一条窗口的祖先链拼成 "类名(句柄) < 类名(句柄)"。
//
// 顶层窗口的 GetParent 返回 0，所以链会自然停住。子窗口（WebView2 会建
// Chrome_WidgetWin_0）能一路走到宿主，这一串正是"谁接住了鼠标"的证据。
func ancestorChain(hwnd uintptr) string {
	var parts []string
	for h := hwnd; h != 0; h, _, _ = procGetParent.Call(h) {
		parts = append(parts, fmt.Sprintf("%s(%#x)", windowClass(h), h))
		if len(parts) > 6 {
			break
		}
	}
	return strings.Join(parts, " < ")
}
