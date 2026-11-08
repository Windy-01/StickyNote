package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"stickynote/internal/domain/component"
)

// VarsJSON 把实例的变量值存成 <root>\<组件id>\<实例id>.json。
//
// 一个实例一个文件，和 storage 目录同构但**分开**：变量是用户填的配置，
// storage 是组件自己写的运行时数据。分开存之后，组件怎么写都碰不到
// 用户填的东西，删实例时也不用去猜"这份文件里哪些是用户的"。
//
// 只存**用户改过的**值：没填过的变量不写进来，读取时由定义里的 Default 兜底。
// 这样作者改清单里的默认值，用户没动过的实例会自动跟着变。
type VarsJSON struct {
	root string
}

// NewVars 创建变量仓储，root 是 vars 目录。
func NewVars(root string) *VarsJSON { return &VarsJSON{root: root} }

var _ component.InstanceVars = (*VarsJSON)(nil)

func (v *VarsJSON) path(componentID, instanceID string) (string, error) {
	for _, id := range []string{componentID, instanceID} {
		if id == "" || strings.ContainsAny(id, `/\:`) || strings.Contains(id, "..") {
			return "", fmt.Errorf("非法 id: %q", id)
		}
	}
	return filepath.Join(v.root, componentID, instanceID+".json"), nil
}

// Load 读取一个实例的全部变量值。文件不存在时返回空表。
func (v *VarsJSON) Load(ctx context.Context, componentID, instanceID string) (map[string]json.RawMessage, error) {
	path, err := v.path(componentID, instanceID)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	if err := readJSON(ctx, path, "实例变量 "+path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Save 覆盖一个实例的全部变量值。空表等于删除文件。
func (v *VarsJSON) Save(ctx context.Context, componentID, instanceID string, values map[string]json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := v.path(componentID, instanceID)
	if err != nil {
		return err
	}
	if len(values) == 0 {
		return removeFile(path)
	}
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// Remove 删除一个实例的变量文件。
func (v *VarsJSON) Remove(ctx context.Context, componentID, instanceID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := v.path(componentID, instanceID)
	if err != nil {
		return err
	}
	return removeFile(path)
}
