package plugin_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stickynote/internal/domain/component"
	"stickynote/internal/infrastructure/plugin"
)

// vendorClock 是一个第三方组件包（没有 builtin. 前缀）。
const vendorClock = `{
  "id": "vendor.clock",
  "name": "时钟",
  "entry": "index.html",
  "defaultBounds": { "width": 240, "height": 120 }
}`

// 装组件：复制进 plugins/components/<id>/，并带上目录里的其他文件。
func TestInstallCopiesTree(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "manifest.json"), vendorClock)
	write(t, filepath.Join(src, "index.html"), "<!doctype html>")
	write(t, filepath.Join(src, "preview.png"), "not really a png")

	root := t.TempDir()
	inst := plugin.NewInstaller(root)

	pkg, err := inst.Install(src)
	if err != nil {
		t.Fatalf("安装失败: %v", err)
	}
	if pkg.Kind != component.PackageComponent || pkg.ID != "vendor.clock" {
		t.Fatalf("识别结果不对: %+v", pkg)
	}

	dest := filepath.Join(root, "components", "vendor.clock")
	if _, err := os.Stat(filepath.Join(dest, "preview.png")); err != nil {
		t.Fatalf("附属文件应一起复制: %v", err)
	}
	if pkg.Dir != dest {
		t.Fatalf("返回的目录应是安装后的位置，实际 %q", pkg.Dir)
	}
}

// 同 id 已存在时必须拒绝，不能覆盖。
func TestInstallRejectsDuplicate(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "manifest.json"), vendorClock)
	write(t, filepath.Join(src, "index.html"), "<!doctype html>")

	root := t.TempDir()
	write(t, filepath.Join(root, "components", "vendor.clock", "manifest.json"), vendorClock)
	write(t, filepath.Join(root, "components", "vendor.clock", "index.html"), "<!doctype html>")

	inst := plugin.NewInstaller(root)
	if _, err := inst.Install(src); err == nil {
		t.Fatal("重复安装应当报错")
	}
}

// builtin. 前缀是内置资源保留的，第三方不能占用。
func TestInstallRejectsReservedPrefix(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "manifest.json"), `{
	  "id": "builtin.evil", "name": "冒充内置", "entry": "index.html"
	}`)
	write(t, filepath.Join(src, "index.html"), "<!doctype html>")

	inst := plugin.NewInstaller(t.TempDir())
	if _, err := inst.Install(src); err == nil {
		t.Fatal("builtin. 前缀应当被拒绝")
	}
}

// 旧版样式包（只有 style.json）不再被接受，但报错必须说清楚该怎么办：
// 让人以为"文件坏了"会让作者白折腾半天。
func TestInspectRejectsLegacyStylePackage(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "style.json"), `{
	  "id": "vendor.style.dark", "name": "暗色浮窗"
	}`)

	inst := plugin.NewInstaller(t.TempDir())
	_, err := inst.Inspect(src)
	if err == nil {
		t.Fatal("旧版样式包应当被拒绝")
	}
	if !strings.Contains(err.Error(), "组") {
		t.Fatalf("报错应指向「组」这个新概念，实际: %v", err)
	}
}

// 选了组件包的上一级目录（里面有若干组件文件夹）时也要给出可读的提示。
func TestInspectRejectsParentDirectory(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "clock", "manifest.json"), vendorClock)
	write(t, filepath.Join(src, "clock", "index.html"), "<!doctype html>")

	inst := plugin.NewInstaller(t.TempDir())
	if _, err := inst.Inspect(src); err == nil {
		t.Fatal("上一级目录应当被拒绝")
	}
}

// 卸载只删插件目录，组件私有数据不在这里，所以不受影响。
func TestUninstallRemovesOnlyPackageDir(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components", "vendor.clock", "manifest.json"), vendorClock)
	write(t, filepath.Join(root, "components", "vendor.clock", "index.html"), "<!doctype html>")
	// 模拟一个不该被碰到的邻居
	write(t, filepath.Join(root, "components", "keep", "manifest.json"), "{}")

	inst := plugin.NewInstaller(root)
	if err := inst.Uninstall(component.PackageComponent, "vendor.clock"); err != nil {
		t.Fatalf("卸载失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "components", "vendor.clock")); !os.IsNotExist(err) {
		t.Fatal("插件目录应被删除")
	}
	if _, err := os.Stat(filepath.Join(root, "components", "keep", "manifest.json")); err != nil {
		t.Fatalf("其他插件不该受影响: %v", err)
	}
}

// id 里带路径分隔符时不能逃出插件目录。
func TestUninstallRejectsPathTraversal(t *testing.T) {
	inst := plugin.NewInstaller(t.TempDir())
	if err := inst.Uninstall(component.PackageComponent, "../evil"); err == nil {
		t.Fatal("含 .. 的 id 应当被拒绝")
	}
}
