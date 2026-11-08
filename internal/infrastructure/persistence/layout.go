package persistence

import (
	"context"

	"stickynote/internal/domain/component"
)

// layoutRecord 是 layout.json 的形状。
type layoutRecord struct {
	Instances []component.Instance `json:"instances"`
}

// LayoutJSON 把实例清单与几何存成单个 JSON 文件。
//
// 实例数量很少（决策 4：暂不考虑大量组件并发），一个文件足够，
// 也便于用户直接查看和手工修复。
type LayoutJSON struct {
	path string
}

// NewLayout 创建布局仓储。
func NewLayout(path string) *LayoutJSON { return &LayoutJSON{path: path} }

var _ component.LayoutRepository = (*LayoutJSON)(nil)

// Load 读取实例清单。文件不存在时返回空清单。
func (l *LayoutJSON) Load(ctx context.Context) ([]component.Instance, error) {
	var rec layoutRecord
	if err := readJSON(ctx, l.path, "实例布局", &rec); err != nil {
		return nil, err
	}
	return rec.Instances, nil
}

// Save 写入实例清单。
func (l *LayoutJSON) Save(ctx context.Context, instances []component.Instance) error {
	if instances == nil {
		// 写 null 的话下次读出来是 nil 切片，和"空清单"混在一起；
		// 明确写成 []，文件也更好读。
		instances = []component.Instance{}
	}
	return writeJSON(ctx, l.path, "实例布局", layoutRecord{Instances: instances})
}
