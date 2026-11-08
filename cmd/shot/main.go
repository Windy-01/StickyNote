// shot 把整块屏幕（或单个窗口）存成 BMP，纯诊断用。
//
// 排查"界面看起来不对"这类问题时，日志只能说明引擎没报错，
// 看不到到底画成了什么。这个工具补上那一眼。
//
// 两种取法：
//
//	shot <bmp> [x,y ...]     整屏截图 + 点采样
//	shot -w <句柄> <bmp>     只抓某个窗口——**被遮挡也能抓到**，
//	                         不用先把它弄到最上层，正好避开"读到别人画面"
//	                         这个假阴性
//
// 必须先声明 DPI 感知：否则 GetSystemMetrics 返回虚拟化尺寸（本机 2048x1152
// 而不是物理的 2560x1440），抓出来的图被缩过，坐标换算全错。
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"stickynote/internal/infrastructure/screen"
)

func main() {
	// -4 = DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2
	if _, err := screen.SetDpiAware(); err != nil {
		fmt.Println("提示: 设置 DPI 感知失败，坐标可能被虚拟化")
	}

	out := "shot.bmp"
	var px []string
	hwnd := uintptr(0)
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-w" && i+1 < len(args):
			i++
			v, err := strconv.ParseUint(strings.TrimPrefix(args[i], "0x"), 16, 64)
			if err != nil {
				fmt.Println("失败: -w 后面要跟窗口句柄（十六进制）")
				os.Exit(1)
			}
			hwnd = uintptr(v)
		case strings.Contains(a, ","):
			px = append(px, a)
		default:
			out = a
		}
	}

	if hwnd != 0 {
		if err := screen.PrintWindow(hwnd, out); err != nil {
			fmt.Println("失败:", err)
			os.Exit(1)
		}
		fmt.Printf("已保存 %s（窗口 %#x）\n", out, hwnd)
		return
	}

	w, h := screen.ScreenSize()
	if err := screen.SaveScreen(out, 0, 0, w, h); err != nil {
		fmt.Println("失败:", err)
		os.Exit(1)
	}
	fmt.Printf("已保存 %s（%dx%d）\n", out, w, h)

	if len(px) == 0 {
		return
	}
	// 采样点直接读同一块屏幕 DC，不再走文件，省一次解析。
	for _, p := range px {
		var x, y int
		if _, err := fmt.Sscanf(p, "%d,%d", &x, &y); err != nil {
			fmt.Printf("  %s: 坐标格式应为 x,y\n", p)
			continue
		}
		r, g, b, err := screen.PixelAt(x, y)
		if err != nil {
			fmt.Printf("  (%d,%d): %v\n", x, y, err)
			continue
		}
		fmt.Printf("  (%4d,%4d) = #%02X%02X%02X  rgb(%3d,%3d,%3d)\n", x, y, r, g, b, r, g, b)
	}
}
