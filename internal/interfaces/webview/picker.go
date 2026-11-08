package webview

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// maxPathChars 是 MAX_PATH。SHGetPathFromIDListW 不会返回更长的路径。
const maxPathChars = 260

// pickFolder 弹出系统的"选择文件夹"对话框。
//
// 用户取消时返回空串（不是错误）：取消是正常操作，不该当成失败上报。
// 必须在 UI 线程调用——它会跑自己的模态消息循环。
func pickFolder(owner uintptr, title string) string {
	var display [maxPathChars]uint16
	bi := browseInfoW{
		Owner:       owner,
		DisplayName: &display[0],
		Title:       utf16Ptr(title),
		// RETURNONLYFSDIRS：只能选真实文件夹，不能选"此电脑"这类虚拟节点。
		// NEWDIALOGSTYLE 让它长得像 Vista 之后的对话框，还能直接在地址栏输路径。
		Flags: bifReturnOnlyFSDirs | bifEditBox | bifNewDialogStyle,
	}

	// 返回的是一份由 COM 分配器给出的 PIDL，必须用 CoTaskMemFree 释放。
	// 这里用 defer 而不是靠 GC：它不是 Go 的堆内存。
	pidl, _, _ := procSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return ""
	}
	defer procCoTaskMemFree.Call(pidl)

	var buf [maxPathChars]uint16
	ok, _, _ := procSHGetPathFromIDList.Call(pidl, uintptr(unsafe.Pointer(&buf[0])))
	if ok == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:])
}

// openInExplorer 用资源管理器打开一个路径。
//
// ShellExecuteW 找不到关联程序时会弹系统错误框，这里不去拦：
// 能走到这一步的路径都是本程序自己创建过的目录。
func openInExplorer(path string) {
	verb := utf16Ptr("open")
	target := utf16Ptr(path)
	if verb == nil || target == nil {
		return
	}
	procShellExecuteW.Call(0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(target)),
		0, 0, swShownormal)
}
