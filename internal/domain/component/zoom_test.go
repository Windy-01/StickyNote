package component_test

import (
	"math"
	"testing"

	"stickynote/internal/domain/component"
)

// 老数据里没有 zoom 字段，读出来是 0。必须补成 1（原始大小），
// 否则一升级所有组件都会被压到最小缩放。
func TestEffectiveZoomDefaultsToOne(t *testing.T) {
	inst := component.Instance{}
	if got := inst.EffectiveZoom(); got != 1 {
		t.Fatalf("没设置时应该是 1，实际 %v", got)
	}
}

func TestEffectiveZoomKeepsNormalValues(t *testing.T) {
	for _, want := range []float64{0.5, 0.75, 1.25, 2, 3} {
		inst := component.Instance{Zoom: want}
		if got := inst.EffectiveZoom(); got != want {
			t.Fatalf("zoom=%v 应该原样保留，实际 %v", want, got)
		}
	}
}

// 手改 layout.json 是明摆着的用法，越界值必须在读的时候就夹回来，
// 而不是等引擎拒绝或者把页面渲染成一片空白。
//
// 「没设置」与「越界」分开处理：0/负数/NaN 一律当没设置（回原始大小），
// 正数但超出范围的夹到边界。理由是一份被手改坏的文件里，
// 「回到 1」比「按 0.5 显示」更容易让人看懂发生了什么。
func TestEffectiveZoomClamps(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0.01, component.MinZoom},
		{99, component.MaxZoom},
		{-2, component.DefaultZoom},
		{0, component.DefaultZoom},
		{math.NaN(), component.DefaultZoom},
	}
	for _, c := range cases {
		inst := component.Instance{Zoom: c.in}
		if got := inst.EffectiveZoom(); got != c.want {
			t.Fatalf("zoom=%v 应该夹到 %v，实际 %v", c.in, c.want, got)
		}
	}
}
