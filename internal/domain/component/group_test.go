package component_test

import (
	"testing"

	"stickynote/internal/domain/component"
)

func intPtr(v int) *int       { return &v }
func strPtr(v string) *string { return &v }

// 组赢：用户亲手调的留白、圆角、底色必须压过组件写死的出厂建议。
func TestResolveGroupWinsOverComponent(t *testing.T) {
	g := component.Group{
		ID:   "builtin.group.topmost",
		Name: "置顶",
		Window: component.WindowSpec{
			Layer:        component.LayerTopmost,
			ClickThrough: false,
			Resizable:    false,
			SizeMode:     component.SizeFixed,
			Titlebar:     false,
		},
		Visual: component.Visual{
			Padding:    intPtr(12),
			Radius:     intPtr(14),
			Background: strPtr("rgba(0,0,0,0.9)"),
		},
	}

	// 组件想改成 0 留白、20 圆角、自己一层阴影。
	over := component.Visual{
		Padding: intPtr(0),
		Radius:  intPtr(20),
		Shadow:  strPtr("0 1px 2px #000"),
	}
	got := component.Resolve(g, over, nil)

	if got.Padding != 12 {
		t.Fatalf("留白应被组压回 12，实际 %d", got.Padding)
	}
	if got.Radius != 14 {
		t.Fatalf("圆角应被组压回 14，实际 %d", got.Radius)
	}
	if got.Background != "rgba(0,0,0,0.9)" {
		t.Fatalf("底色应来自组，实际 %q", got.Background)
	}
	// 组没设阴影，组件设了 → 组没意见的地方组件说了算。
	if got.Shadow != "0 1px 2px #000" {
		t.Fatalf("组没设阴影时应保留组件值，实际 %q", got.Shadow)
	}

	// 窗口语义只来自组，组件那边根本没有这些字段。
	if got.Layer != component.LayerTopmost {
		t.Fatalf("layer 应来自组，实际 %q", got.Layer)
	}
	if got.Resizable {
		t.Fatal("resizable 应来自组（false）")
	}
	if got.GroupID != "builtin.group.topmost" || got.GroupName != "置顶" {
		t.Fatalf("组标识丢失: %q / %q", got.GroupID, got.GroupName)
	}
}

// 组和组件都没写视觉字段时，落到兜底值。
func TestResolveDefaults(t *testing.T) {
	got := component.Resolve(component.Group{ID: "g", Name: "g"}, component.Visual{}, nil)

	if got.Radius != component.DefaultRadius {
		t.Fatalf("radius 兜底值应为 %d，实际 %d", component.DefaultRadius, got.Radius)
	}
	if got.Background != component.DefaultBackground {
		t.Fatalf("background 兜底值应为 %q，实际 %q", component.DefaultBackground, got.Background)
	}
	if got.Padding != component.DefaultPadding {
		t.Fatalf("padding 兜底值应为 %d，实际 %d", component.DefaultPadding, got.Padding)
	}
	if got.Layer != component.LayerNormal {
		t.Fatalf("layer 缺省应为 normal，实际 %q", got.Layer)
	}
}

// 穿透窗口收不到鼠标，因此不可能被激活——这条由 Normalize 推导，不靠用户勾。
func TestClickThroughForcesNoActivate(t *testing.T) {
	g := component.Group{
		ID:   "g",
		Name: "g",
		Window: component.WindowSpec{
			Layer:        component.LayerNormal,
			ClickThrough: true,
			Activation:   component.Activate, // 自相矛盾的组合
		},
	}
	got := component.Resolve(g, component.Visual{}, nil)

	if got.Activation != component.NoActivate {
		t.Fatalf("穿透窗口应强制 noActivate，实际 %q", got.Activation)
	}
	if !got.ClickThrough {
		t.Fatal("clickThrough 丢了")
	}
}

// 桌面层贴在图标之上、应用窗口之下，抢焦点会让它变成"一个被盖住的窗口"。
func TestDesktopLayerForcesNoActivate(t *testing.T) {
	g := component.Group{
		ID:   "g",
		Name: "g",
		Window: component.WindowSpec{
			Layer:      component.LayerDesktop,
			Activation: component.Activate,
		},
	}
	got := component.Resolve(g, component.Visual{}, nil)
	if got.Activation != component.NoActivate {
		t.Fatalf("桌面层应强制 noActivate，实际 %q", got.Activation)
	}
}

// CSS 变量是组与组件之间的契约，必须包含窗口外壳那四个，且组的值优先。
func TestCSSVariables(t *testing.T) {
	resolved := component.Resolve(
		component.Group{
			ID:      "g",
			Name:    "g",
			Visual:  component.Visual{Padding: intPtr(18), Radius: intPtr(8)},
			CSSVars: map[string]string{"--ink": "#000"},
		},
		component.Visual{},
		map[string]string{"--ink": "#111", "--extra": "1"},
	)

	vars := resolved.CSSVariables()
	want := map[string]string{
		"--sn-padding":    "18px",
		"--sn-radius":     "8px",
		"--sn-background": component.DefaultBackground,
		"--ink":           "#000", // 组覆盖组件
		"--extra":         "1",    // 组件独有的保留
	}
	for k, v := range want {
		if vars[k] != v {
			t.Errorf("%s = %q，期望 %q", k, vars[k], v)
		}
	}
	if _, ok := vars["--sn-shadow"]; !ok {
		t.Error("缺少 --sn-shadow")
	}
}

func TestWindowSpecValidate(t *testing.T) {
	cases := []struct {
		name string
		spec component.WindowSpec
		ok   bool
	}{
		{"默认值合法", component.DefaultWindowSpec(), true},
		{"normal 合法", component.WindowSpec{Layer: component.LayerNormal}, true},
		{"topmost 合法", component.WindowSpec{Layer: component.LayerTopmost}, true},
		{"desktop 合法", component.WindowSpec{Layer: component.LayerDesktop}, true},
		{"toast 已从词表移除", component.WindowSpec{Layer: "toast"}, false},
		{"未知层级", component.WindowSpec{Layer: "floating"}, false},
		{"未知激活模式", component.WindowSpec{Activation: "sometimes"}, false},
		{"未知尺寸模式", component.WindowSpec{SizeMode: "auto"}, false},
		{"未知停靠", component.WindowSpec{Dock: "middle"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.spec.Normalize().Validate()
			if c.ok && err != nil {
				t.Fatalf("应通过，实际: %v", err)
			}
			if !c.ok && err == nil {
				t.Fatal("应失败，实际通过")
			}
		})
	}
}

// Visual 的指针语义：0 是合法取值（直角），不能和"没设"混为一谈。
func TestVisualValuesRoundTrip(t *testing.T) {
	v := component.Visual{Padding: intPtr(0), Radius: intPtr(0)}
	vals := v.Values()

	if vals.Padding != 0 || vals.Radius != 0 {
		t.Fatalf("显式 0 被当成没设: %+v", vals)
	}
	if vals.Background != component.DefaultBackground {
		t.Fatalf("没设的底色应落到兜底值，实际 %q", vals.Background)
	}

	back := component.VisualFrom(vals)
	if back.Padding == nil || *back.Padding != 0 {
		t.Fatal("VisualFrom 应把每个字段都写成具体值")
	}
}

func TestGroupValidate(t *testing.T) {
	base := component.Group{
		ID:     "vendor.group.a",
		Name:   "甲",
		Window: component.DefaultWindowSpec(),
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("基准组应通过: %v", err)
	}

	noName := base
	noName.Name = "  "
	if err := noName.Validate(); err == nil {
		t.Fatal("空名字应被拒绝")
	}

	badID := base
	badID.ID = "../etc"
	if err := badID.Validate(); err == nil {
		t.Fatal("非法 id 应被拒绝")
	}

	dup := base
	dup.Components = []string{"note", "note"}
	if err := dup.Validate(); err == nil {
		t.Fatal("同一组件重复出现在一个组里应被拒绝")
	}
}

func TestGroupHas(t *testing.T) {
	g := component.Group{Components: []string{"note", "clock"}}
	if !g.Has("note") {
		t.Fatal("note 应在组里")
	}
	if g.Has("danmaku") {
		t.Fatal("danmaku 不在组里")
	}
}

// 颜色只收 #rrggbb：这个值要同时喂给 <input type="color"> 和 CSS 变量，
// 形状不统一就会一半能用一半不能用。
func TestGroupValidateColor(t *testing.T) {
	base := component.Group{ID: "g", Name: "g", Window: component.DefaultWindowSpec()}

	for _, ok := range []string{"", "#c08a1e", "#FFFFFF"} {
		g := base
		g.Color = ok
		if err := g.Validate(); err != nil {
			t.Errorf("%q 应合法: %v", ok, err)
		}
	}
	for _, bad := range []string{"c08a1e", "#fff", "#c08a1e00", "red", "rgb(1,2,3)"} {
		g := base
		g.Color = bad
		if err := g.Validate(); err == nil {
			t.Errorf("%q 应被拒绝", bad)
		}
	}
}

func TestValidateID(t *testing.T) {
	ok := []string{"builtin.note", "builtin.group.desktop", "vendor_sidebar", "a", "x-1.2"}
	for _, id := range ok {
		if err := component.ValidateID(id); err != nil {
			t.Errorf("%q 应合法: %v", id, err)
		}
	}
	bad := []string{"", "../etc", "a/b", ".hidden", "带中文", string(make([]byte, 70))}
	for _, id := range bad {
		if err := component.ValidateID(id); err == nil {
			t.Errorf("%q 应被拒绝", id)
		}
	}
}

// entry 会被拼进虚拟主机目录，必须挡住路径穿越。
func TestDefinitionValidateRejectsTraversal(t *testing.T) {
	base := component.Definition{ID: "a", Name: "a", Entry: "index.html"}
	if err := base.Validate(); err != nil {
		t.Fatalf("基准清单应通过: %v", err)
	}

	for _, entry := range []string{"../secret.html", "/abs.html", `C:\x.html`, "a/../../b.html"} {
		d := base
		d.Entry = entry
		if err := d.Validate(); err == nil {
			t.Errorf("entry=%q 应被拒绝", entry)
		}
	}
}
