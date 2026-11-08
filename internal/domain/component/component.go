package component

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// 领域错误。上层用 errors.Is 判断，不依赖错误文本。
var (
	ErrGroupNotFound     = errors.New("组不存在")
	ErrComponentNotFound = errors.New("组件不存在")
	ErrInstanceNotFound  = errors.New("实例不存在")
	ErrComponentDisabled = errors.New("组件已停用")
)

// Size 是逻辑像素尺寸。
type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// PackageKind 是插件包的类型。
//
// 只剩一种：窗口外观已经变成内核里的「组」，不再是可安装的包。
// 保留这个类型是为了让安装器与错误文案有明确的词可用。
type PackageKind string

const (
	// PackageComponent 是 L2 组件（有入口页与脚本）。
	PackageComponent PackageKind = "component"
)

// Package 是一个插件包：待安装的，或已经装好的。
type Package struct {
	Kind PackageKind
	ID   string
	Name string
	// Dir 是包所在目录。安装成功后指向插件目录里的新位置。
	Dir        string
	Definition *Definition
}

// Rect 是实例的位置与尺寸。
//
// 口径刻意分开：X/Y 是屏幕物理像素（和窗口位置一一对应），
// Width/Height 是逻辑像素（96 DPI 基准）——跨不同缩放的显示器时，
// 位置不需要换算，尺寸必须按目标屏 DPI 再乘一次。
type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// 尺寸兜底与下限。
const (
	DefaultWidth  = 420
	DefaultHeight = 360
	MinWidth      = 260
	MinHeight     = 200
)

// Definition 是一个 L2 组件的清单（manifest.json）。
//
// 它是"程序"这一级的静态定义：有入口页、有权限、有默认尺寸。
type Definition struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Entry       string `json:"entry"`
	Author      string `json:"author,omitempty"`
	Description string `json:"description,omitempty"`

	// 以下四个字段宿主只解析、暂不执行。它们是清单格式的一部分
	// （见 docs/component-framework.md 的清单小节），先把格式定下来，
	// 免得插件作者写好的清单因为内核还没实现而报错——删掉等于改格式。
	Icon         string   `json:"icon,omitempty"`
	Permissions  []string `json:"permissions,omitempty"`
	Themes       []string `json:"themes,omitempty"`
	DefaultTheme string   `json:"defaultTheme,omitempty"`

	DefaultBounds Size              `json:"defaultBounds"`
	MinBounds     Size              `json:"minBounds"`
	Singleton     bool              `json:"singleton"`
	Visual        Visual            `json:"visual,omitempty"`
	CSSVars       map[string]string `json:"cssVars,omitempty"`

	// Variables 是作者声明的变量：要哪些、叫什么、怎么填。
	//
	// 只有**声明**在这里；值属于实例，存在 data\vars\ 下，一个实例一份。
	// 所以同一个组件的两个窗口可以填不同的变量——这正是"变量按实例存"
	// 的意义（一个便签用大字号、另一个用小字号）。
	Variables []Variable `json:"variables,omitempty"`

	// Builtin 与 SourceDir 由加载器在运行时填入，不参与序列化。
	Builtin   bool   `json:"-"`
	SourceDir string `json:"-"`
}

// Normalize 补齐尺寸兜底值。
func (d Definition) Normalize() Definition {
	if d.DefaultBounds.Width <= 0 {
		d.DefaultBounds.Width = DefaultWidth
	}
	if d.DefaultBounds.Height <= 0 {
		d.DefaultBounds.Height = DefaultHeight
	}
	if d.MinBounds.Width <= 0 {
		d.MinBounds.Width = MinWidth
	}
	if d.MinBounds.Height <= 0 {
		d.MinBounds.Height = MinHeight
	}
	for i := range d.Variables {
		d.Variables[i] = d.Variables[i].Normalize()
	}
	return d
}

// Validate 校验清单的结构性字段。
//
// 不再检查"绑定了哪个样式"：组件不声明外观，它长什么样取决于被放进了哪个组。
func (d Definition) Validate() error {
	if err := ValidateID(d.ID); err != nil {
		return fmt.Errorf("id: %w", err)
	}
	if d.Name == "" {
		return errors.New("name 不能为空")
	}
	if d.Entry == "" {
		return errors.New("entry 不能为空")
	}
	if err := validateRelativePath(d.Entry); err != nil {
		return fmt.Errorf("entry: %w", err)
	}
	// 变量声明写错了要在加载清单时就报，而不是等用户打开管理界面
	// 才发现有一个填不了的控件。
	if err := ValidateVariables(d.Variables); err != nil {
		return fmt.Errorf("variables: %w", err)
	}
	return nil
}

// Instance 是组件的一次运行。
//
// 位置尺寸存在这里，而不是存在组件定义里：同一个组件可以开多个实例，
// 每个实例有自己的几何与显隐状态。
//
// **分组也在这里**：一个组管的是一批窗口，不是一批组件。同一个便签组件
// 可以开两个实例，一个贴在桌面层、一个浮在最上面——组件级归属表达不了这件事。
type Instance struct {
	ID          string `json:"id"`
	ComponentID string `json:"componentId"`
	// GroupID 是这个实例所属的组，决定它的窗口形态与外观。
	// 空表示还没归属（老数据），启动时按旧归属或兜底组补上。
	GroupID string `json:"groupId,omitempty"`
	Bounds  Rect   `json:"bounds"`
	// Zoom 是**内容缩放**：页面按这个倍数放大，窗口尺寸不变。
	//
	// 它和 Bounds 是两个正交的维度——Bounds 决定窗口多大，Zoom 决定窗口里
	// 那一页内容多大。合成一个值会丢掉"窗口变大但字不变"这种正当需求
	// （便签拉长时不该顺便把字撑大）。
	//
	// 0 表示没设置过（老数据），由 EffectiveZoom 归一成 1。
	Zoom float64 `json:"zoom,omitempty"`
	// Positioned 为 false 表示还没摆过位置（新建实例），启动时居中。
	Positioned bool      `json:"positioned"`
	Visible    bool      `json:"visible"`
	CreatedAt  time.Time `json:"createdAt"`
}

// 内容缩放的取值范围。
//
// 下限取 0.5：再小页面里的字就没法读了，不如让用户直接缩窗口。
// 上限取 3.0：WebView2 的栅格化缩放超过 3 倍时纹理开销开始明显，
// 而 3 倍对桌面上任何一个组件都已经够用。
const (
	MinZoom     = 0.5
	MaxZoom     = 3.0
	DefaultZoom = 1.0
)

// EffectiveZoom 返回可以直接下发给引擎的缩放倍数。
//
// 三件事一起做：把"没设置"补成 1、把越界值夹回范围、把 NaN 挡掉。
// 夹取放在领域层而不是界面上，是因为手改 layout.json 是明摆着的用法，
// 界面上的滑块管不到那种输入。
func (i Instance) EffectiveZoom() float64 {
	z := i.Zoom
	if z <= 0 || math.IsNaN(z) {
		return DefaultZoom
	}
	if z < MinZoom {
		return MinZoom
	}
	if z > MaxZoom {
		return MaxZoom
	}
	return z
}

// validateRelativePath 拒绝绝对路径与 .. 段。
//
// 这保护的是用户机器，不是插件作者：entry 会被拼进虚拟主机目录，
// 允许 ../ 就等于允许读插件目录之外的文件。
func validateRelativePath(p string) error {
	if len(p) == 0 {
		return errors.New("不能为空")
	}
	if p[0] == '/' || p[0] == '\\' {
		return fmt.Errorf("%q 不能是绝对路径", p)
	}
	if len(p) >= 2 && p[1] == ':' {
		return fmt.Errorf("%q 不能带盘符", p)
	}
	// 逐段检查，拦住 "a/../../b" 这类绕过。
	start := 0
	for i := 0; i <= len(p); i++ {
		if i == len(p) || p[i] == '/' || p[i] == '\\' {
			seg := p[start:i]
			if seg == ".." {
				return fmt.Errorf("%q 不能包含 ..", p)
			}
			start = i + 1
		}
	}
	return nil
}
