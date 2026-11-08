package webview

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"stickynote/internal/infrastructure/webview2"
)

// hostSeq 给每个页面一个独立的虚拟主机名。
//
// 不同主机名就是不同源：组件之间天然隔离，一个组件读不到另一个的存储，
// 也不需要再靠 DENY_CORS 之类的开关去补。
var hostSeq struct {
	sync.Mutex
	n int
}

func nextHostName() string {
	hostSeq.Lock()
	hostSeq.n++
	n := hostSeq.n
	hostSeq.Unlock()
	return fmt.Sprintf("sn%d.stickynote", n)
}

// bootstrapJS 在页面脚本之前注入，建立宿主 <-> 前端的最小通道。
//
// 出站走 window.chrome.webview.postMessage，入站走 message 事件——
// 这是 WebView2 的原生通道，不像之前那样要靠自定义事件骗过引擎。
// 先到的消息会被排队，等前端注册处理器后再统一投递，避免竞态。
//
// 前端看到的仍然是 host.send / host.onMessage 这一层，换引擎对它透明。
const bootstrapJS = `(function(){` +
	`var q=[],h=null;` +
	`function deliver(d){if(h){h(d);}else{q.push(d);}}` +
	`window.__host={` +
	`receive:deliver,` +
	`onMessage:function(f){h=f;while(q.length){f(q.shift());}},` +
	`send:function(m){window.chrome.webview.postMessage(m);},` +
	`applyStyle:function(s){var r=document.documentElement;if(!r)return;` +
	`var v=(s&&s.vars)||{},k;` +
	`for(k in v){r.style.setProperty(k,v[k]);}` +
	`r.setAttribute('data-sn-titlebar',(s&&s.titlebar)?'1':'0');` +
	`r.setAttribute('data-sn-resizable',(s&&s.resizable)?'1':'0');` +
	`r.setAttribute('data-sn-layer',(s&&s.layer)||'');}` +
	`};` +
	`if(window.chrome&&window.chrome.webview){` +
	`window.chrome.webview.addEventListener('message',function(e){deliver(e.data);});}` +
	`})();`

// stylePayload 是 applyStyle 的入参形状。
type stylePayload struct {
	Vars      map[string]string `json:"vars"`
	Titlebar  bool              `json:"titlebar"`
	Resizable bool              `json:"resizable"`
	Layer     string            `json:"layer"`
}

// waitRootJS 是一段"等 documentElement 出现再干活"的壳。
//
// 注入脚本在 document_start 就跑，那时根元素可能还没建出来。直接操作会抛错，
// 而这个错误只会进控制台，界面上表现为"样式没生效"，很难查。
// 用 1ms 轮询兜住这几十毫秒，比监听一堆事件简单可靠。
func waitRootJS(body string) string {
	return `(function(){` +
		`function run(){var r=document.documentElement;if(!r)return false;` +
		body + `return true;}` +
		`if(!run()){var iv=setInterval(function(){if(run())clearInterval(iv);},1);` +
		`setTimeout(function(){clearInterval(iv);},3000);}})();`
}

// baseCSSScript 把基座样式塞进文档。
//
// 不再像 Sciter 版那样拼进 HTML 字符串：现在页面是通过虚拟主机加载的，
// 宿主拿不到 HTML 文本。动态插入 <style> 在 WebKit 里是正常工作的
// （Sciter 里才不生效），所以这条路反而更简单。
//
// **必须插在文档最前面**，不能 appendChild。同一个选择器（比如 body）
// 在特异度相同时靠"谁在后面谁赢"，而 appendChild 会把它挂到 <html> 的
// 末尾——也就是排在组件自己的样式表**后面**，于是基座的
// `html,body{background:transparent}` 会盖掉组件写的
// `body{background:var(--sn-background)}`。后果是三个组件的 body 全是
// rgba(0,0,0,0)：便签看起来是"透明背景"，时钟和弹幕只剩下一圈窗口投影。
// 基座是兜底，就该排在最后生效，所以这里插到最前面。
func baseCSSScript() string {
	b, err := json.Marshal(baseCSS)
	if err != nil {
		return ""
	}
	return waitRootJS(`var s=document.getElementById('sn-base');` +
		`if(!s){s=document.createElement('style');s.id='sn-base';` +
		`s.textContent=` + string(b) + `;` +
		`var p=r.querySelector('head')||r;p.insertBefore(s,p.firstChild);}`)
}

// styleInitScript 生成"文档一建好就应用样式"的脚本。
func styleInitScript(p stylePayload) string {
	data, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return waitRootJS(`if(window.__host){window.__host.applyStyle(` + string(data) + `);}`)
}

// Host 封装一个窗口上的页面。
//
// 所有导出方法都只能从 UI 线程调用；跨线程投递请用 Post。
type Host struct {
	win      *Window
	ctrl     *webview2.Controller
	folder   string
	entry    string
	hostName string
	initial  stylePayload

	mu    sync.Mutex
	ready bool
	queue []string

	// OnReady 在文档首次就绪后触发一次。宿主只导航一次，
	// 所以"首次"就是"每次"，不需要区分。
	OnReady func()
}

// NewHost 创建宿主。folder 是组件包解包后的真实目录，entry 是包内入口页
// 的相对路径，initial 是首屏就要生效的样式。
func NewHost(win *Window, folder, entry string, initial stylePayload) *Host {
	e := entry
	for len(e) > 0 && e[0] == '/' {
		e = e[1:]
	}
	return &Host{
		win:      win,
		folder:   folder,
		entry:    e,
		hostName: nextHostName(),
		initial:  initial,
	}
}

// Start 在窗口上建控制器并挂上桥接。
func (h *Host) Start(onMessage func(string), devtools bool) error {
	ctrl, err := webview2.New(h.win.Handle())
	if err != nil {
		return err
	}
	h.ctrl = ctrl
	h.win.setController(ctrl)

	ctrl.OnMessage(onMessage)
	// 光标由引擎算好，宿主只负责设到系统上。
	ctrl.OnCursor(func(hcursor uintptr) { h.win.onCursor(hcursor) })
	ctrl.OnNavigationCompleted(func(ok bool) {
		if !ok {
			log.Printf("窗口 %s 导航失败", h.win.title)
			return
		}
		h.win.Post(h.onDocumentReady)
	})

	if err := ctrl.MapHost(h.hostName, h.folder); err != nil {
		log.Printf("窗口 %s 映射虚拟主机失败: %v", h.win.title, err)
	}

	// 三段脚本合成一次注入：基座样式 -> 桥接外壳 -> 首屏样式。
	// 顺序有依赖：applyStyle 要先有 __host。
	init := baseCSSScript() + bootstrapJS + styleInitScript(h.initial)
	if err := ctrl.InjectScript(init); err != nil {
		return err
	}
	if devtools {
		ctrl.SetDevToolsEnabled(true)
	}
	return nil
}

// onDocumentReady 在文档就绪后触发：放行积压的消息。
func (h *Host) onDocumentReady() {
	h.mu.Lock()
	if h.ready {
		h.mu.Unlock()
		return
	}
	h.ready = true
	q := h.queue
	h.queue = nil
	h.mu.Unlock()

	for _, msg := range q {
		if err := h.ctrl.PostJSON(msg); err != nil {
			log.Printf("窗口 %s 补发消息失败: %v", h.win.title, err)
		}
	}
	if h.OnReady != nil {
		h.OnReady()
	}
}

// Navigate 打开组件的入口页。
func (h *Host) Navigate() {
	url := "https://" + h.hostName + "/" + h.entry
	if err := h.ctrl.Navigate(url); err != nil {
		log.Printf("窗口 %s 打开页面失败: %v", h.win.title, err)
	}
}

// Resize 把新的客户区尺寸告诉引擎。尺寸不对页面就不重排。
func (h *Host) Resize() {
	if h.ctrl != nil {
		h.ctrl.Resize()
	}
}

// SetZoom 设内容缩放（窗口尺寸不变，页面按倍数放大/缩小）。
//
// 排到 UI 队列：这里要碰 COM 对象，而调用方可能正站在引擎的消息回调里。
// 一次拖动滑块会来几十条消息，队列深度只有个位数——前端的防抖已经把
// 频率压住了，不会出现"排不上队所以不生效"。
func (h *Host) SetZoom(zoom float64) {
	h.win.Post(func() {
		if h.ctrl == nil {
			return
		}
		if !h.ctrl.SetZoom(zoom) {
			log.Printf("窗口 %s 设置缩放 %.2f 失败：引擎不支持 RasterizationScale", h.win.title, zoom)
		}
	})
}

// ApplyStyle 把新的外观参数推给已经在跑的页面。
//
// 只改视觉（留白、圆角、底色、阴影、CSS 变量）：这些是纯 CSS，改完立刻生效。
// 窗口开关（层级、穿透、可缩放…）改不了，那些是建窗口时的选项，
// 只能拆了重建——调用方负责区分这两类变更。
func (h *Host) ApplyStyle(p stylePayload) {
	data, err := json.Marshal(p)
	if err != nil {
		return
	}
	js := `if(window.__host){window.__host.applyStyle(` + string(data) + `);}`
	h.win.Post(func() {
		if h.ctrl == nil {
			return
		}
		h.ctrl.Eval(js, nil)
	})
}

// Post 向前端投递一条消息。可从任意 goroutine 调用。
func (h *Host) Post(payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	js := string(data)
	h.win.Post(func() { h.deliver(js) })
}

func (h *Host) deliver(js string) {
	if h.ctrl == nil {
		return
	}
	h.mu.Lock()
	ready := h.ready
	if !ready {
		h.queue = append(h.queue, js)
	}
	h.mu.Unlock()
	if !ready {
		return
	}
	// 直接把 JSON 交给引擎，页面侧拿到的是已解析的对象，
	// 省掉一次反序列化，也避开引号转义的坑。
	if err := h.ctrl.PostJSON(js); err != nil {
		log.Printf("窗口 %s 投递消息失败: %v", h.win.title, err)
	}
}
