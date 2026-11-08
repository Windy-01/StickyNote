// Package screen 提供屏幕与窗口的像素抓取能力，纯诊断用。
//
// 与渲染引擎无关：走 GDI 的 BitBlt / PrintWindow 直接读合成后的结果，
// 所以无论页面是哪个引擎画的，抓出来都是最终画面。
package screen

import (
	"encoding/binary"
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modUser32 = windows.NewLazySystemDLL("user32.dll")
	modGdi32  = windows.NewLazySystemDLL("gdi32.dll")

	procGetSystemMetrics   = modUser32.NewProc("GetSystemMetrics")
	procGetDC              = modUser32.NewProc("GetDC")
	procReleaseDC          = modUser32.NewProc("ReleaseDC")
	procGetPixel           = modGdi32.NewProc("GetPixel")
	procSetDpiContext      = modUser32.NewProc("SetProcessDpiAwarenessContext")
	procGetWindowRect      = modUser32.NewProc("GetWindowRect")
	procPrintWindow        = modUser32.NewProc("PrintWindow")
	procCreateCompatibleDC = modGdi32.NewProc("CreateCompatibleDC")
	procDeleteDC           = modGdi32.NewProc("DeleteDC")
	procCreateDIBSection   = modGdi32.NewProc("CreateDIBSection")
	procSelectObject       = modGdi32.NewProc("SelectObject")
	procDeleteObject       = modGdi32.NewProc("DeleteObject")
	procBitBlt             = modGdi32.NewProc("BitBlt")
)

const (
	smCXScreen = 0
	smCYScreen = 1

	srcCopy      = 0x00CC0020
	dibRGBColors = 0

	// pwRenderFullContent 让 PrintWindow 走完整渲染路径，
	// 分层窗口与 DirectComposition 内容也能取到（0 只能取到窗口自己画的）。
	pwRenderFullContent = 2
)

type bitmapInfoHeader struct {
	size          uint32
	width         int32
	height        int32
	planes        uint16
	bitCount      uint16
	compression   uint32
	sizeImage     uint32
	xPelsPerMeter int32
	yPelsPerMeter int32
	clrUsed       uint32
	clrImportant  uint32
}

type bitmapInfo struct {
	header bitmapInfoHeader
	colors [3]uint32
}

type rect struct {
	left, top, right, bottom int32
}

// ScreenSize 返回主屏像素尺寸。
func ScreenSize() (w, h int) {
	cw, _, _ := procGetSystemMetrics.Call(smCXScreen)
	ch, _, _ := procGetSystemMetrics.Call(smCYScreen)
	return int(cw), int(ch)
}

// SetDpiAware 声明进程为 per-monitor v2 DPI 感知，返回非 0 表示成功。
//
// 诊断工具必须调：否则 GetSystemMetrics 与屏幕 DC 都按 96DPI 虚拟化，
// 读到的坐标与真实物理像素差一个缩放倍率，量出来的结论是错的。
func SetDpiAware() (uintptr, error) {
	r, _, err := procSetDpiContext.Call(^uintptr(3)) // -4
	if r == 0 {
		return 0, err
	}
	return r, nil
}

// PixelAt 读屏幕上一点的 RGB。纯诊断用。
func PixelAt(x, y int) (r, g, b int, err error) {
	dc, _, _ := procGetDC.Call(0)
	if dc == 0 {
		return 0, 0, 0, errors.New("取屏幕 DC 失败")
	}
	defer procReleaseDC.Call(0, dc)

	// GetPixel 返回 COLORREF（0x00BBGGRR），失败时是 CLR_INVALID（0xFFFFFFFF）。
	v, _, _ := procGetPixel.Call(dc, uintptr(int32(x)), uintptr(int32(y)))
	if uint32(v) == 0xFFFFFFFF {
		return 0, 0, 0, errors.New("读像素失败（坐标越界？）")
	}
	return int(v & 0xFF), int((v >> 8) & 0xFF), int((v >> 16) & 0xFF), nil
}

// SaveScreen 把屏幕上一个矩形存成 BMP。纯诊断用。
func SaveScreen(path string, x, y, w, h int) error {
	if w <= 0 || h <= 0 {
		return errors.New("截图尺寸非法")
	}
	dc, _, _ := procGetDC.Call(0)
	if dc == 0 {
		return errors.New("取屏幕 DC 失败")
	}
	defer procReleaseDC.Call(0, dc)

	bits, bmp, mem, err := newDIB(dc, w, h)
	if err != nil {
		return err
	}
	defer releaseDIB(bmp, mem)

	ok, _, _ := procBitBlt.Call(mem, 0, 0, uintptr(w), uintptr(h), dc,
		uintptr(x), uintptr(y), srcCopy)
	if ok == 0 {
		return errors.New("BitBlt 失败")
	}
	return writeBMP(path, w, h, bits)
}

// PrintWindow 把一个窗口的内容存成 BMP（被遮挡、分层窗口都能取到）。纯诊断用。
func PrintWindow(hwnd uintptr, path string) error {
	if hwnd == 0 {
		return errors.New("窗口句柄为空")
	}
	var rc rect
	ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	if ok == 0 {
		return errors.New("取窗口矩形失败")
	}
	w, h := int(rc.right-rc.left), int(rc.bottom-rc.top)
	if w <= 0 || h <= 0 {
		return errors.New("窗口尺寸非法")
	}

	dc, _, _ := procGetDC.Call(0)
	if dc == 0 {
		return errors.New("取屏幕 DC 失败")
	}
	defer procReleaseDC.Call(0, dc)

	bits, bmp, mem, err := newDIB(dc, w, h)
	if err != nil {
		return err
	}
	defer releaseDIB(bmp, mem)

	ok, _, _ = procPrintWindow.Call(hwnd, mem, pwRenderFullContent)
	if ok == 0 {
		return errors.New("PrintWindow 失败")
	}
	return writeBMP(path, w, h, bits)
}

// newDIB 建一块 32 位自上而下的 DIB 及其内存 DC，返回像素首地址。
func newDIB(dc uintptr, w, h int) (bits unsafe.Pointer, bmp, mem uintptr, err error) {
	mem, _, _ = procCreateCompatibleDC.Call(dc)
	if mem == 0 {
		err = errors.New("建内存 DC 失败")
		return
	}
	var bi bitmapInfo
	bi.header.size = uint32(unsafe.Sizeof(bi.header))
	bi.header.width = int32(w)
	bi.header.height = -int32(h) // 负 = 自上而下，省一次翻转
	bi.header.planes = 1
	bi.header.bitCount = 32
	bi.header.compression = 0 // BI_RGB

	bmp, _, _ = procCreateDIBSection.Call(mem, uintptr(unsafe.Pointer(&bi)),
		dibRGBColors, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == nil {
		procDeleteDC.Call(mem)
		err = errors.New("建 DIB 失败")
		return
	}
	procSelectObject.Call(mem, bmp)
	return
}

func releaseDIB(bmp, mem uintptr) {
	procDeleteObject.Call(bmp)
	procDeleteDC.Call(mem)
}

// writeBMP 把一行行 BGRA 像素写成 BMP。px 是自上而下的，文件按自下而上写。
func writeBMP(path string, w, h int, px unsafe.Pointer) error {
	if px == nil || w <= 0 || h <= 0 {
		return errors.New("像素数据非法")
	}
	const fileHeaderSize = 14
	const infoHeaderSize = 40
	stride := w * 4
	imgBytes := stride * h

	out := make([]byte, fileHeaderSize+infoHeaderSize+imgBytes)
	le := binary.LittleEndian

	// BITMAPFILEHEADER
	out[0], out[1] = 'B', 'M'
	le.PutUint32(out[2:], uint32(len(out)))
	le.PutUint32(out[10:], fileHeaderSize+infoHeaderSize)

	// BITMAPINFOHEADER
	hdr := out[fileHeaderSize:]
	le.PutUint32(hdr[0:], infoHeaderSize)
	le.PutUint32(hdr[4:], uint32(w))
	le.PutUint32(hdr[8:], uint32(h)) // 正数 = 自下而上
	le.PutUint16(hdr[12:], 1)
	le.PutUint16(hdr[14:], 32)
	le.PutUint32(hdr[16:], 0) // BI_RGB
	le.PutUint32(hdr[20:], uint32(imgBytes))

	// 内存里是自上而下，文件要自下而上 → 逐行倒着搬。
	src := unsafe.Slice((*byte)(px), imgBytes)
	dst := out[fileHeaderSize+infoHeaderSize:]
	for row := 0; row < h; row++ {
		copy(dst[row*stride:(row+1)*stride], src[(h-1-row)*stride:(h-row)*stride])
	}

	return os.WriteFile(path, out, 0o644)
}
