// Command stickynote 是组件宿主的组装入口。
//
// 这里只做依赖装配：基础设施 -> 应用层 -> 表现层，不含业务逻辑。
// 内核里只有样式（L1）与管理界面；**便签是插件**，随包放在 bundles\note\。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"math"
	"os"
	"path/filepath"

	"stickynote/builtin"
	"stickynote/internal/application/componentapp"
	"stickynote/internal/domain/component"
	"stickynote/internal/infrastructure/persistence"
	"stickynote/internal/infrastructure/plugin"
	"stickynote/internal/interfaces/webview"
)

// noteComponentID 是便签插件的 id。迁移旧版单便签数据时按它找实例。
const noteComponentID = "note"

func main() {
	setupLogging()

	ctx := context.Background()

	dataDir, err := persistence.Root()
	if err != nil {
		log.Fatalf("初始化数据目录失败: %v", err)
	}
	// 从旧版本升级上来时，数据还在 %AppData% 里，先搬过来。
	// 必须早于下面所有"读文件"的动作，否则会读到一个空目录。
	persistence.MigrateFromAppData(dataDir)

	layoutPath, err := persistence.LayoutPath()
	if err != nil {
		log.Fatalf("初始化数据目录失败: %v", err)
	}
	// 布局文件不存在 = 首次运行，此时才做旧数据迁移，
	// 否则每次启动都会把用户后来的改动覆盖回去。
	_, statErr := os.Stat(layoutPath)
	firstRun := errors.Is(statErr, fs.ErrNotExist)

	registryPath, err := persistence.RegistryPath()
	if err != nil {
		log.Fatalf("初始化数据目录失败: %v", err)
	}
	storageRoot, err := persistence.StorageRoot()
	if err != nil {
		log.Fatalf("初始化数据目录失败: %v", err)
	}
	varsRoot, err := persistence.VarsRoot()
	if err != nil {
		log.Fatalf("初始化数据目录失败: %v", err)
	}
	uiPath, err := persistence.UIStatePath()
	if err != nil {
		log.Fatalf("初始化数据目录失败: %v", err)
	}
	pluginRoot, err := persistence.PluginRoot()
	if err != nil {
		log.Fatalf("初始化插件目录失败: %v", err)
	}
	// 插件目录可能不存在（用户一个都没装），建出来省得后面判断。
	if err := os.MkdirAll(pluginRoot, 0o755); err != nil {
		log.Printf("创建插件目录失败: %v", err)
	}

	cacheRoot, err := persistence.CacheRoot()
	if err != nil {
		log.Fatalf("初始化缓存目录失败: %v", err)
	}
	// 内置资源解包一次，目录加载器反复用它：安装/卸载插件之后要重扫。
	builtinDir, err := builtin.Materialize(filepath.Join(cacheRoot, "builtin"))
	if err != nil {
		log.Fatalf("解包内置资源失败: %v", err)
	}
	scan := func() (componentapp.Catalog, []error) {
		return mergeCatalog(builtinDir, pluginRoot)
	}
	// loadCatalog 是"正式的"目录载入：会记一行数量，便于从日志判断插件有没有被读到。
	loadCatalog := func() (componentapp.Catalog, []error) {
		cat, problems := scan()
		log.Printf("目录载入完成：组件 %d 个", len(cat.Components))
		return cat, problems
	}

	// 安装器只建一次：预置包发放与用户手动安装必须走同一套规则，
	// 各建一个实例虽然行为一致，但两处根目录就容易改漏一处。
	installer := plugin.NewInstaller(pluginRoot)

	groupsPath, err := persistence.GroupsPath()
	if err != nil {
		log.Fatalf("初始化数据目录失败: %v", err)
	}

	svc := componentapp.NewService(
		persistence.NewLayout(layoutPath),
		persistence.NewRegistry(registryPath),
		persistence.NewGroups(groupsPath),
		persistence.NewVars(varsRoot),
		persistence.NewStorage(storageRoot),
		persistence.NewUI(uiPath),
		installer,
	)
	svc.SetCatalogLoader(loadCatalog)
	// 首次启动生成哪些组、哪个组件进哪个组，是产品决策，放在组合根里。
	svc.SetDefaultGroups(defaultGroups(), defaultGroupMembers())

	// 预置包：只发放一次。用户卸载之后不再自动装回来——
	// 靠 bundles.json 里的"发放过"记录判断，而不是靠"插件目录里有没有"。
	issued := persistence.BundledIssued()
	var fresh []string
	var freshComponents []string
	if bundlesDir, err := persistence.BundlesDir(); err == nil {
		for _, pkg := range installer.InstallBundles(bundlesDir, issued) {
			if issued[pkg.ID] {
				continue
			}
			fresh = append(fresh, pkg.ID)
			if pkg.Kind == component.PackageComponent {
				// 首次发给用户就直接能用，否则开箱只有一个空桌面。
				svc.SetBootstrapEnabled(pkg.ID)
				freshComponents = append(freshComponents, pkg.ID)
			}
		}
	}
	if err := persistence.MarkBundledIssued(fresh); err != nil {
		log.Printf("记录预置包发放状态失败: %v", err)
	}

	// 便签的 id 从 builtin.note 改成了 note。必须早于 Boot：
	// Boot 读到指向已消失组件的实例会直接丢掉，那等于删了用户的数据。
	persistence.MigrateBuiltinNote(dataDir)

	cat, problems := loadCatalog()
	for _, p := range problems {
		log.Printf("插件问题: %v", p)
	}
	if err := svc.Boot(ctx, cat); err != nil {
		log.Fatalf("载入组件目录失败: %v", err)
	}
	// 刚发放的组件各开一个实例：用户刚拿到的东西得能看见。
	// 只在"还没有实例"时补，不会覆盖用户已经关掉的窗口。
	openInitialInstances(ctx, svc, freshComponents)
	if firstRun {
		migrateLegacyNote(ctx, svc)
	}

	opts := webview.Options{
		CacheDir:   cacheRoot,
		ManagerDir: filepath.Join(builtinDir, "manager"),
		PluginRoot: pluginRoot,
		// 首次运行直接把管理界面摆出来：否则用户只看到一个便签，
		// 根本不知道还有"装插件"这回事。
		// 开发模式下也自动打开，省得每次都要点托盘。
		OpenManagerOnStart: firstRun || os.Getenv("STICKYNOTE_DEV") != "",
		// 设置 STICKYNOTE_DEV=1 可打开 DevTools，调试组件前端时用。
		DevMode: os.Getenv("STICKYNOTE_DEV") != "",
	}
	if err := webview.Run(svc, opts); err != nil {
		log.Fatalf("启动失败: %v", err)
	}
}

// openInitialInstances 给刚发放的组件各开一个实例。
//
// 只补"一个实例都没有"的组件：用户把窗口关掉之后再重启，不该又冒出来。
// 这一步和 Boot 里"一个实例都没有时建一个"是两件事——后者是兜底，
// 前者是"刚装的东西得能看见"。
func openInitialInstances(ctx context.Context, svc *componentapp.Service, ids []string) {
	changed := false
	for _, id := range ids {
		cv, ok := svc.Component(id)
		// 停用的组件不该凭空多出一条实例记录：窗口不会建，用户只会在
		// 管理界面里看到一个"未运行"的空壳，还得自己去删。
		if !ok || !cv.Enabled || len(cv.InstanceIDs) > 0 {
			continue
		}
		v, err := svc.CreateInstance(ctx, id, componentapp.DefaultGroupID)
		if err != nil {
			log.Printf("为预置组件 %s 建实例失败: %v", id, err)
			continue
		}
		log.Printf("已为预置组件 %s 打开实例 %s", id, v.ID)
		changed = true
	}
	if !changed {
		return
	}
	if err := svc.SaveLayout(ctx); err != nil {
		log.Printf("保存实例布局失败: %v", err)
	}
}

// mergeCatalog 扫描内置资源与已安装插件，合成一份静态目录。
//
// 单个包出错只记日志，不影响其他包：用户装坏一个插件，
// 不该导致整个程序起不来。
func mergeCatalog(builtinDir, pluginRoot string) (componentapp.Catalog, []error) {
	builtinCat, problems := plugin.Load(builtinDir, true)
	installedCat, installedProblems := plugin.Load(pluginRoot, false)
	problems = append(problems, installedProblems...)

	merged, conflicts := plugin.Merge(builtinCat, installedCat)
	problems = append(problems, conflicts...)
	return merged, problems
}

// defaultGroups 是首次启动时生成的组。
//
// 它们对应原来那三种"窗口形态"，但现在是**用户能改的开关组合**：
// 想要一个既置顶又穿透的组，自己新建一个勾上就行，内核不需要再支持第四种形态。
//
// 「默认」组是兜底：新装的组件没地方去时落在这里。它刻意是"常规层级 + 可交互"，
// 用户装完一个组件，最先看到的应该是一个能点能拖的正常窗口，
// 而不是一个穿透明、点不到的影子。
func defaultGroups() []component.Group {
	visual := func(radius int, bg string) component.Visual {
		return component.VisualFrom(component.VisualValues{
			Padding: 0, Radius: radius, Background: bg, Shadow: "",
		})
	}
	return []component.Group{
		{
			ID:   "builtin.group.desktop",
			Name: "桌面层",
			Window: component.WindowSpec{
				Layer:        component.LayerDesktop,
				ClickThrough: true,
				Resizable:    false,
				SizeMode:     component.SizeUser,
				Titlebar:     false,
			},
			Visual: visual(0, "transparent"),
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
			Visual: visual(12, "#fffbf0"),
		},
		{
			ID:   "builtin.group.overlay",
			Name: "浮层",
			Window: component.WindowSpec{
				Layer:        component.LayerTopmost,
				ClickThrough: true,
				Resizable:    false,
				SizeMode:     component.SizeUser,
				Titlebar:     false,
			},
			Visual: visual(0, "transparent"),
		},
		{
			ID:   componentapp.DefaultGroupID,
			Name: "默认",
			Window: component.WindowSpec{
				Layer:     component.LayerNormal,
				Resizable: true,
				SizeMode:  component.SizeUser,
				Titlebar:  true,
			},
			Visual: visual(12, "#fffbf0"),
		},
	}
}

// defaultGroupMembers 是预置组件的默认归属。
//
// 只影响首次生成组的那一刻；之后用户随便挪。
func defaultGroupMembers() map[string]string {
	return map[string]string{
		"clock":   "builtin.group.desktop",
		"note":    "builtin.group.topmost",
		"danmaku": "builtin.group.overlay",
	}
}

// migrateLegacyNote 把旧版单便签的数据搬进便签插件的实例存储。
//
// 旧 note.json 的宽高是物理像素，而实例布局存的是逻辑像素，
// 所以要按当前缩放折回去，否则在 125% 屏幕上窗口会大一圈。
// 迁移完成后把原文件改名保留，**不删除用户数据**。
func migrateLegacyNote(ctx context.Context, svc *componentapp.Service) {
	path, err := persistence.LegacyNotePath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	var rec struct {
		Content string `json:"content"`
		Bounds  struct {
			X      int `json:"x"`
			Y      int `json:"y"`
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"bounds"`
		Pinned bool `json:"pinned"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		log.Printf("旧便签数据无法解析，跳过迁移: %v", err)
		return
	}

	for _, v := range svc.ActiveInstances() {
		if v.ComponentID != noteComponentID {
			continue
		}

		raw, err := json.Marshal(rec.Content)
		if err == nil {
			if err := svc.SetStorage(ctx, v.ID, "content", raw); err != nil {
				log.Printf("迁移便签正文失败: %v", err)
			}
		}

		scale := webview.SystemScale()
		if scale <= 0 {
			scale = 1
		}
		rect := component.Rect{
			X:      rec.Bounds.X,
			Y:      rec.Bounds.Y,
			Width:  int(math.Round(float64(rec.Bounds.Width) / scale)),
			Height: int(math.Round(float64(rec.Bounds.Height) / scale)),
		}
		if rect.Width <= 0 || rect.Height <= 0 {
			rect = component.Rect{Width: component.DefaultWidth, Height: component.DefaultHeight}
		}
		if err := svc.SetBounds(ctx, v.ID, rect); err != nil {
			log.Printf("迁移便签几何失败: %v", err)
		}
		// 旧数据里的 pinned 不再迁移：置顶现在是组的属性，
		// 由用户在建组时决定，不该从一条实例记录里猜出来。

		if err := svc.SaveLayout(ctx); err != nil {
			log.Printf("迁移后保存布局失败: %v", err)
		}
		log.Printf("已把旧便签迁移到实例 %s", v.ID)
		break
	}

	if err := os.Rename(path, path+".migrated"); err != nil {
		log.Printf("重命名旧便签文件失败: %v", err)
	}
}

// setupLogging 桌面程序没有控制台，日志落到 %AppData%\StickyNote\app.log。
func setupLogging() {
	path, err := persistence.LogPath()
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags)
}
