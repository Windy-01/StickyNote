// Package persistence 是组件宿主的基础设施实现（JSON 文件）。
//
// 布局刻意保持"一个用途一个文件"：注册表、实例布局、组件私有存储分开，
// 互不影响，出问题也好定位。
package persistence

import (
	"log"
	"os"
	"path/filepath"
	"sync"
)

// dataDirName 是数据目录名。它跟着 exe 走，不在用户配置目录里。
const dataDirName = "data"

// bundlesDirName 是随包分发的插件目录名（exe 旁边）。
const bundlesDirName = "bundles"

// legacyDirName 是旧版本（数据放 %AppData%）用的文件夹名，只在迁移时出现。
const legacyDirName = "StickyNote"

var (
	rootOnce sync.Once
	rootDir  string
	rootErr  error
)

// Root 返回数据根目录。
//
// 便携优先：数据放在 exe 旁边的 data\，用户只需要管一个文件夹，
// 把整个目录拷走就是迁移，不残留任何东西在用户配置目录里。
//
// 只有 exe 所在目录不可写时（装在 Program Files 又没有管理员权限）
// 才退回 %AppData%\StickyNote——那种情况下没得选，总不能起不来。
func Root() (string, error) {
	rootOnce.Do(func() { rootDir, rootErr = resolveRoot() })
	return rootDir, rootErr
}

func resolveRoot() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("取程序路径失败，数据改放用户配置目录: %v", err)
		return legacyRoot()
	}

	dir := filepath.Join(filepath.Dir(exe), dataDirName)
	if err := ensureWritable(dir); err != nil {
		log.Printf("安装目录不可写（%s），数据改放用户配置目录: %v", dir, err)
		return legacyRoot()
	}
	return dir, nil
}

// legacyRoot 是兜底位置。只在便携模式用不了时才会走到这里。
func legacyRoot() (string, error) {
	base, err := os.UserConfigDir() // Windows: %AppData%
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, legacyDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// ensureWritable 建目录并试写一个文件。
//
// 只 MkdirAll 不够：目录可能建得出来但文件写不进去（只读介质、ACL 拒绝、
// 受控文件夹访问）。真正的判定标准是"能不能写"。
func ensureWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	probe := filepath.Join(dir, ".write-test")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_ = f.Close()
	return os.Remove(probe)
}

// under 拼出数据根目录下的路径。
func under(name string) (string, error) {
	dir, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// LogPath 返回日志文件路径。
func LogPath() (string, error) { return under("app.log") }

// RegistryPath 返回组件注册表路径。
func RegistryPath() (string, error) { return under("components.json") }

// GroupsPath 返回组配置路径。
func GroupsPath() (string, error) { return under("groups.json") }

// LayoutPath 返回实例布局路径。
func LayoutPath() (string, error) { return under("layout.json") }

// UIStatePath 返回内核界面状态的路径（管理窗口几何）。
func UIStatePath() (string, error) { return under("ui.json") }

// StorageRoot 返回组件私有存储的根目录。
func StorageRoot() (string, error) { return under("storage") }

// VarsRoot 返回实例变量的根目录。
//
// 和 storage 分开两个目录，是因为两者归属不同：storage 是组件自己写的
// 运行时数据，vars 是用户在管理界面里填的配置。分开之后组件怎么写都碰不到
// 用户填的东西，删实例时也不用去分辨一份文件里哪些内容该留。
func VarsRoot() (string, error) { return under("vars") }

// PluginRoot 返回已安装插件目录。
//
// 目录结构：plugins\styles\<id>\ 与 plugins\components\<id>\。
func PluginRoot() (string, error) { return under("plugins") }

// CacheRoot 返回缓存目录：内置资源解包、WebView2 用户数据都放这里。
//
// 和用户数据分开命名，是因为这一整块**随时可以删**——
// 删掉只是下次启动慢一点，不丢任何东西。
func CacheRoot() (string, error) { return under("cache") }

// BundlesDir 返回随包分发的插件目录（exe 旁边的 bundles\）。
//
// 它是"预置包的来源"，不是运行目录：里面的插件会被装进 plugins\，
// 之后就和第三方插件没有区别。目录不存在时调用方按"没有预置包"处理。
func BundlesDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), bundlesDirName), nil
}

// LegacyNotePath 返回旧版单便签数据文件路径。
// 只在迁移时用：读到它就把内容搬进便签插件的实例存储。
func LegacyNotePath() (string, error) { return under("note.json") }
