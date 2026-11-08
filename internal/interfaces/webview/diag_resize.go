package webview

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 改尺寸的节奏诊断。
//
// 用户报的两个现象——"直接拖窗口边框时内容更新不及时"和"从布局板改尺寸动画
// 不丝滑"——是两条不同的触发链，但都要经过同一个汇合点：
//
//	WM_SIZE → Host.Resize() → put_Bounds() → DComp Commit()
//
// 汇合点之前的差别只有"谁发的 SetWindowPos"：系统模态循环发的（拖窗口边框），
// 还是布局板桥接发的（拖方块边框）。所以先量汇合点本身：
//
//   - 吞吐：背靠背 60 帧，每帧耗时就是 UI 线程被占住的时间。超过一帧预算
//     （约 16ms）就必然掉帧，拖起来一顿一顿——"不丝滑"的直接来源。
//   - 跟手：按 16ms 的节奏走 60 帧，同时读页面 clientWidth。帧数和窗口尺寸
//     对得上，说明引擎收下并重排了；对不上说明 put_bounds 没到渲染进程，
//     画面落后于窗口——"更新不及时"的直接来源。
//
// 这不是在模拟用户操作：它调用的就是布局板拖边框真正走的 resizeInstanceQuiet，
// 参数口径也一致（x/y 物理像素，w/h 逻辑像素）。真实鼠标的跟手程度仍然只能
// 由用户判断，这里量的是这条链本身的成本与响应性。

// probeResizePacing 跑两轮改尺寸，把吞吐与跟手读数打进日志。
func probeResizePacing(a *App) {
	const id = "note-1"
	const frames = 60

	r := a.running[id]
	if r == nil {
		log.Printf("探针 改尺寸节奏：%s 没有在跑", id)
		return
	}
	v, ok := a.svc.Instance(id)
	if !ok {
		log.Printf("探针 改尺寸节奏：%s 不存在", id)
		return
	}
	// x/y 是物理像素，w/h 是逻辑像素——和 layout.json 一个口径。
	x0, y0 := v.Bounds.X, v.Bounds.Y
	w0, h0 := v.Bounds.Width, v.Bounds.Height
	zoom := v.Zoom
	if zoom <= 0 {
		zoom = 1
	}
	log.Printf("探针 改尺寸节奏：起点 %d,%d %d×%d（逻辑） 缩放 %.2f 一帧=%v",
		x0, y0, w0, h0, zoom, screenFrameInterval().Round(time.Microsecond))

	// ---- 〇、对照：只挪位置不改尺寸 ----
	//
	// 这一段是为了把"SetWindowPos 本身"和"改尺寸这件事"分开。两者都在
	// 同一个 API 里，但后者会连带 WM_SIZE、连带给 WebView2 的子窗口发
	// 一次尺寸变更——不分开量就分不清该优化哪一段。
	var mvTotal, mvWorst time.Duration
	mvMsgs0 := r.win.SizeMsgs()
	for i := 1; i <= frames; i++ {
		t0 := time.Now()
		r.win.SetPositionQuiet(x0, y0+i%3)
		d := time.Since(t0)
		mvTotal += d
		if d > mvWorst {
			mvWorst = d
		}
	}
	log.Printf("探针 只挪位置：%d 帧背靠背，平均 %v，最慢 %v，WM_SIZE=%d 次",
		frames, (mvTotal / time.Duration(frames)).Round(time.Microsecond),
		mvWorst.Round(time.Microsecond), r.win.SizeMsgs()-mvMsgs0)

	// ---- 一、吞吐：背靠背，量节流前"每帧都同步"的成本 ----
	var total, worst time.Duration
	slow := 0
	done := 0
	msgs1, syncs1 := r.win.SizeMsgs(), r.win.SyncCount()
	for i := 1; i <= frames; i++ {
		t0 := time.Now()
		if err := a.resizeInstanceQuiet(id, x0, y0, w0+i*2, h0+i*2); err != nil {
			log.Printf("探针 改尺寸节奏：第 %d 帧失败 %v", i, err)
			break
		}
		d := time.Since(t0)
		total += d
		if d > worst {
			worst = d
		}
		if d > 16*time.Millisecond {
			slow++
		}
		done++
	}
	if done == 0 {
		return
	}
	// 等补帧落地，读数才完整。
	time.Sleep(30 * time.Millisecond)
	log.Printf("探针 改尺寸吞吐：%d 帧背靠背，总 %v，平均 %v，最慢 %v，超 16ms 的 %d 帧"+
		"｜WM_SIZE=%d 次 实际同步=%d 次",
		done, total.Round(time.Microsecond),
		(total / time.Duration(done)).Round(time.Microsecond),
		worst.Round(time.Microsecond), slow,
		r.win.SizeMsgs()-msgs1, r.win.SyncCount()-syncs1)

	// ---- 二、跟手：回到起点，按 60fps 走一遍 ----
	if err := a.resizeInstance(context.Background(), id, x0, y0, w0, h0, false); err != nil {
		log.Printf("探针 改尺寸节奏：回起点失败 %v", err)
		return
	}

	var step func(i int)
	step = func(i int) {
		a.deferToUI(func() {
			if i > frames {
				return
			}
			wl, hl := w0+i*2, h0+i*2
			if err := a.resizeInstanceQuiet(id, x0, y0, wl, hl); err != nil {
				log.Printf("探针 改尺寸节奏：节奏第 %d 帧失败 %v", i, err)
				return
			}
			// 每 10 帧读一次页面排版视口：它跟得上窗口，说明重排真的发生了。
			if i%10 == 0 {
				probePageExpect(a, id, i, wl, hl, zoom)
			}
			if i < frames {
				time.AfterFunc(16*time.Millisecond, func() { step(i + 1) })
				return
			}
			// 收尾：留 400ms 让引擎把最后几帧消化掉，再读一次并还原。
			time.AfterFunc(400*time.Millisecond, func() {
				a.deferToUI(func() {
					probePageExpect(a, id, frames, w0+frames*2, h0+frames*2, zoom)
					if err := a.resizeInstance(context.Background(), id, x0, y0, w0, h0, true); err != nil {
						log.Printf("探针 改尺寸节奏：还原失败 %v", err)
						return
					}
					log.Printf("探针 改尺寸节奏：已还原 %d,%d %d×%d（逻辑）", x0, y0, w0, h0)
				})
			})
		})
	}
	step(1)

	// ---- 三、高频：按 125Hz（8ms）走 2 秒，看节流把同步次数压到多少 ----
	//
	// 鼠标报点频率是这一段的关键变量：前两段是"我们发多快就同步多快"，
	// 这一段是"上游发得比屏幕快"，正是用户真实拖动的样子。
	msgs0, syncs0 := r.win.SizeMsgs(), r.win.SyncCount()
	const hiFrames = 250
	var step2 func(i int)
	step2 = func(i int) {
		a.deferToUI(func() {
			if i > hiFrames {
				msgs := r.win.SizeMsgs() - msgs0
				syncs := r.win.SyncCount() - syncs0
				ratio := float64(syncs) / float64(max(msgs, 1))
				log.Printf("探针 改尺寸高频：%d 帧(8ms 间隔) WM_SIZE=%d 次 实际同步=%d 次 节流比=%.2f",
					hiFrames, msgs, syncs, ratio)
				if err := a.resizeInstance(context.Background(), id, x0, y0, w0, h0, true); err != nil {
					log.Printf("探针 改尺寸高频：还原失败 %v", err)
					return
				}
				log.Printf("探针 改尺寸节奏：已还原 %d,%d %d×%d（逻辑）", x0, y0, w0, h0)
				return
			}
			// 幅度小、方向来回：贴近真实拖动时"一格一格挪"的样子。
			dy := i % 20
			if dy > 10 {
				dy = 20 - dy
			}
			a.resizeInstanceQuiet(id, x0, y0, w0+dy, h0+dy/2)
			time.AfterFunc(8*time.Millisecond, func() { step2(i + 1) })
		})
	}
	time.AfterFunc(3*time.Second, func() { step2(1) })
}

// reportContentLag 把"页面排完版的时刻"和"宿主发出尺寸的时刻"对齐相减。
//
// 两边的时钟都是墙上时钟（Date.now() / time.Now()），毫秒精度，够用：
// 要判断的是"差几毫秒"还是"差几十毫秒"，不是微秒级对齐。
//
// 取中位数而不是平均：拖动开头几帧页面还没开始重排，平均值会被拉偏。
func (a *App) reportContentLag() {
	if len(a.bridgeTs) == 0 {
		a.bridgeTs = nil
		return
	}
	host := a.bridgeTs
	a.bridgeTs = nil
	r := a.running["note-1"]
	if r == nil {
		return
	}
	c := r.win.Controller()
	if c == nil {
		return
	}
	// 等页面把最后几帧画完再读。
	time.AfterFunc(300*time.Millisecond, func() {
		a.deferToUI(func() {
			c.Eval(`(window.__snTs||[]).join(',')`, func(ok bool, res string) {
				if !ok {
					log.Printf("探针 内容延迟：读取页面时间戳失败")
					return
				}
				page := parseMillis(decodeEvalResult(res))
				if len(page) == 0 {
					log.Printf("探针 内容延迟：页面没有 resize 记录")
					return
				}
				// 逐帧配对：第 i 次 resize 对应第 i 次尺寸变更。
				var lags []int64
				n := min(len(page), len(host))
				for i := 0; i < n; i++ {
					lags = append(lags, page[i]-host[i])
				}
				sort.Slice(lags, func(i, j int) bool { return lags[i] < lags[j] })
				log.Printf("探针 内容延迟：宿主 %d 帧，页面 resize %d 次，延迟 中位 %dms 最慢 %dms 最快 %dms",
					len(host), len(page), lags[len(lags)/2], lags[len(lags)-1], lags[0])
			})
		})
	})
}

// parseMillis 把逗号分隔的毫秒时间戳解析成切片。
func parseMillis(s string) []int64 {
	var out []int64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, v)
	}
	return out
}

// probePageFrameRate 量页面自己的出帧节奏。
//
// 这一项决定了"内容能不能跟上窗口"：窗口是按输入事件改几何的，而画面只能
// 一帧一帧地出。如果渲染端只有 60fps，而屏幕是 165Hz、鼠标是 125Hz，
// 那么无论几何算得多准，内容看起来都是一格一格跳的——那不是几何的问题。
//
// requestAnimationFrame 的回调由合成器驱动，它的间隔就是这个页面真正
// 能画出来的节奏。
func probePageFrameRate(a *App, id string) {
	r := a.running[id]
	if r == nil {
		log.Printf("探针 页面出帧：%s 没有在跑", id)
		return
	}
	c := r.win.Controller()
	if c == nil {
		log.Printf("探针 页面出帧：%s 还没有页面", id)
		return
	}
	c.Eval(`(function(){`+
		`window.__snFps='采样中';`+
		`var n=0,first=0;`+
		`function tick(t){`+
		`if(!first)first=t;n++;`+
		`if(t-first<600){requestAnimationFrame(tick);}`+
		`else{window.__snFps=n+' 帧 / '+Math.round(t-first)+'ms = '+(n*1000/(t-first)).toFixed(1)+'fps';}`+
		`}`+
		`requestAnimationFrame(tick);`+
		`return 'started';})()`, nil)
	// 采 600ms，等 900ms 再读：Eval 拿到的是脚本的返回值，Promise 不会被等待。
	time.AfterFunc(900*time.Millisecond, func() {
		a.deferToUI(func() {
			c.Eval(`window.__snFps||'没有采样'`, func(ok bool, res string) {
				if !ok {
					log.Printf("探针 页面出帧(%s)：读取失败", id)
					return
				}
				log.Printf("探针 页面出帧(%s)：%s", id, decodeEvalResult(res))
			})
		})
	})
}

// probeBridgeResize 从页面里驱动真实的"布局板拖边框"链路。
//
// 和上面那些段的区别是**入口**：这一段的消息是从 WebView2 的 web 消息回调
// 进来的，SetWindowPos 引发的 WM_SIZE → put_Bounds 是在引擎正持锁时重入引擎。
// 走 Go 直接调 resizeInstanceQuiet 量不出这一层——那正是最容易出问题的一层。
//
// 用的是页面本来就有的出口 window.chrome.webview.postMessage，消息体就是
// 桥接自己的 JSON-RPC 格式，参数解析、尺寸档位校验、commit 分支全是真实代码。
func probeBridgeResize(a *App) {
	if a.manager == nil {
		log.Printf("探针 拖边框链路：管理界面没开")
		return
	}
	v, ok := a.svc.Instance("note-1")
	if !ok {
		log.Printf("探针 拖边框链路：没有 note-1")
		return
	}
	x, y, w, h := v.Bounds.X, v.Bounds.Y, v.Bounds.Width, v.Bounds.Height
	log.Printf("探针 拖边框链路：起点 %d,%d %d×%d（逻辑）", x, y, w, h)

	// 被拖的是 note-1，所以时间戳钩子要挂在**它**的页面上。
	// 挂在管理界面里量到的是管理页自己的 resize，那是另一回事。
	if r := a.running["note-1"]; r != nil {
		if c := r.win.Controller(); c != nil {
			c.Eval(`(function(){window.__snTs=[];`+
				`if(!window.__snTsHooked){window.__snTsHooked=1;`+
				`window.addEventListener('resize',function(){`+
				`if(window.__snTs&&window.__snTs.length<400)window.__snTs.push(Date.now());});}`+
				`return 'hooked';})()`, nil)
		}
	}

	// 60 帧按 16ms 一帧（60fps 拖动），最后补一条 commit 收尾。
	//
	// 传的是对象不是字符串：postMessage 传字符串会被原样当 JSON 字符串值投递，
	// 宿主解出来是 string 而不是 request，日志里只会看到"无法解析前端消息"。
	js := fmt.Sprintf(`(function(){`+
		`var i=0,n=60,bx=%d,by=%d,bw=%d,bh=%d;`+
		`var t=setInterval(function(){`+
		`i++;var k=i%%10;`+
		`window.chrome.webview.postMessage({id:9000+i,`+
		`method:'manager.setInstanceBounds',`+
		`params:{instanceId:'note-1',x:bx,y:by,w:bw+k,h:bh+k,commit:false}});`+
		`if(i>=n){clearInterval(t);`+
		`window.chrome.webview.postMessage({id:9100,`+
		`method:'manager.setInstanceBounds',`+
		`params:{instanceId:'note-1',x:bx,y:by,w:bw,h:bh,commit:true}});}`+
		`},16);`+
		`return 'started';})()`, x, y, w, h)

	c := a.manager.win.Controller()
	if c == nil {
		log.Printf("探针 拖边框链路：管理窗口还没有页面")
		return
	}
	c.Eval(js, func(ok bool, res string) {
		if !ok {
			log.Printf("探针 拖边框链路：启动脚本失败")
			return
		}
		log.Printf("探针 拖边框链路：已启动 %s", decodeEvalResult(res))
	})
}

// probePageExpect 读页面排版视口，和"这一刻窗口该有多大"并排打出来。
//
// clientWidth 是 CSS 像素，窗口尺寸是逻辑像素，两者只在缩放 100% 时相等；
// 内容缩放会改"一个 CSS 像素占几个物理像素"，所以期望值要除一次 zoom。
func probePageExpect(a *App, id string, frame, wl, hl int, zoom float64) {
	r := a.running[id]
	if r == nil {
		return
	}
	c := r.win.Controller()
	if c == nil {
		return
	}
	ew := int(math.Round(float64(wl) / zoom))
	eh := int(math.Round(float64(hl) / zoom))
	c.Eval(`(function(){var e=document.documentElement;`+
		`return e.clientWidth+'x'+e.clientHeight;})()`,
		func(ok bool, result string) {
			if !ok {
				log.Printf("探针 改尺寸跟手(帧%d)：读取失败", frame)
				return
			}
			log.Printf("探针 改尺寸跟手(帧%d)：期望 client=%d×%d 实际 %s",
				frame, ew, eh, decodeEvalResult(result))
		})
}
