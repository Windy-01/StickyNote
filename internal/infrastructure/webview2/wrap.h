// WebView2 Composition 的 C 接口。
//
// 为什么是 Composition 而不是普通模式：产品要「桌面层（Progman 子窗口）+
// 置顶层 + 点击穿透」。普通模式的 WebView2 自己建子窗口、自己管合成，
// 塞不进 DirectComposition 视觉树；Composition 模式把页面画进宿主给的
// IDCompositionVisual，扩展样式与 Z 序全部归宿主，两条需求才成立。
//
// 这一层只做「引擎搬运」，不认识组件、样式、布局——那些在上面几层。
#ifndef SN_WV2_WRAP_H
#define SN_WV2_WRAP_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef void *SN_WV2;

// ---------------------------------------------------------------------------
// 进程级：一个环境给所有窗口共用（每个环境是一整套浏览器进程，多开会翻倍）
// ---------------------------------------------------------------------------

// 启动环境创建（异步）。返回 0 表示已发起，非 0 是 HRESULT。
int32_t sn_wv2_start_env(const wchar_t *userDataDir, int32_t debug);

// 0 = 还没好，1 = 就绪，-1 = 失败。
int32_t sn_wv2_env_state(void);
int32_t sn_wv2_env_hr(void);

// 环境创建好之后才知道运行时版本，界面上显示用。返回实际写入的字符数。
int32_t sn_wv2_runtime_version(wchar_t *buf, int32_t cap);

// ---------------------------------------------------------------------------
// 每个窗口一个
// ---------------------------------------------------------------------------

// 建 DComp 视觉树并**发起**控制器创建，立即返回句柄。
// 之后用 sn_wv2_ready 轮询，就绪前不要调别的方法。
SN_WV2 sn_wv2_new(uintptr_t hwnd, uintptr_t user);

// 0 = 进行中，1 = 就绪，-1 = 失败。
int32_t sn_wv2_ready(SN_WV2 h);
int32_t sn_wv2_hr(SN_WV2 h);

// 释放控制器与 DComp 对象。窗口本身由调用方销毁。
void sn_wv2_free(SN_WV2 h);

// ---------------------------------------------------------------------------
// 页面
// ---------------------------------------------------------------------------

// 把虚拟主机名映射到一个真实目录，页面就能用相对路径加载自己的资源，
// 而且天然和别的组件隔离（不同主机名 = 不同源）。
int32_t sn_wv2_map_host(SN_WV2 h, const wchar_t *host, const wchar_t *folder);

// 在每次导航的文档创建时执行（早于页面自己的脚本）。
// 基座样式、桥接外壳、首屏样式都靠它，晚于页面就会闪一下。
int32_t sn_wv2_inject_script(SN_WV2 h, const wchar_t *js);

int32_t sn_wv2_navigate(SN_WV2 h, const wchar_t *url);

// 宿主 -> 页面。页面侧收到的是已解析的对象（window.chrome.webview 的 message 事件）。
int32_t sn_wv2_post_json(SN_WV2 h, const wchar_t *json);

// 执行脚本。结果异步回调，token 原样带回。
int32_t sn_wv2_eval(SN_WV2 h, const wchar_t *js, uintptr_t token);

// 尺寸变了要告诉引擎，否则页面按旧尺寸排版。
int32_t sn_wv2_resize(SN_WV2 h);

// 内容缩放：窗口尺寸不变，页面按倍数放大/缩小（1 表示原始大小）。
//
// 实现是栅格化缩放——它同时决定"一个 CSS 像素占几个物理像素"和排版视口
// 的大小，所以页面是真的按倍数重排，不是把位图拉大。屏幕 DPI 那份已经由
// 创建时设的 h->scale 承载，这里乘上去而不是覆盖它。
int32_t sn_wv2_set_zoom(SN_WV2 h, double zoom);

int32_t sn_wv2_set_devtools_enabled(SN_WV2 h, int32_t on);

// ---------------------------------------------------------------------------
// 输入
//
// Composition 模式不接管输入：窗口收到的鼠标消息必须由宿主转给引擎，
// 否则页面里点不动、也没有 hover。
// ---------------------------------------------------------------------------

// kind 直接用 COREWEBVIEW2_MOUSE_EVENT_KIND 的值（0x0200 起）。
// virtualKeys 是 COREWEBVIEW2_MOUSE_EVENT_VIRTUAL_KEYS 的位组合。
int32_t sn_wv2_send_mouse(SN_WV2 h, int32_t kind, int32_t virtualKeys,
                          int32_t mouseData, int32_t x, int32_t y);

// 让页面拿到键盘焦点。
int32_t sn_wv2_focus(SN_WV2 h);

#ifdef __cplusplus
}
#endif

#endif // SN_WV2_WRAP_H
