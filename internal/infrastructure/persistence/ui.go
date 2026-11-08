package persistence

import (
	"context"

	"stickynote/internal/domain/component"
)

// uiRecord 是 ui.json 的形状。
type uiRecord struct {
	Manager component.Rect `json:"manager"`
}

// UIJSON 保存内核自身界面的状态。
//
// 和 layout.json 分开是因为语义不同：layout 里的每一条都对应一个
// 组件实例窗口，而管理窗口属于内核，混进去会让"实例数"变得含糊。
type UIJSON struct {
	path string
}

// NewUI 创建内核界面状态仓储。
func NewUI(path string) *UIJSON { return &UIJSON{path: path} }

var _ component.UIRepository = (*UIJSON)(nil)

// Load 读取管理窗口几何。文件不存在时返回零值（调用方据此居中）。
func (u *UIJSON) Load(ctx context.Context) (component.Rect, error) {
	var rec uiRecord
	if err := readJSON(ctx, u.path, "界面状态", &rec); err != nil {
		return component.Rect{}, err
	}
	return rec.Manager, nil
}

// Save 写入管理窗口几何。
func (u *UIJSON) Save(ctx context.Context, r component.Rect) error {
	return writeJSON(ctx, u.path, "界面状态", uiRecord{Manager: r})
}
