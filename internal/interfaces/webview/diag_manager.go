package webview

import (
	"encoding/json"
	"log"
	"time"
)

// probeInstanceStyles 在改组之后读一次每个窗口的扩展样式。
//
// 为什么要单独读：`layer=topmost` 只是**配置**，真正生效要看窗口上有没有
// WS_EX_TOPMOST 这一位。拆了重建之后如果这一位没落上，窗口看起来"配置是
// 置顶的"、实际却被别的应用盖住，而且不会有任何报错。
//
// 时间点选在改组动作之后：15s 是刚重建完，21s 是再等一会儿，用来区分
// "没设" 和 "设了又被别人重置"。
func probeInstanceStyles(a *App) {
	for _, at := range []time.Duration{15 * time.Second, 21 * time.Second} {
		at := at
		time.AfterFunc(at, func() {
			a.deferToUI(func() {
				for _, id := range a.order {
					r := a.running[id]
					if r == nil {
						continue
					}
					ex := r.win.ExStyle()
					log.Printf("探针 %s@%v：layer=%s WS_EX_TOPMOST=%v ex=0x%08x",
						id, at, r.view.Style.Layer, ex&wsExTopmost != 0, ex)
				}
			})
		})
	}
}

// 管理界面的启动自检。
//
// 全部只在 STICKYNOTE_DIAG=1 时跑：它们问的是"画出来的状态对不对"，
// 而这类问题不会报错、也不会崩，只能靠读数发现。单独放一个文件，
// 免得诊断脚本淹掉 openManager 的主流程。
func probeManagerUI(a *App, win *Window) {
	// 开关状态：确认 checked 属性、:checked 选择器、DOM 属性三者是否一致。
	// 这三者一旦不一致，开关就会"数据是开的、画出来是关的"，而且不报错。
	time.AfterFunc(2000*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "开关",
				`(function(){`+
					`function stat(sel){var ns=document.querySelectorAll(sel),p=0,m=0,a=0;`+
					`for(var i=0;i<ns.length;i++){if(ns[i].checked)p++;`+
					`if(ns[i].matches(':checked'))m++;`+
					`if(ns[i].getAttribute('checked')!==null)a++;}`+
					`return p+'/'+m+'/'+a+'/'+ns.length;}`+
					`return '组件行(prop/sel/attr/total)='+stat('#groupList .switch input');})()`)
		})
	})

	// 侧边栏结构：组卡片数、每个组的实例行数、无实例的组件行。
	// 这几项一旦对不上，就是"组件没挂在任何组下面"或"实例没列出来"这类问题。
	time.AfterFunc(2600*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "侧边栏",
				`(function(){`+
					`var gs=document.querySelectorAll('#groupList .group');`+
					`var r=['组='+gs.length];`+
					`for(var i=0;i<gs.length;i++){`+
					`var n=gs[i].querySelector('.group__name');`+
					`var c=gs[i].querySelectorAll('.item').length;`+
					`r.push((n?n.textContent:'?')+':'+c);}`+
					`r.push('实例行='+document.querySelectorAll('#groupList .item').length);`+
					`r.push('无实例行='+document.querySelectorAll('#groupList .item--empty').length);`+
					`var rows=[];`+
					`var its=document.querySelectorAll('#groupList .item');`+
					`for(var k=0;k<its.length&&k<6;k++){`+
					`var nm=its[k].querySelector('.item__name');`+
					`var mt=its[k].querySelector('.item__meta');`+
					`rows.push((nm?nm.textContent:'?')+'/'+(mt?mt.textContent:'?'));}`+
					`r.push('行='+rows.join(','));`+
					`var side=document.querySelector('.sidebar');`+
					`if(side){var sr=side.getBoundingClientRect();`+
					`r.push('sidebar='+Math.round(sr.width)+'x'+Math.round(sr.height));}`+
					`return r.join(' ');})()`)
		})
	})

	// 布局板读数。量尺寸要等一会儿：面板刚显示时布局还没算完，
	// 立刻量一定是 0（第一版就是同步量的，差点被当成第二个 bug）。
	time.AfterFunc(3600*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "布局板(全部组)",
				`(function(){`+
					`var bd=document.getElementById('board');`+
					`if(!bd)return '没有布局板';`+
					`var br=bd.getBoundingClientRect();`+
					`var wrap=document.getElementById('boardWrap');`+
					`var wr=wrap?wrap.getBoundingClientRect():null;`+
					`var r=['board='+Math.round(br.width)+'x'+Math.round(br.height)];`+
					`if(wr)r.push('wrap='+Math.round(wr.width)+'x'+Math.round(wr.height));`+
					`r.push('溢出='+(wr&&(br.width>wr.width+1||br.height>wr.height+1)?'是':'否'));`+
					`r.push('只读='+(bd.classList.contains('is-readonly')?'是':'否'));`+
					`r.push('标题='+((document.getElementById('boardTitle')||{}).textContent||'?'));`+
					`r.push('图例='+document.querySelectorAll('.legend__item').length);`+
					`var bs=bd.querySelectorAll('.box');`+
					`r.push('方块='+bs.length);`+
					`for(var j=0;j<bs.length;j++){var q=bs[j].getBoundingClientRect();`+
					`r.push(bs[j].getAttribute('data-instance')+'='+Math.round(q.left-br.left)+','+`+
					`Math.round(q.top-br.top)+' '+Math.round(q.width)+'x'+Math.round(q.height)+`+
					`'['+getComputedStyle(bs[j]).borderTopColor+']');}`+
					`return r.join(' ');})()`)
		})
	})

	// 切到「某一个组」：点第一个组的组头，布局板应该只剩这个组的方块。
	//
	// 用 dispatchEvent 而不是 el.click()：后者在部分引擎里是空操作，
	// 拿它做诊断会得出"点了没反应"的错误结论。这里是页面内的 DOM 断言，
	// 不是伪造系统输入——鼠标真点还是要靠人。
	time.AfterFunc(4200*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var h=document.querySelector('#groupList .group__head');`+
					`if(h)h.dispatchEvent(new Event('click',{bubbles:true}));})()`, nil)
			}
		})
	})

	time.AfterFunc(4700*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "布局板(单个组)",
				`(function(){`+
					`var bd=document.getElementById('board');`+
					`if(!bd)return '没有布局板';`+
					`var out=['标题='+((document.getElementById('boardTitle')||{}).textContent||'?')];`+
					`out.push('只读='+(bd.classList.contains('is-readonly')?'是':'否'));`+
					`out.push('方块='+bd.querySelectorAll('.box').length);`+
					`out.push('图例='+document.querySelectorAll('.legend__item').length);`+
					`var sa=document.getElementById('showAll');`+
					`out.push('显示全部按钮='+(sa&&!sa.hasAttribute('hidden')?'显示':'隐藏'));`+
					`var act=document.querySelectorAll('.group.is-active');`+
					`out.push('选中组='+(act.length?(act[0].querySelector('.group__name')||{}).textContent:'无'));`+
					`out.push('提示='+((document.getElementById('boardHint')||{}).textContent||''));`+
					`return out.join(' ');})()`)
		})
	})

	// 抽屉读数：点开当前组的「⋯」，确认所有控件都真的画出来了。
	time.AfterFunc(5100*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var b=document.querySelector('#groupList .group__gear');`+
					`if(b)b.dispatchEvent(new Event('click',{bubbles:true}));})()`, nil)
			}
		})
	})

	time.AfterFunc(5600*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "组设置抽屉",
				`(function(){`+
					`var d=document.getElementById('drawer');`+
					`if(!d)return '没有抽屉';`+
					`if(d.hasAttribute('hidden'))return '抽屉没打开';`+
					`var r=d.getBoundingClientRect();`+
					`var out=['drawer='+Math.round(r.width)+'x'+Math.round(r.height)];`+
					`out.push('标题='+((d.querySelector('.drawer__title')||{}).textContent||'?'));`+
					`out.push('输入框='+d.querySelectorAll('.input').length);`+
					`out.push('三选一='+d.querySelectorAll('.seg').length);`+
					`out.push('开关='+d.querySelectorAll('.switch input').length);`+
					`out.push('滑杆='+d.querySelectorAll('input[type=range]').length);`+
					`out.push('取色='+d.querySelectorAll('input[type=color]').length);`+
					`out.push('成员行='+d.querySelectorAll('.member').length);`+
					`var seg=d.querySelector('.seg .seg__btn.is-on');`+
					`out.push('当前档位='+(seg?seg.textContent:'无'));`+
					`var ci=d.querySelector('[data-act="groupColorText"]');`+
					`out.push('识别色='+(ci?ci.value:'无'));`+
					`var body=d.querySelector('.drawer__body');`+
					`if(body)out.push('滚动='+(body.scrollHeight>body.clientHeight?'是':'否'));`+
					`return out.join(' ');})()`)
		})
	})

	// 折叠：点组头上的箭头，成员列表应该收起来；再点一次恢复。
	// 这是页面内的 DOM 断言，不是伪造系统输入。
	time.AfterFunc(6100*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var b=document.querySelector('#groupList .group__chev');`+
					`if(b)b.dispatchEvent(new Event('click',{bubbles:true}));})()`, nil)
			}
		})
	})

	time.AfterFunc(6500*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "折叠",
				`(function(){`+
					`var g=document.querySelector('#groupList .group');`+
					`if(!g)return '没有组';`+
					`var m=g.querySelector('.group__members');`+
					`var ch=g.querySelector('.group__chev');`+
					`var out=['成员='+(m?(getComputedStyle(m).display==='none'?'收起':'展开'):'无')];`+
					`out.push('箭头='+(ch&&ch.classList.contains('is-collapsed')?'已转':'正常'));`+
					`out.push('行仍在DOM='+(m?m.querySelectorAll('.item').length:0));`+
					`var gr=g.getBoundingClientRect();`+
					`out.push('组高='+Math.round(gr.height));`+
					// 量完再点一次恢复，别把界面留在折叠状态。
					`if(ch)ch.dispatchEvent(new Event('click',{bubbles:true}));`+
					`return out.join(' ');})()`)
		})
	})

	// 切到「组件」视角。归属下移到实例之后，组件卡里列的是它名下的窗口，
	// 每行带一个"属于哪个组"的下拉——这两项对不上就说明扁平清单没下发。
	time.AfterFunc(7100*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var b=document.querySelector('#sideTabs [data-tab="components"]');`+
					`if(b)b.dispatchEvent(new Event('click',{bubbles:true}));})()`, nil)
			}
		})
	})

	time.AfterFunc(7600*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "侧边栏(组件视角)",
				`(function(){`+
					`var cs=document.querySelectorAll('#groupList .comp');`+
					`var r=['组件卡='+cs.length];`+
					`var rows=[];`+
					`for(var i=0;i<cs.length&&i<6;i++){`+
					`var n=cs[i].querySelector('.comp__name');`+
					`r.push((n?n.textContent:'?')+':'+cs[i].querySelectorAll('.item').length);`+
					`var it=cs[i].querySelector('.item');`+
					`if(it){var nm=it.querySelector('.item__name');`+
					`var gs=it.querySelector('select.item__group');`+
					`rows.push((nm?nm.textContent:'?')+'→'+(gs?gs.options[gs.selectedIndex].text:'无下拉'));}}`+
					`r.push('实例行='+document.querySelectorAll('#groupList .item').length);`+
					`r.push('组下拉='+document.querySelectorAll('#groupList select.item__group').length);`+
					`r.push('明细='+rows.join(','));`+
					`var ng=document.getElementById('newGroup');`+
					`r.push('新建组按钮='+(ng&&!ng.hasAttribute('hidden')?'显示':'隐藏'));`+
					`return r.join(' ');})()`)
		})
	})

	// 组件设置抽屉：实例行里要能看到组下拉（实例级改组）与两个操作按钮。
	time.AfterFunc(8100*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var b=document.querySelector('#groupList .comp__head .group__gear');`+
					`if(b)b.dispatchEvent(new Event('click',{bubbles:true}));})()`, nil)
			}
		})
	})

	time.AfterFunc(8600*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "组件设置抽屉",
				`(function(){`+
					`var d=document.getElementById('drawer');`+
					`if(!d)return '没有抽屉';`+
					`if(d.hasAttribute('hidden'))return '抽屉没打开';`+
					`var out=['标题='+((d.querySelector('.drawer__title')||{}).textContent||'?')];`+
					`out.push('实例行='+d.querySelectorAll('.member').length);`+
					`out.push('组下拉='+d.querySelectorAll('.member select').length);`+
					`out.push('新建组下拉='+d.querySelectorAll('[data-act="newInstanceGroup"]').length);`+
					`var sel=d.querySelector('.member select');`+
					`out.push('首个实例组='+(sel?sel.options[sel.selectedIndex].text:'无'));`+
					`return out.join(' ');})()`)
		})
	})

	// 回到组视角，点组头的「+」打开选组件抽屉。
	time.AfterFunc(9100*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var t=document.querySelector('#sideTabs [data-tab="groups"]');`+
					`if(t)t.dispatchEvent(new Event('click',{bubbles:true}));`+
					`var b=document.querySelector('#groupList .group__add');`+
					`if(b)b.dispatchEvent(new Event('click',{bubbles:true}));})()`, nil)
			}
		})
	})

	time.AfterFunc(9600*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "选组件抽屉",
				`(function(){`+
					`var d=document.getElementById('drawer');`+
					`if(!d||d.hasAttribute('hidden'))return '抽屉没打开';`+
					`var out=['标题='+((d.querySelector('.drawer__title')||{}).textContent||'?')];`+
					`out.push('副标题='+((d.querySelector('.drawer__sub')||{}).textContent||'?'));`+
					`out.push('可选组件='+d.querySelectorAll('.pick').length);`+
					`out.push('可选(未禁用)='+d.querySelectorAll('.pick:not([disabled])').length);`+
					`var names=[];`+
					`var ps=d.querySelectorAll('.pick');`+
					`for(var i=0;i<ps.length&&i<5;i++){`+
					`var n=ps[i].querySelector('.pick__name');`+
					`var m=ps[i].querySelector('.pick__meta');`+
					`names.push((n?n.textContent:'?')+'/'+(m?m.textContent:'?'));}`+
					`out.push('列表='+names.join(','));`+
					// 量完把抽屉收起来，别把界面留在打开状态。
					`var x=d.querySelector('[data-act="drawerClose"]');`+
					`if(x)x.dispatchEvent(new Event('click',{bubbles:true}));`+
					`return out.join(' ');})()`)
		})
	})

	// 端到端走一遍"组里选组件新建实例"：打开选组件抽屉 → 点第一个组件
	// → 宿主建窗口 → 状态推回来。建出来的实例最后要删掉，别留一个多余窗口。
	//
	// 这一条验的是桥接链路（manager.createInstance 带 groupId + spawn + pushState），
	// 单测只覆盖到应用层，建窗口那一段只有跑起来才知道通不通。
	time.AfterFunc(10100*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`window.__snBefore=document.querySelectorAll('#groupList .item').length;`+
					`var add=document.querySelector('#groupList .group__add');`+
					`if(add)add.dispatchEvent(new Event('click',{bubbles:true}));`+
					`var p=document.querySelector('#drawer .pick:not([disabled])');`+
					`if(p)p.dispatchEvent(new Event('click',{bubbles:true}));})()`, nil)
			}
		})
	})

	// 建窗口是异步的（请求排到 UI 队列上，还要等 WebView2 控制器就绪），
	// 所以读数要留足时间。读得太早会看到"没建出来"，然后误删掉一个老实例。
	time.AfterFunc(11300*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "新建实例(组内选组件)",
				`(function(){`+
					`var gs=document.querySelectorAll('#groupList .group');`+
					`var r=[];`+
					`for(var i=0;i<gs.length;i++){`+
					`var n=gs[i].querySelector('.group__name');`+
					`r.push((n?n.textContent:'?')+':'+gs[i].querySelectorAll('.item').length);}`+
					`var now=document.querySelectorAll('#groupList .item').length;`+
					`var before=window.__snBefore||0;`+
					`window.__snProbeId='';`+
					`if(now>before){var g=gs[0];var its=g?g.querySelectorAll('.item'):[];`+
					`if(its.length)window.__snProbeId=its[its.length-1].getAttribute('data-instance');}`+
					`return '分布='+r.join(',')+' 前='+before+' 后='+now+`+
					`' 状态栏='+((document.getElementById('status')||{}).textContent||'?')+`+
					`' 新实例='+(window.__snProbeId||'（没建出来，跳过删除）');})()`)
		})
	})

	// 删掉刚才那个实例：点它那一行进实例抽屉 → 关闭实例 → 确认框点确定。
	// 没建出来就什么都不做，别误删用户的窗口。
	time.AfterFunc(11800*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var id=window.__snProbeId;if(!id)return;`+
					`var it=document.querySelector('#groupList .item[data-instance="'+id+'"]');`+
					`if(it)it.dispatchEvent(new Event('click',{bubbles:true}));})()`, nil)
			}
		})
	})

	time.AfterFunc(12200*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`if(!window.__snProbeId)return;`+
					`var b=document.querySelector('#drawer [data-act="instanceRemove"]');`+
					`if(b)b.dispatchEvent(new Event('click',{bubbles:true}));})()`, nil)
			}
		})
	})

	time.AfterFunc(12600*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`if(!window.__snProbeId)return;`+
					`var ok=document.getElementById('modalOk');`+
					`if(ok)ok.dispatchEvent(new Event('click',{bubbles:true}));})()`, nil)
			}
		})
	})

	time.AfterFunc(13100*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "删除实例后",
				`(function(){`+
					`var gs=document.querySelectorAll('#groupList .group');`+
					`var r=[];`+
					`for(var i=0;i<gs.length;i++){`+
					`var n=gs[i].querySelector('.group__name');`+
					`r.push((n?n.textContent:'?')+':'+gs[i].querySelectorAll('.item').length);}`+
					`var d=document.getElementById('drawer');`+
					`return '分布='+r.join(',')+`+
					`' 状态栏='+((document.getElementById('status')||{}).textContent||'?')+`+
					`' 抽屉='+(d&&!d.hasAttribute('hidden')?'开着':'已收');})()`)
		})
	})

	// 实例级改组：在组件视角里把 note-1 从「置顶」挪到「桌面层」。
	// 这一步验的是 manager.setInstanceGroup + 单实例重建——它必须只动这一个
	// 窗口，同组件的另一个实例（如果开着）不受影响。
	time.AfterFunc(13600*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var t=document.querySelector('#sideTabs [data-tab="components"]');`+
					`if(t)t.dispatchEvent(new Event('click',{bubbles:true}));`+
					`var s=document.querySelector('#groupList select.item__group[data-instance="note-1"]');`+
					`if(!s)return;`+
					`for(var i=0;i<s.options.length;i++){`+
					`if(s.options[i].text==='桌面层'){s.value=s.options[i].value;break;}}`+
					`s.dispatchEvent(new Event('change',{bubbles:true}));})()`, nil)
			}
		})
	})

	time.AfterFunc(14200*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "实例改组(→桌面层)",
				`(function(){`+
					`var s=document.querySelector('#groupList select.item__group[data-instance="note-1"]');`+
					`var cur=s?(s.options[s.selectedIndex]||{}).text:'无';`+
					`var gs=document.querySelectorAll('#groupList .group');`+
					`var r=[];`+
					`for(var i=0;i<gs.length;i++){`+
					`var n=gs[i].querySelector('.group__name');`+
					`r.push((n?n.textContent:'?')+':'+gs[i].querySelectorAll('.item').length);}`+
					// 改回「置顶」，别把用户的窗口留在别的层。
					`if(s){for(var j=0;j<s.options.length;j++){`+
					`if(s.options[j].text==='置顶'){s.value=s.options[j].value;break;}}`+
					`s.dispatchEvent(new Event('change',{bubbles:true}));}`+
					`return 'note-1 现在属于='+cur+' 状态栏='+((document.getElementById('status')||{}).textContent||'?');})()`)
		})
	})

	// 变量表单：打开 note-1 的实例抽屉，看它有没有按声明渲染出控件。
	// 声明在组件上、值在实例上——抽屉是唯一同时需要两者的地方。
	time.AfterFunc(14600*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var it=document.querySelector('#groupList .item[data-instance="note-1"]');`+
					`if(it)it.dispatchEvent(new Event('click',{bubbles:true}));})()`, nil)
			}
		})
	})

	time.AfterFunc(15100*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "实例抽屉(变量)",
				`(function(){`+
					`var d=document.getElementById('drawer');`+
					`if(!d||d.hasAttribute('hidden'))return '抽屉没打开';`+
					`var f=d.querySelectorAll('[data-var]');`+
					`var out=['控件='+f.length];`+
					`var tags=[];`+
					`for(var i=0;i<f.length;i++){`+
					`var val=f[i].dataset.records`+
					`?('记录表'+f[i].querySelectorAll('.rec').length+'条')`+
					`:(f[i].type==='checkbox'?(f[i].checked?'true':'false'):f[i].value);`+
					`tags.push(f[i].tagName.toLowerCase()+'/'+f[i].getAttribute('data-var')+'='+val);}`+
					`out.push('明细='+tags.join(','));`+
					`out.push('必填角标='+d.querySelectorAll('.var__req').length);`+
					`out.push('变量区提示='+d.querySelectorAll('.field--vars .field__hint').length);`+
					`var body=d.querySelector('.drawer__body');`+
					`if(body)out.push('滚动='+(body.scrollHeight>body.clientHeight?'是':'否'));`+
					`return out.join(' ');})()`)
		})
	})

	// 改一个选项：走真实的 change 事件（页面内的 DOM 断言，不是伪造系统输入）。
	// 桥接会把 manager.setVars 发出去，宿主写盘、再推 host:vars 给组件。
	time.AfterFunc(15600*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var s=document.querySelector('#drawer [data-var="fontSize"]');`+
					`if(!s)return;`+
					`s.value='l';`+
					`s.dispatchEvent(new Event('change',{bubbles:true}));})()`, nil)
			}
		})
	})

	// 读数必须**重新查一次元素**：提交之后抽屉会按新状态重渲染，
	// 手里那个 s 已经是脱离文档的旧节点，读它的 value 等于读自己写进去的东西。
	time.AfterFunc(16300*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "变量已改(字号=l)",
				`(function(){`+
					`var s=document.querySelector('#drawer [data-var="fontSize"]');`+
					`var out=['重渲染后='+(s?s.value:'无')];`+
					// 改回默认值并收起抽屉，别把用户的便签留在别的字号上。
					`if(s){s.value='m';s.dispatchEvent(new Event('change',{bubbles:true}));}`+
					`var x=document.querySelector('#drawer [data-act="drawerClose"]');`+
					`if(x)x.dispatchEvent(new Event('click',{bubbles:true}));`+
					`return out.join(' ');})()`)
		})
	})

	// 列表式变量（`type:"list"`）：弹幕的 `lines`。
	//
	// 这一段验的是"用户只填文字、结构化交给内核"那条路：界面上是一个
	// 普通多行文本框（一行一条），宿主把它包成 `[{"text":"…"}]` 再下发给
	// 组件。真正的凭据有两个——`data/vars/danmaku/danmaku-1.json` 里存的
	// 是不是对象数组，以及弹幕窗口里那行字有没有变成"诊断一 · 诊断二"。
	//
	// 收尾必须把 `lines` 清回默认值：全空会走 `Coerce` 返回 nil 那条路，
	// 把 key 从文件里删掉，用户原来的 `text`/`speed` 一个字都不动。
	time.AfterFunc(17000*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var it=document.querySelector('#groupList .item[data-instance="danmaku-1"]');`+
					`if(it)it.dispatchEvent(new Event('click',{bubbles:true}));})()`, nil)
			}
		})
	})

	time.AfterFunc(17600*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "实例抽屉(列表变量)",
				`(function(){`+
					`var d=document.getElementById('drawer');`+
					`if(!d||d.hasAttribute('hidden'))return '抽屉没打开';`+
					`var j=d.querySelector('[data-var="lines"]');`+
					`if(!j)return '没有 lines 控件';`+
					`var f=j.closest('.field');`+
					`var hints=f?f.querySelectorAll('.field__hint').length:0;`+
					`return ['控件='+j.tagName.toLowerCase(),`+
					`'data-list='+j.getAttribute('data-list'),`+
					`'行数='+j.getAttribute('rows'),`+
					`'初值='+JSON.stringify(j.value),`+
					`'提示行='+hints,`+
					`'还有 JSON 框='+d.querySelectorAll('[data-json]').length].join(' ');})()`)
		})
	})

	// 往框里写两行并触发 input（走 400ms 合并那条路）。
	time.AfterFunc(18000*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var j=document.querySelector('#drawer [data-var="lines"]');`+
					`if(!j)return;`+
					`j.value='诊断一\n诊断二';`+
					`j.dispatchEvent(new Event('input',{bubbles:true}));})()`, nil)
			}
		})
	})

	// 读回重渲染后的框：两行原样还在，说明存下去又取回来是同一份东西。
	time.AfterFunc(18800*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "列表已存(界面)",
				`(function(){`+
					`var d=document.getElementById('drawer');`+
					`var j=d?d.querySelector('[data-var="lines"]'):null;`+
					`if(!j)return '没有 lines 控件';`+
					`return '重渲染后='+JSON.stringify(j.value);})()`)
		})
	})

	// 组件侧：弹幕把每条的 text 拼成 " · " 一行，所以窗口里那行字就是
	// "数组真的到了组件手里"的唯一凭据（若宿主下发的是字符串，
	// `Array.isArray` 为假，这里会退回原来的单行文字）。
	time.AfterFunc(19400*time.Millisecond, func() {
		win.Post(func() {
			r := a.running["danmaku-1"]
			if r == nil || r.win == nil {
				log.Printf("弹幕收到列表：窗口不在")
				return
			}
			evalProbe(r.win, "弹幕收到列表",
				`(function(){`+
					`var it=document.getElementById('item');`+
					`var all=document.querySelectorAll('.danmaku__item');`+
					`return '正文='+JSON.stringify(it?it.textContent.trim():'')+`+
					`' 轨道份数='+all.length;})()`)
		})
	})

	// 一行里写方括号：以前那是"JSON 写错了"，现在只是这一行的文字。
	time.AfterFunc(20000*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var j=document.querySelector('#drawer [data-var="lines"]');`+
					`if(!j)return;`+
					`j.value='[{"text":"a"},';`+
					`j.dispatchEvent(new Event('input',{bubbles:true}));})()`, nil)
			}
		})
	})

	time.AfterFunc(20800*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "方括号当成普通文字",
				`(function(){`+
					`var d=document.getElementById('drawer');`+
					`var j=d?d.querySelector('[data-var="lines"]'):null;`+
					`if(!j)return '没有 lines 控件';`+
					`return '重渲染后='+JSON.stringify(j.value)+`+
					`' 有红框='+(j.className.indexOf('is-bad')>=0?'是':'否');})()`)
		})
	})

	// 收尾：清空并关抽屉。清空之后弹幕应当**退回**单行文字
	// （`lines` 空 = 没用这个变量），这一条也顺手验掉。
	time.AfterFunc(21400*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var j=document.querySelector('#drawer [data-var="lines"]');`+
					`if(j){j.value='';j.dispatchEvent(new Event('input',{bubbles:true}));}`+
					`var x=document.querySelector('#drawer [data-act="drawerClose"]');`+
					`if(x)x.dispatchEvent(new Event('click',{bubbles:true}));})()`, nil)
			}
		})
	})

	time.AfterFunc(22400*time.Millisecond, func() {
		win.Post(func() {
			r := a.running["danmaku-1"]
			if r == nil || r.win == nil {
				log.Printf("弹幕退回单行文字：窗口不在")
				return
			}
			evalProbe(r.win, "弹幕退回单行文字",
				`(function(){`+
					`var it=document.getElementById('item');`+
					`var t=it?it.textContent.trim():'';`+
					`return '正文='+JSON.stringify(t)+' 长度='+t.length;})()`)
		})
	})

	// 记录表（`type:"group"`）：一条记录一张卡片，字段纵向排列，可增删。
	//
	// 这一段用 note 的**已安装副本**里临时挂上去的 `cards` 变量来验
	// （跑完会还原那份清单）：`title` 单行 + `context` 多行，正是
	// "一条记录几个字段"的典型形状。
	//
	// 要验的是三件事：卡片按声明画出来了、加一条能落盘、删一条能落盘。
	// 真正的凭据仍然是 `data/vars/note/note-1.json` 里那段是不是
	// `[{"title":"…","context":"…"}]`。
	time.AfterFunc(23000*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var t=document.querySelector('#sideTabs [data-tab="components"]');`+
					`if(t)t.dispatchEvent(new Event('click',{bubbles:true}));`+
					`var it=document.querySelector('#groupList .item[data-instance="note-1"]');`+
					`if(it)it.dispatchEvent(new Event('click',{bubbles:true}));})()`, nil)
			}
		})
	})

	time.AfterFunc(23600*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "记录表渲染",
				`(function(){`+
					`var d=document.getElementById('drawer');`+
					`if(!d||d.hasAttribute('hidden'))return '抽屉没打开';`+
					`var box=d.querySelector('.recs[data-var="cards"]');`+
					`if(!box)return '没有 cards 记录表';`+
					`var add=d.querySelector('[data-act="recAdd"]');`+
					`var cards=box.querySelectorAll('.rec');`+
					`var first=cards.length?cards[0]:null;`+
					`var fs=first?first.querySelectorAll('[data-rec-field]'):[];`+
					`var names=[];`+
					`for(var i=0;i<fs.length;i++){`+
					`names.push(fs[i].getAttribute('data-rec-field')+'/'+fs[i].getAttribute('data-rec-type')+'/'+fs[i].tagName.toLowerCase());}`+
					`return ['卡片='+cards.length,`+
					`'加按钮='+(add?'有':'无'),`+
					`'字段='+names.join(','),`+
					`'还有 JSON 框='+d.querySelectorAll('[data-json]').length].join(' ');})()`)
		})
	})

	// 点「+ 添加一条」：应当追加一张空卡片，且**不写盘**（空卡片不该留痕）。
	time.AfterFunc(24000*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var add=document.querySelector('#drawer [data-act="recAdd"]');`+
					`if(add)add.dispatchEvent(new Event('click',{bubbles:true}));})()`, nil)
			}
		})
	})

	time.AfterFunc(24400*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "加一条(空)",
				`(function(){`+
					`var d=document.getElementById('drawer');`+
					`var box=d?d.querySelector('.recs[data-var="cards"]'):null;`+
					`if(!box)return '没有 cards 记录表';`+
					`var cards=box.querySelectorAll('.rec');`+
					`var last=cards[cards.length-1];`+
					`var no=last?last.querySelector('.rec__no'):null;`+
					`return '卡片='+cards.length+' 编号='+(no?no.textContent:'?');})()`)
		})
	})

	// 往新卡片里填两个字，走 input 事件（400ms 合并提交）。
	time.AfterFunc(24800*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var box=document.querySelector('#drawer .recs[data-var="cards"]');`+
					`if(!box)return;`+
					`var cards=box.querySelectorAll('.rec');`+
					`var last=cards[cards.length-1];`+
					`if(!last)return;`+
					`var t=last.querySelector('[data-rec-field="title"]');`+
					`var x=last.querySelector('[data-rec-field="context"]');`+
					`if(t)t.value='第二张';`+
					`if(x)x.value='内容二';`+
					`if(x)x.dispatchEvent(new Event('input',{bubbles:true}));})()`, nil)
			}
		})
	})

	time.AfterFunc(25800*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "记录表已存(界面)",
				`(function(){`+
					`var d=document.getElementById('drawer');`+
					`var box=d?d.querySelector('.recs[data-var="cards"]'):null;`+
					`if(!box)return '没有 cards 记录表';`+
					`var out=[];`+
					`var cards=box.querySelectorAll('.rec');`+
					`for(var i=0;i<cards.length;i++){`+
					`var t=cards[i].querySelector('[data-rec-field="title"]');`+
					`var x=cards[i].querySelector('[data-rec-field="context"]');`+
					`out.push((t?t.value:'?')+'|'+(x?x.value:'?'));}`+
					`return '卡片='+cards.length+' 明细='+out.join(' , ');})()`)
		})
	})

	// 删掉第一张：应当立刻提交，且编号往前挪。
	time.AfterFunc(26400*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var box=document.querySelector('#drawer .recs[data-var="cards"]');`+
					`if(!box)return;`+
					`var first=box.querySelector('.rec [data-act="recDel"]');`+
					`if(first)first.dispatchEvent(new Event('click',{bubbles:true}));})()`, nil)
			}
		})
	})

	time.AfterFunc(27200*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "删一条(界面)",
				`(function(){`+
					`var d=document.getElementById('drawer');`+
					`var box=d?d.querySelector('.recs[data-var="cards"]'):null;`+
					`if(!box)return '没有 cards 记录表';`+
					`var cards=box.querySelectorAll('.rec');`+
					`var out=[];`+
					`for(var i=0;i<cards.length;i++){`+
					`var t=cards[i].querySelector('[data-rec-field="title"]');`+
					`var no=cards[i].querySelector('.rec__no');`+
					`out.push((no?no.textContent:'?')+':'+(t?t.value:'?'));}`+
					`var x=document.querySelector('#drawer [data-act="drawerClose"]');`+
					`if(x)x.dispatchEvent(new Event('click',{bubbles:true}));`+
					`return '卡片='+cards.length+' 明细='+out.join(' , ');})()`)
		})
	})

	// -----------------------------------------------------------------------
	// 改尺寸 / 内容缩放
	//
	// 这一段的探针**只碰 DOM 和宿主侧**，一律不引用 app.js 里的变量。
	// app.js 是 <script type="module">，`state`/`findInstance`/`call` 这些
	// 顶层绑定是模块作用域，ExecuteScript 里根本看不到——第一版探针直接写了
	// `state.scale`，结果整段 eval 抛 ReferenceError，读数是空的，
	// 看起来像"功能没做"。
	//
	// 真正的鼠标拖拽留给人去拖：合成 MouseEvent 不会进到拖动逻辑。
	// 这里能证明的是"手柄在、边值齐、桥接解得开、窗口跟着变、页面真的重排了"。
	// -----------------------------------------------------------------------

	// 先把侧边栏切回「组」视角：上一步的探针把它切到了「组件」，那里没有
	// 组头可点，点了个空就会读到旧组的数字。
	time.AfterFunc(27400*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var t=document.querySelector('#sideTabs [data-tab="groups"]');`+
					`if(t)t.dispatchEvent(new Event('click',{bubbles:true}));})()`, nil)
			}
		})
	})

	// 边框手柄：**逐个组**点开量一遍。
	//
	// 只量一个组会得出片面的结论：三个内置组里桌面层和浮层都是穿透窗口，
	// 它们的 `resizable` 永远是 false（收不到鼠标，拖自己的边框本来就没意义），
	// 而布局板是它们唯一的尺寸入口——所以手柄必须照样出现。
	// 这条探针就是钉住这一点。
	//
	// 点击前先看标题：`focusGroup` 是"再点同一个组就取消"，板子已经停在
	// 那个组时再点一下会切回「全部组」，读出来就是"块3 柄0"——
	// 第一版探针就是这么把自己骗过去的。
	time.AfterFunc(28000*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "布局板手柄(各组)",
				`(function(){`+
					`function title(){return String((document.getElementById('boardTitle')||{}).textContent||'');}`+
					`var out=[];`+
					`var total=document.querySelectorAll('#groupList .group__head').length;`+
					`if(!total)return '没有组头';`+
					`for(var i=0;i<total;i++){`+
					`var hs=document.querySelectorAll('#groupList .group__head');`+
					`if(i>=hs.length)break;`+
					`var n=hs[i].querySelector('.group__name');`+
					`var nm=n?n.textContent.replace(/\s/g,''):'?';`+
					`if(title()!==nm)hs[i].dispatchEvent(new Event('click',{bubbles:true}));`+
					`var bd=document.getElementById('board');`+
					`var hint=String((document.getElementById('boardHint')||{}).textContent||'');`+
					`out.push(nm+'(板='+title()+' 块'+bd.querySelectorAll('.box').length+`+
					`' 柄'+bd.querySelectorAll('.grip').length+`+
					`' '+(hint.indexOf('拖边角')>=0?'可拖':'不可拖')+')');}`+
					`return out.join(' | ');})()`)
		})
	})

	// 手柄的边值与光标，在最后一个组（浮层）上量——顺带证明穿透组也给了手柄。
	time.AfterFunc(28200*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "布局板手柄(边值)",
				`(function(){`+
					`var bd=document.getElementById('board');`+
					`var bs=bd?bd.querySelectorAll('.box'):[];`+
					`if(!bs.length)return '没有方块';`+
					`var gs=bs[0].querySelectorAll('.grip'),e=[];`+
					`for(var i=0;i<gs.length;i++)e.push(gs[i].getAttribute('data-edge'));`+
					`return '标题='+((document.getElementById('boardTitle')||{}).textContent||'?')+`+
					`' 边='+e.join('/')+' 光标='+(gs.length?getComputedStyle(gs[0]).cursor:'?');})()`)
		})
	})

	// 后面几条探针都要用 note-1，把布局板切回「置顶」。
	// 标题已经是「置顶」时不能再点——那一下会把它切回「全部组」。
	time.AfterFunc(28300*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var t=String((document.getElementById('boardTitle')||{}).textContent||'');`+
					`if(t==='置顶')return;`+
					`var hs=document.querySelectorAll('#groupList .group__head');`+
					`for(var i=0;i<hs.length;i++){`+
					`var n=hs[i].querySelector('.group__name');`+
					`if(n&&n.textContent.replace(/\s/g,'')==='置顶'){`+
					`hs[i].dispatchEvent(new Event('click',{bubbles:true}));return;}}})()`, nil)
			}
		})
	})

	// 打开 note-1 的实例抽屉：缩放控件在这里，不在组设置里（缩放是实例的属性）。
	time.AfterFunc(28400*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var it=document.querySelector('#groupList .item[data-instance="note-1"]');`+
					`if(it)it.dispatchEvent(new Event('click',{bubbles:true}));})()`, nil)
			}
		})
	})

	time.AfterFunc(29000*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "实例抽屉(缩放)",
				`(function(){`+
					`var d=document.getElementById('drawer');`+
					`if(!d||d.hasAttribute('hidden'))return '抽屉没打开';`+
					`var s=d.querySelector('[data-act="instanceZoom"]');`+
					`if(!s)return '没有缩放滑杆';`+
					`var v=d.querySelector('[data-val="instanceZoom"]');`+
					`var r=d.querySelector('[data-act="instanceZoomReset"]');`+
					`return '滑杆='+s.type+' min='+s.min+' max='+s.max+' step='+s.step+`+
					`' 值='+s.value+' 读数='+(v?v.textContent:'?')+`+
					`' 复位='+(r?'有':'无');})()`)
		})
	})

	// 缩放前的页面读数。**这是"内容缩放真的生效了"的唯一硬证据**：
	// 栅格化缩放会改变排版视口，clientWidth 必须按倍数变小。
	// 只看宿主里的 view.Zoom=1.5 是不够的——那只证明参数存进去了。
	time.AfterFunc(29400*time.Millisecond, func() {
		win.Post(func() {
			probePageMetrics(a, "note-1", "缩放前")
		})
	})

	// 把滑杆拨到 150% 并走一遍 input→change：这是抽屉里真实的两条路径，
	// 区别只在落盘时机（input 只预览、change 才写文件）。
	time.AfterFunc(30000*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var d=document.getElementById('drawer');`+
					`var s=d?d.querySelector('[data-act="instanceZoom"]'):null;`+
					`if(!s)return;`+
					`s.value='150';`+
					`s.dispatchEvent(new Event('input',{bubbles:true}));`+
					`s.dispatchEvent(new Event('change',{bubbles:true}));})()`, nil)
			}
		})
	})

	time.AfterFunc(30800*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "缩放已推(界面)",
				`(function(){`+
					`var d=document.getElementById('drawer');`+
					`var s=d?d.querySelector('[data-act="instanceZoom"]'):null;`+
					`var v=d?d.querySelector('[data-val="instanceZoom"]'):null;`+
					`var row=document.querySelector('#groupList .member__id');`+
					`return '滑杆='+(s?s.value:'?')+' 读数='+(v?v.textContent:'?')+`+
					`' 侧边栏='+(row?row.textContent:'无');})()`)
		})
	})
	time.AfterFunc(31100*time.Millisecond, func() {
		win.Post(func() {
			if r := a.running["note-1"]; r != nil {
				log.Printf("探针 缩放已推(宿主)：view.Zoom=%v", r.view.Zoom)
			} else {
				log.Printf("探针 缩放已推(宿主)：note-1 没有在跑")
			}
		})
	})

	time.AfterFunc(31400*time.Millisecond, func() {
		win.Post(func() {
			probePageMetrics(a, "note-1", "缩放后")
		})
	})

	// 尺寸桥接：按"拖完松手"那条路走一遍。
	//
	// 从页面里调不通（call() 是模块作用域），所以这里在宿主侧直接把请求
	// 喂给 onManagerMessage——除了传输层，参数解析、校验、commit 分支
	// 全都走真实代码。位置和尺寸**都要改**：只改位置的话窗口矩形看起来
	// 没变，就看不出尺寸这条有没有走通。
	time.AfterFunc(32000*time.Millisecond, func() {
		win.Post(func() {
			probeResizeBridge(a)
		})
	})

	time.AfterFunc(33000*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "改尺寸已推(界面)",
				`(function(){`+
					`var b=document.querySelector('.box[data-instance="note-1"]');`+
					`var d=document.getElementById('drawer');`+
					`var hint='';`+
					`if(d){var hs=d.querySelectorAll('.field__hint');`+
					`for(var i=0;i<hs.length;i++){`+
					`if(hs[i].textContent.indexOf('逻辑像素')>=0){hint=hs[i].textContent;break;}}}`+
					`return '方块='+(b?(b.dataset.w+'x'+b.dataset.h):'无')+`+
					`' 方块样式='+(b?(b.style.width+'/'+b.style.height):'无')+`+
					`' 抽屉状态='+hint;})()`)
		})
	})
	time.AfterFunc(33300*time.Millisecond, func() {
		win.Post(func() {
			if r := a.running["note-1"]; r != nil {
				x, y, w, h := r.win.Bounds()
				log.Printf("探针 改尺寸已推(宿主)：窗口 outer=(%d,%d) %dx%d scale=%.3f",
					x, y, w, h, r.win.Scale())
			} else {
				log.Printf("探针 改尺寸已推(宿主)：note-1 没有在跑")
			}
		})
	})

	// 改尺寸之后再看一眼页面：窗口从 325×250 变成 400×300 物理像素，
	// 缩放要是还在，clientWidth 就该是 400/1.875≈213；要是被引擎在重排时
	// 重置回 DPI，就会变成 400/1.25=320。
	//
	// 这一问必须有答案：重置了就得在每次改尺寸之后补一次缩放，
	// 不补的话用户拖完边框会发现"字突然变小了"。
	time.AfterFunc(33600*time.Millisecond, func() {
		win.Post(func() {
			probePageMetrics(a, "note-1", "改尺寸后")
		})
	})

	// 唯一该"没有手柄"的情况：组的尺寸档位选了「固定」。
	// 拿浮层组试，改完立刻改回「可调」——不把用户的组设置留在测试状态。
	time.AfterFunc(34200*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var g=document.querySelector('#groupList .group[data-group="builtin.group.overlay"] .group__gear');`+
					`if(g)g.dispatchEvent(new Event('click',{bubbles:true}));})()`, nil)
			}
		})
	})
	time.AfterFunc(34600*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var s=document.querySelector('#drawer [data-act="groupSizeMode"]');`+
					`if(!s)return;`+
					`var bs=s.querySelectorAll('.seg__btn');`+
					`for(var i=0;i<bs.length;i++){`+
					`if(bs[i].dataset.value==='fixed'){`+
					`bs[i].dispatchEvent(new Event('click',{bubbles:true}));return;}}})()`, nil)
			}
		})
	})
	time.AfterFunc(35200*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "尺寸档位=固定",
				`(function(){`+
					`var bd=document.getElementById('board');`+
					`var hint=String((document.getElementById('boardHint')||{}).textContent||'');`+
					`return '板='+((document.getElementById('boardTitle')||{}).textContent||'?')+`+
					`' 块'+bd.querySelectorAll('.box').length+`+
					`' 柄'+bd.querySelectorAll('.grip').length+`+
					`' 提示='+(hint.indexOf('固定')>=0?'说固定':'没提固定');})()`)
		})
	})
	time.AfterFunc(35700*time.Millisecond, func() {
		win.Post(func() {
			if c := win.Controller(); c != nil {
				c.Eval(`(function(){`+
					`var s=document.querySelector('#drawer [data-act="groupSizeMode"]');`+
					`if(s){var bs=s.querySelectorAll('.seg__btn');`+
					`for(var i=0;i<bs.length;i++){`+
					`if(bs[i].dataset.value==='user'){`+
					`bs[i].dispatchEvent(new Event('click',{bubbles:true}));break;}}}`+
					`var x=document.querySelector('#drawer [data-act="drawerClose"]');`+
					`if(x)x.dispatchEvent(new Event('click',{bubbles:true}));})()`, nil)
			}
		})
	})
	time.AfterFunc(36400*time.Millisecond, func() {
		win.Post(func() {
			evalProbe(win, "尺寸档位已还原",
				`(function(){`+
					`var bd=document.getElementById('board');`+
					`return '块'+bd.querySelectorAll('.box').length+' 柄'+bd.querySelectorAll('.grip').length;})()`)
		})
	})
}

// probePageMetrics 读某个实例页面的排版视口与像素比。
//
// 内容缩放改的是"一个 CSS 像素占几个物理像素"，所以生效与否直接体现在
// clientWidth 上：150% 缩放后它必须变成原来的 2/3 左右。
func probePageMetrics(a *App, id, tag string) {
	r := a.running[id]
	if r == nil {
		log.Printf("探针 %s 页面尺寸(%s)：没有在跑", id, tag)
		return
	}
	c := r.win.Controller()
	if c == nil {
		log.Printf("探针 %s 页面尺寸(%s)：窗口还没有页面", id, tag)
		return
	}
	c.Eval(`(function(){`+
		`var e=document.documentElement;`+
		`return 'client='+e.clientWidth+'x'+e.clientHeight+`+
		`' inner='+window.innerWidth+'x'+window.innerHeight+`+
		`' dpr='+window.devicePixelRatio;})()`,
		func(ok bool, result string) {
			if !ok {
				log.Printf("探针 %s 页面尺寸(%s)：读取失败", id, tag)
				return
			}
			log.Printf("探针 %s 页面尺寸(%s)：%s", id, tag, decodeEvalResult(result))
		})
}

// probeResizeBridge 把一条 setInstanceBounds 请求直接喂给桥接处理函数。
//
// 为什么不从页面里调：app.js 是模块脚本，`call()` 取不到。
// 这里绕开的只有传输层——参数解析、可缩放校验、commit 分支都是真实代码。
func probeResizeBridge(a *App) {
	v, ok := a.svc.Instance("note-1")
	if !ok {
		log.Printf("探针 改尺寸(桥接)：没有 note-1")
		return
	}
	x, y := v.Bounds.X+40, v.Bounds.Y+20
	w, h := v.Bounds.Width+60, v.Bounds.Height+40
	body, err := json.Marshal(map[string]any{
		"instanceId": "note-1",
		"x":          x,
		"y":          y,
		"w":          w,
		"h":          h,
		"commit":     true,
	})
	if err != nil {
		log.Printf("探针 改尺寸(桥接)：拼参数失败 %v", err)
		return
	}
	log.Printf("探针 改尺寸(桥接)：请求 x=%d y=%d w=%d h=%d（逻辑像素）", x, y, w, h)
	a.onManagerMessage(request{Method: "manager.setInstanceBounds", Params: body})
}
