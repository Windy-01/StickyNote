package componentapp

import (
	"encoding/json"

	"stickynote/internal/domain/component"
)

// InstanceView 是表现层唯一能拿到的实例快照。
//
// 表现层不持有领域对象：要窗口就按 Style 建，要路径就用 AssetsDir，
// 不需要知道组配置、仓储、合并规则长什么样。
type InstanceView struct {
	ID            string
	ComponentID   string
	ComponentName string
	// GroupID/GroupName 是这个实例所属的组。**归属在实例上**：同一个组件
	// 可以开两个实例，一个贴桌面层、一个浮在最上面，各归各的组。
	GroupID   string
	GroupName string
	// Entry 是组件包内的入口页相对路径，例如 "index.html"。
	Entry string
	// AssetsDir 是组件包解包后的真实目录，WebView2 的虚拟主机映射要用它。
	AssetsDir string
	Style     component.ResolvedStyle
	// Vars 是这个实例的变量**最终值**（没填过的是声明里的默认值）。
	// 声明在 ComponentView.Variables 里——同一个组件的两个实例共用一份声明，
	// 只有值各不相同，所以两处分开下发。
	//
	// 值保持为 json.RawMessage：文本类是 JSON 字符串，`json` 类型可能是
	// 数组或对象，表现层按原样下发即可。
	Vars   map[string]json.RawMessage
	Bounds component.Rect
	// Zoom 是内容缩放倍数（1 表示原始大小）。**不改窗口尺寸**，
	// 只影响页面渲染大小，所以它和 Bounds 分开下发。
	Zoom float64
	// Positioned 为 false 表示还没摆过位置，表现层应居中显示。
	Positioned bool
	MinBounds  component.Size
	Visible    bool
}

// GroupView 是组列表用的扁平快照。
//
// 它带着 Window（窗口开关）与 Visual（外观参数），因为界面上要能直接编辑。
// **不列成员**：成员是实例，从 InstanceView 里按 GroupID 过滤即可，
// 两处各存一份迟早对不上。
type GroupView struct {
	ID   string
	Name string
	// Color 是布局板与侧边栏用的识别色（`#rrggbb`），纯标记，不参与窗口计算。
	Color   string
	Window  component.WindowSpec
	Visual  component.Visual
	CSSVars map[string]string
}

// ComponentView 是组件列表用的扁平快照。
//
// 组件是"能开窗口的那种东西"的静态定义，本身没有组也没有窗口；
// 它名下有哪些窗口，看 InstanceIDs。
type ComponentView struct {
	ID          string
	Name        string
	Author      string
	Builtin     bool
	Enabled     bool
	Description string
	// Singleton 表示这个组件同时只能有一个实例。界面要靠它把
	// "再建一个"这条注定失败的路直接禁掉，而不是让用户点了才收到报错。
	Singleton bool
	// Variables 是作者声明的变量：要哪些、叫什么、怎么填。值属于实例，
	// 见 InstanceView.Vars。
	Variables   []component.Variable
	InstanceIDs []string
}
