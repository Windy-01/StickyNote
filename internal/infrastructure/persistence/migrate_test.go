package persistence_test

import (
	"os"
	"path/filepath"
	"testing"

	"stickynote/internal/infrastructure/persistence"
)

// 从旧版本（数据在 %AppData%）升级上来时，用户数据必须被搬走，
// 而且旧目录要留成 .migrated 备份，不能删。
func TestMigrateFromAppData(t *testing.T) {
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)

	old := filepath.Join(appData, "StickyNote")
	writeFile(t, filepath.Join(old, "layout.json"), `{"instances":[]}`)
	writeFile(t, filepath.Join(old, "components.json"), `{"enabled":{"builtin.note":true}}`)
	writeFile(t, filepath.Join(old, "storage", "builtin.note", "builtin.note-1.json"), `{"content":"正文"}`)
	// 缓存不该被搬：它会自动重建，搬它只是浪费时间。
	writeFile(t, filepath.Join(old, "webview", "EBWebView", "junk.bin"), "70MB 的样子")

	root := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}

	persistence.MigrateFromAppData(root)

	// 用户数据搬到了新位置
	for _, name := range []string{"layout.json", "components.json"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("%s 没被搬过来: %v", name, err)
		}
	}
	body := filepath.Join(root, "storage", "builtin.note", "builtin.note-1.json")
	if data, err := os.ReadFile(body); err != nil || string(data) != `{"content":"正文"}` {
		t.Fatalf("组件私有数据没搬对: %q, %v", data, err)
	}

	// 缓存不搬
	if _, err := os.Stat(filepath.Join(root, "webview")); !os.IsNotExist(err) {
		t.Fatal("缓存不该被搬过来")
	}

	// 旧目录改名保留，不删除
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("旧目录应已改名")
	}
	if _, err := os.Stat(old + ".migrated"); err != nil {
		t.Fatalf("旧目录应改名为 .migrated 备份: %v", err)
	}
}

// 已经搬过一次的目录不能重复搬（否则每次启动都覆盖用户后来的改动）。
func TestMigrateSkipsWhenAlreadyDone(t *testing.T) {
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)

	old := filepath.Join(appData, "StickyNote")
	writeFile(t, filepath.Join(old, "layout.json"), `{"instances":[{"id":"old"}]}`)

	root := filepath.Join(t.TempDir(), "data")
	// 新位置已经有布局文件 = 已经搬过了
	writeFile(t, filepath.Join(root, "layout.json"), `{"instances":[{"id":"new"}]}`)

	persistence.MigrateFromAppData(root)

	data, err := os.ReadFile(filepath.Join(root, "layout.json"))
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if string(data) != `{"instances":[{"id":"new"}]}` {
		t.Fatalf("已有数据不该被覆盖: %s", data)
	}
}

// 便携模式可用时，旧目录根本不存在，迁移应当是空操作。
func TestMigrateNoopWithoutLegacyDir(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())

	root := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	persistence.MigrateFromAppData(root)

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("不该产生任何文件: %v", entries)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
}
