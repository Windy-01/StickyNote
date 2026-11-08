package persistence

import (
	"context"

	"stickynote/internal/domain/component"
)

// registryRecord 是 components.json 的形状。
type registryRecord struct {
	Enabled map[string]bool `json:"enabled"`
}

// RegistryJSON 把"哪些组件被启用"存成一个 JSON 对象。
//
// 只存开关，不存组件定义：定义永远从插件目录现读，
// 用户手改 manifest.json 后重启即生效，不会出现注册表与文件不一致。
type RegistryJSON struct {
	path string
}

// NewRegistry 创建注册表仓储。
func NewRegistry(path string) *RegistryJSON { return &RegistryJSON{path: path} }

var _ component.RegistryRepository = (*RegistryJSON)(nil)

// Load 读取启用状态。文件不存在时返回空表，不报错（首次启动就是这种情况）。
func (r *RegistryJSON) Load(ctx context.Context) (map[string]bool, error) {
	rec := registryRecord{Enabled: map[string]bool{}}
	if err := readJSON(ctx, r.path, "组件注册表", &rec); err != nil {
		return nil, err
	}
	if rec.Enabled == nil {
		rec.Enabled = map[string]bool{}
	}
	return rec.Enabled, nil
}

// Save 写入启用状态。
func (r *RegistryJSON) Save(ctx context.Context, enabled map[string]bool) error {
	return writeJSON(ctx, r.path, "组件注册表", registryRecord{Enabled: enabled})
}
