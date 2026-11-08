// Package webview2 是 WebView2 Composition 的 Go 绑定。
//
// 它只负责「引擎搬运」：把页面接进宿主的 DirectComposition 视觉树、
// 转发输入、搬运消息。窗口形态、样式、组件这些都在上层。
//
// 线程约定：所有导出方法都必须在创建控制器的那个 OS 线程上调用
// （组合根里用 runtime.LockOSThread 固定）。回调也从同一个线程上来。
package webview2

/*
#cgo CXXFLAGS: -std=c++17 -O2 -I${SRCDIR}/../../../third_party/webview2/include
#cgo LDFLAGS: -lole32 -loleaut32 -luuid -ld3d11 -ldxgi -lgdi32 -luser32 -lpsapi
#include <stdlib.h>
#include "wrap.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ---------------------------------------------------------------------------
// 消息泵
//
// 创建环境与控制器都是异步的，等它们的过程中必须把消息队列抽干，
// 否则 COM 的完成回调永远送不到。
// ---------------------------------------------------------------------------

type msgT struct {
	Hwnd     uintptr
	Message  uint32
	_        uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       struct{ X, Y int32 }
	LPrivate uint32
	_        uint32
}

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procPeekMessageW     = user32.NewProc("PeekMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
)

const pmRemove = 0x0001

// pumpOnce 抽一条消息并派发。返回是否取到了消息。
func pumpOnce() bool {
	var m msgT
	got, _, _ := procPeekMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, pmRemove)
	if got == 0 {
		return false
	}
	procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
	procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	return true
}

// ---------------------------------------------------------------------------
// 控制器注册表
// C 侧只带得回一个 user 值，用它找回 Go 对象。
// ---------------------------------------------------------------------------
var (
	regMu   sync.RWMutex
	regByID         = map[uintptr]*Controller{}
	nextID  uintptr = 1

	evalMu      sync.Mutex
	evalWaiters = map[uintptr]func(ok bool, json string){}
	evalSeq     uintptr
)

func register(c *Controller) {
	regMu.Lock()
	c.id = nextID
	nextID++
	regByID[c.id] = c
	regMu.Unlock()
}

func unregister(id uintptr) {
	regMu.Lock()
	delete(regByID, id)
	regMu.Unlock()
}

func lookup(id uintptr) *Controller {
	regMu.RLock()
	defer regMu.RUnlock()
	return regByID[id]
}

// ---------------------------------------------------------------------------
// 导出给 C 的回调
// ---------------------------------------------------------------------------

//export snGoMessage
func snGoMessage(user C.uintptr_t, utf8 *C.char) {
	c := lookup(uintptr(user))
	if c == nil || c.onMessage == nil {
		return
	}
	c.onMessage(C.GoString(utf8))
}

//export snGoReady
func snGoReady(user C.uintptr_t, kind C.int32_t) {
	c := lookup(uintptr(user))
	if c == nil || c.onNav == nil {
		return
	}
	c.onNav(kind == 1)
}

//export snGoCursor
func snGoCursor(user C.uintptr_t, hcursor C.intptr_t) {
	c := lookup(uintptr(user))
	if c == nil || c.onCursor == nil {
		return
	}
	c.onCursor(uintptr(hcursor))
}

//export snGoEval
func snGoEval(user C.uintptr_t, token C.uintptr_t, ok C.int32_t, json *C.char) {
	_ = user
	evalMu.Lock()
	fn := evalWaiters[uintptr(token)]
	delete(evalWaiters, uintptr(token))
	evalMu.Unlock()
	if fn != nil {
		fn(ok == 1, C.GoString(json))
	}
}

// ---------------------------------------------------------------------------
// 环境
// ---------------------------------------------------------------------------

// Init 创建（进程内唯一的）WebView2 环境并等到就绪。
//
// userDataDir 是浏览器的用户数据目录，放缓存目录下即可。
// 一个环境就是一整套浏览器进程，所有窗口共用，多开只会翻倍内存。
func Init(userDataDir string, debug bool) error {
	dir, err := windows.UTF16PtrFromString(userDataDir)
	if err != nil {
		return err
	}
	var dbg C.int32_t
	if debug {
		dbg = 1
	}
	if hr := C.sn_wv2_start_env((*C.wchar_t)(unsafe.Pointer(dir)), dbg); hr != 0 {
		return fmt.Errorf("创建 WebView2 环境失败: HRESULT 0x%08X", uint32(hr))
	}

	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		switch C.sn_wv2_env_state() {
		case 1:
			return nil
		case -1:
			return fmt.Errorf("WebView2 环境初始化失败: HRESULT 0x%08X",
				uint32(C.sn_wv2_env_hr()))
		}
		pumpOnce()
		time.Sleep(5 * time.Millisecond)
	}
	return errors.New("等待 WebView2 环境超时")
}

// RuntimeVersion 返回运行时版本号（环境就绪后才有值）。
func RuntimeVersion() string {
	buf := make([]uint16, 64)
	n := C.sn_wv2_runtime_version((*C.wchar_t)(unsafe.Pointer(&buf[0])), 64)
	if n <= 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// ---------------------------------------------------------------------------
// 控制器
// ---------------------------------------------------------------------------

// Controller 是一个窗口上的页面。
type Controller struct {
	h    C.SN_WV2
	id   uintptr
	done bool

	onMessage func(string)
	onNav     func(ok bool)
	onCursor  func(hcursor uintptr)
}

// New 在 hwnd 上建控制器，阻塞到就绪。
//
// hwnd 必须是已经建好、并且带 WS_EX_NOREDIRECTIONBITMAP 的窗口——
// Composition 模式要求内容由 DComp 合成，没有重定向表面。
func New(hwnd uintptr) (*Controller, error) {
	c := &Controller{}
	register(c)

	c.h = C.sn_wv2_new(C.uintptr_t(hwnd), C.uintptr_t(c.id))
	if c.h == nil {
		unregister(c.id)
		return nil, errors.New("创建 WebView2 控制器失败（环境未就绪？）")
	}

	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		switch C.sn_wv2_ready(c.h) {
		case 1:
			return c, nil
		case -1:
			hr := uint32(C.sn_wv2_hr(c.h))
			C.sn_wv2_free(c.h)
			unregister(c.id)
			return nil, fmt.Errorf("WebView2 控制器初始化失败: HRESULT 0x%08X", hr)
		}
		pumpOnce()
		time.Sleep(5 * time.Millisecond)
	}
	C.sn_wv2_free(c.h)
	unregister(c.id)
	return nil, errors.New("等待 WebView2 控制器超时")
}

// OnMessage 注册「页面发来一条消息」的回调。参数是 JSON 文本。
func (c *Controller) OnMessage(f func(string)) { c.onMessage = f }

// OnNavigationCompleted 注册导航完成回调。
func (c *Controller) OnNavigationCompleted(f func(ok bool)) { c.onNav = f }

// OnCursor 注册光标变更回调，参数是 HCURSOR。
func (c *Controller) OnCursor(f func(hcursor uintptr)) { c.onCursor = f }

// MapHost 把虚拟主机名映射到真实目录。
//
// 页面因此能用相对路径加载自己的资源，而且不同组件天然是不同源。
func (c *Controller) MapHost(host, folder string) error {
	h, err := windows.UTF16PtrFromString(host)
	if err != nil {
		return err
	}
	f, err := windows.UTF16PtrFromString(folder)
	if err != nil {
		return err
	}
	hr := C.sn_wv2_map_host(c.h, (*C.wchar_t)(unsafe.Pointer(h)),
		(*C.wchar_t)(unsafe.Pointer(f)))
	if hr != 0 {
		return fmt.Errorf("映射虚拟主机失败: HRESULT 0x%08X", uint32(hr))
	}
	return nil
}

// InjectScript 注册「每次文档创建时执行」的脚本，早于页面自己的脚本。
func (c *Controller) InjectScript(js string) error {
	p, err := windows.UTF16PtrFromString(js)
	if err != nil {
		return err
	}
	hr := C.sn_wv2_inject_script(c.h, (*C.wchar_t)(unsafe.Pointer(p)))
	if hr != 0 {
		return fmt.Errorf("注入脚本失败: HRESULT 0x%08X", uint32(hr))
	}
	return nil
}

// Navigate 打开页面。
func (c *Controller) Navigate(url string) error {
	p, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	hr := C.sn_wv2_navigate(c.h, (*C.wchar_t)(unsafe.Pointer(p)))
	if hr != 0 {
		return fmt.Errorf("导航失败: HRESULT 0x%08X", uint32(hr))
	}
	return nil
}

// PostJSON 向页面投递一条消息。页面侧收到的是已解析的对象。
func (c *Controller) PostJSON(json string) error {
	p, err := windows.UTF16PtrFromString(json)
	if err != nil {
		return err
	}
	hr := C.sn_wv2_post_json(c.h, (*C.wchar_t)(unsafe.Pointer(p)))
	if hr != 0 {
		return fmt.Errorf("投递消息失败: HRESULT 0x%08X", uint32(hr))
	}
	return nil
}

// Eval 执行脚本，结果异步回调（json 是返回值序列化后的文本）。
//
// 故意不做同步：等待会把消息循环交给引擎，站在回调里调用就会重入。
func (c *Controller) Eval(js string, cb func(ok bool, json string)) {
	p, err := windows.UTF16PtrFromString(js)
	if err != nil {
		if cb != nil {
			cb(false, "")
		}
		return
	}
	evalMu.Lock()
	evalSeq++
	token := evalSeq
	if cb != nil {
		evalWaiters[token] = cb
	}
	evalMu.Unlock()

	if hr := C.sn_wv2_eval(c.h, (*C.wchar_t)(unsafe.Pointer(p)),
		C.uintptr_t(token)); hr != 0 {
		evalMu.Lock()
		delete(evalWaiters, token)
		evalMu.Unlock()
		if cb != nil {
			cb(false, "")
		}
	}
}

// Resize 把新的客户区尺寸告诉引擎。
func (c *Controller) Resize() { C.sn_wv2_resize(c.h) }

// SetZoom 设内容缩放倍数（1 表示原始大小）。
//
// 窗口尺寸不变，页面按倍数重新排版——这是"字太小想放大"和"窗口太小想放大"
// 两件不同的事里前一件的实现。屏幕 DPI 已经由创建时设的栅格化缩放承载，
// 这里乘上去，两者互不覆盖。
//
// 返回是否设置成功：拿不到 ICoreWebView2Controller3 时会失败（引擎版本过老），
// 调用方据此决定要不要提示用户，而不是静默当成功。
func (c *Controller) SetZoom(zoom float64) bool {
	return C.sn_wv2_set_zoom(c.h, C.double(zoom)) == 0
}

// SetDevToolsEnabled 开关开发者工具。
func (c *Controller) SetDevToolsEnabled(on bool) {
	var v C.int32_t
	if on {
		v = 1
	}
	C.sn_wv2_set_devtools_enabled(c.h, v)
}

// SendMouse 转发一条鼠标事件。
//
// kind 用 COREWEBVIEW2_MOUSE_EVENT_KIND 的值，virtualKeys 用
// COREWEBVIEW2_MOUSE_EVENT_VIRTUAL_KEYS 的位组合。Composition 模式
// 不接管输入，不转发的话页面里点不动、也没有 hover。
func (c *Controller) SendMouse(kind, virtualKeys, mouseData, x, y int32) {
	C.sn_wv2_send_mouse(c.h, C.int32_t(kind), C.int32_t(virtualKeys),
		C.int32_t(mouseData), C.int32_t(x), C.int32_t(y))
}

// Focus 让页面拿到键盘焦点。
func (c *Controller) Focus() { C.sn_wv2_focus(c.h) }

// Close 关闭并释放控制器。窗口本身由调用方销毁。
func (c *Controller) Close() {
	if c.done || c.h == nil {
		return
	}
	c.done = true
	C.sn_wv2_free(c.h)
	c.h = nil
	unregister(c.id)
}

// ---------------------------------------------------------------------------
// 鼠标事件常量（与 COREWEBVIEW2_MOUSE_EVENT_KIND 一致）
// ---------------------------------------------------------------------------

const (
	MouseMove       = 0x0200
	MouseLeave      = 0x02A3
	MouseLeftDown   = 0x0201
	MouseLeftUp     = 0x0202
	MouseRightDown  = 0x0204
	MouseRightUp    = 0x0205
	MouseMiddleDown = 0x0207
	MouseMiddleUp   = 0x0208
	MouseWheel      = 0x020A
)

// 虚拟键位（COREWEBVIEW2_MOUSE_EVENT_VIRTUAL_KEYS）
const (
	VkLeftButton   = 0x10
	VkRightButton  = 0x20
	VkMiddleButton = 0x40
)
