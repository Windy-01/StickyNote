// Package component 是组件宿主的领域层。
//
// 它只描述"组件是什么、组是什么、两者怎么合并"，不含任何 Win32、文件或 WebView 细节。
// 插件只剩一级：L2 组件（程序）。窗口外观由「组」决定，而组是内核里的运行时数据，
// 不是可安装的插件包——用户自己勾开关就能造一个新组。
package component

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ReservedIDPrefix 是内置资源的 id 前缀。
//
// 带这个前缀的东西由 exe 内嵌、每次启动重新解包，所以安装器拒绝写入。
// 组的 id 也用这个前缀（`builtin.group.desktop` 之类）来表示"随程序生成"。
const ReservedIDPrefix = "builtin."

// Layer 是窗口层级，三选一互斥。
//
// 它是**组**的属性，不是组件的属性：同一套外观下的所有组件行为一致，
// 用户调整组就等于调整这一批窗口。
type Layer string

const (
	// LayerNormal 是普通层级，会被别的窗口盖住。
	LayerNormal Layer = "normal"
	// LayerTopmost 是置顶窗口，被其他应用覆盖不了。
	LayerTopmost Layer = "topmost"
	// LayerDesktop 是桌面层：窗口锚在 Progman 正上方，
	// 因此被任何应用窗口覆盖、Win+D 时跟随桌面显隐。桌面组件用它。
	LayerDesktop Layer = "desktop"
)

// Activation 决定点击窗口是否抢焦点。
type Activation string

const (
	Activate   Activation = "activate"
	NoActivate Activation = "noActivate"
)

// SizeMode 决定窗口尺寸由谁说了算。
type SizeMode string

const (
	SizeFixed SizeMode = "fixed"
	SizeUser  SizeMode = "user"
	SizeFit   SizeMode = "fitContent"
)

// Dock 是初始停靠位置。
//
// 目前只影响"第一次出现在哪"，不做贴边吸附；UI 也还没暴露它。
// 留着是因为"贴边自动隐藏"这类需求迟早要落到某个统一词表上。
type Dock string

const (
	DockFree   Dock = "free"
	DockLeft   Dock = "left"
	DockRight  Dock = "right"
	DockTop    Dock = "top"
	DockBottom Dock = "bottom"
)

// WindowSpec 是组的窗口开关。
//
// 全部是正交开关：用户可以任意组合，没有"预设形态"这种东西。
// 唯一的约束是互斥项（层级三选一）与推导项（穿透必然不抢焦点），
// 后者由 Normalize 自动补齐，不需要用户在界面上再点一次。
type WindowSpec struct {
	Layer        Layer      `json:"layer"`
	ClickThrough bool       `json:"clickThrough"`
	Activation   Activation `json:"activation"`
	Resizable    bool       `json:"resizable"`
	SizeMode     SizeMode   `json:"sizeMode"`
	Titlebar     bool       `json:"titlebar"`
	Dock         Dock       `json:"dock,omitempty"`
}

// DefaultWindowSpec 是新建组时的初始开关。
//
// 层级默认「常规」而不是置顶：用户新建一个组，最不容易被吓到的是"它跟普通窗口一样"。
// 想要浮在最上面，界面上有明确的档位可选。
func DefaultWindowSpec() WindowSpec {
	return WindowSpec{
		Layer:        LayerNormal,
		ClickThrough: false,
		Activation:   Activate,
		Resizable:    true,
		SizeMode:     SizeUser,
		Titlebar:     true,
		Dock:         DockFree,
	}
}

// Normalize 补齐空字段，并推导出必须成立的组合。
//
// 只补枚举类的空值；布尔字段保持原样——false 是合法取值，不能被默认值覆盖。
func (w WindowSpec) Normalize() WindowSpec {
	d := DefaultWindowSpec()
	if w.Layer == "" {
		w.Layer = d.Layer
	}
	if w.Activation == "" {
		w.Activation = d.Activation
	}
	if w.SizeMode == "" {
		w.SizeMode = d.SizeMode
	}
	if w.Dock == "" {
		w.Dock = d.Dock
	}
	// 穿透窗口永远收不到鼠标，也就永远不可能被激活。
	// 允许"穿透 + 抢焦点"这种组合存在于数据里，只会在运行时自相矛盾，
	// 所以在归一化这一步就把它抹平。
	if w.ClickThrough {
		w.Activation = NoActivate
	}
	// 桌面层贴在图标之上、应用窗口之下，本来就不该抢焦点——
	// 抢了焦点会让它从"桌面的一部分"变成"一个被盖住的窗口"。
	if w.Layer == LayerDesktop {
		w.Activation = NoActivate
	}
	return w
}

// Validate 检查每个枚举值是否都在词表内。
func (w WindowSpec) Validate() error {
	switch w.Layer {
	case LayerNormal, LayerTopmost, LayerDesktop:
	default:
		return fmt.Errorf("layer=%q 不在词表内（normal | topmost | desktop）", w.Layer)
	}
	switch w.Activation {
	case Activate, NoActivate:
	default:
		return fmt.Errorf("activation=%q 不在词表内（activate | noActivate）", w.Activation)
	}
	switch w.SizeMode {
	case SizeFixed, SizeUser, SizeFit:
	default:
		return fmt.Errorf("sizeMode=%q 不在词表内（fixed | user | fitContent）", w.SizeMode)
	}
	switch w.Dock {
	case "", DockFree, DockLeft, DockRight, DockTop, DockBottom:
	default:
		return fmt.Errorf("dock=%q 不在词表内（free | left | right | top | bottom）", w.Dock)
	}
	return nil
}

// Visual 是外观参数。字段用指针表达"没写"，这样上层只覆盖想改的那几项。
//
// 用指针而不是零值判断，是因为 0 和 "" 本身是合法设计取值：
// radius=0 表示直角，不能和"没指定"混为一谈。
type Visual struct {
	Padding    *int    `json:"padding,omitempty"`
	Radius     *int    `json:"radius,omitempty"`
	Background *string `json:"background,omitempty"`
	Shadow     *string `json:"shadow,omitempty"`
}

// Merge 用 over 覆盖 v 中已指定的字段，返回新值（不修改入参）。
func (v Visual) Merge(over Visual) Visual {
	out := v
	if over.Padding != nil {
		out.Padding = over.Padding
	}
	if over.Radius != nil {
		out.Radius = over.Radius
	}
	if over.Background != nil {
		out.Background = over.Background
	}
	if over.Shadow != nil {
		out.Shadow = over.Shadow
	}
	return out
}

// Group 是一组组件的共享外观与窗口行为。
//
// 它取代了原来那个"可安装的 L1 样式包"：不再是插件，而是内核里的数据，
// 用户在界面上直接编辑。一个组件只能属于一个组，组内所有组件共用这套外观。
type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Color 是布局板与侧边栏用来区分这个组的颜色（`#rrggbb`）。
	//
	// 它是**给人看的标记**，不参与任何窗口或样式计算——窗口长什么样只由
	// Visual 决定。空表示还没分配，由应用层按调色板补齐。
	Color   string            `json:"color,omitempty"`
	Window  WindowSpec        `json:"window"`
	Visual  Visual            `json:"visual,omitempty"`
	CSSVars map[string]string `json:"cssVars,omitempty"`
	// Components 是**旧模型**留下的字段：那时归属落在组件上（组件→组一对多）。
	//
	// 现在归属落在实例上（`Instance.GroupID`），这个字段只在启动时用来迁移
	// 老数据——把老归属搬到实例上——然后清空，之后一直是空的。
	// 保留字段名是因为 json 解析必须能读到它，删掉就等于迁移前就把数据丢了。
	Components []string `json:"components,omitempty"`

	// Builtin 由组合根在首次生成时标记，不参与序列化。
	Builtin bool `json:"-"`
}

// colorPattern 是颜色字段允许的形状：只收 `#rrggbb`。
//
// 刻意不收 rgba()、颜色名、8 位带 alpha 的写法：这个值要同时喂给
// `<input type="color">` 和 CSS 变量，形状统一才不会一半能用一半不能用。
var colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Validate 校验组的结构性字段。
func (g Group) Validate() error {
	if err := ValidateID(g.ID); err != nil {
		return fmt.Errorf("id: %w", err)
	}
	if strings.TrimSpace(g.Name) == "" {
		return errors.New("name 不能为空")
	}
	if g.Color != "" && !colorPattern.MatchString(g.Color) {
		return fmt.Errorf("color=%q 必须是 #rrggbb 形式", g.Color)
	}
	if err := g.Window.Normalize().Validate(); err != nil {
		return err
	}
	seen := make(map[string]bool, len(g.Components))
	for _, id := range g.Components {
		if err := ValidateID(id); err != nil {
			return fmt.Errorf("components: %w", err)
		}
		if seen[id] {
			return fmt.Errorf("components: 组件 %s 重复出现在同一个组里", id)
		}
		seen[id] = true
	}
	return nil
}

// Normalize 补齐窗口开关的缺省值。
func (g Group) Normalize() Group {
	g.Window = g.Window.Normalize()
	return g
}

// Has 报告组件是否在这个组里。
func (g Group) Has(componentID string) bool {
	for _, id := range g.Components {
		if id == componentID {
			return true
		}
	}
	return false
}

// ResolvedStyle 是合并后的最终窗口配置。宿主只认这个结果。
type ResolvedStyle struct {
	GroupID      string            `json:"groupId"`
	GroupName    string            `json:"groupName"`
	Layer        Layer             `json:"layer"`
	ClickThrough bool              `json:"clickThrough"`
	Activation   Activation        `json:"activation"`
	Resizable    bool              `json:"resizable"`
	SizeMode     SizeMode          `json:"sizeMode"`
	Dock         Dock              `json:"dock"`
	Titlebar     bool              `json:"titlebar"`
	Padding      int               `json:"padding"`
	Radius       int               `json:"radius"`
	Background   string            `json:"background"`
	Shadow       string            `json:"shadow"`
	CSSVars      map[string]string `json:"cssVars"`
}

// 外观兜底值。组和组件都没指定时用它们。
const (
	DefaultPadding    = 0
	DefaultRadius     = 12
	DefaultBackground = "#fffbf0"
	DefaultShadow     = ""
)

// valueOr 取指针指向的值，为 nil 时用兜底值。
//
// Visual 的字段全是指针，而 0 和 "" 都是合法设计取值（radius=0 表示直角），
// 所以不能按零值判断"有没有指定"。
func valueOr[T any](p *T, def T) T {
	if p != nil {
		return *p
	}
	return def
}

// VisualValues 是补齐兜底值后的外观参数。
//
// 界面要显示和编辑具体数字，而 Visual 的字段是指针（用来表达"没设"），
// 所以中间需要这一层：读出来是具体值，写回去再变回指针。
type VisualValues struct {
	Padding    int    `json:"padding"`
	Radius     int    `json:"radius"`
	Background string `json:"background"`
	Shadow     string `json:"shadow"`
}

// Values 用兜底值补齐指针字段。
func (v Visual) Values() VisualValues {
	return VisualValues{
		Padding:    valueOr(v.Padding, DefaultPadding),
		Radius:     valueOr(v.Radius, DefaultRadius),
		Background: valueOr(v.Background, DefaultBackground),
		Shadow:     valueOr(v.Shadow, DefaultShadow),
	}
}

// VisualFrom 把界面传回来的具体值转回指针形式。
//
// 全部转成非 nil：用户在组设置面板里看到的是具体值，保存时就是要"这些值"，
// 而不是"这几项没设"。想让某项回到默认，就把默认值填回去。
func VisualFrom(v VisualValues) Visual {
	return Visual{
		Padding:    &v.Padding,
		Radius:     &v.Radius,
		Background: &v.Background,
		Shadow:     &v.Shadow,
	}
}

// Resolve 把组的外观与组件自带的外观合并成最终结果。
//
// **组永远赢**：组件只提供"组没设时"的默认值。理由很直接——组是用户在界面上
// 亲手调的，他调过的东西必须生效；组件作者写的那份是出厂建议，不是决定权。
func Resolve(g Group, over Visual, overVars map[string]string) ResolvedStyle {
	w := g.Window.Normalize()
	merged := over.Merge(g.Visual)

	return ResolvedStyle{
		GroupID:      g.ID,
		GroupName:    g.Name,
		Layer:        w.Layer,
		ClickThrough: w.ClickThrough,
		Activation:   w.Activation,
		Resizable:    w.Resizable,
		SizeMode:     w.SizeMode,
		Dock:         w.Dock,
		Titlebar:     w.Titlebar,
		Padding:      valueOr(merged.Padding, DefaultPadding),
		Radius:       valueOr(merged.Radius, DefaultRadius),
		Background:   valueOr(merged.Background, DefaultBackground),
		Shadow:       valueOr(merged.Shadow, DefaultShadow),
		CSSVars:      MergeVars(overVars, g.CSSVars),
	}
}

// MergeVars 合并 CSS 变量，over 优先。
func MergeVars(base, over map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// CSSVariables 把解析结果摊平成注入页面的变量表。
//
// 这些 --sn-* 变量就是组与组件之间的契约：组管窗口外壳（留白、圆角、底色、阴影），
// 组件必须用 var(--sn-xxx) 消费它们，否则组对它的外观无效。
func (r ResolvedStyle) CSSVariables() map[string]string {
	vars := make(map[string]string, len(r.CSSVars)+4)
	for k, v := range r.CSSVars {
		vars[k] = v
	}
	vars["--sn-padding"] = fmt.Sprintf("%dpx", r.Padding)
	vars["--sn-radius"] = fmt.Sprintf("%dpx", r.Radius)
	vars["--sn-background"] = r.Background
	vars["--sn-shadow"] = r.Shadow
	return vars
}

// LayerLabel 是层级的界面文案。放在领域层，是因为日志与界面要一致。
func (l Layer) Label() string {
	switch l {
	case LayerTopmost:
		return "置顶"
	case LayerDesktop:
		return "置底"
	default:
		return "常规"
	}
}

// idPattern 限定 id 可用字符：它会被拼进目录名，必须防止路径穿越。
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidateID 校验 id 合法性。id 会用于目录名，所以不接受斜杠与 ".."。
func ValidateID(id string) error {
	if id == "" {
		return errors.New("不能为空")
	}
	if !idPattern.MatchString(id) {
		return fmt.Errorf("%q 含非法字符（只允许字母、数字、点、下划线、连字符，且不超过 64 字符）", id)
	}
	return nil
}
