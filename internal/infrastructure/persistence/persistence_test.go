package persistence_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"stickynote/internal/domain/component"
	"stickynote/internal/infrastructure/persistence"
)

func TestLayoutRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "layout.json")
	repo := persistence.NewLayout(path)

	// 文件不存在时应返回空清单而不是报错。
	got, err := repo.Load(ctx)
	if err != nil {
		t.Fatalf("空文件读取失败: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("应返回空清单，实际 %d 条", len(got))
	}

	want := []component.Instance{{
		ID:          "builtin.note-1",
		ComponentID: "builtin.note",
		Bounds:      component.Rect{X: 100, Y: 200, Width: 420, Height: 360},
		// 内容缩放必须一起往返：它和几何是两件事，丢了这一项
		// 用户重启后会发现"字又变回原来那么大"。
		Zoom:       1.25,
		Positioned: true,
		Visible:    true,
	}}
	if err := repo.Save(ctx, want); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	got, err = repo.Load(ctx)
	if err != nil {
		t.Fatalf("重新读取失败: %v", err)
	}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("往返不一致: %+v", got)
	}
}

// 组是内核运行时数据，不是插件：它单独一个文件，往返必须完全一致。
func TestGroupsRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := persistence.NewGroups(filepath.Join(t.TempDir(), "groups.json"))

	got, err := repo.Load(ctx)
	if err != nil {
		t.Fatalf("空文件读取失败: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("应返回空清单，实际 %d 条", len(got))
	}

	radius := 14
	want := []component.Group{
		{
			ID:   "builtin.group.desktop",
			Name: "桌面层",
			Window: component.WindowSpec{
				Layer:        component.LayerDesktop,
				ClickThrough: true,
				Resizable:    false,
				SizeMode:     component.SizeFixed,
				Titlebar:     false,
				Dock:         component.DockFree,
			},
			Visual:     component.Visual{Radius: &radius},
			CSSVars:    map[string]string{"--sn-ink": "#fff"},
			Components: []string{"clock"},
		},
		{ID: "builtin.group.default", Name: "常规", Window: component.DefaultWindowSpec()},
	}
	if err := repo.Save(ctx, want); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	got, err = repo.Load(ctx)
	if err != nil {
		t.Fatalf("重新读取失败: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("条数不一致: %d", len(got))
	}
	if got[0].ID != want[0].ID || got[0].Name != want[0].Name {
		t.Fatalf("标识不一致: %+v", got[0])
	}
	if got[0].Window != want[0].Window {
		t.Fatalf("窗口开关不一致: %+v", got[0].Window)
	}
	if got[0].Visual.Radius == nil || *got[0].Visual.Radius != 14 {
		t.Fatalf("外观没存住: %+v", got[0].Visual)
	}
	if got[0].CSSVars["--sn-ink"] != "#fff" {
		t.Fatalf("CSS 变量没存住: %v", got[0].CSSVars)
	}
	if !got[0].Has("clock") {
		t.Fatalf("成员没存住: %v", got[0].Components)
	}
	// 没设的视觉字段必须仍然是 nil，不能变成"设成 0"——0 是合法取值。
	if got[1].Visual.Padding != nil {
		t.Fatalf("没设的留白被写成了具体值: %+v", got[1].Visual)
	}
}

func TestRegistryRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := persistence.NewRegistry(filepath.Join(t.TempDir(), "components.json"))

	got, err := repo.Load(ctx)
	if err != nil {
		t.Fatalf("空文件读取失败: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("应返回空表，实际 %v", got)
	}

	if err := repo.Save(ctx, map[string]bool{"builtin.note": true, "x.y": false}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	got, err = repo.Load(ctx)
	if err != nil {
		t.Fatalf("重新读取失败: %v", err)
	}
	if !got["builtin.note"] || got["x.y"] {
		t.Fatalf("往返不一致: %v", got)
	}
}

func TestStorageRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := persistence.NewStorage(t.TempDir())

	// 未写过时返回空表。
	all, err := repo.All(ctx, "builtin.note", "builtin.note-1")
	if err != nil {
		t.Fatalf("空存储读取失败: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("应为空，实际 %v", all)
	}

	raw, _ := json.Marshal("第一行\n第二行")
	if err := repo.Set(ctx, "builtin.note", "builtin.note-1", "content", raw); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	all, err = repo.All(ctx, "builtin.note", "builtin.note-1")
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	var content string
	if err := json.Unmarshal(all["content"], &content); err != nil {
		t.Fatalf("值无法还原: %v", err)
	}
	if content != "第一行\n第二行" {
		t.Fatalf("内容不一致: %q", content)
	}

	// 不同实例之间必须隔离。
	other, err := repo.All(ctx, "builtin.note", "builtin.note-2")
	if err != nil {
		t.Fatalf("读取另一实例失败: %v", err)
	}
	if len(other) != 0 {
		t.Fatal("实例之间没有隔离")
	}

	if err := repo.Remove(ctx, "builtin.note", "builtin.note-1", "content"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	all, _ = repo.All(ctx, "builtin.note", "builtin.note-1")
	if len(all) != 0 {
		t.Fatalf("删除后仍能读到: %v", all)
	}
}

// id 会拼进文件路径，必须挡住穿越。
func TestStorageRejectsTraversal(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := persistence.NewStorage(root)

	raw, _ := json.Marshal("x")
	cases := [][2]string{
		{"..", "inst"},
		{"comp", "../../etc"},
		{"comp/evil", "inst"},
		{`comp\evil`, "inst"},
	}
	for _, c := range cases {
		if err := repo.Set(ctx, c[0], c[1], "k", raw); err == nil {
			t.Errorf("id=(%q,%q) 应被拒绝", c[0], c[1])
		}
	}
}

// 变量按 (组件, 实例) 分文件存：同一个组件的两个实例互不干扰。
func TestVarsRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := persistence.NewVars(root)

	// 没写过时返回空表而不是报错——"没填过"是正常状态。
	got, err := repo.Load(ctx, "note", "note-1")
	if err != nil {
		t.Fatalf("空变量读取失败: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("应为空表，实际 %v", got)
	}

	if err := repo.Save(ctx, "note", "note-1", map[string]json.RawMessage{
		"title": json.RawMessage(`"工作"`),
		"body":  json.RawMessage(`"第一行\n第二行"`),
		// json 类型的变量：值本身就是数组，落盘后仍是数组而不是字符串。
		"items": json.RawMessage(`[{"text":"a"},{"text":"b"}]`),
	}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	got, err = repo.Load(ctx, "note", "note-1")
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if string(got["title"]) != `"工作"` || string(got["body"]) != `"第一行\n第二行"` {
		t.Fatalf("往返不一致: %v", got)
	}
	var items []map[string]string
	if err := json.Unmarshal(got["items"], &items); err != nil {
		t.Fatalf("数组没存成数组: %s / %v", got["items"], err)
	}
	if len(items) != 2 || items[1]["text"] != "b" {
		t.Fatalf("数组内容不一致: %v", items)
	}

	// 同组件的另一个实例必须是空的。
	other, err := repo.Load(ctx, "note", "note-2")
	if err != nil {
		t.Fatalf("读取另一实例失败: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("实例之间没有隔离: %v", other)
	}

	// 空表等于删文件：变量全被改回默认值之后，磁盘上不该留一个空壳。
	if err := repo.Save(ctx, "note", "note-1", nil); err != nil {
		t.Fatalf("清空失败: %v", err)
	}
	got, _ = repo.Load(ctx, "note", "note-1")
	if len(got) != 0 {
		t.Fatalf("清空后仍能读到: %v", got)
	}

	// Remove 是幂等的：文件已经没了也不该报错。
	if err := repo.Remove(ctx, "note", "note-1"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if err := repo.Remove(ctx, "note", "note-1"); err != nil {
		t.Fatalf("重复删除应幂等: %v", err)
	}
}

// id 会拼进文件路径，必须挡住穿越（和 storage 同一套规则）。
func TestVarsRejectsTraversal(t *testing.T) {
	ctx := context.Background()
	repo := persistence.NewVars(t.TempDir())

	cases := [][2]string{
		{"..", "inst"},
		{"comp", "../../etc"},
		{"comp/evil", "inst"},
		{`comp\evil`, "inst"},
		{"", "inst"},
	}
	for _, c := range cases {
		if err := repo.Save(ctx, c[0], c[1], map[string]json.RawMessage{"k": json.RawMessage(`"v"`)}); err == nil {
			t.Errorf("id=(%q,%q) 应被拒绝", c[0], c[1])
		}
		if _, err := repo.Load(ctx, c[0], c[1]); err == nil {
			t.Errorf("Load id=(%q,%q) 应被拒绝", c[0], c[1])
		}
	}
}
