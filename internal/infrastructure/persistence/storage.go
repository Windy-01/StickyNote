package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"stickynote/internal/domain/component"
)

// StorageJSON 把组件私有数据存成 <root>\<组件id>\<实例id>.json。
//
// 每个实例一个文件：组件读不到别人的数据，删除实例时直接删文件，
// 也不会因为某个组件写坏了数据而连累其他组件。
type StorageJSON struct {
	root string
}

// NewStorage 创建存储仓储，root 是 storage 目录。
func NewStorage(root string) *StorageJSON { return &StorageJSON{root: root} }

var _ component.Storage = (*StorageJSON)(nil)

// All 读取实例的全部键值。文件不存在时返回空表。
func (s *StorageJSON) All(ctx context.Context, componentID, instanceID string) (map[string]json.RawMessage, error) {
	path, err := s.path(componentID, instanceID)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	if err := readJSON(ctx, path, "组件存储 "+path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Set 写入一个键。
func (s *StorageJSON) Set(ctx context.Context, componentID, instanceID, key string, value json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if key == "" {
		return errors.New("storage key 不能为空")
	}
	all, err := s.All(ctx, componentID, instanceID)
	if err != nil {
		return err
	}
	all[key] = value
	return s.write(ctx, componentID, instanceID, all)
}

// Remove 删除一个键；key 为空表示清空该实例的全部数据。
func (s *StorageJSON) Remove(ctx context.Context, componentID, instanceID, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.path(componentID, instanceID)
	if err != nil {
		return err
	}
	if key == "" {
		return removeFile(path)
	}
	all, err := s.All(ctx, componentID, instanceID)
	if err != nil {
		return err
	}
	delete(all, key)
	if len(all) == 0 {
		return removeFile(path)
	}
	return s.write(ctx, componentID, instanceID, all)
}

// removeFile 删掉一个存储文件；本来就不存在不算错。
func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("删除组件存储: %w", err)
	}
	return nil
}

func (s *StorageJSON) write(ctx context.Context, componentID, instanceID string, all map[string]json.RawMessage) error {
	path, err := s.path(componentID, instanceID)
	if err != nil {
		return err
	}
	return writeJSON(ctx, path, "组件存储 "+path, all)
}

// path 拼出文件路径，并拒绝任何可能逃出 storage 根目录的 id。
//
// id 已经在领域层校验过，这里再挡一次：存储是最危险的一层，
// 一旦 id 里混进 .. 就会写到用户目录之外。
func (s *StorageJSON) path(componentID, instanceID string) (string, error) {
	for _, id := range []string{componentID, instanceID} {
		if id == "" || strings.ContainsAny(id, `/\:`) || strings.Contains(id, "..") {
			return "", fmt.Errorf("非法 id: %q", id)
		}
	}
	return filepath.Join(s.root, componentID, instanceID+".json"), nil
}
