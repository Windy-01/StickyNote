package plugin

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"stickynote/internal/domain/component"
)

// reservedPrefix 是内置资源占用的 id 前缀。
//
// 第三方包不允许用这个前缀：内置资源由二进制内嵌、每次启动重新解包，
// 即使允许覆盖也会在下次启动被还原，让用户以为"装了但没生效"。
const reservedPrefix = component.ReservedIDPrefix

// Installer 实现 component.PackageRepository。
type Installer struct {
	root string
}

// NewInstaller 创建插件安装器。root 是插件根目录（其下有 components/）。
func NewInstaller(root string) *Installer { return &Installer{root: root} }

var _ component.PackageRepository = (*Installer)(nil)

// Inspect 识别一个目录是不是组件包，并做完整的结构性校验。
//
// 判断依据只有根目录里有没有 manifest.json。
// 旧版的 L1 样式包（只有 style.json）不再被接受——窗口外观现在由「组」决定，
// 那个概念已经从插件体系里移除了，报错要写清楚，别让人以为文件坏了。
func (i *Installer) Inspect(dir string) (component.Package, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return component.Package{}, fmt.Errorf("无法访问 %s: %w", dir, err)
	}
	if !info.IsDir() {
		return component.Package{}, fmt.Errorf("%s 不是文件夹", dir)
	}

	if !fileExists(filepath.Join(dir, manifestFile)) {
		if fileExists(filepath.Join(dir, "style.json")) {
			return component.Package{}, errors.New("这是旧版的样式包（只有 style.json）：" +
				"新版本里窗口外观由「组」决定，不再需要单独的样式包")
		}
		return component.Package{}, errors.New("目录里没有 manifest.json，" +
			"这不像是组件包（请选择组件包本身的目录，而不是它的上一级）")
	}

	d, err := ReadComponent(dir, false)
	if err != nil {
		return component.Package{}, err
	}
	return component.Package{
		Kind: component.PackageComponent, ID: d.ID, Name: d.Name, Dir: dir, Definition: &d,
	}, nil
}

// Install 把 srcDir 复制进插件根目录。
//
// 只做冲突检测，不校验签名与版本——这是刻意的：
// 这套框架面向的是自己写组件的人，不是应用商店。
func (i *Installer) Install(srcDir string) (component.Package, error) {
	pkg, err := i.Inspect(srcDir)
	if err != nil {
		return pkg, err
	}
	if strings.HasPrefix(pkg.ID, reservedPrefix) {
		return pkg, fmt.Errorf("id %q 使用了保留前缀 %q，请换一个", pkg.ID, reservedPrefix)
	}

	dest := PackageDir(i.root, pkg.Kind, pkg.ID)
	if err := ensureOutside(i.root, srcDir); err != nil {
		return pkg, err
	}
	if dirExists(dest) {
		return pkg, fmt.Errorf("已存在 id 为 %s 的插件，请先卸载旧版本再安装", pkg.ID)
	}

	if err := copyTree(srcDir, dest); err != nil {
		// 半成品必须清掉，否则下次启动会读到一个残缺插件，
		// 而且它的 id 已经占了位置，用户装不进去也删不掉。
		_ = os.RemoveAll(dest)
		return pkg, err
	}
	pkg.Dir = dest
	return pkg, nil
}

// InstallBundles 把预置包目录里的组件装进插件目录。
//
// skip 里的 id 表示"已经发放过"：用户卸载之后不该被自动装回来。
// 单个包失败只记日志，不影响其他包——预置包装坏了不该让程序起不来。
//
// 返回值包含**本次发放的全部包**（含"本来就已经装着"的），
// 调用方据此记录发放状态并设置默认启用。
func (i *Installer) InstallBundles(dir string, skip map[string]bool) []component.Package {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	sort.Slice(entries, func(a, b int) bool { return entries[a].Name() < entries[b].Name() })

	var out []component.Package
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		src := filepath.Join(dir, e.Name())

		// 先认 id 再判断要不要装：目录名不一定等于 id，跳过判断必须按 id。
		pkg, err := i.Inspect(src)
		if err != nil {
			log.Printf("预置包 %s 无法识别: %v", src, err)
			continue
		}
		if skip[pkg.ID] {
			continue
		}
		if dirExists(PackageDir(i.root, pkg.Kind, pkg.ID)) {
			// 已经躺在插件目录里了，只需要补一条发放记录。
			out = append(out, pkg)
			continue
		}
		installed, err := i.Install(pkg.Dir)
		if err != nil {
			log.Printf("安装预置包 %s 失败: %v", pkg.ID, err)
			continue
		}
		log.Printf("已安装预置插件 %s（%s）", installed.ID, installed.Dir)
		out = append(out, installed)
	}
	return out
}

// Uninstall 删除一个已安装的插件目录。
//
// 只删插件文件，不动组件私有数据（storage/<组件 id>/）：
// 用户卸载后重新装回来，正文还在。要彻底清掉数据是另一件事，
// 不应该藏在一个"卸载"按钮里顺手做掉。
func (i *Installer) Uninstall(kind component.PackageKind, id string) error {
	dir, err := i.Dir(kind, id)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("插件 %s 不存在（可能已经被删除）", id)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("删除 %s: %w", dir, err)
	}
	return nil
}

// Dir 返回组件在插件根目录下的位置。
func (i *Installer) Dir(kind component.PackageKind, id string) (string, error) {
	if kind != component.PackageComponent {
		return "", fmt.Errorf("未知的插件类型 %q", kind)
	}
	if err := component.ValidateID(id); err != nil {
		return "", fmt.Errorf("插件 id 非法: %w", err)
	}
	return PackageDir(i.root, kind, id), nil
}

// PackageDir 返回组件在插件根目录下的位置（不做校验的版本）。
func PackageDir(root string, kind component.PackageKind, id string) string {
	if kind == component.PackageComponent {
		return filepath.Join(root, componentsDir, id)
	}
	return ""
}

// ensureOutside 拦住"从插件目录里再装一次"这种自我复制的操作。
func ensureOutside(root, srcDir string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	src, err := filepath.Abs(srcDir)
	if err != nil {
		return err
	}
	prefix := strings.ToLower(absRoot) + string(os.PathSeparator)
	if strings.EqualFold(src, absRoot) || strings.HasPrefix(strings.ToLower(src), prefix) {
		return errors.New("插件源目录已经在插件目录里面了，不需要重复安装")
	}
	return nil
}

// copyTree 递归复制目录，跳过版本控制目录。
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		// .git 可能有几万个文件，且对运行毫无用处。
		if d.IsDir() && d.Name() == ".git" {
			return fs.SkipDir
		}

		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			// 符号链接、设备文件之类的一律跳过，不给它们留到组件目录里。
			return nil
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("读取 %s: %w", src, err)
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("创建 %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("写入 %s: %w", dst, err)
	}
	return out.Close()
}

// dirExists 判断路径是不是一个已存在的目录。
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// fileExists 判断路径是不是一个已存在的普通文件。
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
