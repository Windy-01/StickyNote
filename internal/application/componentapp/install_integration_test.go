package componentapp_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"stickynote/internal/application/componentapp"
	"stickynote/internal/domain/component"
	"stickynote/internal/infrastructure/plugin"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
}

// 安装 → 出现在目录 → 自动归组 → 卸载守卫，整条链路走一遍。
//
// 这里用真实的 Installer 与真实的目录扫描，只把仓储换成内存替身：
// 中间任何一环（复制、重扫、合并、归组、守卫）出问题都会在这里暴露。
func TestInstallUninstallRoundTrip(t *testing.T) {
	ctx := context.Background()

	builtinDir := t.TempDir()
	writeFile(t, filepath.Join(builtinDir, "components", "note", "manifest.json"), `{
	  "id": "builtin.note", "name": "便签", "entry": "index.html"
	}`)
	writeFile(t, filepath.Join(builtinDir, "components", "note", "index.html"), "<!doctype html>")

	pluginRoot := t.TempDir()
	svc := componentapp.NewService(
		&fakeLayout{}, &fakeRegistry{}, &fakeGroups{}, &fakeVars{}, &fakeStorage{}, &fakeUI{},
		plugin.NewInstaller(pluginRoot),
	)
	svc.SetCatalogLoader(func() (componentapp.Catalog, []error) {
		builtinCat, p1 := plugin.Load(builtinDir, true)
		installedCat, p2 := plugin.Load(pluginRoot, false)
		merged, conflicts := plugin.Merge(builtinCat, installedCat)
		return merged, append(append(p1, p2...), conflicts...)
	})

	base, _ := plugin.Load(builtinDir, true)
	if err := svc.Boot(ctx, base); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	if len(svc.Components()) != 1 {
		t.Fatalf("初始应只有 1 个组件，实际 %d", len(svc.Components()))
	}

	// 1. 装一个第三方组件
	compSrc := t.TempDir()
	writeFile(t, filepath.Join(compSrc, "manifest.json"), `{
	  "id": "vendor.card", "name": "卡片", "entry": "index.html"
	}`)
	writeFile(t, filepath.Join(compSrc, "index.html"), "<!doctype html>")

	pkg, err := svc.InstallPackage(ctx, compSrc)
	if err != nil {
		t.Fatalf("安装组件失败: %v", err)
	}
	if pkg.Kind != component.PackageComponent {
		t.Fatalf("类型识别不对: %+v", pkg)
	}
	if len(svc.Components()) != 2 {
		t.Fatalf("装完应有 2 个组件，实际 %d", len(svc.Components()))
	}

	cv, ok := svc.Component("vendor.card")
	if !ok {
		t.Fatal("组件应出现在目录里")
	}
	if cv.Enabled {
		t.Fatal("第三方组件应默认停用")
	}
	// 组件本身不带组：归属在实例上。新建实例时不给组就落兜底组，
	// 否则它解析不出外观，界面上会变成一个打不开的空壳。
	if len(cv.InstanceIDs) != 0 {
		t.Fatalf("刚装完不该有实例，实际 %v", cv.InstanceIDs)
	}

	// 2. 有实例时不能卸载
	inst, err := svc.CreateInstance(ctx, "vendor.card", "")
	if err != nil {
		t.Fatalf("建实例失败: %v", err)
	}
	if inst.GroupID != componentapp.DefaultGroupID {
		t.Fatalf("新实例应落兜底组，实际 %q", inst.GroupID)
	}
	if err := svc.UninstallPackage(ctx, component.PackageComponent, "vendor.card"); err == nil {
		t.Fatal("还有实例时应当拒绝卸载")
	}

	// 3. 关掉实例后可以卸载
	for _, v := range svc.AllInstances() {
		if v.ComponentID != "vendor.card" {
			continue
		}
		if err := svc.RemoveInstance(ctx, v.ID); err != nil {
			t.Fatalf("删实例失败: %v", err)
		}
	}
	if err := svc.UninstallPackage(ctx, component.PackageComponent, "vendor.card"); err != nil {
		t.Fatalf("卸载组件失败: %v", err)
	}
	if _, ok := svc.Component("vendor.card"); ok {
		t.Fatal("卸载后组件应消失")
	}
	// 卸载掉的组件不能留任何实例记录，否则重启时会指向一个不存在的组件。
	for _, v := range svc.AllInstances() {
		if v.ComponentID == "vendor.card" {
			t.Fatalf("实例 %s 还挂在已卸载的组件上", v.ID)
		}
	}
}
