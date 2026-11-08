package webview

import (
	"log"
	"os"
	"strings"
)

// diagEnabled 决定要不要跑布局探针。
//
// 默认关闭：它会在每次启动时打一串读数，属于排查工具而不是常规输出。
// 要看"到底排成什么样"时设 STICKYNOTE_DIAG=1。
func diagEnabled() bool { return os.Getenv("STICKYNOTE_DIAG") != "" }

// resizeBench 只开"改尺寸节奏"这一项诊断。
//
// 和 STICKYNOTE_DIAG 分开是有意的：整串界面探针会开抽屉、点方块、改尺寸档位，
// 跑完之后窗口状态已经不是用户摆的那个样子，量出来的成本不干净。
// 这个开关不打开管理界面、不动任何界面，只对实例窗口做两轮改尺寸。
func resizeBench() bool { return os.Getenv("STICKYNOTE_RESIZE_BENCH") != "" }

// buildLayoutProbe 生成一段探针脚本，用来在没有截图的情况下判断"排版乱"乱在哪。
//
// 一次 eval 拿到：根元素/body 的盒子、背景/内边距/字体，
// 以及调用方点名要看的若干选择器（盒子 + computed display）。
func buildLayoutProbe(sels []string) string {
	var sb strings.Builder
	sb.WriteString(`(function(){`)
	sb.WriteString(`function r(e){if(!e)return 'null';var x=e.getBoundingClientRect();`)
	sb.WriteString(`return Math.round(x.left)+','+Math.round(x.top)+' '+Math.round(x.width)+'x'+Math.round(x.height);}`)
	sb.WriteString(`function cs(e){try{return window.getComputedStyle?window.getComputedStyle(e):null;}catch(x){return null;}}`)
	sb.WriteString(`var de=document.documentElement,b=document.body,o=[];`)
	sb.WriteString(`o.push('html='+r(de));o.push('body='+r(b));`)
	// 宿主注入的 --sn-background 内联值。透明底色的组件读 body 也是
	// rgba(0,0,0,0)，跟"基座样式把组件底色盖掉了"这个老 bug 长得一模一样；
	// 只有把变量本身读出来才能分清"本来就要透明"和"透明是被盖出来的"。
	sb.WriteString(`o.push('varBg='+(de.style.getPropertyValue('--sn-background')||'-'));`)
	sb.WriteString(`if(b){var bc=cs(b);o.push('bg='+(bc?bc.backgroundColor:'?'));`)
	sb.WriteString(`o.push('bpad='+(bc?bc.padding:'?'));`)
	sb.WriteString(`o.push('font='+(bc?bc.fontSize+' '+bc.fontFamily:'?'));}`)
	sb.WriteString(`o.push('host='+(typeof window.__host));`)
	// 第一个子元素的底色与圆角：窗口不透明靠的往往不是 body（html 透明时
	// body 的背景会传播到画布，border-radius 就管不住它），而是内层画的那一块。
	// 只读 body 会得出"窗口是透明的"的假结论——便签就是这么被骗过一次。
	sb.WriteString(`if(b&&b.firstElementChild){var fe=b.firstElementChild,fc=cs(fe);`)
	sb.WriteString(`o.push('子元素='+fe.tagName+'.'+(fe.className||'-'));`)
	sb.WriteString(`o.push('子bg='+(fc?fc.backgroundColor:'?'));`)
	sb.WriteString(`o.push('子圆角='+(fc?fc.borderTopLeftRadius:'?'));}`)
	for _, s := range sels {
		// 选择器只允许字母数字和 . # - _ * 空格 >，避免拼进脚本出岔子。
		// `*` 必须留着：`body>*` 去掉星号就成了非法的 `body>`，
		// querySelector 会抛 SyntaxError，整条探针返回 null（一个读数都拿不到）。
		clean := strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
				return r
			case r == '.', r == '#', r == '-', r == '_', r == ' ', r == '>', r == '*':
				return r
			}
			return -1
		}, s)
		// 单项查询失败只该让这一项变成 '?'，不该把整条探针打掉：
		// 一个写错的选择器不该让其他读数一起消失。
		sb.WriteString(`(function(){var e=null,x=null;try{e=document.querySelector('` + clean + `');}catch(err){}`)
		sb.WriteString(`if(!e){o.push('` + clean + `=无');return;}`)
		sb.WriteString(`x=cs(e);o.push('` + clean + `='+r(e)+'/'+(x?x.display:'?'));})();`)
	}
	sb.WriteString(`return o.join(' ');})()`)
	return sb.String()
}

// decodeEvalResult 把 ExecuteScript 的结果还原成人读的字符串。
//
// 引擎返回的是 JSON：字符串结果外面多一层引号，内部的引号和换行也被转义过。
func decodeEvalResult(s string) string {
	s = strings.TrimSpace(s)
	// ExecuteScript 在脚本抛异常时返回 null（不是报错），
	// 直接打出来只有个 "null"，看不出是"没数据"还是"脚本挂了"。
	if s == "" || s == "null" {
		return "（没有返回值：脚本可能抛了异常）"
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	s = strings.ReplaceAll(s, `\"`, `"`)
	s = strings.ReplaceAll(s, `\n`, " ")
	return s
}

// evalProbe 执行一段探针脚本并把结果打进日志。
func evalProbe(win *Window, tag, js string) {
	ctrl := win.Controller()
	if ctrl == nil {
		log.Printf("%s：窗口还没有页面", tag)
		return
	}
	ctrl.Eval(js, func(ok bool, result string) {
		if !ok {
			log.Printf("%s 探针失败", tag)
			return
		}
		log.Printf("%s：%s", tag, decodeEvalResult(result))
	})
}

// logLayout 把布局探针的结果写进日志。
//
// 引擎的 ExecuteScript 是异步的，结果从回调里来——所以不能"取回字符串
// 再打印"，只能在回调里打印。
func logLayout(tag string, win *Window, sels ...string) {
	ctrl := win.Controller()
	if ctrl == nil {
		log.Printf("布局 %s：窗口还没有页面", tag)
		return
	}
	ctrl.Eval(buildLayoutProbe(sels), func(ok bool, result string) {
		if !ok {
			log.Printf("布局 %s：探针失败", tag)
			return
		}
		log.Printf("布局 %s：%s", tag, decodeEvalResult(result))
	})
}

// exStyleOf 取任意窗口的扩展样式位。
func exStyleOf(hwnd uintptr) uint32 {
	idx := int32(-20) // GWL_EXSTYLE
	ret, _, _ := procGetWindowLongPtrW.Call(hwnd, uintptr(idx))
	return uint32(ret)
}

// dumpWindowTree 把窗口树连同每个窗口的扩展样式打进日志。
//
// 排查"点不穿"必须看这个：WS_EX_TRANSPARENT 只加在宿主窗口上，
// 而 WebView2 会在自己底下建子窗口。子窗口**不继承**父窗口的扩展样式，
// 鼠标落在子窗口上就被它接住，父窗口透不透明都没用。
// 光看宿主窗口的 exStyle 会得出"设对了"的假结论。
func dumpWindowTree(tag string, win *Window) {
	host := win.Handle()
	log.Printf("窗口树 %s：宿主 %#x ex=0x%08x", tag, host, exStyleOf(host))

	var walk func(parent uintptr, depth int)
	walk = func(parent uintptr, depth int) {
		child, _, _ := procGetWindow.Call(parent, gwChild)
		for child != 0 {
			ex := exStyleOf(child)
			vis, _, _ := procIsWindowVisible.Call(child)
			log.Printf("窗口树 %s：%s%s %#x ex=0x%08x visible=%v layered=%v transparent=%v",
				tag, strings.Repeat("  ", depth+1), windowClass(child), child, ex,
				vis != 0, ex&wsExLayered != 0, ex&wsExTransparent != 0)
			walk(child, depth+1)
			child, _, _ = procGetWindow.Call(child, gwHwndNext)
		}
	}
	walk(host, 0)
}
