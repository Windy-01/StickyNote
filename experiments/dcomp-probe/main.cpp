// DirectComposition 合成探针
//
// 目的：在**不接任何 WebView** 的前提下，先回答一个决定性的问题——
//      DirectComposition 的内容，能不能同时存在于
//        ① 分层窗口（WS_EX_LAYERED，点击穿透的唯一可靠写法）
//        ② 桌面窗口的子窗口（SetParent 到 Progman）
//
// 为什么先问这个：WebView2 Composition 模式要求内容必须接进 DComp 视觉树
// （ICoreWebView2CompositionController::put_RootVisualTarget 只接受
// IDCompositionVisual）。而穿透要求 WS_EX_LAYERED，DComp 的标准用法又要求
// WS_EX_NOREDIRECTIONBITMAP。这两个扩展样式在 Windows 上是互斥的合成路径：
//   - NOREDIRECTIONBITMAP：窗口不参与 DWM 重定向，内容由 DComp 自己合成
//   - LAYERED：窗口必须走 DWM 重定向
// 如果两者不能共存，Composition 路线在"透明 + 穿透"这条需求上直接出局，
// 后面 1000 行 Composition 胶水一行都不用写。
//
// 判定方法：
//   - 内容：往 DComp surface 填纯洋红（R=255,G=0,B=255），从屏幕抓窗口矩形
//           数洋红像素。>1000 视为画出来了。
//   - 穿透：ChildWindowFromPointEx(CWP_SKIPTRANSPARENT|...) 取窗口中心点的
//           命中结果，祖先链里不含本窗口 = 穿透成立。
//
// 每一项都重建窗口，避免扩展样式互相污染。

#include <windows.h>
#include <d3d11.h>
#include <dxgi.h>
#include <dxgi1_2.h>

// MinGW 8.1 的 dcomp.h 在 IDCompositionTarget 里用到了 IDCompositionVisual，
// 但它的定义在文件更后面，导致被解析成 int。补一个前向声明绕过去。
struct IDCompositionVisual;

#include <dcomp.h>
#include <stdio.h>
#include <stdlib.h>

#ifndef WS_EX_NOREDIRECTIONBITMAP
#define WS_EX_NOREDIRECTIONBITMAP 0x00200000L
#endif

// MinGW 的 dcomp.h 不生成 IID 符号（__CRT_UUID_DECL 分支没走到），
// 所以这几个 GUID 从 DECLARE_INTERFACE_IID_ 里抄出来手写，避免链接依赖。
static const GUID kIID_DCompDevice = {
    0xc37ea93a, 0xe7aa, 0x450d, {0xb1, 0x6f, 0x97, 0x46, 0xcb, 0x04, 0x07, 0xf3}};
static const GUID kIID_DXGISurface = {
    0xcafcb56c, 0x6ac3, 0x4889, {0xbf, 0x47, 0x9e, 0x23, 0xbb, 0xd2, 0x60, 0xec}};
static const GUID kIID_DXGIDevice = {
    0x54ec77fa, 0x1377, 0x44e6, {0x8c, 0x32, 0x88, 0xfd, 0x5f, 0x44, 0xc8, 0x4c}};
static const GUID kIID_D3D11Texture2D = {
    0x6f15aaf2, 0xd208, 0x4e89, {0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c}};

typedef HRESULT(WINAPI *PFN_DCompositionCreateDevice)(IDXGIDevice *, REFIID, void **);

static ID3D11Device *g_d3d = nullptr;
static ID3D11DeviceContext *g_ctx = nullptr;
static ID3D11Texture2D *g_staging = nullptr; // CPU 可写的洋红图，一次填好反复用
static IDXGIDevice *g_dxgi = nullptr;
static PFN_DCompositionCreateDevice g_createDComp = nullptr;

static const int WIN_W = 420;
static const int WIN_H = 260;
// 落点由 pickFreeOrigin 在运行时挑一个「没被应用窗口盖住」的位置。
// 之所以不能写死：countMagenta 抓的是屏幕像素，窗口被别的应用盖住时
// 抓到的是那个应用，洋红必然是 0 —— 会把「内容画出来了」误判成「没画出来」。
static int WIN_X = 300;
static int WIN_Y = 300;

// ---------------------------------------------------------------------------
// 窗口
// ---------------------------------------------------------------------------

// 对照组开关：置 true 时窗口用 GDI 直接填洋红（不走 DComp）。
// 用途：如果「挂 Progman + GDI」也数不到洋红，说明是窗口被别的应用遮住了，
// 而不是 DComp 不合成 —— 这是区分「真失败」和「假阴性」的唯一办法。
static bool g_gdiFill = false;

static LRESULT CALLBACK probeProc(HWND h, UINT m, WPARAM w, LPARAM l) {
  switch (m) {
    case WM_PAINT: {
      if (g_gdiFill) {
        PAINTSTRUCT ps;
        HDC dc = BeginPaint(h, &ps);
        RECT rc;
        GetClientRect(h, &rc);
        HBRUSH br = CreateSolidBrush(RGB(255, 0, 255));
        FillRect(dc, &rc, br);
        DeleteObject(br);
        EndPaint(h, &ps);
        return 0;
      }
      // NOREDIRECTIONBITMAP 的窗口没有重定向表面，不能 BeginPaint。
      // 内容全部由 DComp 合成，这里只把脏区标记为已处理。
      ValidateRect(h, nullptr);
      return 0;
    }
    case WM_ERASEBKGND:
      return 1;
    default:
      return DefWindowProcW(h, m, w, l);
  }
}

static void registerClass(void) {
  WNDCLASSEXW wc;
  ZeroMemory(&wc, sizeof(wc));
  wc.cbSize = sizeof(wc);
  wc.lpfnWndProc = probeProc;
  wc.hInstance = GetModuleHandleW(nullptr);
  wc.hCursor = LoadCursorW(nullptr, (LPCWSTR)IDC_ARROW);
  wc.lpszClassName = L"DCompProbeWindow";
  wc.hbrBackground = nullptr;
  RegisterClassExW(&wc);
}

static HWND makeWindow(DWORD exStyle) {
  HWND h = CreateWindowExW(exStyle, L"DCompProbeWindow", L"DCompProbe",
                           WS_POPUP, WIN_X, WIN_Y, WIN_W, WIN_H, nullptr,
                           nullptr, GetModuleHandleW(nullptr), nullptr);
  if (!h) return nullptr;
  ShowWindow(h, SW_SHOWNOACTIVATE);
  SetWindowPos(h, HWND_TOPMOST, WIN_X, WIN_Y, WIN_W, WIN_H,
               SWP_NOACTIVATE | SWP_SHOWWINDOW);
  return h;
}

// 找"桌面图标宿主"：含 SHELLDLL_DefView 子窗口的那个顶层窗口。
// Win10/11 上它有时是 Progman，有时是某个 WorkerW。
static HWND findIconHost(void) {
  HWND prog = FindWindowW(L"Progman", nullptr);
  if (prog && FindWindowExW(prog, nullptr, L"SHELLDLL_DefView", nullptr)) return prog;

  HWND w = nullptr;
  while ((w = FindWindowExW(nullptr, w, L"WorkerW", nullptr)) != nullptr) {
    if (FindWindowExW(w, nullptr, L"SHELLDLL_DefView", nullptr)) return w;
  }
  return prog;
}

// ---------------------------------------------------------------------------
// 无遮挡落点
// ---------------------------------------------------------------------------

// 这几个类名都算「桌面本身」，落在它们上面等于没被应用盖住。
static bool isDesktopClass(HWND w) {
  wchar_t cls[64] = {0};
  if (!GetClassNameW(w, cls, 64)) return false;
  return !lstrcmpW(cls, L"Progman") || !lstrcmpW(cls, L"WorkerW") ||
         !lstrcmpW(cls, L"SHELLDLL_DefView") || !lstrcmpW(cls, L"SysListView32");
}

// 该点最底层的命中窗口（一路钻到子窗口链末端）。
// 注意 ChildWindowFromPointEx 收的是**父窗口客户区坐标**，每下一层都要换算，
// 否则窗口一旦不在 (0,0) 就会钻错分支。
static HWND bottomHitAt(POINT screenPt) {
  HWND w = GetDesktopWindow();
  POINT pt = screenPt;
  for (int i = 0; i < 32; i++) {
    HWND child = ChildWindowFromPointEx(w, pt, CWP_SKIPINVISIBLE |
                                                 CWP_SKIPDISABLED);
    if (!child || child == w) break;
    w = child;
    pt = screenPt;
    MapWindowPoints(nullptr, w, &pt, 1); // 屏幕 -> w 客户区
  }
  return w;
}

// 扫网格找一个整块矩形都没被应用窗口盖住的落点。
// 取 5 个采样点（四角内缩 + 中心），全落在桌面类窗口上才算数。
static bool pickFreeOrigin(int *outX, int *outY) {
  int vx = GetSystemMetrics(SM_XVIRTUALSCREEN);
  int vy = GetSystemMetrics(SM_YVIRTUALSCREEN);
  int vw = GetSystemMetrics(SM_CXVIRTUALSCREEN);
  int vh = GetSystemMetrics(SM_CYVIRTUALSCREEN);
  const int inset = 6;
  for (int y = vy; y + WIN_H <= vy + vh; y += 40) {
    for (int x = vx; x + WIN_W <= vx + vw; x += 40) {
      POINT pts[5] = {{x + inset, y + inset},
                      {x + WIN_W - inset, y + inset},
                      {x + inset, y + WIN_H - inset},
                      {x + WIN_W - inset, y + WIN_H - inset},
                      {x + WIN_W / 2, y + WIN_H / 2}};
      bool ok = true;
      for (int i = 0; i < 5 && ok; i++) {
        HWND w = bottomHitAt(pts[i]);
        ok = w && isDesktopClass(w);
      }
      if (ok) {
        *outX = x;
        *outY = y;
        return true;
      }
    }
  }
  return false;
}

// ---------------------------------------------------------------------------
// DirectComposition
// ---------------------------------------------------------------------------

// 建一张洋红源纹理，供每次 BeginDraw 拷贝。
//
// 用 DEFAULT + UpdateSubresource，不用 STAGING + Map：
// staging 纹理虽然文档上允许做 CopySubresourceRegion 的源，
// 但实测在这个驱动上拷不出内容，换成 DEFAULT 直接写。
static bool initSource(void) {
  D3D11_TEXTURE2D_DESC d;
  ZeroMemory(&d, sizeof(d));
  d.Width = WIN_W;
  d.Height = WIN_H;
  d.MipLevels = 1;
  d.ArraySize = 1;
  d.Format = DXGI_FORMAT_B8G8R8A8_UNORM;
  d.SampleDesc.Count = 1;
  d.Usage = D3D11_USAGE_DEFAULT;
  if (FAILED(g_d3d->CreateTexture2D(&d, nullptr, &g_staging))) return false;

  BYTE *px = (BYTE *)malloc((size_t)WIN_W * WIN_H * 4);
  if (!px) return false;
  for (int i = 0; i < WIN_W * WIN_H; i++) {
    px[i * 4 + 0] = 255; // B
    px[i * 4 + 1] = 0;   // G
    px[i * 4 + 2] = 255; // R
    px[i * 4 + 3] = 255; // A
  }
  g_ctx->UpdateSubresource(g_staging, 0, nullptr, px, WIN_W * 4, 0);
  free(px);
  return true;
}

// DComp 的三个对象必须一直活着。
//
// 这是最容易踩的坑：CreateTargetForHwnd 返回的 target 一旦 Release 到 0 就销毁，
// 窗口的合成树跟着被拆掉，内容立刻消失，而且**没有任何错误码**。
// 所以三个对象都交回给调用方，等窗口销毁时再按 visual -> target -> device 顺序放。
struct DCompHandles {
  IDCompositionDevice *dev;
  IDCompositionTarget *target;
  IDCompositionVisual *visual;
};

// 把 DComp 接到窗口上，并画一块纯洋红。
static bool attachDComp(HWND h, DCompHandles *out) {
  out->dev = nullptr;
  out->target = nullptr;
  out->visual = nullptr;

  IDCompositionDevice *dev = nullptr;
  HRESULT hr = g_createDComp(g_dxgi, kIID_DCompDevice, (void **)&dev);
  if (FAILED(hr)) {
    printf("    CreateDevice hr=0x%08lX\n", hr);
    return false;
  }

  IDCompositionTarget *target = nullptr;
  hr = dev->CreateTargetForHwnd(h, TRUE, &target);
  if (FAILED(hr)) {
    printf("    CreateTargetForHwnd hr=0x%08lX\n", hr);
    dev->Release();
    return false;
  }

  IDCompositionVisual *visual = nullptr;
  hr = dev->CreateVisual(&visual);
  if (FAILED(hr)) {
    printf("    CreateVisual hr=0x%08lX\n", hr);
    target->Release();
    dev->Release();
    return false;
  }

  // 填一块洋红。
  //
  // 注意：IDCompositionSurface 背后是 GPU 纹理，BeginDraw 拿到的 IDXGISurface
  // 不能 Map（第一次就是这么失败的）。正确做法是 BeginDraw 直接要一张
  // ID3D11Texture2D，然后从 CPU 可写的 staging 纹理拷进去。
  IDCompositionSurface *surf = nullptr;
  hr = dev->CreateSurface(WIN_W, WIN_H, DXGI_FORMAT_B8G8R8A8_UNORM,
                          DXGI_ALPHA_MODE_PREMULTIPLIED, &surf);
  if (FAILED(hr)) {
    printf("    CreateSurface hr=0x%08lX\n", hr);
  } else {
    POINT off = {0, 0};
    ID3D11Texture2D *dst = nullptr;
    hr = surf->BeginDraw(nullptr, kIID_D3D11Texture2D, (void **)&dst, &off);
    if (FAILED(hr)) {
      printf("    BeginDraw hr=0x%08lX\n", hr);
    } else {
      if (g_ctx && g_staging) {
        g_ctx->CopySubresourceRegion(dst, 0, (UINT)off.x, (UINT)off.y, 0,
                                     g_staging, 0, nullptr);
      } else {
        printf("    没有 source 纹理，跳过拷贝\n");
      }
      dst->Release();
      HRESULT endHr = surf->EndDraw();
      if (FAILED(endHr)) printf("    EndDraw hr=0x%08lX\n", endHr);
      printf("    surface atlas offset=(%ld,%ld)\n", off.x, off.y);
    }
    visual->SetContent(surf);
    surf->Release();
  }

  target->SetRoot(visual);
  hr = dev->Commit();
  if (FAILED(hr)) printf("    Commit hr=0x%08lX\n", hr);

  out->dev = dev;
  out->target = target;
  out->visual = visual;
  return true;
}

// ---------------------------------------------------------------------------
// 判定
// ---------------------------------------------------------------------------

static void pump(int ms) {
  DWORD end = GetTickCount() + ms;
  MSG msg;
  while (GetTickCount() < end) {
    while (PeekMessageW(&msg, nullptr, 0, 0, PM_REMOVE)) {
      TranslateMessage(&msg);
      DispatchMessageW(&msg);
    }
    Sleep(10);
  }
}

// 从屏幕抓窗口矩形，数洋红像素。窗口必须可见且在最上层。
static int countMagenta(HWND h) {
  RECT rc;
  if (!GetWindowRect(h, &rc)) return -1;
  int w = rc.right - rc.left, ht = rc.bottom - rc.top;
  if (w <= 0 || ht <= 0) return -1;

  HDC screen = GetDC(nullptr);
  HDC mem = CreateCompatibleDC(screen);
  HBITMAP bmp = CreateCompatibleBitmap(screen, w, ht);
  HGDIOBJ old = SelectObject(mem, bmp);
  BitBlt(mem, 0, 0, w, ht, screen, rc.left, rc.top, SRCCOPY);

  BITMAPINFO bi;
  ZeroMemory(&bi, sizeof(bi));
  bi.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
  bi.bmiHeader.biWidth = w;
  bi.bmiHeader.biHeight = -ht;
  bi.bmiHeader.biPlanes = 1;
  bi.bmiHeader.biBitCount = 32;
  bi.bmiHeader.biCompression = BI_RGB;

  BYTE *px = (BYTE *)malloc((size_t)w * ht * 4);
  int n = -1;
  if (px && GetDIBits(mem, bmp, 0, (UINT)ht, px, &bi, DIB_RGB_COLORS)) {
    n = 0;
    for (int i = 0; i < w * ht; i++) {
      BYTE b = px[i * 4 + 0], g = px[i * 4 + 1], r = px[i * 4 + 2];
      if (r > 180 && b > 180 && g < 90) n++;
    }
  }
  free(px);
  SelectObject(mem, old);
  DeleteObject(bmp);
  DeleteDC(mem);
  ReleaseDC(nullptr, screen);
  return n;
}

// 命中测试：窗口中心点是否落在自己身上。false = 穿透成立。
//
// 必须用 ChildWindowFromPointEx 而不是 WindowFromPoint：后者只跳过隐藏/禁用，
// 不看 WS_EX_TRANSPARENT，会给假结论。
static bool hitSelf(HWND h) {
  RECT rc;
  if (!GetWindowRect(h, &rc)) return true;
  POINT pt;
  pt.x = (rc.left + rc.right) / 2;
  pt.y = (rc.top + rc.bottom) / 2;

  HWND probes[2];
  probes[0] = GetDesktopWindow();
  probes[1] = GetAncestor(h, GA_PARENT);
  if (probes[1] == probes[0]) probes[1] = nullptr;

  for (int i = 0; i < 2; i++) {
    if (!probes[i]) continue;
    HWND hit = ChildWindowFromPointEx(probes[i], pt,
                                      CWP_SKIPTRANSPARENT | CWP_SKIPINVISIBLE |
                                          CWP_SKIPDISABLED);
    while (hit) {
      if (hit == h) return true;
      hit = GetParent(hit);
    }
  }
  return false;
}

// 遮挡检测：窗口中心点**最上层**是谁（不跳过 WS_EX_TRANSPARENT）。
//
// 为什么必须查：countMagenta 是从屏幕 DC 抓像素，抓到的是"最终合成结果"。
// 如果探针窗口被别的应用盖住，抓到的是那个应用 —— 洋红 0 就变成了假阴性。
static const wchar_t *topClassAt(HWND h, wchar_t *buf, int cap) {
  RECT rc;
  if (!GetWindowRect(h, &rc)) return L"(取几何失败)";
  POINT pt;
  pt.x = (rc.left + rc.right) / 2;
  pt.y = (rc.top + rc.bottom) / 2;

  HWND hit = bottomHitAt(pt);
  if (!hit) return L"(空)";
  buf[0] = 0;
  GetClassNameW(hit, buf, cap);
  return buf;
}

// 本窗口此刻是否真的处在该点最上层（即屏幕像素能反映它的内容）。
static bool isSelfOnTop(HWND h) {
  RECT rc;
  if (!GetWindowRect(h, &rc)) return false;
  POINT pt;
  pt.x = (rc.left + rc.right) / 2;
  pt.y = (rc.top + rc.bottom) / 2;
  return bottomHitAt(pt) == h;
}

// ---------------------------------------------------------------------------
// 单项验证
// ---------------------------------------------------------------------------

struct Case {
  const char *name;
  DWORD exStyle;
  bool layered;
  bool toDesktop;   // SetParent 到图标宿主（子窗口）
  bool zAboveIcons; // 不 SetParent，只把 Z 序排到图标宿主正上方（仍是顶层窗口）
  bool gdiOnly;     // 对照组：用 GDI 填色，不接 DComp
};

static void runCase(const Case &c) {
  printf("\n== %s ==\n", c.name);
  printf("   exstyle=0x%08lX%s%s%s\n", c.exStyle,
         c.layered ? " (SetLayeredWindowAttributes)" : "",
         c.toDesktop ? " (SetParent -> 图标宿主)" : "",
         c.zAboveIcons ? " (Z序 -> 图标宿主正上方)" : "");

  HWND h = makeWindow(c.exStyle);
  if (!h) {
    printf("   创建窗口失败 err=%lu\n", GetLastError());
    return;
  }
  if (c.layered) SetLayeredWindowAttributes(h, 0, 255, LWA_ALPHA);

  if (c.toDesktop) {
    HWND host = findIconHost();
    if (!host) {
      printf("   找不到图标宿主\n");
    } else {
      SetParent(h, host);
      // 挂上去之后 SetWindowPos 收的是父客户坐标；图标宿主的客户区原点
      // 基本就是屏幕原点，这里直接沿用屏幕坐标。
      SetWindowPos(h, HWND_TOP, WIN_X, WIN_Y, WIN_W, WIN_H,
                   SWP_NOACTIVATE | SWP_SHOWWINDOW);
      wchar_t cls[64] = {0};
      GetClassNameW(host, cls, 64);
      printf("   已挂到 %ls (0x%p)\n", cls, (void *)host);
    }
  }

  // 路线 B：不 SetParent（保住顶层窗口身份，DComp 才能建 target），
  // 只把 Z 序插到图标宿主**正上方**。这样应用窗口永远在本窗口之上，
  // 图标永远在本窗口之下 —— 正是「桌面组件」要的位置。
  if (c.zAboveIcons) {
    HWND host = findIconHost();
    if (!host) {
      printf("   找不到图标宿主\n");
    } else {
      wchar_t hcls[64] = {0};
      GetClassNameW(host, hcls, 64);
      // SetWindowPos 的 hWndInsertAfter 语义是「本窗口排在这个窗口**之下**」。
      // 所以要落到宿主正上方，必须传"紧邻宿主上方的那个窗口"，不能直接传宿主
      // （直接传宿主会把自己插到宿主下面，反而被桌面盖住）。
      HWND above = GetWindow(host, GW_HWNDPREV);
      HWND insertAfter = above ? above : HWND_BOTTOM;
      SetWindowPos(h, insertAfter, WIN_X, WIN_Y, WIN_W, WIN_H,
                   SWP_NOACTIVATE | SWP_SHOWWINDOW);
      HWND prev = GetWindow(h, GW_HWNDPREV);
      wchar_t pcls[64] = {0};
      if (prev) GetClassNameW(prev, pcls, 64);
      // GW_HWNDPREV 只是"紧邻的上一个"，不紧邻不代表不在宿主之上。
      // 沿 Z 序往下走一遍，看宿主在不在本窗口下面。
      bool hostBelow = false;
      HWND w = h;
      for (int i = 0; i < 400 && (w = GetWindow(w, GW_HWNDNEXT)) != nullptr; i++) {
        if (w == host) {
          hostBelow = true;
          break;
        }
      }
      printf("   图标宿主=%ls，紧邻上方窗口=%ls\n", hcls, prev ? pcls : L"(无)");
      printf("   宿主是否在本窗口之下=%s%s\n", hostBelow ? "是" : "否",
             hostBelow ? "  ← 「图标之上」成立" : "  ← 位置不对");
      printf("   topmost=%s\n",
             (GetWindowLongPtrW(h, GWL_EXSTYLE) & WS_EX_TOPMOST) ? "是" : "否");
    }
  }



  DCompHandles dc;
  ZeroMemory(&dc, sizeof(dc));
  if (c.gdiOnly) {
    g_gdiFill = true;
    printf("   对照组：GDI 填色，不接 DComp\n");
    RedrawWindow(h, nullptr, nullptr,
                 RDW_INVALIDATE | RDW_ERASE | RDW_UPDATENOW);
  } else {
    g_gdiFill = false;
    bool ok = attachDComp(h, &dc);
    printf("   DComp 挂载=%s\n", ok ? "OK" : "FAIL");
  }

  // 等 DWM 合成两帧
  pump(600);

  // 关键：先确认自己真的在最上层，否则屏幕像素反映的是别人。
  // 不做这一步就会把"被遮挡"读成"内容没画出来"（上一轮就是这么误判的）。
  bool onTop = false;
  for (int i = 0; i < 25 && !onTop; i++) {
    onTop = isSelfOnTop(h);
    if (!onTop) pump(200);
  }

  wchar_t cls[64];
  printf("   该点最上层窗口=%ls  可测量=%s\n", topClassAt(h, cls, 64),
         onTop ? "是" : "否");

  int magenta = countMagenta(h);
  bool self = hitSelf(h);
  g_gdiFill = false;

  if (!onTop) {
    printf("   洋红像素=—（被其他窗口遮挡，本次不作判定）\n");
  } else {
    printf("   洋红像素=%d  %s\n", magenta,
           magenta > 1000 ? "内容已显示" : "内容未显示");
  }
  printf("   命中自己=%s  %s\n", self ? "是" : "否",
         self ? "不穿透" : "穿透成立");

  if (dc.visual) dc.visual->Release();
  if (dc.target) dc.target->Release();
  if (dc.dev) dc.dev->Release();
  DestroyWindow(h);
  pump(200);
}

// ---------------------------------------------------------------------------

int main(void) {
  // MinGW 8.1 的头文件里没有 DPI 感知函数，动态取一下。
  // 不设的话进程是 DPI-unaware，GetWindowRect 给逻辑像素，BitBlt 抓屏会错位。
  typedef BOOL(WINAPI * PFN_SetProcessDPIAware)(void);
  if (HMODULE u = GetModuleHandleW(L"user32.dll")) {
    if (PFN_SetProcessDPIAware fn =
            (PFN_SetProcessDPIAware)GetProcAddress(u, "SetProcessDPIAware")) {
      fn();
    }
  }

  D3D_FEATURE_LEVEL lvl;
  HRESULT hr = D3D11CreateDevice(nullptr, D3D_DRIVER_TYPE_HARDWARE, nullptr,
                                 D3D11_CREATE_DEVICE_BGRA_SUPPORT, nullptr, 0,
                                 D3D11_SDK_VERSION, &g_d3d, &lvl, nullptr);
  if (FAILED(hr)) {
    printf("D3D11CreateDevice 失败 hr=0x%08lX\n", hr);
    return 1;
  }
  if (FAILED(g_d3d->QueryInterface(kIID_DXGIDevice, (void **)&g_dxgi))) {
    printf("QueryInterface(IDXGIDevice) 失败\n");
    return 1;
  }
  g_d3d->GetImmediateContext(&g_ctx);
  if (!initSource()) {
    printf("建 staging 纹理失败\n");
    return 1;
  }

  HMODULE dcomp = LoadLibraryW(L"dcomp.dll");
  if (!dcomp) {
    printf("加载 dcomp.dll 失败\n");
    return 1;
  }
  g_createDComp = (PFN_DCompositionCreateDevice)GetProcAddress(dcomp,
                                                               "DCompositionCreateDevice");
  if (!g_createDComp) {
    printf("找不到 DCompositionCreateDevice\n");
    return 1;
  }

  registerClass();

  if (!pickFreeOrigin(&WIN_X, &WIN_Y)) {
    printf("\n!! 桌面被应用窗口完全盖住了，现在测不了「桌面层」。\n");
    printf("!! 请按 Win+D（或最小化浏览器）把桌面露出来，探针会自动继续…\n");
    printf("!! 若 90 秒内没露出桌面，直接退出。\n\n");
    bool got = false;
    for (int i = 0; i < 180; i++) {
      pump(500);
      if (pickFreeOrigin(&WIN_X, &WIN_Y)) {
        got = true;
        break;
      }
    }
    if (!got) {
      printf("!! 超时，未检测到可见桌面。结果不可信，已中止。\n");
      return 2;
    }
    printf("!! 已找到无遮挡落点，继续。\n\n");
    pump(800); // 等桌面切换动画结束
  }

  printf("DComp 合成探针：逐项验证内容能否显示、点击能否穿透\n");
  printf("窗口 %dx%d @ (%d,%d)，洋红填充\n", WIN_W, WIN_H, WIN_X, WIN_Y);

  const DWORD NOREDIR = WS_EX_NOREDIRECTIONBITMAP;

  Case cases[] = {
      {"1 普通窗口 + DComp", 0, false, false, false, false},
      {"2 NOREDIRECTIONBITMAP + DComp", NOREDIR, false, false, false, false},
      {"3 NOREDIRECT|LAYERED + DComp", NOREDIR | WS_EX_LAYERED, true, false, false,
       false},
      {"4 NOREDIRECT|LAYERED|TRANSPARENT + DComp",
       NOREDIR | WS_EX_LAYERED | WS_EX_TRANSPARENT, true, false, false, false},
      {"5 LAYERED|TRANSPARENT + DComp（不用 NOREDIRECT）",
       WS_EX_LAYERED | WS_EX_TRANSPARENT, true, false, false, false},
      {"6 NOREDIRECT + 挂 Progman", NOREDIR, false, true, false, false},
      {"7 NOREDIRECT|LAYERED|TRANSPARENT + 挂 Progman",
       NOREDIR | WS_EX_LAYERED | WS_EX_TRANSPARENT, true, true, false, false},
      // 路线 B：顶层窗口 + Z 序插到图标宿主正上方（不 SetParent）
      {"8 NOREDIRECT|LAYERED|TRANSPARENT + Z序排到图标宿主上方",
       NOREDIR | WS_EX_LAYERED | WS_EX_TRANSPARENT, true, false, true, false},
      {"9 LAYERED|TRANSPARENT + Z序排到图标宿主上方（不用 NOREDIRECT）",
       WS_EX_LAYERED | WS_EX_TRANSPARENT, true, false, true, false},
      // 对照组：同样挂 Progman，但用 GDI 填色。
      // 若这里也数不到洋红 → 是遮挡，不是 DComp 的锅。
      {"10 对照组：挂 Progman + GDI 填色（不接 DComp）", 0, false, true, false,
       true},
  };

  for (size_t i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
    runCase(cases[i]);
  }

  printf("\n探针结束\n");
  return 0;
}
