package webview

// loadIcon 从 exe 图标资源里取出指定边长的应用图标。
//
// 资源缺失时退回系统通用图标：宁可图标不好看，也不要托盘或窗口留一个空位。
// 句柄带 LR_SHARED，由系统缓存，调用方不要 DestroyIcon。
func loadIcon(size int) uintptr {
	hInst, _, _ := procGetModuleHandleW.Call(0)
	h, _, _ := procLoadImageW.Call(
		hInst, iconResourceID, imageIcon,
		uintptr(size), uintptr(size), lrShared,
	)
	if h != 0 {
		return h
	}
	fallback, _, _ := procLoadIconW.Call(0, idiApplication)
	return fallback
}

// systemIconSize 返回系统建议的图标边长（已按 DPI 缩放）。
// small 为 true 取小图标尺寸（托盘用），否则取大图标尺寸。
func systemIconSize(small bool) int {
	idx := uintptr(smCXIcon)
	fallback := 32
	if small {
		idx = smCXSmIcon
		fallback = 16
	}
	if v, _, _ := procGetSystemMetrics.Call(idx); v > 0 {
		return int(v)
	}
	return fallback
}
