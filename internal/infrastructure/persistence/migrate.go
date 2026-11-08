package persistence

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// legacyEntries 是从旧版数据目录里要搬走的东西。
//
// 只搬用户真正拥有的：配置、实例布局、界面状态、私有数据、插件。
// **缓存不搬**——内置资源解包和 WebView2 用户数据会自动重建，
// 搬 70MB 浏览器缓存只会拖慢首次启动。
var legacyEntries = []string{
	"components.json",
	"layout.json",
	"ui.json",
	"storage",
	"plugins",
}

// MigrateFromAppData 把旧版本放在 %AppData%\StickyNote 的数据搬进当前数据目录。
//
// 这条路径只对"从旧版本升级上来"的用户有意义：新版把数据放在 exe 旁边，
// 老用户第一次用新版时数据还在 AppData 里，不搬的话便签正文就"不见了"。
//
// 搬完把旧目录改名为 StickyNote.migrated，**不删除**：
// 万一搬错，用户手上还有原件。
func MigrateFromAppData(root string) {
	base, err := os.UserConfigDir()
	if err != nil {
		return
	}
	old := filepath.Join(base, legacyDirName)
	if samePath(old, root) {
		return // 便携模式用不了，本来就落在 AppData，不需要搬
	}
	if _, err := os.Stat(old); err != nil {
		return // 没有旧数据
	}
	// 新目录已经有布局文件，说明已经搬过了。
	if _, err := os.Stat(filepath.Join(root, "layout.json")); err == nil {
		return
	}

	moved := 0
	for _, name := range legacyEntries {
		src := filepath.Join(old, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := copyEntry(src, filepath.Join(root, name)); err != nil {
			log.Printf("迁移 %s 失败: %v", name, err)
			continue
		}
		moved++
	}
	if moved == 0 {
		return
	}
	log.Printf("已把数据从 %s 迁移到 %s", old, root)

	if err := os.Rename(old, backupName(old)); err != nil {
		log.Printf("旧数据目录仍在 %s（改名失败: %v），确认无误后可手动删除", old, err)
		return
	}
	log.Printf("旧数据目录已改名为 %s.migrated，确认无误后可删除", old)
}

// backupName 给旧目录找一个没被占用的备份名。
func backupName(old string) string {
	for i := 0; ; i++ {
		name := old + ".migrated"
		if i > 0 {
			name = fmt.Sprintf("%s.migrated%d", old, i)
		}
		if _, err := os.Stat(name); errors.Is(err, fs.ErrNotExist) {
			return name
		}
	}
}

func samePath(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
}

// copyEntry 复制文件或整个目录。
func copyEntry(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst)
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
