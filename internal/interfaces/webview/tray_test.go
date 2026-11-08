package webview

import (
	"testing"
	"unsafe"
)

// NOTIFYICONDATAW 的布局必须与 Windows SDK 完全一致。
//
// 这个结构体最容易出错的地方不是字段名而是隐式对齐：x64 下 HWND/HICON 是
// 8 字节，如果漏了填充，cbSize 一错 Shell_NotifyIcon 就静默失败——返回
// false 但不给任何错误信息，图标就是不出现。所以用测试把偏移钉死。
func TestNotifyIconDataLayout(t *testing.T) {
	var n notifyIconDataW

	if got, want := unsafe.Sizeof(n), uintptr(976); got != want {
		t.Fatalf("NOTIFYICONDATAW 大小 = %d, want %d", got, want)
	}

	checks := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"hWnd", unsafe.Offsetof(n.HWnd), 8},
		{"uID", unsafe.Offsetof(n.UID), 16},
		{"uFlags", unsafe.Offsetof(n.UFlags), 20},
		{"uCallbackMessage", unsafe.Offsetof(n.UCallbackMessage), 24},
		{"hIcon", unsafe.Offsetof(n.HIcon), 32},
		{"szTip", unsafe.Offsetof(n.SzTip), 40},
		{"dwState", unsafe.Offsetof(n.DwState), 296},
		{"dwStateMask", unsafe.Offsetof(n.DwStateMask), 300},
		{"szInfo", unsafe.Offsetof(n.SzInfo), 304},
		{"szInfoTitle", unsafe.Offsetof(n.SzInfoTitle), 820},
		{"dwInfoFlags", unsafe.Offsetof(n.DwInfoFlags), 948},
		{"guidItem", unsafe.Offsetof(n.GuidItem), 952},
		{"hBalloonIcon", unsafe.Offsetof(n.HBalloonIcon), 968},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s 偏移 = %d, want %d", c.name, c.got, c.want)
		}
	}
}
