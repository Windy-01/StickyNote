package webview

import "testing"

// TestHitTestBand 钉住缩放边框的几何判定。
//
// 这段逻辑以前写在前端（mousemove 判边缘 → 写 data-cursor），踩过两个坑：
// 一是用 window.innerWidth 算宽度，它含滚动条、也不等于客户区宽度，
// 右/下两条边的判定会偏；二是 CSS cursor 一旦改成缩放箭头就再也回不来。
// 现在挪到宿主侧由 WM_NCHITTEST 回答，这段纯几何就是唯一需要保证对的地方。
func TestHitTestBand(t *testing.T) {
	const (
		cw, ch = 960, 660
		band   = 6
	)

	cases := []struct {
		name string
		x, y int32
		want uintptr
		hit  bool
	}{
		{"正中不算边框", 480, 330, 0, false},
		{"左边缘", 0, 330, htLeft, true},
		{"左边缘内侧", band - 1, 330, htLeft, true},
		{"刚出边框带", band, 330, 0, false},
		{"右边缘", cw - 1, 330, htRight, true},
		{"上边缘", 480, 0, htTop, true},
		{"下边缘", 480, ch - 1, htBottom, true},
		{"左上角", 0, 0, htTopLeft, true},
		{"右上角", cw - 1, 0, htTopRight, true},
		{"左下角", 0, ch - 1, htBottomLeft, true},
		{"右下角", cw - 1, ch - 1, htBottomRight, true},
	}

	for _, c := range cases {
		got, hit := hitTestBand(cw, ch, c.x, c.y, band)
		if hit != c.hit || got != c.want {
			t.Errorf("%s：(%d,%d) 得到 %d/%v，期望 %d/%v",
				c.name, c.x, c.y, got, hit, c.want, c.hit)
		}
	}
}

// TestHitTestBandTooSmall 钉住"窗口比两倍边框还窄"的退化情况：
// 否则整个窗口都会变成边框，用户点哪儿都在缩放。
func TestHitTestBandTooSmall(t *testing.T) {
	for _, size := range [][2]int32{{10, 660}, {960, 10}, {0, 0}, {12, 12}} {
		if _, hit := hitTestBand(size[0], size[1], 0, 0, 6); hit {
			t.Errorf("%dx%d 不该判成边框", size[0], size[1])
		}
	}
}
