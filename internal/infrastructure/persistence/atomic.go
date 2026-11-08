package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// readJSON 把一个 JSON 文件读进 out。
//
// 文件不存在时**不动 out 直接返回 nil**：首次启动就是这种情况，
// 各仓储的零值正好是它们要的默认值（空清单 / 空表 / 零矩形）。
// what 只用于错误信息，让日志能说清是哪个文件坏了。
func readJSON(ctx context.Context, path, what string, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取%s: %w", what, err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("解析%s: %w", what, err)
	}
	return nil
}

// writeJSON 把 v 缩进序列化后原子写入 path。
func writeJSON(ctx context.Context, path, what string, v any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化%s: %w", what, err)
	}
	return writeFileAtomic(path, data)
}

// writeFileAtomic 用"临时文件 + 重命名"替换目标文件。
//
// 直接覆盖写在进程崩溃时会留下半个 JSON，下次启动就解析失败；
// rename 在同一卷上是原子的，要么是旧内容，要么是新内容。
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建目录: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("创建临时文件: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // 重命名成功后这里是空操作

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("刷盘: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换文件: %w", err)
	}
	return nil
}
