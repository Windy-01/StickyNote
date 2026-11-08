package webview

import (
	"testing"
	"time"
)

// 节流是"画面会不会停在旧尺寸"的唯一防线：写错了不会报错，
// 只会表现为拖完之后内容还是原来那么大。所以把判定单独拎出来测。
func TestNeedSync(t *testing.T) {
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	gap := 6 * time.Millisecond

	cases := []struct {
		name  string
		now   time.Time
		last  time.Time
		force bool
		want  bool
	}{
		{"从没同步过", base, time.Time{}, false, true},
		{"强制同步总是生效", base, base.Add(-1 * time.Millisecond), true, true},
		{"距上次不足一帧", base, base.Add(-2 * time.Millisecond), false, false},
		{"距上次刚好一帧", base, base.Add(-gap), false, true},
		{"距上次超过一帧", base, base.Add(-20 * time.Millisecond), false, true},
	}
	for _, c := range cases {
		if got := needSync(c.now, c.last, gap, c.force); got != c.want {
			t.Errorf("%s：needSync=%v，期望 %v", c.name, got, c.want)
		}
	}
}

// 刷新率读数的兜底范围：读不到按 60Hz，读到离谱的值也不能让它变成
// "每帧都同步"（等于没节流）或"半天不同步"（内容明显落后）。
func TestScreenFrameInterval(t *testing.T) {
	d := screenFrameInterval()
	if d < 6*time.Millisecond || d > 20*time.Millisecond {
		t.Fatalf("一帧时长 %v 超出 6~20ms 的夹取范围", d)
	}
	// 幂等：反复读必须拿到同一个值，否则节流判定会来回跳。
	if again := screenFrameInterval(); again != d {
		t.Fatalf("两次读数不一致：%v vs %v", d, again)
	}
}
