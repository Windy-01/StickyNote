package persistence

import (
	"context"

	"stickynote/internal/domain/component"
)

// groupRecord 是 groups.json 的形状。
type groupRecord struct {
	Groups []component.Group `json:"groups"`
}

// GroupsJSON 把组存成单个 JSON 文件。
//
// 组是用户在主界面上亲手调出来的东西（层级、穿透、底色、留白…），
// 丢了就得重配一遍，所以它和 layout.json 一样是"用户数据"，不是缓存。
type GroupsJSON struct {
	path string
}

// NewGroups 创建组仓储。
func NewGroups(path string) *GroupsJSON { return &GroupsJSON{path: path} }

var _ component.GroupRepository = (*GroupsJSON)(nil)

// Load 读取组。文件不存在时返回空清单——首次启动就是这种情况，
// 由应用层负责生成默认组。
func (g *GroupsJSON) Load(ctx context.Context) ([]component.Group, error) {
	var rec groupRecord
	if err := readJSON(ctx, g.path, "组配置", &rec); err != nil {
		return nil, err
	}
	return rec.Groups, nil
}

// Save 写入组。
func (g *GroupsJSON) Save(ctx context.Context, groups []component.Group) error {
	if groups == nil {
		// 同 LayoutJSON：明确写成 []，不写 null。
		groups = []component.Group{}
	}
	return writeJSON(ctx, g.path, "组配置", groupRecord{Groups: groups})
}
