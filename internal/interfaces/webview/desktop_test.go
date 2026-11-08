package webview

import (
	"testing"

	"stickynote/internal/domain/component"
)

// initialPosition 决定"第一次出现落在哪"。
//
// 它必须真的把 dock 落到坐标上：一条点击穿透的悬浮提示栏如果居中出现在
// 屏幕正中，用户既点不到也拖不动，只能去管理界面里删掉。
func TestInitialPositionDock(t *testing.T) {
	wa := workArea()
	aw := int(wa.Right - wa.Left)
	ah := int(wa.Bottom - wa.Top)
	if aw <= 0 || ah <= 0 {
		t.Skipf("工作区尺寸异常（%dx%d），跳过", aw, ah)
	}

	const w, h = 400, 60

	cases := []struct {
		dock component.Dock
		x, y int
	}{
		{component.DockTop, int(wa.Left) + (aw-w)/2, int(wa.Top)},
		{component.DockBottom, int(wa.Left) + (aw-w)/2, int(wa.Bottom) - h},
		{component.DockLeft, int(wa.Left), int(wa.Top) + (ah-h)/2},
		{component.DockRight, int(wa.Right) - w, int(wa.Top) + (ah-h)/2},
		{component.DockFree, int(wa.Left) + (aw-w)/2, int(wa.Top) + (ah-h)/2},
	}
	for _, c := range cases {
		x, y := initialPosition(c.dock, w, h)
		if x != c.x || y != c.y {
			t.Errorf("dock=%s 得到 (%d,%d)，期望 (%d,%d)", c.dock, x, y, c.x, c.y)
		}
	}
}
