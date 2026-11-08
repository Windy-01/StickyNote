package componentapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"stickynote/internal/application/componentapp"
	"stickynote/internal/domain/component"
)

// ---- 测试替身 -------------------------------------------------------------

type fakeLayout struct{ insts []component.Instance }

func (f *fakeLayout) Load(context.Context) ([]component.Instance, error) { return f.insts, nil }
func (f *fakeLayout) Save(_ context.Context, in []component.Instance) error {
	f.insts = in
	return nil
}

type fakeRegistry struct{ m map[string]bool }

func (f *fakeRegistry) Load(context.Context) (map[string]bool, error) {
	if f.m == nil {
		f.m = map[string]bool{}
	}
	return f.m, nil
}
func (f *fakeRegistry) Save(_ context.Context, m map[string]bool) error { f.m = m; return nil }

// fakeGroups 用内存切片模拟 data/groups.json。
type fakeGroups struct{ list []component.Group }

func (f *fakeGroups) Load(context.Context) ([]component.Group, error) {
	out := make([]component.Group, len(f.list))
	copy(out, f.list)
	return out, nil
}

func (f *fakeGroups) Save(_ context.Context, in []component.Group) error {
	f.list = make([]component.Group, len(in))
	copy(f.list, in)
	return nil
}

// fakeVars 用内存表模拟 data/vars/<组件>/<实例>.json。
type fakeVars struct {
	m map[string]map[string]json.RawMessage
}

func (f *fakeVars) key(comp, inst string) string { return comp + "/" + inst }

func (f *fakeVars) Load(_ context.Context, comp, inst string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	for k, v := range f.m[f.key(comp, inst)] {
		out[k] = v
	}
	return out, nil
}

func (f *fakeVars) Save(_ context.Context, comp, inst string, values map[string]json.RawMessage) error {
	if f.m == nil {
		f.m = map[string]map[string]json.RawMessage{}
	}
	if len(values) == 0 {
		delete(f.m, f.key(comp, inst))
		return nil
	}
	cp := make(map[string]json.RawMessage, len(values))
	for k, v := range values {
		cp[k] = v
	}
	f.m[f.key(comp, inst)] = cp
	return nil
}

func (f *fakeVars) Remove(_ context.Context, comp, inst string) error {
	delete(f.m, f.key(comp, inst))
	return nil
}

type fakeStorage struct{ m map[string]json.RawMessage }

func (f *fakeStorage) key(comp, inst string) string { return comp + "/" + inst }
func (f *fakeStorage) All(_ context.Context, comp, inst string) (map[string]json.RawMessage, error) {
	if f.m == nil {
		f.m = map[string]json.RawMessage{}
	}
	out := map[string]json.RawMessage{}
	prefix := f.key(comp, inst) + "\x00"
	for k, v := range f.m {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			out[k[len(prefix):]] = v
		}
	}
	return out, nil
}
func (f *fakeStorage) Set(_ context.Context, comp, inst, key string, v json.RawMessage) error {
	if f.m == nil {
		f.m = map[string]json.RawMessage{}
	}
	f.m[f.key(comp, inst)+"\x00"+key] = v
	return nil
}
func (f *fakeStorage) Remove(_ context.Context, comp, inst, key string) error {
	delete(f.m, f.key(comp, inst)+"\x00"+key)
	return nil
}

// fakeUI 只记最后一组窗口几何。
type fakeUI struct{ rect component.Rect }

func (f *fakeUI) Load(context.Context) (component.Rect, error) { return f.rect, nil }
func (f *fakeUI) Save(_ context.Context, r component.Rect) error {
	f.rect = r
	return nil
}

// fakePackages 用内存表模拟插件目录，避免测试碰真实文件系统。
type fakePackages struct {
	installed map[string]component.Package // kind+"/"+id -> 包
	uninstall int
}

func (f *fakePackages) key(kind component.PackageKind, id string) string {
	return string(kind) + "/" + id
}

func (f *fakePackages) Inspect(string) (component.Package, error) {
	return component.Package{}, errors.New("测试里不调用 Inspect")
}

func (f *fakePackages) Install(src string) (component.Package, error) {
	if f.installed == nil {
		f.installed = map[string]component.Package{}
	}
	pkg := component.Package{Kind: component.PackageComponent, ID: src, Name: src, Dir: "/plugins/" + src}
	f.installed[f.key(pkg.Kind, pkg.ID)] = pkg
	return pkg, nil
}

func (f *fakePackages) Uninstall(kind component.PackageKind, id string) error {
	f.uninstall++
	delete(f.installed, f.key(kind, id))
	return nil
}

func (f *fakePackages) Dir(kind component.PackageKind, id string) (string, error) {
	return "/plugins/" + string(kind) + "/" + id, nil
}

// ---- 夹具 -----------------------------------------------------------------

// testGroups 是两个形状完全不同的组：一个贴桌面且穿透，一个置顶可缩放。
// 组之间的差别够大，才能验证"换了组窗口形态就跟着变"。
func testGroups() []component.Group {
	return []component.Group{
		{
			ID:   "builtin.group.desktop",
			Name: "桌面层",
			Window: component.WindowSpec{
				Layer:        component.LayerDesktop,
				ClickThrough: true,
				Resizable:    false,
				SizeMode:     component.SizeFixed,
				Titlebar:     false,
			},
		},
		{
			ID:   "builtin.group.topmost",
			Name: "置顶",
			Window: component.WindowSpec{
				Layer:     component.LayerTopmost,
				Resizable: true,
				SizeMode:  component.SizeUser,
				Titlebar:  true,
			},
		},
	}
}

func noteDef() component.Definition {
	return component.Definition{
		ID:            "note",
		Name:          "便签",
		Entry:         "index.html",
		DefaultBounds: component.Size{Width: 420, Height: 360},
		MinBounds:     component.Size{Width: 260, Height: 200},
		SourceDir:     "/plugins/components/note",
	}
}

func clockDef() component.Definition {
	return component.Definition{
		ID:            "clock",
		Name:          "时钟",
		Entry:         "index.html",
		DefaultBounds: component.Size{Width: 240, Height: 120},
		SourceDir:     "/plugins/components/clock",
	}
}

type fixture struct {
	svc      *componentapp.Service
	layout   *fakeLayout
	registry *fakeRegistry
	groups   *fakeGroups
	vars     *fakeVars
	pkgs     *fakePackages
}

func newFixture() *fixture {
	f := &fixture{
		layout:   &fakeLayout{},
		registry: &fakeRegistry{},
		groups:   &fakeGroups{},
		vars:     &fakeVars{},
		pkgs:     &fakePackages{},
	}
	f.svc = componentapp.NewService(f.layout, f.registry, f.groups, f.vars, &fakeStorage{}, &fakeUI{}, f.pkgs)
	// 便签与时钟是预置包：首次出现时默认启用，否则用户第一次启动看到的是空桌面。
	f.svc.SetBootstrapEnabled("note", "clock")
	f.svc.SetDefaultGroups(testGroups(), map[string]string{
		"note":  "builtin.group.topmost",
		"clock": "builtin.group.desktop",
	})
	return f
}

func catalogOf(defs ...component.Definition) componentapp.Catalog {
	return componentapp.Catalog{Components: defs}
}

// ---- 启动 -----------------------------------------------------------------

// 首次启动：生成默认组、按预置归属给组件各建一个实例。
func TestBootSeedsGroupsAndInstances(t *testing.T) {
	ctx := context.Background()
	f := newFixture()

	if err := f.svc.Boot(ctx, catalogOf(noteDef(), clockDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	gs := f.svc.Groups()
	if len(gs) != 2 {
		t.Fatalf("应生成 2 个组，实际 %d", len(gs))
	}

	active := f.svc.ActiveInstances()
	if len(active) != 2 {
		t.Fatalf("每个预置组件应各建 1 个实例，实际 %d", len(active))
	}

	byComponent := map[string]componentapp.InstanceView{}
	for _, v := range active {
		byComponent[v.ComponentID] = v
	}

	note, ok := byComponent["note"]
	if !ok {
		t.Fatal("便签实例没建出来")
	}
	if note.GroupID != "builtin.group.topmost" {
		t.Fatalf("便签应在置顶组，实际 %q", note.GroupID)
	}
	if note.GroupName != "置顶" {
		t.Fatalf("组名没带上，界面会显示成空白: %q", note.GroupName)
	}
	if note.Style.Layer != component.LayerTopmost {
		t.Fatalf("便签窗口应是置顶，实际 %q", note.Style.Layer)
	}
	if note.Bounds.Width != 420 || note.Bounds.Height != 360 {
		t.Fatalf("默认尺寸应来自清单: %+v", note.Bounds)
	}
	if note.Positioned {
		t.Fatal("新实例不应标记为已定位")
	}

	clock := byComponent["clock"]
	if clock.Style.Layer != component.LayerDesktop {
		t.Fatalf("时钟窗口应在桌面层，实际 %q", clock.Style.Layer)
	}
	if !clock.Style.ClickThrough {
		t.Fatal("桌面层组的穿透开关没生效")
	}
	// 穿透窗口不可能被激活——这条由领域层推出来，不该指望用户去勾。
	if clock.Style.Activation != component.NoActivate {
		t.Fatalf("穿透窗口应 noActivate，实际 %q", clock.Style.Activation)
	}

	if len(f.layout.insts) != 2 {
		t.Fatalf("布局应已落盘，实际 %d 条", len(f.layout.insts))
	}
	// 归属是实例的属性，必须真的写进 layout.json，否则每次启动都要重算。
	for _, inst := range f.layout.insts {
		if inst.GroupID == "" {
			t.Fatalf("实例 %s 的归属没落盘", inst.ID)
		}
	}
}

// 预置归属指向的组被删掉时，实例落兜底组——不能因为一条默认偏好
// 就让组件开不出窗口。
func TestBootFallsBackWhenSeedGroupGone(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	// 只留桌面层，seedMember 里的 note→置顶 就指向一个不存在的组。
	f.svc.SetDefaultGroups(testGroups()[:1], map[string]string{
		"note": "builtin.group.topmost",
	})

	if err := f.svc.Boot(ctx, catalogOf(noteDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	active := f.svc.ActiveInstances()
	if len(active) != 1 {
		t.Fatalf("兜底组里该有一个实例，实际 %d", len(active))
	}
	if active[0].GroupID != componentapp.DefaultGroupID {
		t.Fatalf("应落兜底组 %s，实际 %s", componentapp.DefaultGroupID, active[0].GroupID)
	}
	if _, ok := f.svc.Group(componentapp.DefaultGroupID); !ok {
		t.Fatal("兜底组应被补出来")
	}
}

// 老数据把归属记在组上（Group.Components）。启动时要搬到实例上，
// 然后把组上那份清掉——两处都留着迟早会打架。
func TestBootMigratesLegacyGroupMembers(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	f.groups.list = []component.Group{{
		ID:         "builtin.group.topmost",
		Name:       "置顶",
		Window:     component.WindowSpec{Layer: component.LayerTopmost},
		Components: []string{"note", "已经卸载了"},
	}}
	f.layout.insts = []component.Instance{
		{ID: "note-1", ComponentID: "note", Visible: true}, // 老数据：没有 GroupID
	}

	if err := f.svc.Boot(ctx, catalogOf(noteDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	v, ok := f.svc.Instance("note-1")
	if !ok {
		t.Fatal("实例不该被丢掉")
	}
	if v.GroupID != "builtin.group.topmost" {
		t.Fatalf("老归属应迁到实例上，实际 %q", v.GroupID)
	}

	// 组上那份要清掉；已卸载的组件本来就不该留着。
	if len(f.groups.list) != 1 || len(f.groups.list[0].Components) != 0 {
		t.Fatalf("迁移后组上不该还留着成员: %+v", f.groups.list)
	}
	if f.layout.insts[0].GroupID != "builtin.group.topmost" {
		t.Fatalf("归属没落盘: %+v", f.layout.insts[0])
	}
}

// 同一个组件在老的组清单里出现两次时，先出现的那个赢。
func TestBootDedupesLegacyMembers(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	f.groups.list = []component.Group{
		{ID: "builtin.group.topmost", Name: "甲", Components: []string{"note"}},
		{ID: "builtin.group.desktop", Name: "乙", Components: []string{"note"}},
	}
	f.layout.insts = []component.Instance{{ID: "note-1", ComponentID: "note", Visible: true}}

	if err := f.svc.Boot(ctx, catalogOf(noteDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	v, _ := f.svc.Instance("note-1")
	if v.GroupID != "builtin.group.topmost" {
		t.Fatalf("先出现的组应赢，实际 %q", v.GroupID)
	}
	if got := f.svc.InstanceIDsInGroup("builtin.group.desktop"); len(got) != 0 {
		t.Fatalf("后出现的组不该有实例，实际 %v", got)
	}
}

// 实例指向一个已经不存在的组（用户删了组、手改了 layout.json）时，
// 落回兜底组，而不是让窗口凭空消失。
func TestBootRescuesInstanceWithMissingGroup(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	f.groups.list = testGroups()
	f.layout.insts = []component.Instance{
		{ID: "note-1", ComponentID: "note", GroupID: "已经删掉的组", Visible: true},
	}

	if err := f.svc.Boot(ctx, catalogOf(noteDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	v, ok := f.svc.Instance("note-1")
	if !ok {
		t.Fatal("实例不该被丢掉")
	}
	if v.GroupID != componentapp.DefaultGroupID {
		t.Fatalf("应改放兜底组，实际 %q", v.GroupID)
	}
}

// ---- 组的增删改 -----------------------------------------------------------

func TestCreateAndDeleteGroup(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(noteDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	before := len(f.svc.Groups())
	g, err := f.svc.CreateGroup(ctx, "我的组")
	if err != nil {
		t.Fatalf("建组失败: %v", err)
	}
	if g.Name != "我的组" {
		t.Fatalf("名字不对: %q", g.Name)
	}
	if len(f.svc.Groups()) != before+1 {
		t.Fatal("新组应出现在列表里")
	}
	// 新建组默认是「常规」层级：用户新建一个组，最不容易被吓到的是它跟普通窗口一样。
	if g.Window.Layer != component.LayerNormal {
		t.Fatalf("新组默认层级应是 normal，实际 %q", g.Window.Layer)
	}

	if err := f.svc.DeleteGroup(ctx, g.ID); err != nil {
		t.Fatalf("删组失败: %v", err)
	}
	if len(f.svc.Groups()) != before {
		t.Fatal("删掉后应回到原来的数量")
	}
}

// 重名自动加序号，不能出现两个同名组。
func TestCreateGroupUniquifiesName(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(noteDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	a, _ := f.svc.CreateGroup(ctx, "浮层")
	b, _ := f.svc.CreateGroup(ctx, "浮层")
	if a.Name == b.Name {
		t.Fatalf("重名应被区分: %q / %q", a.Name, b.Name)
	}
}

// 组里还有实例时不允许删除：删了那些窗口就无处可去。
func TestDeleteGroupBlockedByInstances(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(noteDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	if err := f.svc.DeleteGroup(ctx, "builtin.group.topmost"); err == nil {
		t.Fatal("有实例的组应当拒绝删除")
	}
	if _, ok := f.svc.Group("builtin.group.topmost"); !ok {
		t.Fatal("被拒绝时组不该消失")
	}
}

// 改组设置不能顺手把归属改掉：归属只走 SetInstanceGroup。
func TestUpdateGroupKeepsInstanceOwnership(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(noteDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	g, _ := f.svc.Group("builtin.group.topmost")
	if err := f.svc.UpdateGroup(ctx, component.Group{
		ID:     g.ID,
		Name:   "改过名的组",
		Window: component.WindowSpec{Layer: component.LayerNormal},
		Visual: g.Visual,
	}); err != nil {
		t.Fatalf("更新失败: %v", err)
	}

	after, _ := f.svc.Group("builtin.group.topmost")
	if after.Name != "改过名的组" {
		t.Fatalf("名字没改到: %q", after.Name)
	}
	if after.Window.Layer != component.LayerNormal {
		t.Fatalf("层级没改到: %q", after.Window.Layer)
	}
	// 成员是实例，不该被组设置的覆盖动作碰到。
	if got := f.svc.InstanceIDsInGroup("builtin.group.topmost"); len(got) != 1 || got[0] != "note-1" {
		t.Fatalf("实例归属不该被组设置改动，实际 %v", got)
	}
}

// 换组 = 换窗口形态：实例的解析结果必须立刻跟着变。
func TestSetInstanceGroupChangesResolvedStyle(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(noteDef(), clockDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	if err := f.svc.SetInstanceGroup(ctx, "note-1", "builtin.group.desktop"); err != nil {
		t.Fatalf("换组失败: %v", err)
	}

	v, ok := f.svc.Instance("note-1")
	if !ok {
		t.Fatal("便签实例不见了")
	}
	if v.GroupID != "builtin.group.desktop" {
		t.Fatalf("组没换过去: %q", v.GroupID)
	}
	if v.Style.Layer != component.LayerDesktop {
		t.Fatalf("窗口形态应跟着换，实际 %q", v.Style.Layer)
	}
	if !v.Style.ClickThrough {
		t.Fatal("新组的穿透开关没生效")
	}

	// 老组里不能再留着它。
	for _, id := range f.svc.InstanceIDsInGroup("builtin.group.topmost") {
		if id == "note-1" {
			t.Fatal("换组后老组里不该还有它")
		}
	}
}

// 内容缩放是实例级的，而且和几何（Bounds）完全无关：
// 改缩放不能顺带把窗口尺寸改掉，反之亦然。
func TestSetZoomDoesNotTouchBounds(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(noteDef(), clockDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	before, _ := f.svc.Instance("note-1")
	if err := f.svc.SetZoom(ctx, "note-1", 1.5); err != nil {
		t.Fatalf("设置缩放失败: %v", err)
	}
	after, ok := f.svc.Instance("note-1")
	if !ok {
		t.Fatal("便签实例不见了")
	}
	if after.Zoom != 1.5 {
		t.Fatalf("缩放应该是 1.5，实际 %v", after.Zoom)
	}
	if after.Bounds != before.Bounds {
		t.Fatalf("改缩放不该动几何：%+v → %+v", before.Bounds, after.Bounds)
	}
}

// 越界的缩放由领域层夹住，服务层不重复判断——调用方给什么都不会把数据写坏。
func TestSetZoomClamps(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(noteDef(), clockDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	if err := f.svc.SetZoom(ctx, "note-1", 99); err != nil {
		t.Fatalf("设置缩放失败: %v", err)
	}
	if v, _ := f.svc.Instance("note-1"); v.Zoom != component.MaxZoom {
		t.Fatalf("超上限应夹到 %v，实际 %v", component.MaxZoom, v.Zoom)
	}

	if err := f.svc.SetZoom(ctx, "note-1", 0); err != nil {
		t.Fatalf("设置缩放失败: %v", err)
	}
	if v, _ := f.svc.Instance("note-1"); v.Zoom != component.DefaultZoom {
		t.Fatalf("0 应当按「没设置」回到 %v，实际 %v", component.DefaultZoom, v.Zoom)
	}

	if err := f.svc.SetZoom(ctx, "note-1", -1); err != nil {
		t.Fatalf("设置缩放失败: %v", err)
	}
	if v, _ := f.svc.Instance("note-1"); v.Zoom != component.DefaultZoom {
		t.Fatalf("负数应当按「没设置」回到 %v，实际 %v", component.DefaultZoom, v.Zoom)
	}

	if err := f.svc.SetZoom(ctx, "不存在", 1.2); err == nil {
		t.Fatal("不存在的实例应该报错")
	}
}

// 同一个组件的两个实例可以分属不同的组：这正是"归属下移到实例"的理由。
func TestSameComponentTwoInstancesTwoGroups(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(noteDef(), clockDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	second, err := f.svc.CreateInstance(ctx, "note", "builtin.group.desktop")
	if err != nil {
		t.Fatalf("在桌面层组建实例失败: %v", err)
	}
	if second.GroupID != "builtin.group.desktop" {
		t.Fatalf("新实例应在桌面层组，实际 %q", second.GroupID)
	}

	// 两个实例各按自己的组解析外观。
	top, _ := f.svc.Instance("note-1")
	if top.Style.Layer != component.LayerTopmost {
		t.Fatalf("note-1 应仍是置顶，实际 %q", top.Style.Layer)
	}
	desk, _ := f.svc.Instance(second.ID)
	if desk.Style.Layer != component.LayerDesktop {
		t.Fatalf("%s 应是桌面层，实际 %q", second.ID, desk.Style.Layer)
	}
	if got := f.svc.InstanceIDsInGroup("builtin.group.desktop"); len(got) != 2 || got[0] != "clock-1" || got[1] != second.ID {
		t.Fatalf("桌面层组应是 clock-1 与新实例，实际 %v", got)
	}
}

// 不给组时落兜底组；给了不存在的组要报错，不能悄悄建成一个没归属的实例。
func TestCreateInstanceGroupRules(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(noteDef(), clockDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	v, err := f.svc.CreateInstance(ctx, "note", "")
	if err != nil {
		t.Fatalf("建实例失败: %v", err)
	}
	if v.GroupID != componentapp.DefaultGroupID {
		t.Fatalf("不给组应落兜底组，实际 %q", v.GroupID)
	}

	if _, err := f.svc.CreateInstance(ctx, "note", "不存在"); !errors.Is(err, component.ErrGroupNotFound) {
		t.Fatalf("应返回 ErrGroupNotFound，实际 %v", err)
	}
	if _, err := f.svc.CreateInstance(ctx, "不存在", ""); !errors.Is(err, component.ErrComponentNotFound) {
		t.Fatalf("应返回 ErrComponentNotFound，实际 %v", err)
	}
}

func TestSetInstanceGroupRejectsUnknown(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(noteDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	if err := f.svc.SetInstanceGroup(ctx, "note-1", "不存在"); !errors.Is(err, component.ErrGroupNotFound) {
		t.Fatalf("应返回 ErrGroupNotFound，实际 %v", err)
	}
	if err := f.svc.SetInstanceGroup(ctx, "不存在", "builtin.group.topmost"); !errors.Is(err, component.ErrInstanceNotFound) {
		t.Fatalf("应返回 ErrInstanceNotFound，实际 %v", err)
	}
}

// ---- 实例与启停 -----------------------------------------------------------

// 停用组件后实例记录仍在（数据不丢），但不再激活。
func TestDisableKeepsInstanceRecord(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(noteDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	if err := f.svc.SetEnabled(ctx, "note", false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	if len(f.svc.ActiveInstances()) != 0 {
		t.Fatal("停用后不应有活动实例")
	}
	if len(f.layout.insts) != 1 {
		t.Fatal("实例记录不该被删除")
	}

	if err := f.svc.SetEnabled(ctx, "note", true); err != nil {
		t.Fatalf("启用失败: %v", err)
	}
	if len(f.svc.ActiveInstances()) != 1 {
		t.Fatal("重新启用后应恢复")
	}
}

// 存储按实例隔离，跨实例读不到对方的数据。
func TestStorageIsolation(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(noteDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	second, err := f.svc.CreateInstance(ctx, "note", "builtin.group.topmost")
	if err != nil {
		t.Fatalf("建第二个实例失败: %v", err)
	}

	raw, _ := json.Marshal("只在第一个里")
	if err := f.svc.SetStorage(ctx, "note-1", "content", raw); err != nil {
		t.Fatalf("写存储失败: %v", err)
	}

	first, err := f.svc.Storage(ctx, "note-1")
	if err != nil {
		t.Fatalf("读存储失败: %v", err)
	}
	if string(first["content"]) != `"只在第一个里"` {
		t.Fatalf("第一个实例读不到自己的数据: %v", first)
	}

	other, err := f.svc.Storage(ctx, second.ID)
	if err != nil {
		t.Fatalf("读第二个存储失败: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("第二个实例不该看到别人的数据: %v", other)
	}
}

// 单例组件不允许再建第二个实例：直接拒绝，而不是悄悄返回已有的那个——
// 用户点了「新建实例」却什么都没发生，比报一句"这是单例"更让人困惑。
func TestSingletonRefusesSecondInstance(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	def := noteDef()
	def.Singleton = true
	if err := f.svc.Boot(ctx, catalogOf(def)); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	if _, err := f.svc.CreateInstance(ctx, "note", "builtin.group.topmost"); err == nil {
		t.Fatal("单例不应允许建第二个实例")
	}
	if len(f.svc.ActiveInstances()) != 1 {
		t.Fatalf("不该出现第二个实例，实际 %d", len(f.svc.ActiveInstances()))
	}
}

// 停用组件后 AllInstances 仍能看到实例，只是不再激活。
// 管理界面靠这一点显示"有 N 个实例在等着"。
func TestAllInstancesIncludesDisabled(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(noteDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	if err := f.svc.SetEnabled(ctx, "note", false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}

	if len(f.svc.ActiveInstances()) != 0 {
		t.Fatal("停用后不应有活动实例")
	}
	all := f.svc.AllInstances()
	if len(all) != 1 {
		t.Fatalf("AllInstances 应仍返回 1 个，实际 %d", len(all))
	}
	if all[0].ID != "note-1" {
		t.Fatalf("实例 id 不对: %q", all[0].ID)
	}
}

// ---- 重扫目录 -------------------------------------------------------------

// 重扫时若某组件还有实例却从目录里消失了，必须拒绝。
// 否则实例就失去了归属：数据还在，但界面上再也找不到它。
func TestReloadRefusesOrphanInstances(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(noteDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	if err := f.svc.Reload(ctx, catalogOf()); err == nil {
		t.Fatal("有实例的组件消失时，Reload 应当报错")
	}
	if _, ok := f.svc.Component("note"); !ok {
		t.Fatal("Reload 失败后目录不应被改动")
	}
}

// 重扫能带进新组件：第三方默认停用，不会凭空多出窗口。
func TestReloadAddsComponentDisabled(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(noteDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	// 换成一个真正的第三方 id：clock 在夹具里是预置包，会被 bootstrap 打开。
	extra := clockDef()
	extra.ID = "vendor.clock"
	extra.Name = "第三方时钟"
	if err := f.svc.Reload(ctx, catalogOf(noteDef(), extra)); err != nil {
		t.Fatalf("Reload 失败: %v", err)
	}

	cv, ok := f.svc.Component("vendor.clock")
	if !ok {
		t.Fatal("新组件应出现在目录里")
	}
	if cv.Enabled {
		t.Fatal("第三方组件应默认停用")
	}
	// 组件本身没有组：用户从组里挑它建实例时才会决定它落在哪儿。
	if len(cv.InstanceIDs) != 0 {
		t.Fatalf("新组件不该自动开窗口，实际 %v", cv.InstanceIDs)
	}
	if len(f.svc.ActiveInstances()) != 1 {
		t.Fatalf("原有实例不该受影响，实际 %d 个", len(f.svc.ActiveInstances()))
	}
}

// ---- 卸载 -----------------------------------------------------------------

// 组件还有实例时不允许卸载。
func TestUninstallBlockedByInstances(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(noteDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	err := f.svc.UninstallPackage(ctx, component.PackageComponent, "note")
	if err == nil {
		t.Fatal("还有实例时应当拒绝卸载")
	}
	if f.pkgs.uninstall != 0 {
		t.Fatal("被拒绝时不该真的去删文件")
	}
}

// ---- 组颜色 ---------------------------------------------------------------

// 每个组都要有识别色，而且彼此不撞：布局板和侧边栏全靠它区分组。
func TestBootAssignsDistinctGroupColors(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(noteDef(), clockDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	seen := map[string]bool{}
	for _, g := range f.svc.Groups() {
		if g.Color == "" {
			t.Fatalf("组 %s 没分到颜色", g.ID)
		}
		if seen[g.Color] {
			t.Fatalf("颜色 %s 被两个组同时占用", g.Color)
		}
		seen[g.Color] = true
	}

	// 颜色要真的落盘，下次启动不能重算。
	if len(f.groups.list) == 0 || f.groups.list[0].Color == "" {
		t.Fatal("颜色应写回 groups.json")
	}
}

// 新建组拿一个当前用得最少的颜色，不能和现有组撞。
func TestCreateGroupGetsUnusedColor(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(noteDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	used := map[string]bool{}
	for _, g := range f.svc.Groups() {
		used[g.Color] = true
	}
	g, err := f.svc.CreateGroup(ctx, "再来一个")
	if err != nil {
		t.Fatalf("建组失败: %v", err)
	}
	if g.Color == "" {
		t.Fatal("新组应带颜色")
	}
	if used[g.Color] {
		t.Fatalf("新组颜色 %s 和现有组撞了", g.Color)
	}
}

// 界面把颜色清空时保留原色：没有"不设颜色"这个状态，
// 真留空会让那个组的方块在布局板上失去归属。
func TestUpdateGroupKeepsColorWhenCleared(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(noteDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	before, _ := f.svc.Group("builtin.group.topmost")
	if before.Color == "" {
		t.Fatal("基准组应有颜色")
	}
	if err := f.svc.UpdateGroup(ctx, component.Group{
		ID:     before.ID,
		Name:   before.Name,
		Color:  "",
		Window: before.Window,
	}); err != nil {
		t.Fatalf("更新失败: %v", err)
	}

	after, _ := f.svc.Group("builtin.group.topmost")
	if after.Color != before.Color {
		t.Fatalf("颜色被清掉了: %q → %q", before.Color, after.Color)
	}
}

// 用户改颜色要能存住。
func TestUpdateGroupChangesColor(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(noteDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	before, _ := f.svc.Group("builtin.group.topmost")
	if err := f.svc.UpdateGroup(ctx, component.Group{
		ID:     before.ID,
		Name:   before.Name,
		Color:  "#123456",
		Window: before.Window,
	}); err != nil {
		t.Fatalf("更新失败: %v", err)
	}

	after, _ := f.svc.Group("builtin.group.topmost")
	if after.Color != "#123456" {
		t.Fatalf("颜色没改到: %q", after.Color)
	}
}

// ---- 预置包首次发放 -------------------------------------------------------

// 预置插件首次发放时不该让用户面对空桌面。
func TestBootstrapComponentEnabledOnFirstSight(t *testing.T) {
	ctx := context.Background()
	f := newFixture()

	if err := f.svc.Boot(ctx, catalogOf(noteDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	cv, ok := f.svc.Component("note")
	if !ok {
		t.Fatal("组件应在目录里")
	}
	if !cv.Enabled {
		t.Fatal("首次发放的预置组件应默认启用")
	}
	if got := f.svc.ActiveInstances(); len(got) != 1 || got[0].ID != "note-1" {
		t.Fatalf("应自动建实例 note-1，实际 %+v", got)
	}
}

// 用户把它关掉之后，bootstrap 不该把它重新打开——那等于用户关不掉。
func TestBootstrapDoesNotOverrideUserChoice(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	f.registry.m = map[string]bool{"note": false}

	if err := f.svc.Boot(ctx, catalogOf(noteDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	if cv, _ := f.svc.Component("note"); cv.Enabled {
		t.Fatal("注册表里记着停用，就不该被 bootstrap 打开")
	}
	if n := len(f.svc.ActiveInstances()); n != 0 {
		t.Fatalf("停用的组件不该有活动实例，实际 %d 个", n)
	}
}

// 没有声明 bootstrap 的第三方组件仍然默认停用，不能被顺手打开。
func TestNonBootstrapPluginStaysDisabled(t *testing.T) {
	ctx := context.Background()
	f := newFixture()

	third := clockDef()
	third.ID = "vendor.clock"
	if err := f.svc.Boot(ctx, catalogOf(third)); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	if cv, _ := f.svc.Component("vendor.clock"); cv.Enabled {
		t.Fatal("未声明 bootstrap 的插件应默认停用")
	}
}

// ---- 变量 -----------------------------------------------------------------

// varDef 是一个声明了五种变量的组件：单行、长消息、选项、列表、记录表。
func varDef() component.Definition {
	d := noteDef()
	d.Variables = []component.Variable{
		{Key: "title", Label: "标题", Type: component.VarText, Default: json.RawMessage(`"便签"`)},
		{Key: "body", Label: "正文", Type: component.VarTextarea},
		{
			Key: "size", Label: "字号", Type: component.VarSelect, Default: json.RawMessage(`"m"`),
			Options: []component.VarOption{{Value: "s", Label: "小"}, {Value: "l", Label: "大"}},
		},
		// 列表式变量：界面上"一行一条"，落盘是 `[{"text":"…"}]`。
		{
			Key: "items", Label: "条目", Type: component.VarList,
			Default: json.RawMessage(`[{"text":"a"}]`),
		},
		// 记录表：一条记录几个字段，落盘同样是对象数组。
		{
			Key: "cards", Label: "卡片", Type: component.VarGroup,
			Fields: []component.VarField{
				{Key: "title", Type: component.VarText},
				{Key: "order", Type: component.VarNumber},
			},
			Default: json.RawMessage(`[{"title":"第一张","order":1}]`),
		},
	}
	return d.Normalize()
}

// varText 把变量值当字符串读出来，省得每处都 json.Unmarshal。
func varText(t *testing.T, m map[string]json.RawMessage, key string) string {
	t.Helper()
	var s string
	if raw, ok := m[key]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("变量 %s 不是字符串: %s", key, raw)
		}
	}
	return s
}

// 没填过时，快照里每个声明都有一项，取的是默认值。
func TestVarsSnapshotFillsDefaults(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(varDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	got := f.svc.AllInstances()[0].Vars
	if len(got) != 5 {
		t.Fatalf("应有 5 个变量，实际 %d: %+v", len(got), got)
	}
	if varText(t, got, "title") != "便签" || varText(t, got, "size") != "m" || varText(t, got, "body") != "" {
		t.Fatalf("默认值不对: %+v", got)
	}
	// 列表变量的默认值保持数组形状，不会被转成字符串。
	if !component.SameJSON(got["items"], json.RawMessage(`[{"text":"a"}]`)) {
		t.Fatalf("列表默认值形状变了: %s", got["items"])
	}
}

// 每个实例各存一份：同一组件的两个实例可以填不同的值。
func TestVarsArePerInstance(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(varDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	second, err := f.svc.CreateInstance(ctx, "note", "builtin.group.desktop")
	if err != nil {
		t.Fatalf("建第二个实例失败: %v", err)
	}

	if err := f.svc.SetVars(ctx, "note-1", map[string]string{"title": "工作", "size": "l"}); err != nil {
		t.Fatalf("设置变量失败: %v", err)
	}
	if err := f.svc.SetVars(ctx, second.ID, map[string]string{"title": "生活"}); err != nil {
		t.Fatalf("设置变量失败: %v", err)
	}

	first := f.svc.AllInstances()[0]
	other := f.svc.AllInstances()[1]
	if varText(t, first.Vars, "title") != "工作" || varText(t, first.Vars, "size") != "l" {
		t.Fatalf("第一个实例的值不对: %+v", first.Vars)
	}
	if varText(t, other.Vars, "title") != "生活" {
		t.Fatalf("第二个实例的值不对: %+v", other.Vars)
	}
	// 没动过的变量仍然跟着默认值走。
	if varText(t, other.Vars, "size") != "m" {
		t.Fatalf("没设过的变量应是默认值，实际 %q", varText(t, other.Vars, "size"))
	}
}

// 列表式变量：用户填的是"一行一条"的文本，落盘是对象数组。
func TestSetVarsStoresList(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(varDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	src := "第一条\n\n  第二条  \n第三条"
	if err := f.svc.SetVars(ctx, "note-1", map[string]string{"items": src}); err != nil {
		t.Fatalf("设置变量失败: %v", err)
	}

	// 存盘的是对象数组；空行被丢掉、首尾空白被去掉。
	if raw := f.vars.m["note/note-1"]["items"]; !component.SameJSON(raw,
		json.RawMessage(`[{"text":"第一条"},{"text":"第二条"},{"text":"第三条"}]`)) {
		t.Fatalf("落盘形状不对: %s", raw)
	}

	// 快照里也是数组，不是字符串——组件那边 `Array.isArray` 要成立。
	var items []map[string]string
	if err := json.Unmarshal(f.svc.AllInstances()[0].Vars["items"], &items); err != nil {
		t.Fatalf("快照里的值不是数组: %v", err)
	}
	if len(items) != 3 || items[0]["text"] != "第一条" || items[2]["text"] != "第三条" {
		t.Fatalf("数组内容不对: %v", items)
	}
}

// 用户在文本框里敲什么都是合法输入：一行里出现 `[{` 也只是那一行的文字，
// 不会被当成"JSON 写错了"。这条是"不让用户填 JSON"的直接后果。
func TestSetVarsTreatsBracketsAsText(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(varDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	if err := f.svc.SetVars(ctx, "note-1", map[string]string{"items": `[{"text":"a"},`}); err != nil {
		t.Fatalf("方括号应当被当成普通文字，实际报错: %v", err)
	}
	if raw := f.vars.m["note/note-1"]["items"]; !component.SameJSON(raw,
		json.RawMessage(`[{"text":"[{\"text\":\"a\"},"}]`)) {
		t.Fatalf("应原样存成一行文字: %s", raw)
	}
}

// 全空 = 清空这一项，回到默认值（key 从文件里删掉）。
func TestSetVarsClearsListWhenEmpty(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(varDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	if err := f.svc.SetVars(ctx, "note-1", map[string]string{"items": "甲\n乙"}); err != nil {
		t.Fatalf("设置变量失败: %v", err)
	}
	if _, ok := f.vars.m["note/note-1"]["items"]; !ok {
		t.Fatal("应先存进去")
	}
	if err := f.svc.SetVars(ctx, "note-1", map[string]string{"items": "  \n\t\n"}); err != nil {
		t.Fatalf("清空失败: %v", err)
	}
	if _, ok := f.vars.m["note/note-1"]["items"]; ok {
		t.Fatalf("清空后不该留记录: %+v", f.vars.m["note/note-1"])
	}
	// 快照退回默认值。
	if !component.SameJSON(f.svc.AllInstances()[0].Vars["items"], json.RawMessage(`[{"text":"a"}]`)) {
		t.Fatalf("清空后应退回默认值: %s", f.svc.AllInstances()[0].Vars["items"])
	}
}

// 与默认值相同的值不落盘：作者日后改默认值，用户没动过的变量自动跟着变。
func TestVarsSkipValuesEqualToDefault(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(varDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	if err := f.svc.SetVars(ctx, "note-1", map[string]string{"title": "便签"}); err != nil {
		t.Fatalf("设置变量失败: %v", err)
	}
	if n := len(f.vars.m["note/note-1"]); n != 0 {
		t.Fatalf("等于默认值的值不该落盘，实际存了 %d 项: %+v", n, f.vars.m["note/note-1"])
	}

	// 改回默认值等于把这条擦掉。
	if err := f.svc.SetVars(ctx, "note-1", map[string]string{"title": "工作"}); err != nil {
		t.Fatalf("设置变量失败: %v", err)
	}
	if err := f.svc.SetVars(ctx, "note-1", map[string]string{"title": "便签"}); err != nil {
		t.Fatalf("设置变量失败: %v", err)
	}
	if n := len(f.vars.m["note/note-1"]); n != 0 {
		t.Fatalf("改回默认值应把记录擦掉，实际还剩 %+v", f.vars.m["note/note-1"])
	}

	// 列表变量同理：把默认值原样填一遍（多敲了空行、多带了空格）也算"没改"。
	if err := f.svc.SetVars(ctx, "note-1", map[string]string{"items": "  a \n\n"}); err != nil {
		t.Fatalf("设置变量失败: %v", err)
	}
	if n := len(f.vars.m["note/note-1"]); n != 0 {
		t.Fatalf("与默认值等价的列表不该落盘，实际 %+v", f.vars.m["note/note-1"])
	}
}

// 选项模式只接受声明过的选项；写错要当场报错，不能静默存下来。
func TestSetVarsRejectsValueOutsideOptions(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(varDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	err := f.svc.SetVars(ctx, "note-1", map[string]string{"size": "xl"})
	if err == nil || !strings.Contains(err.Error(), "不在可选项里") {
		t.Fatalf("非法选项应报错，实际: %v", err)
	}
	// 报错时不该留下半份数据。
	if n := len(f.vars.m["note/note-1"]); n != 0 {
		t.Fatalf("失败时不该写盘，实际 %+v", f.vars.m["note/note-1"])
	}
}

// 只提交改过的那一个：没提到的变量保持原样，不会被顺手清空。
func TestSetVarsKeepsUntouchedValues(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(varDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	if err := f.svc.SetVars(ctx, "note-1", map[string]string{"title": "工作", "body": "第一行"}); err != nil {
		t.Fatalf("设置变量失败: %v", err)
	}
	if err := f.svc.SetVars(ctx, "note-1", map[string]string{"size": "l"}); err != nil {
		t.Fatalf("设置变量失败: %v", err)
	}

	got := f.svc.AllInstances()[0].Vars
	if varText(t, got, "title") != "工作" || varText(t, got, "body") != "第一行" || varText(t, got, "size") != "l" {
		t.Fatalf("没提到的变量被改了: %+v", got)
	}
}

// 声明里已经没有的 key 要丢掉：留着既没人读，又会在作者重新用同一个
// key 时冒出一个"用户从没填过"的值。
func TestSetVarsDropsUndeclaredKeys(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	f.vars.m = map[string]map[string]json.RawMessage{
		"note/note-1": {
			"title":  json.RawMessage(`"工作"`),
			"oldKey": json.RawMessage(`"旧值"`),
		},
	}
	if err := f.svc.Boot(ctx, catalogOf(varDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	if err := f.svc.SetVars(ctx, "note-1", map[string]string{"size": "l"}); err != nil {
		t.Fatalf("设置变量失败: %v", err)
	}

	got := f.vars.m["note/note-1"]
	if _, ok := got["oldKey"]; ok {
		t.Fatalf("未声明的 key 应被丢掉，实际 %+v", got)
	}
	if varText(t, got, "title") != "工作" {
		t.Fatalf("已声明的旧值应保留，实际 %+v", got)
	}
}

// 删实例时变量跟着走：窗口没了，留着配置只会在下次装回来时塞给新实例。
func TestRemoveInstanceDropsVars(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(varDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	if err := f.svc.SetVars(ctx, "note-1", map[string]string{"title": "工作"}); err != nil {
		t.Fatalf("设置变量失败: %v", err)
	}
	if err := f.svc.RemoveInstance(ctx, "note-1"); err != nil {
		t.Fatalf("删实例失败: %v", err)
	}
	if _, ok := f.vars.m["note/note-1"]; ok {
		t.Fatal("删实例应连带删掉变量文件")
	}
}

// 变量值在重启后还在：Boot 要把 data/vars 里的东西读回来。
func TestVarsSurviveReboot(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(varDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	if err := f.svc.SetVars(ctx, "note-1", map[string]string{
		"title": "工作",
		"items": "甲\n乙",
	}); err != nil {
		t.Fatalf("设置变量失败: %v", err)
	}

	// 换一个 Service 实例、共用同一份"磁盘"，模拟重启。
	g := &fixture{layout: f.layout, registry: f.registry, groups: f.groups, vars: f.vars, pkgs: f.pkgs}
	g.svc = componentapp.NewService(g.layout, g.registry, g.groups, g.vars, &fakeStorage{}, &fakeUI{}, g.pkgs)
	g.svc.SetBootstrapEnabled("note", "clock")
	g.svc.SetDefaultGroups(testGroups(), map[string]string{"note": "builtin.group.topmost"})
	if err := g.svc.Boot(ctx, catalogOf(varDef())); err != nil {
		t.Fatalf("重启 Boot 失败: %v", err)
	}

	got := g.svc.AllInstances()[0].Vars
	if varText(t, got, "title") != "工作" {
		t.Fatalf("重启后变量丢了: %+v", got)
	}
	var items []map[string]string
	if err := json.Unmarshal(got["items"], &items); err != nil || len(items) != 2 || items[1]["text"] != "乙" {
		t.Fatalf("重启后数组形状丢了: %s / %v", got["items"], err)
	}
}

// 组件抽屉要拿到变量声明，否则界面上画不出表单。
func TestComponentViewCarriesVariables(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(varDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	cv, _ := f.svc.Component("note")
	if len(cv.Variables) != 5 {
		t.Fatalf("组件快照应带 5 个变量声明，实际 %d", len(cv.Variables))
	}
	if cv.Variables[0].Label != "标题" {
		t.Fatalf("声明内容不对: %+v", cv.Variables[0])
	}
	// 声明的默认值要原样带下去：界面靠它决定控件里初始显示什么。
	if !component.SameJSON(cv.Variables[3].Default, json.RawMessage(`[{"text":"a"}]`)) {
		t.Fatalf("列表默认值没带下去: %s", cv.Variables[3].Default)
	}
	// 记录表还要带上字段声明，否则界面画不出卡片里的那几行。
	if len(cv.Variables[4].Fields) != 2 {
		t.Fatalf("记录表应带 2 个字段声明，实际 %d", len(cv.Variables[4].Fields))
	}
}

// 记录表：界面把每张卡片拼成对象、序列化成一整个数组交上来，
// 落盘同样是对象数组，组件那边 `Array.isArray` 成立。
func TestSetVarsStoresRecords(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(varDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	src := `[{"title":" 第一张 ","order":3},{"title":"","order":0}]`
	if err := f.svc.SetVars(ctx, "note-1", map[string]string{"cards": src}); err != nil {
		t.Fatalf("设置变量失败: %v", err)
	}

	want := json.RawMessage(`[{"order":3,"title":"第一张"},{"order":0}]`)
	if raw := f.vars.m["note/note-1"]["cards"]; !component.SameJSON(raw, want) {
		t.Fatalf("落盘形状不对: %s", raw)
	}

	// 快照里是数组，不是字符串。
	var recs []map[string]any
	if err := json.Unmarshal(f.svc.AllInstances()[0].Vars["cards"], &recs); err != nil {
		t.Fatalf("快照里的值不是数组: %v", err)
	}
	if len(recs) != 2 || recs[0]["title"] != "第一张" {
		t.Fatalf("数组内容不对: %v", recs)
	}
	// 数字要真是数字，不是 "3"。
	if n, ok := recs[0]["order"].(float64); !ok || n != 3 {
		t.Fatalf("order 应存成数字: %#v", recs[0]["order"])
	}
}

// 清空记录表 = 把 key 从文件里删掉，默认值重新生效。
func TestSetVarsClearsRecordsWhenEmpty(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.svc.Boot(ctx, catalogOf(varDef())); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	if err := f.svc.SetVars(ctx, "note-1", map[string]string{"cards": `[{"title":"甲"}]`}); err != nil {
		t.Fatalf("设置变量失败: %v", err)
	}
	if err := f.svc.SetVars(ctx, "note-1", map[string]string{"cards": "[]"}); err != nil {
		t.Fatalf("清空失败: %v", err)
	}
	if _, ok := f.vars.m["note/note-1"]["cards"]; ok {
		t.Fatalf("清空后不该留记录: %+v", f.vars.m["note/note-1"])
	}
	if !component.SameJSON(f.svc.AllInstances()[0].Vars["cards"],
		json.RawMessage(`[{"order":1,"title":"第一张"}]`)) {
		t.Fatalf("清空后应退回默认值: %s", f.svc.AllInstances()[0].Vars["cards"])
	}
}
