package persistence

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// 便签从"内置组件"变成插件时改过一次 id。
//
// 旧 id 占用了 builtin. 保留前缀，而插件不允许用这个前缀（否则第三方
// 能冒充内置资源），所以改名是必须的，不能只把 Builtin 标记去掉。
const (
	legacyNoteComponentID = "builtin.note"
	noteComponentID       = "note"
)

// MigrateBuiltinNote 把便签的数据从内置组件时期的 id 迁到插件 id。
//
// 改的不只是名字，四处必须一起改：`layout.json` 的 componentId、实例 id、
// `components.json` 的键、`storage/` 下的目录与文件名。
// 漏任何一处都会变成"组件还在、数据找不到"——那比直接报错更难查。
func MigrateBuiltinNote(root string) {
	layoutPath := filepath.Join(root, "layout.json")
	layoutData, err := os.ReadFile(layoutPath)
	if err != nil {
		return // 还没有实例布局，没什么可迁的
	}

	var lay layoutRecord
	if err := json.Unmarshal(layoutData, &lay); err != nil {
		log.Printf("便签 id 迁移跳过：layout.json 无法解析: %v", err)
		return
	}

	// 新旧 id 同时存在说明数据已经乱了，不要动它——静默合并会丢实例。
	for _, inst := range lay.Instances {
		if inst.ComponentID == noteComponentID {
			return
		}
	}

	changed := false
	for i := range lay.Instances {
		inst := &lay.Instances[i]
		if inst.ComponentID != legacyNoteComponentID {
			continue
		}
		inst.ComponentID = noteComponentID
		inst.ID = renameInstanceID(inst.ID)
		changed = true
	}

	if changed {
		ctx := context.Background()
		if err := NewLayout(layoutPath).Save(ctx, lay.Instances); err != nil {
			log.Printf("迁移实例布局失败: %v", err)
			return
		}
	}

	// 注册表：启用状态跟着改名，否则插件装好了却是停用的。
	registryPath := filepath.Join(root, "components.json")
	enabled := map[string]bool{}
	if data, err := os.ReadFile(registryPath); err == nil {
		var rec registryRecord
		if json.Unmarshal(data, &rec) == nil && rec.Enabled != nil {
			enabled = rec.Enabled
		}
	}
	if was, ok := enabled[legacyNoteComponentID]; ok {
		enabled[noteComponentID] = was
		delete(enabled, legacyNoteComponentID)
		if err := NewRegistry(registryPath).Save(context.Background(), enabled); err != nil {
			log.Printf("迁移组件注册表失败: %v", err)
		}
		changed = true
	}

	// 私有数据：目录名和文件名都带着旧 id。
	oldDir := filepath.Join(root, "storage", legacyNoteComponentID)
	newDir := filepath.Join(root, "storage", noteComponentID)
	if _, err := os.Stat(oldDir); err == nil {
		if err := os.Rename(oldDir, newDir); err != nil {
			log.Printf("迁移便签存储目录失败（数据仍在 %s）: %v", oldDir, err)
		} else {
			renameInstanceFiles(newDir)
			changed = true
		}
	}

	if changed {
		log.Printf("已把便签数据从 %s 迁移到 %s", legacyNoteComponentID, noteComponentID)
	}
}

// renameInstanceID 把 "builtin.note-1" 改成 "note-1"。
func renameInstanceID(id string) string {
	return strings.Replace(id, legacyNoteComponentID+"-", noteComponentID+"-", 1)
}

// renameInstanceFiles 把存储目录里的 "builtin.note-1.json" 改成 "note-1.json"。
func renameInstanceFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, legacyNoteComponentID+"-") {
			continue
		}
		target := renameInstanceID(name)
		if err := os.Rename(filepath.Join(dir, name), filepath.Join(dir, target)); err != nil {
			log.Printf("迁移存储文件 %s 失败: %v", name, err)
		}
	}
}
