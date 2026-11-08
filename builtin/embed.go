// Package builtin 把管理界面编进二进制。
//
// 内置的只剩一样东西：manager/ —— 内核自己的管理界面。
// 窗口外观已经变成运行时数据（组），不再有内置的样式包。
//
// **便签不在这里**。它和第三方组件一样是个插件，随包放在 exe 旁边的
// bundles\note\，首次启动装进 data\plugins\。想换掉、删掉、用别的便签
// 顶替它，都不需要动内核。
package builtin

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

//go:embed all:manager
var embedded embed.FS

// FS 返回内嵌资源的根目录（只有 manager/）。
func FS() (fs.FS, error) {
	return fs.Sub(embedded, ".")
}

// Materialize 把内置资源解包到磁盘并返回目录。
//
// WebView2 的虚拟主机映射只接受真实目录，所以必须先落地。
// 目录名带内容哈希：内置资源一改，哈希变，新目录自动生成，旧目录清掉。
func Materialize(cacheRoot string) (string, error) {
	root, err := FS()
	if err != nil {
		return "", fmt.Errorf("打开内置资源: %w", err)
	}

	digest, err := hashTree(root)
	if err != nil {
		return "", err
	}

	dir := filepath.Join(cacheRoot, digest)
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}

	if err := extract(root, dir); err != nil {
		return "", err
	}
	prune(cacheRoot, digest)
	return dir, nil
}

// prune 清掉旧版本目录，否则每改一次内置资源就多留一份。
func prune(cacheRoot, keep string) {
	entries, err := os.ReadDir(cacheRoot)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == keep {
			continue
		}
		_ = os.RemoveAll(filepath.Join(cacheRoot, e.Name()))
	}
}

func hashTree(root fs.FS) (string, error) {
	var paths []string
	err := fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)

	h := sha256.New()
	for _, p := range paths {
		data, err := fs.ReadFile(root, p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s:%d:", p, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

func extract(root fs.FS, dir string) error {
	return fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(root, path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
