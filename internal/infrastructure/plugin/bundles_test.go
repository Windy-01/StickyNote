package plugin_test

import (
	"os"
	"path/filepath"
	"testing"

	"stickynote/internal/infrastructure/plugin"
)

// bundleNote 是随包分发的便签清单。id 必须不带 builtin. 前缀，
// 否则会被 Install 当成冒充内置资源而拒绝。
const bundleNote = `{
  "id": "note",
  "name": "便签",
  "entry": "index.html",
  "defaultBounds": { "width": 420, "height": 360 }
}`

// writeBundle 造一个预置包目录。
func writeBundle(t *testing.T, dir string) string {
	t.Helper()
	write(t, filepath.Join(dir, "manifest.json"), bundleNote)
	write(t, filepath.Join(dir, "index.html"), "<!doctype html><title>note</title>")
	return dir
}

// 首次启动：预置包被复制进插件目录，并返回给调用方记账。
func TestInstallBundlesInstallsFresh(t *testing.T) {
	bundles := t.TempDir()
	writeBundle(t, filepath.Join(bundles, "note"))

	root := t.TempDir()
	inst := plugin.NewInstaller(root)

	got := inst.InstallBundles(bundles, map[string]bool{})
	if len(got) != 1 || got[0].ID != "note" {
		t.Fatalf("应装 1 个包，实际 %+v", got)
	}
	dest := filepath.Join(root, "components", "note")
	if got[0].Dir != dest {
		t.Fatalf("返回的目录应是安装后的位置，实际 %q", got[0].Dir)
	}
	if _, err := os.Stat(filepath.Join(dest, "index.html")); err != nil {
		t.Fatalf("前端文件应一起复制: %v", err)
	}
}

// 已发放过的 id 不再安装：用户卸载之后不该被自动装回来。
func TestInstallBundlesSkipsIssued(t *testing.T) {
	bundles := t.TempDir()
	writeBundle(t, filepath.Join(bundles, "note"))

	root := t.TempDir()
	inst := plugin.NewInstaller(root)

	got := inst.InstallBundles(bundles, map[string]bool{"note": true})
	if len(got) != 0 {
		t.Fatalf("已发放过的包不该再装，实际 %+v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "components", "note")); !os.IsNotExist(err) {
		t.Fatal("已发放过时不该留下任何目录")
	}
}

// 已经躺在插件目录里、但没记账（比如用户手动放进去的）：
// 只补一条发放记录，不重复安装、不覆盖用户改过的文件。
func TestInstallBundlesReportsAlreadyInstalled(t *testing.T) {
	bundles := t.TempDir()
	writeBundle(t, filepath.Join(bundles, "note"))

	root := t.TempDir()
	dest := filepath.Join(root, "components", "note")
	write(t, filepath.Join(dest, "manifest.json"), bundleNote)
	write(t, filepath.Join(dest, "index.html"), "<!doctype html>")
	write(t, filepath.Join(dest, "marker.txt"), "用户自己改过的")

	inst := plugin.NewInstaller(root)
	got := inst.InstallBundles(bundles, map[string]bool{})
	if len(got) != 1 || got[0].ID != "note" {
		t.Fatalf("已装着的包也应出现在返回值里（供记账），实际 %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dest, "marker.txt")); err != nil {
		t.Fatalf("不该覆盖已有目录: %v", err)
	}
}

// 一个包装坏了不影响其他包：预置包出问题不该让程序起不来。
//
// 第二个目录里只有一个 style.json——那是旧版的样式包，现在不被识别，
// 会被当成坏包跳过，而不是把整个启动流程打断。
func TestInstallBundlesSkipsBrokenPackage(t *testing.T) {
	bundles := t.TempDir()
	writeBundle(t, filepath.Join(bundles, "note"))
	write(t, filepath.Join(bundles, "legacy-style", "style.json"), `{"id":"style.old","name":"旧样式"}`)

	root := t.TempDir()
	inst := plugin.NewInstaller(root)

	got := inst.InstallBundles(bundles, map[string]bool{})
	if len(got) != 1 || got[0].ID != "note" {
		t.Fatalf("坏包应被跳过，好的照装，实际 %+v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "styles")); !os.IsNotExist(err) {
		t.Fatal("不该再创建 styles 目录")
	}
}
