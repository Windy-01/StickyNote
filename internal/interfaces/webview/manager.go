package webview

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"time"

	"stickynote/internal/domain/component"
)

// managerInstanceID 是管理界面在桥接协议里的实例 id。
//
// 用一个不可能与真实实例冲突的名字：真实实例 id 由 "<组件id>-<序号>"
// 生成，而 "kernel." 前缀的组件 id 被加载器禁止，所以这里不会撞车。
const managerInstanceID = "kernel.manager"

// 管理窗口的尺寸（逻辑像素）。
const (
	managerDefaultWidth  = 960
	managerDefaultHeight = 660
	managerMinWidth      = 720
	managerMinHeight     = 480
)

// managerStyle 是管理界面的窗口外观。
//
// 它不走 L1 样式：管理界面是内核的 UI，不该依赖"样式包是否装好"。
// 但它仍然用同一套 --sn-* 变量契约，这样前端只需要认识一种取色方式。
var managerStyle = stylePayload{
	Vars: map[string]string{
		"--sn-padding":    "0px",
		"--sn-radius":     "10px",
		"--sn-background": "#f7f5f1",
		"--sn-shadow":     "0 18px 48px rgba(0, 0, 0, 0.22)",
		"--sn-ink":        "#2b2823",
		"--sn-ink-soft":   "#7d766a",
		"--sn-line":       "rgba(0, 0, 0, 0.08)",
		"--sn-hover":      "rgba(0, 0, 0, 0.045)",
		"--sn-accent":     "#c08a1e",
		"--sn-danger":     "#c4302b",
		"--sn-surface":    "#ffffff",
		"--sn-surface-2":  "#efece5",
	},
	Titlebar:  true,
	Resizable: true,
	Layer:     string(component.LayerNormal),
}

// managerWindow 是内核自己的管理界面。
//
// 它不是组件：组件由它管理，如果它本身是一个组件，那么"停用管理界面"
// 之后就再也没有入口把它点回来了。这条边界和通知浮层一样——
// 内核自己的能力不能交给被自己管理的东西去实现。
type managerWindow struct {
	win  *Window
	host *Host
}

// openManager 打开（或从后面拽出）管理界面。
func (a *App) openManager() {
	if a.manager != nil {
		a.manager.win.ShowRestore()
		return
	}
	if a.opts.ManagerDir == "" {
		log.Printf("管理界面资源目录未配置，无法打开")
		return
	}

	ctx := context.Background()
	rect, err := a.svc.UIState(ctx)
	if err != nil {
		log.Printf("读取管理窗口几何失败: %v", err)
	}

	scale := systemScale()
	width := int(math.Round(float64(rect.Width) * scale))
	height := int(math.Round(float64(rect.Height) * scale))
	if width <= 0 || height <= 0 {
		width = int(math.Round(managerDefaultWidth * scale))
		height = int(math.Round(managerDefaultHeight * scale))
	}

	win, err := createWindow(WindowOptions{
		Title:     "StickyNote",
		Width:     width,
		Height:    height,
		MinWidth:  managerMinWidth,
		MinHeight: managerMinHeight,
		Resizable: true,
		// 管理窗口要在任务栏占位：它是用户真正会切走再切回来的窗口。
		ShowInTaskbar: true,
		// 主界面是浮在桌面上的普通窗口，带投影才站得住（managerStyle 里
		// 也声明了 --sn-shadow，两者说的是同一件事）。
		Shadow: true,
	})
	if err != nil {
		log.Printf("创建管理窗口失败: %v", err)
		return
	}

	m := &managerWindow{win: win}
	a.manager = m

	win.OnResize = func() {
		if m.host != nil {
			m.host.Resize()
		}
	}
	win.OnSettle = a.persistManager
	win.OnClose = func() bool {
		a.persistManager()
		return true
	}
	win.OnDestroy = func() {
		a.manager = nil
		a.maybeQuit()
	}

	// applyGeometry 在 Navigate 之后再调一次：WebView2 首次导航会把视图的
	// 尺寸按自己的默认值重算一遍（RasterizationScale 也会被重置，那个在
	// webview2 包里靠先关掉 ShouldDetectMonitorScaleChanges 解决），
	// 所以落位与尺寸要补一遍才稳。
	applyGeometry := func() {
		if rect.Width > 0 {
			win.SetPosition(rect.X, rect.Y)
		} else {
			win.Center()
		}
		win.SetSize(width, height)
	}
	applyGeometry()
	win.Show()

	host := NewHost(win, a.opts.ManagerDir, "index.html", managerStyle)
	m.host = host
	if err := host.Start(func(raw string) { a.onMessage(managerInstanceID, raw) },
		a.opts.DevMode); err != nil {
		log.Printf("管理界面启动失败: %v", err)
		win.Destroy()
		return
	}
	win.Settle()
	host.Navigate()
	applyGeometry()

	// 文档就绪后取布局读数（只在诊断模式下）。
	// 引擎子窗口的扩展样式不要动：WebView2 自己维护那个窗口，
	// 外部改 exStyle 会让引擎重建它，反而把输入路径弄坏。
	host.OnReady = func() {
		a.deferToUI(func() {
			if diagEnabled() {
				logLayout("manager", win, ".shell", ".titlebar", ".body", ".sidebar",
					".main", ".board-wrap", "#board", ".statusbar", ".group", ".item")
			}
		})
	}

	if diagEnabled() {
		probeManagerUI(a, win)
		probeInstanceStyles(a)
	}

	if a.opts.DevMode {
		x, y, w, h := win.Bounds()
		ex := win.ExStyle()
		log.Printf("管理窗口：outer=(%d,%d) %dx%d scale=%.3f taskbar=%v dir=%s",
			x, y, w, h, win.Scale(), ex&wsExToolWindow == 0, a.opts.ManagerDir)

		// 光标通道探针：引擎通过 CursorChanged 报过几次、当前句柄是多少。
		// 次数恒为 0 说明这条通道根本没通，页面里的 CSS cursor 不会生效。
		for _, at := range []time.Duration{6 * time.Second, 16 * time.Second} {
			time.AfterFunc(at, func() {
				n, cur := win.CursorStats()
				log.Printf("光标通道：CursorChanged %d 次，当前 HCURSOR=%#x", n, cur)
			})
		}
	}
}

// closeManager 关闭管理界面（走正常的关闭流程，几何会被记下来）。
func (a *App) closeManager() {
	if a.manager != nil {
		a.manager.win.Close()
	}
}

// persistManager 记录管理窗口几何。
//
// 和组件实例一样：X/Y 存物理像素，宽高存逻辑像素。
func (a *App) persistManager() {
	m := a.manager
	if m == nil {
		return
	}
	x, y, w, h := m.win.Bounds()
	scale := m.win.Scale()
	if scale <= 0 {
		scale = 1
	}
	rect := component.Rect{
		X:      x,
		Y:      y,
		Width:  int(math.Round(float64(w) / scale)),
		Height: int(math.Round(float64(h) / scale)),
	}
	if err := a.svc.SetUIState(context.Background(), rect); err != nil {
		log.Printf("记录管理窗口几何失败: %v", err)
	}
}

// managerHost 取当前管理界面的宿主（可能为 nil）。
func (a *App) managerHost() *Host {
	if a.manager == nil {
		return nil
	}
	return a.manager.host
}

// pushManager 主动推一条事件给管理界面。
func (a *App) pushManager(name string, data any) {
	if h := a.managerHost(); h != nil {
		h.Post(event{From: "host", Event: name, Data: data})
	}
}

// pushVars 把实例的最新变量值投给正在运行的窗口。
//
// 变量会改变组件显示的内容（字号、标题、模板…），所以改完必须让页面
// 重新读一次。窗口没在跑就什么也不用做：下次 spawn 时 `component.ready`
// 会把值一并带过去。
func (a *App) pushVars(instanceID string) {
	r := a.running[instanceID]
	if r == nil {
		return
	}
	v, ok := a.svc.Instance(instanceID)
	if !ok {
		return
	}
	// running.view 是建窗那一刻的快照，变量改过就旧了。换掉它，
	// 免得后面读它的人（比如再次 component.ready）拿到旧值。
	r.view.Vars = v.Vars
	// 这一行是"改完变量组件到底收没收到"的唯一凭据：页面那边改了什么
	// （字号、标题）肉眼才看得出来，而读数只能到这里。
	if diagEnabled() {
		log.Printf("推送变量 %s: %v", instanceID, v.Vars)
	}
	r.host.Post(event{From: "host", Event: "vars", Data: v.Vars})
}

// toast 是给管理界面的一条提示。OK=false 时界面按错误样式显示。
type toast struct {
	OK   bool   `json:"ok"`
	Text string `json:"text"`
}

// accepted 是"请求已收下、稍后执行"的回复体。
//
// 凡是会动窗口的请求都先回这一句，真正的动作排到 UI 队列上执行，
// 结果用 manager:state 事件推回去。
var accepted = map[string]bool{"accepted": true}

// deferToUI 把一段会动窗口的操作排到 UI 消息循环里执行。
//
// 走内核窗口的 Post 队列：它从启动活到退出，不像管理界面那样会被用户关掉，
// 所以不需要"管理界面不在就直接执行"这种特例。
//
// **不能在 WebView2 的事件回调里建窗口**：`Host.Start` 内部的 `Embed`
// 会跑一段嵌套 GetMessage 循环等控制器就绪，而这个回调本身就是在
// 消息循环派发进来的。在回调里再进一层嵌套循环，运行时的内部锁
// 再也拿不到，整个进程静默卡死——没有崩溃、没有日志，任务管理器里
// 还活着。销毁窗口同理：`DestroyWindow` 会同步走到承载控制器的子窗口。
//
// 这条和拖动缩放（`BeginDrag` 内部 Post）、选文件夹（`beginInstall`）
// 是同一条规矩：模态或重建类操作一律先出回调。
func (a *App) deferToUI(fn func()) {
	a.kernel.Post(fn)
}

// ---------------------------------------------------------------------------
// 状态快照
// ---------------------------------------------------------------------------

type managerInstance struct {
	ID          string `json:"id"`
	ComponentID string `json:"componentId"`
	Name        string `json:"name"`
	GroupID     string `json:"groupId"`
	GroupName   string `json:"groupName"`
	Layer       string `json:"layer"`
	// X/Y/W/H 是屏幕上的**物理像素**矩形，布局视图按它画方块。
	// 和 Width/Height 分开是因为后者是逻辑像素（界面显示尺寸用），
	// 两者差一个 DPI 缩放，混用会让方块和窗口对不上。
	X      int `json:"x"`
	Y      int `json:"y"`
	W      int `json:"w"`
	H      int `json:"h"`
	Width  int `json:"width"`
	Height int `json:"height"`
	// MinW/MinH 是组件声明的最小尺寸，同样是**物理像素**（已乘过 DPI）。
	// 布局板拖边框时按它夹住，否则方块会缩到比窗口实际能到的最小值还小，
	// 松手后又弹回去。
	MinW int `json:"minW"`
	MinH int `json:"minH"`
	// Zoom 是内容缩放倍数（1 表示原始大小），只影响页面渲染，不影响 W/H。
	Zoom    float64 `json:"zoom"`
	Visible bool    `json:"visible"`
	Running bool    `json:"running"`
	// Vars 是这个实例的变量**最终值**：作者声明的每个变量都有一项，
	// 没填过的是声明里的默认值。声明在 managerComponent.Variables 里。
	// 文本类是 JSON 字符串，`json` 类型可能是数组或对象。
	Vars map[string]json.RawMessage `json:"vars"`
}

// managerScreen 是布局视图要画的"桌面"范围（物理像素，已去掉任务栏）。
type managerScreen struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

type managerComponent struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Author      string `json:"author"`
	Description string `json:"description"`
	Builtin     bool   `json:"builtin"`
	Enabled     bool   `json:"enabled"`
	// Singleton 为真时最多一个实例。界面据此禁掉"再建一个"，
	// 而不是让用户点了才收到一句"这是单例"。
	Singleton bool `json:"singleton"`
	// Variables 是作者声明的变量。**值不在这里**：同一个组件的两个实例
	// 共用一份声明，各持一份值，所以声明挂在组件上、值挂在实例上。
	Variables []managerVar `json:"variables"`
	// Instances 是这个组件名下的窗口。组件是静态定义，本身没有组——
	// 这些实例可以分别待在不同的组里。
	Instances []managerInstance `json:"instances"`
}

// managerVar 是一个变量声明在界面上的形状。
//
// 给的都是**补齐默认值之后**的具体值（label 不会是空串、rows 不会是 0）：
// 界面按它直接画控件，不需要再认识"留空表示用 key"这类规则。
//
// Default 是原始 JSON：文本类是一个字符串，`number`/`bool` 是标量，
// `list` / `group` 是对象数组。界面按 type 决定怎么显示——`list` 取出每项
// 的 Item 字段拼成多行文本，`group` 每条画一张卡片（见 Fields）。
type managerVar struct {
	Key         string             `json:"key"`
	Label       string             `json:"label"`
	Description string             `json:"description"`
	Type        string             `json:"type"`
	Default     json.RawMessage    `json:"default"`
	Placeholder string             `json:"placeholder"`
	Item        string             `json:"item,omitempty"`
	Fields      []managerField     `json:"fields,omitempty"`
	Options     []managerVarOption `json:"options,omitempty"`
	Required    bool               `json:"required"`
	Rows        int                `json:"rows"`
}

// managerField 是 group 变量里一条记录的一个字段。
//
// 和 managerVar 同构但不带 item/fields——字段不能再嵌套，
// 这一层限制在加载清单时就已经拒掉了（见 component.VarField.Validate）。
type managerField struct {
	Key         string             `json:"key"`
	Label       string             `json:"label"`
	Type        string             `json:"type"`
	Default     json.RawMessage    `json:"default"`
	Placeholder string             `json:"placeholder"`
	Options     []managerVarOption `json:"options,omitempty"`
	Rows        int                `json:"rows"`
}

type managerVarOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// managerVars 把领域里的变量声明摊成界面要的形状。
func managerVars(list []component.Variable) []managerVar {
	out := make([]managerVar, 0, len(list))
	for _, v := range list {
		v = v.Normalize()
		opts := make([]managerVarOption, 0, len(v.Options))
		for _, o := range v.Options {
			opts = append(opts, managerVarOption{Value: o.Value, Label: o.Display()})
		}
		out = append(out, managerVar{
			Key:         v.Key,
			Label:       v.Title(),
			Description: v.Description,
			Type:        string(v.Type),
			Default:     v.Default,
			Placeholder: v.Placeholder,
			Item:        v.Item,
			Fields:      managerFields(v.Fields),
			Options:     opts,
			Required:    v.Required,
			Rows:        v.Rows,
		})
	}
	return out
}

// managerFields 把 group 的字段声明摊成界面要的形状。
func managerFields(list []component.VarField) []managerField {
	if len(list) == 0 {
		return nil
	}
	out := make([]managerField, 0, len(list))
	for _, f := range list {
		f = f.Normalize()
		opts := make([]managerVarOption, 0, len(f.Options))
		for _, o := range f.Options {
			opts = append(opts, managerVarOption{Value: o.Value, Label: o.Display()})
		}
		out = append(out, managerField{
			Key:         f.Key,
			Label:       f.Title(),
			Type:        string(f.Type),
			Default:     f.Default,
			Placeholder: f.Placeholder,
			Options:     opts,
			Rows:        f.Rows,
		})
	}
	return out
}

// managerGroup 是一个组在界面上的完整形状：窗口开关 + 外观。
//
// 外观字段给的是**补齐兜底值之后的具体值**，界面直接显示与编辑，
// 不需要再认识"指针为 nil 表示没设"这一层。
//
// 不列成员：成员是实例，界面按 `instances[].groupId` 过滤即可。
type managerGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Color 是布局板与侧边栏用来区分这个组的识别色（`#rrggbb`）。
	Color        string            `json:"color"`
	Layer        string            `json:"layer"`
	ClickThrough bool              `json:"clickThrough"`
	Activation   string            `json:"activation"`
	Resizable    bool              `json:"resizable"`
	SizeMode     string            `json:"sizeMode"`
	Titlebar     bool              `json:"titlebar"`
	Padding      int               `json:"padding"`
	Radius       int               `json:"radius"`
	Background   string            `json:"background"`
	Shadow       string            `json:"shadow"`
	CSSVars      map[string]string `json:"cssVars"`
}

type managerState struct {
	Components []managerComponent `json:"components"`
	Groups     []managerGroup     `json:"groups"`
	// Instances 是全部实例的扁平清单，侧边栏与布局板都按它画。
	// 组件里那份嵌套的是给组件列表页用的，两者是同一批数据的两种切法。
	Instances  []managerInstance `json:"instances"`
	PluginRoot string            `json:"pluginRoot"`
	// Screen 是桌面工作区，Scale 是当前 DPI 缩放。
	// 布局视图只做等比映射，所以坐标一律用物理像素，缩放只用于显示尺寸。
	Screen managerScreen `json:"screen"`
	Scale  float64       `json:"scale"`
}

// managerState 把应用层的快照拼成界面要的形状。
//
// 唯一需要表现层补充的信息是 "Running"：窗口存不存在只有这里有答案，
// 应用层只管数据，不管窗口。
func (a *App) managerState() managerState {
	scale := systemScale()
	if scale <= 0 {
		scale = 1
	}
	byComponent := map[string][]managerInstance{}
	flat := make([]managerInstance, 0, len(a.svc.AllInstances()))
	for _, v := range a.svc.AllInstances() {
		_, running := a.running[v.ID]
		mi := managerInstance{
			ID:          v.ID,
			ComponentID: v.ComponentID,
			Name:        v.ComponentName,
			GroupID:     v.GroupID,
			GroupName:   v.GroupName,
			Layer:       string(v.Style.Layer),
			X:           v.Bounds.X,
			Y:           v.Bounds.Y,
			W:           int(math.Round(float64(v.Bounds.Width) * scale)),
			H:           int(math.Round(float64(v.Bounds.Height) * scale)),
			Width:       v.Bounds.Width,
			Height:      v.Bounds.Height,
			MinW:        int(math.Round(float64(v.MinBounds.Width) * scale)),
			MinH:        int(math.Round(float64(v.MinBounds.Height) * scale)),
			Zoom:        v.Zoom,
			Visible:     v.Visible,
			Running:     running,
			Vars:        v.Vars,
		}
		byComponent[v.ComponentID] = append(byComponent[v.ComponentID], mi)
		flat = append(flat, mi)
	}
	wa := workArea()

	comps := a.svc.Components()
	out := managerState{
		Components: make([]managerComponent, 0, len(comps)),
		Instances:  flat,
		PluginRoot: a.opts.PluginRoot,
		Scale:      scale,
		Screen: managerScreen{
			X: int(wa.Left), Y: int(wa.Top),
			W: int(wa.Right - wa.Left), H: int(wa.Bottom - wa.Top),
		},
	}
	for _, cv := range comps {
		instances := byComponent[cv.ID]
		if instances == nil {
			instances = []managerInstance{}
		}
		out.Components = append(out.Components, managerComponent{
			ID:          cv.ID,
			Name:        cv.Name,
			Author:      cv.Author,
			Description: cv.Description,
			Builtin:     cv.Builtin,
			Enabled:     cv.Enabled,
			Singleton:   cv.Singleton,
			Variables:   managerVars(cv.Variables),
			Instances:   instances,
		})
	}

	for _, gv := range a.svc.Groups() {
		v := gv.Visual.Values()
		out.Groups = append(out.Groups, managerGroup{
			ID:           gv.ID,
			Name:         gv.Name,
			Color:        gv.Color,
			Layer:        string(gv.Window.Layer),
			ClickThrough: gv.Window.ClickThrough,
			Activation:   string(gv.Window.Activation),
			Resizable:    gv.Window.Resizable,
			SizeMode:     string(gv.Window.SizeMode),
			Titlebar:     gv.Window.Titlebar,
			Padding:      v.Padding,
			Radius:       v.Radius,
			Background:   v.Background,
			Shadow:       v.Shadow,
			CSSVars:      gv.CSSVars,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// 桥接
// ---------------------------------------------------------------------------

// onManagerMessage 处理管理界面的请求。
//
// 方法名统一带 manager. 前缀：一眼能看出这条消息只有管理界面能发，
// 组件那边就算伪造同名消息也进不到这里（入口按实例 id 分流）。
func (a *App) onManagerMessage(req request) {
	m := a.manager
	if m == nil || m.host == nil {
		return
	}
	h := m.host
	ctx := context.Background()

	switch req.Method {
	case "manager.state":
		replyTo(h, req.ID, a.managerState(), nil)

	case "manager.setEnabled":
		var p struct {
			ComponentID string `json:"componentId"`
			Enabled     bool   `json:"enabled"`
		}
		if !params(h, req, &p) {
			return
		}
		// 先回包，再排到 UI 队列：启用要建窗口和 WebView2 控制器，
		// 而控制器不能在本回调里创建（见 deferToUI）。停用同理——
		// 在回调里销毁承载控制器的窗口会和运行时的内部锁撞上。
		replyTo(h, req.ID, accepted, nil)
		a.deferToUI(func() {
			if err := a.svc.SetEnabled(ctx, p.ComponentID, p.Enabled); err != nil {
				a.fail(err)
				a.pushState()
				return
			}
			if p.Enabled {
				a.spawnMissing(p.ComponentID)
			} else {
				// 停用只关窗口，不删实例：用户的几何与数据都留着，
				// 重新启用时原样回来。
				a.stopInstances(p.ComponentID)
			}
			a.pushState()
		})

	case "manager.createGroup":
		var p struct {
			Name string `json:"name"`
		}
		if !params(h, req, &p) {
			return
		}
		g, err := a.svc.CreateGroup(ctx, p.Name)
		if err != nil {
			replyTo(h, req.ID, nil, err)
			return
		}
		replyTo(h, req.ID, g, nil)
		a.pushState()

	case "manager.updateGroup":
		var p groupParams
		if !params(h, req, &p) {
			return
		}
		before, ok := a.svc.Group(p.ID)
		if !ok {
			replyTo(h, req.ID, nil, fmt.Errorf("组 %s 不存在", p.ID))
			return
		}
		next := p.toGroup()
		replyTo(h, req.ID, accepted, nil)
		a.deferToUI(func() {
			if err := a.svc.UpdateGroup(ctx, next); err != nil {
				a.fail(err)
				a.pushState()
				return
			}
			// 窗口开关变了就必须拆了重建：层级、穿透、可缩放、标题栏
			// 都是 CreateWindow 时的选项，没有"就地改"这回事。
			// 只改外观（留白、圆角、底色、阴影）时不用动窗口，推一次样式即可。
			if before.Window != next.Window.Normalize() {
				a.rebuildGroup(p.ID)
			} else {
				a.restyleGroup(p.ID)
			}
			a.pushState()
		})

	case "manager.deleteGroup":
		var p struct {
			GroupID string `json:"groupId"`
		}
		if !params(h, req, &p) {
			return
		}
		if err := a.svc.DeleteGroup(ctx, p.GroupID); err != nil {
			replyTo(h, req.ID, nil, err)
			return
		}
		replyTo(h, req.ID, a.managerState(), nil)
		a.pushState()

	case "manager.setInstanceGroup":
		var p struct {
			InstanceID string `json:"instanceId"`
			GroupID    string `json:"groupId"`
		}
		if !params(h, req, &p) {
			return
		}
		replyTo(h, req.ID, accepted, nil)
		a.deferToUI(func() {
			if err := a.svc.SetInstanceGroup(ctx, p.InstanceID, p.GroupID); err != nil {
				a.fail(err)
				a.pushState()
				return
			}
			// 换了组就等于换了窗口形态（层级、穿透都可能变），
			// 只能拆了重建。只重建这一个实例：同组件的另一个实例
			// 可能还待在自己那个组里，不该被牵连。
			a.rebuildInstance(p.InstanceID)
			a.pushState()
		})

	case "manager.createInstance":
		var p struct {
			ComponentID string `json:"componentId"`
			// GroupID 为空表示"放兜底组"。
			GroupID string `json:"groupId"`
		}
		if !params(h, req, &p) {
			return
		}
		replyTo(h, req.ID, accepted, nil)
		a.deferToUI(func() {
			v, err := a.svc.CreateInstance(ctx, p.ComponentID, p.GroupID)
			if err != nil {
				a.fail(err)
				return
			}
			if err := a.spawn(v); err != nil {
				// 窗口建不起来就别留一条指向不存在窗口的实例记录。
				if rmErr := a.svc.RemoveInstance(ctx, v.ID); rmErr != nil {
					log.Printf("清理实例 %s 失败: %v", v.ID, rmErr)
				}
				a.fail(err)
				return
			}
			if err := a.svc.SaveLayout(ctx); err != nil {
				log.Printf("保存实例布局失败: %v", err)
			}
			a.pushState()
		})

	case "manager.removeInstance":
		var p struct {
			InstanceID string `json:"instanceId"`
		}
		if !params(h, req, &p) {
			return
		}
		replyTo(h, req.ID, accepted, nil)
		a.deferToUI(func() {
			// 直接销毁而不是 Close：实例马上要被删掉，
			// 走 OnClose 会先给一条注定消失的记录写一遍几何。
			if r := a.running[p.InstanceID]; r != nil {
				r.win.Destroy()
			}
			if err := a.svc.RemoveInstance(ctx, p.InstanceID); err != nil {
				a.fail(err)
			}
			// 必须自己落盘：Destroy 走的是直接销毁，不经过 OnClose，
			// 没有别的地方会替这次删除写 layout.json——不写的话
			// 下次启动那个实例又回来了。
			if err := a.svc.SaveLayout(ctx); err != nil {
				log.Printf("保存实例布局失败: %v", err)
			}
			a.pushState()
		})

	case "manager.setVisible":
		var p struct {
			InstanceID string `json:"instanceId"`
			Visible    bool   `json:"visible"`
		}
		if !params(h, req, &p) {
			return
		}
		a.setVisible(p.InstanceID, p.Visible)
		replyTo(h, req.ID, a.managerState(), nil)

	case "manager.setVars":
		var p struct {
			InstanceID string `json:"instanceId"`
			// Values 只包含**这次改动的**变量；没提到的保持原样。
			// 表单可以整份提交，也可以只提交动过的那一个。
			Values map[string]string `json:"values"`
		}
		if !params(h, req, &p) {
			return
		}
		// 不用 deferToUI：这里不动窗口，只是写文件 + 给组件投一条消息。
		// 投递走 Host.Post，它自己会排到 UI 线程上。
		if err := a.svc.SetVars(ctx, p.InstanceID, p.Values); err != nil {
			a.fail(err)
			replyTo(h, req.ID, a.managerState(), nil)
			return
		}
		a.pushVars(p.InstanceID)
		replyTo(h, req.ID, a.managerState(), nil)

	case "manager.moveInstance":
		var p struct {
			InstanceID string `json:"instanceId"`
			X          int    `json:"x"`
			Y          int    `json:"y"`
			// Commit 表示"拖完了，落盘"。拖动过程中只挪窗口不写文件：
			// 一次拖拽会来几十上百条消息，每条都写一遍 layout.json 没有意义。
			Commit bool `json:"commit"`
		}
		if !params(h, req, &p) {
			return
		}
		// 和建窗/销毁一样，移动窗口也要出回调：SetPosition 会同步给窗口
		// 发 WM_WINDOWPOSCHANGING，而我们正站在引擎的消息回调里。
		//
		// 拖动过程中的每一帧例外：那一帧改用 SetPositionQuiet（压掉
		// WM_WINDOWPOSCHANGING）**当场挪**，不排队列。排队列会饿死——
		// WM_MOUSEMOVE 是输入消息，优先级高于 PostMessage 的 WM_APP，
		// 拖得越连续队列越排不上，最后只在松手时挪一次，
		// 视觉上就是"从起点直接跳到终点"。
		if !p.Commit {
			replyTo(h, req.ID, accepted, nil)
			if err := a.moveInstanceQuiet(p.InstanceID, p.X, p.Y); err != nil {
				log.Printf("拖动实例 %s 失败: %v", p.InstanceID, err)
			}
			return
		}

		replyTo(h, req.ID, accepted, nil)
		a.deferToUI(func() {
			if err := a.moveInstance(ctx, p.InstanceID, p.X, p.Y, true); err != nil {
				a.fail(err)
				return
			}
			// 和前端报的帧数对一下：两边都是几十说明链路通，窗口没动就是
			// 渲染/合成的问题；宿主这边是 0/1 就是桥接没送到。
			// WM_MOUSEMOVE 条数是上游：它只有几条，说明引擎就没把移动送来。
			mm := 0
			if m.win != nil {
				mm = m.win.MouseMoveMsgs()
			}
			log.Printf("拖动落定 %s：窗口收 WM_MOUSEMOVE %d 条，宿主挪窗 %d 次 → %d,%d",
				p.InstanceID, mm, a.moveFrames, p.X, p.Y)
			a.moveFrames = 0
			// 松手时推一次状态，让各方块的坐标跟真实窗口对齐。
			a.pushState()
		})

	case "manager.setInstanceBounds":
		var p struct {
			InstanceID string `json:"instanceId"`
			X          int    `json:"x"`
			Y          int    `json:"y"`
			// W/H 是**逻辑像素**，和 layout.json 里存的口径一致。
			// X/Y 是物理像素，和 manager.moveInstance 一致。
			W int `json:"w"`
			H int `json:"h"`
			// Commit 的含义同 moveInstance：拖动中每帧只改窗口，
			// 松手那一下才写 layout.json。
			Commit bool `json:"commit"`
		}
		if !params(h, req, &p) {
			return
		}
		// 尺寸档位是「固定」时内核拒绝改尺寸。这里**只看 sizeMode，不看
		// resizable**：后者说的是"用户能不能直接拖窗口边框"，而穿透窗口和
		// 桌面层窗口收不到鼠标，它永远是 false——拿它挡布局板，这两个组就
		// 再也没有尺寸入口了。布局板是内核自己的管理面，不受那条约束。
		if v, ok := a.svc.Instance(p.InstanceID); ok {
			if v.Style.SizeMode == component.SizeFixed {
				replyTo(h, req.ID, nil,
					fmt.Errorf("「%s」的尺寸档位是「固定」，改不了大小", v.Style.GroupName))
				return
			}
		}
		if !p.Commit {
			replyTo(h, req.ID, accepted, nil)
			now := time.Now()
			if !a.bridgeResizeLast.IsZero() {
				g := now.Sub(a.bridgeResizeLast)
				if a.bridgeGapMin == 0 || g < a.bridgeGapMin {
					a.bridgeGapMin = g
				}
				if g > a.bridgeGapMax {
					a.bridgeGapMax = g
				}
				a.bridgeGapSum += g
				if g > 25*time.Millisecond {
					a.bridgeGapLate++
				}
			}
			a.bridgeResizeLast = now
			t0 := now
			if err := a.resizeInstanceQuiet(p.InstanceID, p.X, p.Y, p.W, p.H); err != nil {
				log.Printf("调整实例 %s 尺寸失败: %v", p.InstanceID, err)
			}
			d := time.Since(t0)
			a.bridgeResizeN++
			a.bridgeResizeCost += d
			if d > a.bridgeResizeWorst {
				a.bridgeResizeWorst = d
			}
			if len(a.bridgeTs) < 400 {
				a.bridgeTs = append(a.bridgeTs, now.UnixMilli())
			}
			return
		}

		replyTo(h, req.ID, accepted, nil)
		a.deferToUI(func() {
			if err := a.resizeInstance(ctx, p.InstanceID, p.X, p.Y, p.W, p.H, true); err != nil {
				a.fail(err)
				return
			}
			// 夹取发生在内核侧（MinBounds），松手后推一次状态，
			// 让方块按真实尺寸回正，不然它会停在拖到的那个偏小的值上。
			a.pushState()
			// 拖动这一段的成本在松手时结算：这是唯一"帧数一定对得上"的时刻。
			if a.bridgeResizeN > 0 {
				n := time.Duration(a.bridgeResizeN)
				log.Printf("拖边框落定 %s：%d 帧，宿主总耗时 %v，平均 %v，最慢 %v",
					p.InstanceID, a.bridgeResizeN, a.bridgeResizeCost.Round(time.Microsecond),
					(a.bridgeResizeCost / n).Round(time.Microsecond),
					a.bridgeResizeWorst.Round(time.Microsecond))
				if a.bridgeResizeN > 1 {
					log.Printf("拖边框投递 %s：间隔 最小 %v 平均 %v 最大 %v，超 25ms 的 %d 次",
						p.InstanceID,
						a.bridgeGapMin.Round(time.Microsecond),
						(a.bridgeGapSum / time.Duration(a.bridgeResizeN-1)).Round(time.Microsecond),
						a.bridgeGapMax.Round(time.Microsecond), a.bridgeGapLate)
				}
				a.bridgeResizeN, a.bridgeResizeCost, a.bridgeResizeWorst = 0, 0, 0
				a.bridgeResizeLast = time.Time{}
				a.bridgeGapMin, a.bridgeGapMax, a.bridgeGapSum, a.bridgeGapLate = 0, 0, 0, 0
				a.reportContentLag()
			}
		})

	case "manager.setInstanceZoom":
		var p struct {
			InstanceID string  `json:"instanceId"`
			Zoom       float64 `json:"zoom"`
			// Commit 同上：拖滑杆时每格只改页面，松手才落盘。
			Commit bool `json:"commit"`
		}
		if !params(h, req, &p) {
			return
		}
		if err := a.setInstanceZoom(ctx, p.InstanceID, p.Zoom, p.Commit); err != nil {
			replyTo(h, req.ID, nil, err)
			return
		}
		replyTo(h, req.ID, a.managerState(), nil)

	case "manager.install":
		// 选文件夹是模态的，不能在消息处理里跑（会重入消息循环）。
		// 先回包，再排到 UI 队列上执行，装完用事件把结果推回去。
		replyTo(h, req.ID, accepted, nil)
		a.beginInstall()

	case "manager.uninstall":
		var p struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		}
		if !params(h, req, &p) {
			return
		}
		if err := a.svc.UninstallPackage(ctx, component.PackageKind(p.Kind), p.ID); err != nil {
			replyTo(h, req.ID, nil, err)
			return
		}
		replyTo(h, req.ID, a.managerState(), nil)

	case "manager.reveal":
		var p struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		}
		if !params(h, req, &p) {
			return
		}
		// 内置资源住在缓存目录里（<cache>/builtin/<hash>/），
		// 那是随 exe 解包的临时产物，不该让用户去翻。
		if a.isBuiltin(component.PackageKind(p.Kind), p.ID) {
			replyTo(h, req.ID, nil, fmt.Errorf("%s 是内置的，没有独立目录可打开", p.ID))
			return
		}
		dir, err := a.svc.PackageDir(component.PackageKind(p.Kind), p.ID)
		if err != nil {
			replyTo(h, req.ID, nil, err)
			return
		}
		openInExplorer(dir)
		replyTo(h, req.ID, nil, nil)

	case "manager.openPluginDir":
		openInExplorer(a.opts.PluginRoot)
		replyTo(h, req.ID, nil, nil)

	case "manager.reload":
		if err := a.svc.ReloadCatalog(ctx); err != nil {
			replyTo(h, req.ID, nil, err)
			return
		}
		replyTo(h, req.ID, a.managerState(), nil)

	case "host.log":
		// 管理界面也要能写日志：它的 JS 异常全靠这条通道才能进 app.log。
		var p struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		}
		if !params(h, req, &p) {
			return
		}
		log.Printf("[%s] %s: %s", managerInstanceID, p.Level, p.Message)
		replyTo(h, req.ID, nil, nil)

	case "manager.close":
		replyTo(h, req.ID, nil, nil)
		a.closeManager()

	case "window.beginDrag":
		replyTo(h, req.ID, nil, nil)
		m.win.BeginDrag()

	case "window.beginResize":
		var p struct {
			Edge string `json:"edge"`
		}
		if !params(h, req, &p) {
			return
		}
		replyTo(h, req.ID, nil, nil)
		m.win.BeginResize(p.Edge)

	case "window.minimize":
		// 管理窗口在任务栏有按钮，最小化之后能找回来，所以给它这个按钮。
		replyTo(h, req.ID, nil, nil)
		m.win.Minimize()

	default:
		replyTo(h, req.ID, nil, fmt.Errorf("未知方法: %s", req.Method))
	}
}

// beginInstall 走一遍"选目录 → 复制 → 重扫 → 通知界面"。
func (a *App) beginInstall() {
	m := a.manager
	if m == nil {
		return
	}
	m.win.Post(func() {
		dir := pickFolder(m.win.Handle(), "选择组件文件夹（里面应有 manifest.json）")
		if dir == "" {
			a.pushManager("manager:toast", toast{OK: true, Text: "已取消安装"})
			return
		}

		pkg, err := a.svc.InstallPackage(context.Background(), dir)
		if err != nil {
			a.fail(err)
			return
		}

		log.Printf("已安装组件 %s（%s）", pkg.ID, pkg.Dir)
		a.pushManager("manager:toast", toast{
			OK:   true,
			Text: fmt.Sprintf("已安装组件「%s」", pkg.Name),
		})
		a.pushState()
	})
}

// spawnMissing 为某组件补齐窗口：已经有的实例不动，缺的补上。
func (a *App) spawnMissing(componentID string) {
	for _, v := range a.svc.ActiveInstances() {
		if v.ComponentID != componentID {
			continue
		}
		if _, ok := a.running[v.ID]; ok {
			continue
		}
		if err := a.spawn(v); err != nil {
			log.Printf("启动实例 %s（%s）失败: %v", v.ID, v.ComponentName, err)
		}
	}
}

// stopInstances 关掉某组件的所有窗口，但保留实例记录。
func (a *App) stopInstances(componentID string) {
	ids := make([]string, 0, len(a.order))
	for _, id := range a.order {
		if r := a.running[id]; r != nil && r.view.ComponentID == componentID {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		r := a.running[id]
		if r == nil {
			continue
		}
		// 停用不是删除，几何要留住，重新启用时才能回到原位。
		a.persistBounds(r)
		r.win.Destroy()
	}
}

// isBuiltin 判断某个插件是不是内置的。
//
// 组不在这条判断里：它是内核数据，没有独立目录，也就没有"打开目录"这回事。
func (a *App) isBuiltin(kind component.PackageKind, id string) bool {
	cv, ok := a.svc.Component(id)
	return ok && cv.Builtin
}

// groupParams 是界面提交组设置时的入参形状。
//
// 和 managerGroup 的区别只有一个：没有 Activation。
// 「抢不抢焦点」不是用户该操心的开关——穿透窗口本来就不可能被激活，
// 桌面层也不该抢焦点，两者都由领域层的 Normalize 推出来。
type groupParams struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Color        string `json:"color"`
	Layer        string `json:"layer"`
	ClickThrough bool   `json:"clickThrough"`
	Resizable    bool   `json:"resizable"`
	SizeMode     string `json:"sizeMode"`
	Titlebar     bool   `json:"titlebar"`
	Padding      int    `json:"padding"`
	Radius       int    `json:"radius"`
	Background   string `json:"background"`
	Shadow       string `json:"shadow"`
}

func (p groupParams) toGroup() component.Group {
	return component.Group{
		ID:    p.ID,
		Name:  p.Name,
		Color: p.Color,
		Window: component.WindowSpec{
			Layer:        component.Layer(p.Layer),
			ClickThrough: p.ClickThrough,
			Resizable:    p.Resizable,
			SizeMode:     component.SizeMode(p.SizeMode),
			Titlebar:     p.Titlebar,
		},
		Visual: component.VisualFrom(component.VisualValues{
			Padding:    p.Padding,
			Radius:     p.Radius,
			Background: p.Background,
			Shadow:     p.Shadow,
		}),
	}
}

// rebuildGroup 把组里所有窗口拆掉重建。
func (a *App) rebuildGroup(groupID string) {
	members := map[string]bool{}
	for _, id := range a.svc.InstanceIDsInGroup(groupID) {
		members[id] = true
	}
	a.rebuild(func(instanceID string) bool { return members[instanceID] })
}

// rebuildInstance 把某一个窗口拆掉重建。
func (a *App) rebuildInstance(instanceID string) {
	a.rebuild(func(id string) bool { return id == instanceID })
}

// rebuild 把匹配的窗口拆掉重建。
//
// 层级、穿透、可缩放、标题栏都是 CreateWindow 时的选项，没有"就地改"这回事，
// 只能拆了重来。位置与显隐从实例记录里恢复，用户看不出差别。
//
// 这里不怕"拆掉最后一个窗口导致进程退出"：管理界面开着，maybeQuit 会直接返回。
func (a *App) rebuild(match func(instanceID string) bool) {
	for _, v := range a.svc.AllInstances() {
		if !match(v.ID) {
			continue
		}
		if r := a.running[v.ID]; r != nil {
			// 先记账：拆窗口之后就拿不到几何了。
			a.persistBounds(r)
			r.win.Destroy()
		}
	}
	// 拆完再取一次快照：persistBounds 刚把几何写回去。
	for _, v := range a.svc.ActiveInstances() {
		if !match(v.ID) {
			continue
		}
		if _, ok := a.running[v.ID]; ok {
			continue
		}
		if err := a.spawn(v); err != nil {
			log.Printf("重建实例 %s 失败: %v", v.ID, err)
		}
	}
}

// restyleGroup 只把外观推给已经在跑的页面，不动窗口。
//
// 改留白、圆角、底色、阴影属于这一类：它们是纯 CSS，改完立刻生效，
// 不需要重建窗口——重建会闪一下，拖动颜色滑杆时尤其明显。
func (a *App) restyleGroup(groupID string) {
	members := map[string]bool{}
	for _, id := range a.svc.InstanceIDsInGroup(groupID) {
		members[id] = true
	}
	for _, v := range a.svc.AllInstances() {
		if !members[v.ID] {
			continue
		}
		r := a.running[v.ID]
		if r == nil || r.host == nil {
			continue
		}
		r.host.ApplyStyle(stylePayload{
			Vars:      v.Style.CSSVariables(),
			Titlebar:  v.Style.Titlebar,
			Resizable: v.Style.Resizable,
			Layer:     string(v.Style.Layer),
		})
	}
}

// fail 把错误提示推到管理界面的浮层上。
//
// 桥接分派里"出错"的收尾几乎都一样：提示一句、必要时再推一次状态。
// 收成一句话是为了让 switch 里只剩下业务差别。
//
// **同时写日志**：浮层 3 秒就消失，而失败往往发生在"用户点了但窗口没出来"
// 这种时候，事后只能翻 app.log。只弹提示等于什么都没留下。
func (a *App) fail(err error) {
	log.Printf("管理界面操作失败: %v", err)
	a.pushManager("manager:toast", toast{Text: err.Error()})
}

// pushState 把完整状态推给管理界面。
//
// 组件的显隐、实例的增减都可能在管理界面之外发生（托盘菜单、组件自己关闭），
// 界面必须跟着变，否则会显示成"关掉了但还列在那儿"。所以凡是改过状态的地方
// 都要调一次，宁可多推也不能少推。
func (a *App) pushState() {
	a.pushManager("manager:state", a.managerState())
}

// params 解析请求参数。解析失败时已经把错误回给前端，返回 false 让调用方直接返回。
func params(h *Host, req request, v any) bool {
	if err := decode(req.Params, v); err != nil {
		replyTo(h, req.ID, nil, err)
		return false
	}
	return true
}

// decode 解析请求参数，顺手把"没有参数"当成空结构体。
func decode(raw []byte, v any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, v)
}
