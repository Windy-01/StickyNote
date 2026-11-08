package persistence_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"stickynote/internal/infrastructure/persistence"
)

// instRef 是迁移测试关心的实例字段：只认 id 与归属，别的一概不解释。
type instRef struct {
	ID          string `json:"id"`
	ComponentID string `json:"componentId"`
}

// layoutOf 读回 layout.json 里的实例清单。
func layoutOf(t *testing.T, root string) []instRef {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "layout.json"))
	if err != nil {
		t.Fatalf("读取 layout.json 失败: %v", err)
	}
	var rec struct {
		Instances []instRef `json:"instances"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("解析 layout.json 失败: %v", err)
	}
	return rec.Instances
}

// enabledOf 读回 components.json 的启用表。
func enabledOf(t *testing.T, root string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "components.json"))
	if err != nil {
		t.Fatalf("读取 components.json 失败: %v", err)
	}
	var rec struct {
		Enabled map[string]bool `json:"enabled"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("解析 components.json 失败: %v", err)
	}
	return rec.Enabled
}

// 便签从内置组件变成插件时改了 id，四处必须一起改：
// 布局里的 componentId、实例 id、注册表的键、私有存储的目录与文件名。
// 漏任何一处都会变成"组件还在、数据找不到"。
func TestMigrateBuiltinNoteRewritesAllFourPlaces(t *testing.T) {
	root := t.TempDir()

	writeFile(t, filepath.Join(root, "layout.json"), `{
  "instances": [
    { "id": "builtin.note-1", "componentId": "builtin.note", "bounds": {"width": 420, "height": 360},
      "visible": true, "pinned": true, "createdAt": "2026-11-01T10:00:00Z" }
  ]
}`)
	writeFile(t, filepath.Join(root, "components.json"),
		`{"enabled":{"builtin.note":true,"vendor.clock":false}}`)
	writeFile(t, filepath.Join(root, "storage", "builtin.note", "builtin.note-1.json"),
		`{"content":"# 123"}`)

	persistence.MigrateBuiltinNote(root)

	// 1) 布局里的 componentId 与 2) 实例 id
	insts := layoutOf(t, root)
	if len(insts) != 1 {
		t.Fatalf("实例数量不该变，实际 %d", len(insts))
	}
	if insts[0].ComponentID != "note" {
		t.Fatalf("componentId 应改成 note，实际 %q", insts[0].ComponentID)
	}
	if insts[0].ID != "note-1" {
		t.Fatalf("实例 id 应改成 note-1，实际 %q", insts[0].ID)
	}

	// 3) 注册表的键：启用状态跟着改名，别的条目不受影响
	enabled := enabledOf(t, root)
	if _, ok := enabled["builtin.note"]; ok {
		t.Fatal("旧 id 不该留在注册表里")
	}
	if !enabled["note"] {
		t.Fatal("启用状态应跟着改名（原本是启用的）")
	}
	if _, ok := enabled["vendor.clock"]; !ok {
		t.Fatal("其他组件的记录不该被动到")
	}

	// 4) 私有存储：目录名与文件名都带 id
	data, err := os.ReadFile(filepath.Join(root, "storage", "note", "note-1.json"))
	if err != nil {
		t.Fatalf("存储应迁到 storage/note/note-1.json: %v", err)
	}
	if string(data) != `{"content":"# 123"}` {
		t.Fatalf("正文不该被改动: %s", data)
	}
	if _, err := os.Stat(filepath.Join(root, "storage", "builtin.note")); !os.IsNotExist(err) {
		t.Fatal("旧存储目录应已改名，不留空壳")
	}
}

// 已经是新 id 时不能重复迁移，否则第二次启动会把实例 id 再改一次。
func TestMigrateBuiltinNoteIsIdempotent(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "layout.json"), `{
  "instances": [ { "id": "note-1", "componentId": "note", "bounds": {"width": 420, "height": 360} } ]
}`)
	writeFile(t, filepath.Join(root, "components.json"), `{"enabled":{"note":true}}`)

	persistence.MigrateBuiltinNote(root)

	insts := layoutOf(t, root)
	if len(insts) != 1 || insts[0].ID != "note-1" || insts[0].ComponentID != "note" {
		t.Fatalf("已迁移过的数据不该被改动: %+v", insts)
	}
}

// 全新安装（没有 layout.json）时是空操作，不能凭空造文件。
func TestMigrateBuiltinNoteNoopWithoutLayout(t *testing.T) {
	root := t.TempDir()
	persistence.MigrateBuiltinNote(root)

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("不该产生任何文件: %v", entries)
	}
}
