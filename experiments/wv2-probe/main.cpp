// WebView2 Composition 探针
//
// 上一轮 experiments/dcomp-probe 已经证明：
//   WS_EX_NOREDIRECTIONBITMAP | WS_EX_LAYERED | WS_EX_TRANSPARENT
//   三者可以共存，DComp 内容正常合成，点击穿透成立，挂 Progman 之后也照常。
//
// 但那只验证了「一个纯色 surface」。真正要落地的方案是把 WebView2 接进 DComp：
//   ICoreWebView2CompositionController::put_RootVisualTarget(IDCompositionVisual*)
// 本探针回答剩下四个问题：
//   1 页面能不能真的画进 DComp visual（洋红像素数）
//   2 页面背景设成透明后，窗口空白处是不是真的透出桌面（与建窗前的桌面快照比对）
//   3 加了这一整套之后，点击穿透是否还成立（ChildWindowFromPointEx）
//   4 开销：本进程工作集 + 拉起了几个子进程
//
// 用法：wv2-probe.exe topmost | desktop
//   topmost = 顶层窗口（模拟「全局置顶 + 穿透」）
//   desktop = 挂到图标宿主下的子窗口（模拟「桌面层」）
//
// 注意：desktop 用例必须让桌面可见才能读数，否则屏幕抓像素抓到的是别的应用。

#include <windows.h>
#include <d3d11.h>
#include <dxgi.h>
#include <objbase.h>
#include <psapi.h>
#include <tlhelp32.h>
#include <stdio.h>
#include <string.h>
#include <string>
#include <vector>

// MinGW 8.1 的 dcomp.h 在 IDCompositionTarget 里用到 IDCompositionVisual，
// 但定义在文件后面，会被解析成 int。补前向声明。
struct IDCompositionVisual;

#include <dcomp.h>
#include "WebView2.h"

#ifndef WS_EX_NOREDIRECTIONBITMAP
#define WS_EX_NOREDIRECTIONBITMAP 0x00200000L
#endif

// ---------------------------------------------------------------------------
// IID（GCC 不支持 __uuidof，只能从 WebView2.h 的 MIDL_INTERFACE 抄下来）
// ---------------------------------------------------------------------------

static const GUID kIID_CompCtrl = {
    0x3df9b733, 0xb9ae, 0x4a15, {0x86, 0xb4, 0xeb, 0x9e, 0xe9, 0x82, 0x64, 0x69}};
static const GUID kIID_Ctrl2 = {
    0xc979903e, 0xd4ca, 0x4228, {0x92, 0xeb, 0x47, 0xee, 0x3f, 0xa9, 0x6e, 0xab}};
static const GUID kIID_Ctrl = {
    0x4d00c0d1, 0x9434, 0x4eb6, {0x80, 0x78, 0x86, 0x97, 0xa5, 0x60, 0x33, 0x4f}};
static const GUID kIID_EnvHandler = {
    0x4e8a3389, 0xc9d8, 0x4bd2, {0xb6, 0xb5, 0x12, 0x4f, 0xee, 0x6c, 0xc1, 0x4d}};
static const GUID kIID_CtrlHandler = {
    0x6c4819f3, 0xc9b7, 0x4260, {0x81, 0x27, 0xc9, 0xf5, 0xbd, 0xe7, 0xf6, 0x8c}};
static const GUID kIID_NavHandler = {
    0xd33a35bf, 0x1c49, 0x4f98, {0x93, 0xab, 0x00, 0x6e, 0x05, 0x33, 0xfe, 0x1c}};
static const GUID kIID_Env = {
    0xb96d755e, 0x0319, 0x4e92, {0xa2, 0x96, 0x23, 0x43, 0x22, 0x25, 0x9c, 0x0a}};
static const GUID kIID_Env3 = {
    0x80a22ae3, 0xbe7c, 0x4ce2, {0xaf, 0xe1, 0x5a, 0x50, 0x05, 0x6c, 0xde, 0xeb}};
static const GUID kIID_CompCtrlHandler = {
    0x02fab84b, 0x1428, 0x4fb7, {0xad, 0x45, 0x1b, 0x2e, 0x64, 0x73, 0x61, 0x84}};
static const GUID kIID_Ctrl3 = {
    0xf9614724, 0x5d2b, 0x41dc, {0xae, 0xf7, 0x73, 0xd6, 0x2b, 0x51, 0x54, 0x3b}};

// ---------------------------------------------------------------------------
// 全局状态
// ---------------------------------------------------------------------------

static const int WIN_W = 480;
static const int WIN_H = 320;
static int WIN_X = 200;
static int WIN_Y = 200;
static int g_dpi = 96;
static double g_wantScale = 0; // 0 = 用 dpi/96

static HWND g_hwnd = nullptr;
static IDCompositionDevice *g_dcomp = nullptr;
static IDCompositionTarget *g_target = nullptr;
static IDCompositionVisual *g_rootVisual = nullptr;

static ICoreWebView2Environment *g_env = nullptr;
static ICoreWebView2Controller *g_ctrl = nullptr;
static ICoreWebView2 *g_webview = nullptr;
static ICoreWebView2CompositionController *g_comp = nullptr;

static volatile LONG g_envDone = 0;
static volatile LONG g_ctrlDone = 0;
static volatile LONG g_navDone = 0;
static HRESULT g_envHr = E_FAIL, g_ctrlHr = E_FAIL;
static wchar_t g_ctrlErr[512] = {0};

// ---------------------------------------------------------------------------
// 基础工具
// ---------------------------------------------------------------------------

static void pump(int ms) {
  DWORD end = GetTickCount() + (DWORD)ms;
  MSG msg;
  while (GetTickCount() < end) {
    while (PeekMessageW(&msg, nullptr, 0, 0, PM_REMOVE)) {
      TranslateMessage(&msg);
      DispatchMessageW(&msg);
    }
    Sleep(5);
  }
}

static LRESULT CALLBACK probeProc(HWND h, UINT m, WPARAM w, LPARAM l) {
  switch (m) {
    case WM_PAINT:
      // NOREDIRECTIONBITMAP 窗口没有重定向表面，内容全部由 DComp/WebView2 合成
      ValidateRect(h, nullptr);
      return 0;
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
  wc.lpszClassName = L"WV2ProbeWindow";
  wc.hbrBackground = nullptr;
  RegisterClassExW(&wc);
}

// 找「桌面图标宿主」：含 SHELLDLL_DefView 子窗口的那个顶层窗口。
static HWND findIconHost(void) {
  HWND prog = FindWindowW(L"Progman", nullptr);
  if (prog && FindWindowExW(prog, nullptr, L"SHELLDLL_DefView", nullptr)) return prog;
  HWND w = nullptr;
  while ((w = FindWindowExW(nullptr, w, L"WorkerW", nullptr)) != nullptr) {
    if (FindWindowExW(w, nullptr, L"SHELLDLL_DefView", nullptr)) return w;
  }
  return prog;
}

// 该点最底层的命中窗口。ChildWindowFromPointEx 收父客户区坐标，每层都要换算。
static HWND bottomHitAt(POINT screenPt) {
  HWND w = GetDesktopWindow();
  POINT pt = screenPt;
  for (int i = 0; i < 32; i++) {
    HWND child = ChildWindowFromPointEx(w, pt, CWP_SKIPINVISIBLE | CWP_SKIPDISABLED);
    if (!child || child == w) break;
    w = child;
    pt = screenPt;
    MapWindowPoints(nullptr, w, &pt, 1);
  }
  return w;
}

static bool isSelfOnTop(HWND h) {
  RECT rc;
  if (!GetWindowRect(h, &rc)) return false;
  POINT pt = {(rc.left + rc.right) / 2, (rc.top + rc.bottom) / 2};
  return bottomHitAt(pt) == h;
}

static bool isDesktopClass(HWND w) {
  wchar_t cls[64] = {0};
  if (!GetClassNameW(w, cls, 64)) return false;
  return !lstrcmpW(cls, L"Progman") || !lstrcmpW(cls, L"WorkerW") ||
         !lstrcmpW(cls, L"SHELLDLL_DefView") || !lstrcmpW(cls, L"SysListView32");
}

// 找一个整块矩形都没被应用窗口盖住的落点（桌面层用例必须）。
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
// 像素测量
// ---------------------------------------------------------------------------

struct Snap {
  int w = 0, h = 0;
  std::vector<BYTE> px; // BGRA
};

static bool grab(RECT rc, Snap *s) {
  int w = rc.right - rc.left, h = rc.bottom - rc.top;
  if (w <= 0 || h <= 0) return false;
  HDC screen = GetDC(nullptr);
  HDC mem = CreateCompatibleDC(screen);
  HBITMAP bmp = CreateCompatibleBitmap(screen, w, h);
  HGDIOBJ old = SelectObject(mem, bmp);
  BitBlt(mem, 0, 0, w, h, screen, rc.left, rc.top, SRCCOPY);

  BITMAPINFO bi;
  ZeroMemory(&bi, sizeof(bi));
  bi.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
  bi.bmiHeader.biWidth = w;
  bi.bmiHeader.biHeight = -h;
  bi.bmiHeader.biPlanes = 1;
  bi.bmiHeader.biBitCount = 32;
  bi.bmiHeader.biCompression = BI_RGB;

  s->w = w;
  s->h = h;
  s->px.assign((size_t)w * h * 4, 0);
  BOOL ok = GetDIBits(mem, bmp, 0, (UINT)h, s->px.data(), &bi, DIB_RGB_COLORS);

  SelectObject(mem, old);
  DeleteObject(bmp);
  DeleteDC(mem);
  ReleaseDC(nullptr, screen);
  return ok != 0;
}

// 数洋红像素（页面里那个纯洋红方块），顺便给出包围盒
static int countMagenta(const Snap &s, RECT *bbox) {
  int n = 0;
  int minx = s.w, miny = s.h, maxx = -1, maxy = -1;
  for (int y = 0; y < s.h; y++) {
    for (int x = 0; x < s.w; x++) {
      int i = y * s.w + x;
      BYTE b = s.px[i * 4 + 0], g = s.px[i * 4 + 1], r = s.px[i * 4 + 2];
      if (r > 200 && b > 200 && g < 60) {
        n++;
        if (x < minx) minx = x;
        if (y < miny) miny = y;
        if (x > maxx) maxx = x;
        if (y > maxy) maxy = y;
      }
    }
  }
  if (bbox) {
    bbox->left = (maxx >= 0) ? minx : 0;
    bbox->top = (maxy >= 0) ? miny : 0;
    bbox->right = maxx;
    bbox->bottom = maxy;
  }
  return n;
}

// 两张快照里差异超过阈值的像素数。
// 传 skipMagenta=true 时跳过 after 里是洋红的像素——那些是页面内容本身，
// 不是"背景不透明"的证据。判透明只看方块以外的区域有没有被涂掉。
static int diffPixels(const Snap &a, const Snap &b, bool skipMagenta) {
  if (a.w != b.w || a.h != b.h) return -1;
  int n = 0;
  for (int i = 0; i < a.w * a.h; i++) {
    if (skipMagenta) {
      BYTE ab = b.px[i * 4 + 0], ag = b.px[i * 4 + 1], ar = b.px[i * 4 + 2];
      if (ar > 200 && ab > 200 && ag < 60) continue;
    }
    int db = abs((int)a.px[i * 4 + 0] - (int)b.px[i * 4 + 0]);
    int dg = abs((int)a.px[i * 4 + 1] - (int)b.px[i * 4 + 1]);
    int dr = abs((int)a.px[i * 4 + 2] - (int)b.px[i * 4 + 2]);
    if (db + dg + dr > 24) n++;
  }
  return n;
}

// 命中测试：中心点是否落在自己身上。false = 穿透成立。
static bool hitSelf(HWND h) {
  RECT rc;
  if (!GetWindowRect(h, &rc)) return true;
  POINT screenPt = {(rc.left + rc.right) / 2, (rc.top + rc.bottom) / 2};

  HWND probes[2];
  probes[0] = GetDesktopWindow();
  probes[1] = GetAncestor(h, GA_PARENT);
  if (probes[1] == probes[0]) probes[1] = nullptr;

  for (int i = 0; i < 2; i++) {
    if (!probes[i]) continue;
    POINT pt = screenPt;
    MapWindowPoints(nullptr, probes[i], &pt, 1);
    HWND hit = ChildWindowFromPointEx(
        probes[i], pt, CWP_SKIPTRANSPARENT | CWP_SKIPINVISIBLE | CWP_SKIPDISABLED);
    while (hit) {
      if (hit == h) return true;
      hit = GetParent(hit);
    }
  }
  return false;
}

// ---------------------------------------------------------------------------
// COM 回调（不用 WRL，手写引用计数）
// ---------------------------------------------------------------------------

struct EnvHandler : ICoreWebView2CreateCoreWebView2EnvironmentCompletedHandler {
  ULONG ref = 1;
  HRESULT STDMETHODCALLTYPE QueryInterface(REFIID riid, void **ppv) override {
    if (!ppv) return E_POINTER;
    if (IsEqualIID(riid, IID_IUnknown) || IsEqualIID(riid, kIID_EnvHandler)) {
      *ppv = static_cast<ICoreWebView2CreateCoreWebView2EnvironmentCompletedHandler *>(this);
      AddRef();
      return S_OK;
    }
    *ppv = nullptr;
    return E_NOINTERFACE;
  }
  ULONG STDMETHODCALLTYPE AddRef() override { return (ULONG)InterlockedIncrement((LONG *)&ref); }
  ULONG STDMETHODCALLTYPE Release() override {
    LONG n = InterlockedDecrement((LONG *)&ref);
    if (n == 0) delete this;
    return (ULONG)n;
  }
  HRESULT STDMETHODCALLTYPE Invoke(HRESULT hr, ICoreWebView2Environment *env) override {
    g_envHr = hr;
    if (SUCCEEDED(hr) && env) {
      g_env = env;
      g_env->AddRef();
    }
    InterlockedExchange(&g_envDone, 1);
    return S_OK;
  }
};

struct CompCtrlHandler
    : ICoreWebView2CreateCoreWebView2CompositionControllerCompletedHandler {
  ULONG ref = 1;
  HRESULT STDMETHODCALLTYPE QueryInterface(REFIID riid, void **ppv) override {
    if (!ppv) return E_POINTER;
    if (IsEqualIID(riid, IID_IUnknown) || IsEqualIID(riid, kIID_CompCtrlHandler)) {
      *ppv = static_cast<ICoreWebView2CreateCoreWebView2CompositionControllerCompletedHandler *>(
          this);
      AddRef();
      return S_OK;
    }
    *ppv = nullptr;
    return E_NOINTERFACE;
  }
  ULONG STDMETHODCALLTYPE AddRef() override { return (ULONG)InterlockedIncrement((LONG *)&ref); }
  ULONG STDMETHODCALLTYPE Release() override {
    LONG n = InterlockedDecrement((LONG *)&ref);
    if (n == 0) delete this;
    return (ULONG)n;
  }
  HRESULT STDMETHODCALLTYPE Invoke(HRESULT hr,
                                   ICoreWebView2CompositionController *comp) override {
    g_ctrlHr = hr;
    if (SUCCEEDED(hr) && comp) {
      g_comp = comp;
      g_comp->AddRef();
      // CompositionController 不继承 ICoreWebView2Controller，要单独 QI 拿控制器
      if (SUCCEEDED(g_comp->QueryInterface(kIID_Ctrl, (void **)&g_ctrl)) && g_ctrl) {
        g_ctrl->AddRef();
        g_ctrl->get_CoreWebView2(&g_webview);
        if (g_webview) g_webview->AddRef();
      }
    }
    InterlockedExchange(&g_ctrlDone, 1);
    return S_OK;
  }
};

struct NavHandler : ICoreWebView2NavigationCompletedEventHandler {
  ULONG ref = 1;
  HRESULT STDMETHODCALLTYPE QueryInterface(REFIID riid, void **ppv) override {
    if (!ppv) return E_POINTER;
    if (IsEqualIID(riid, IID_IUnknown) || IsEqualIID(riid, kIID_NavHandler)) {
      *ppv = static_cast<ICoreWebView2NavigationCompletedEventHandler *>(this);
      AddRef();
      return S_OK;
    }
    *ppv = nullptr;
    return E_NOINTERFACE;
  }
  ULONG STDMETHODCALLTYPE AddRef() override { return (ULONG)InterlockedIncrement((LONG *)&ref); }
  ULONG STDMETHODCALLTYPE Release() override {
    LONG n = InterlockedDecrement((LONG *)&ref);
    if (n == 0) delete this;
    return (ULONG)n;
  }
  HRESULT STDMETHODCALLTYPE Invoke(ICoreWebView2 *sender,
                                   ICoreWebView2NavigationCompletedEventArgs *args) override {
    BOOL ok = FALSE;
    if (args) args->get_IsSuccess(&ok);
    printf("   导航完成：IsSuccess=%s\n", ok ? "是" : "否");
    InterlockedExchange(&g_navDone, 1);
    return S_OK;
  }
};

// ---------------------------------------------------------------------------
// 进程开销
// ---------------------------------------------------------------------------

static void reportMemory(void) {
  PROCESS_MEMORY_COUNTERS pmc;
  ZeroMemory(&pmc, sizeof(pmc));
  if (GetProcessMemoryInfo(GetCurrentProcess(), &pmc, sizeof(pmc))) {
    printf("   本进程工作集=%.1f MB\n", pmc.WorkingSetSize / 1048576.0);
  }
  DWORD me = GetCurrentProcessId();
  HANDLE snap = CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0);
  int kids = 0;
  if (snap != INVALID_HANDLE_VALUE) {
    PROCESSENTRY32W pe;
    ZeroMemory(&pe, sizeof(pe));
    pe.dwSize = sizeof(pe);
    if (Process32FirstW(snap, &pe)) {
      do {
        if (pe.th32ParentProcessID == me) kids++;
      } while (Process32NextW(snap, &pe));
    }
    CloseHandle(snap);
  }
  printf("   子进程数=%d\n", kids);
}

// ---------------------------------------------------------------------------
// DComp
// ---------------------------------------------------------------------------

static bool setupDComp(HWND h) {
  ID3D11Device *d3d = nullptr;
  IDXGIDevice *dxgi = nullptr;
  HRESULT hr = D3D11CreateDevice(nullptr, D3D_DRIVER_TYPE_HARDWARE, nullptr,
                                 D3D11_CREATE_DEVICE_BGRA_SUPPORT, nullptr, 0,
                                 D3D11_SDK_VERSION, &d3d, nullptr, nullptr);
  if (FAILED(hr)) {
    printf("   D3D11CreateDevice 失败 0x%08lX\n", (unsigned long)hr);
    return false;
  }
  if (FAILED(d3d->QueryInterface(__uuidof(IDXGIDevice), (void **)&dxgi))) {
    printf("   QueryInterface(IDXGIDevice) 失败\n");
    d3d->Release();
    return false;
  }

  typedef HRESULT(WINAPI * PFN_Create)(IUnknown *, REFIID, void **);
  HMODULE dcompDll = LoadLibraryW(L"dcomp.dll");
  if (!dcompDll) {
    printf("   加载 dcomp.dll 失败\n");
    return false;
  }
  PFN_Create create = (PFN_Create)GetProcAddress(dcompDll, "DCompositionCreateDevice");
  if (!create) {
    printf("   找不到 DCompositionCreateDevice\n");
    return false;
  }
  hr = create(dxgi, __uuidof(IDCompositionDevice), (void **)&g_dcomp);
  dxgi->Release();
  d3d->Release();
  if (FAILED(hr)) {
    printf("   DCompositionCreateDevice 失败 0x%08lX\n", (unsigned long)hr);
    return false;
  }

  hr = g_dcomp->CreateTargetForHwnd(h, TRUE, &g_target);
  if (FAILED(hr)) {
    printf("   CreateTargetForHwnd 失败 0x%08lX\n", (unsigned long)hr);
    return false;
  }
  hr = g_dcomp->CreateVisual(&g_rootVisual);
  if (FAILED(hr)) {
    printf("   CreateVisual 失败 0x%08lX\n", (unsigned long)hr);
    return false;
  }
  g_target->SetRoot(g_rootVisual);
  g_dcomp->Commit();
  return true;
}

// ---------------------------------------------------------------------------
// WebView2
// ---------------------------------------------------------------------------

typedef HRESULT(STDAPICALLTYPE *PFN_CreateEnv)(
    PCWSTR, PCWSTR, ICoreWebView2EnvironmentOptions *,
    ICoreWebView2CreateCoreWebView2EnvironmentCompletedHandler *);

static bool setupWebView2(HWND h, const wchar_t *url) {
  HMODULE loader = LoadLibraryW(L"WebView2Loader.dll");
  if (!loader) {
    printf("   加载 WebView2Loader.dll 失败（要把它放到 exe 旁边）\n");
    return false;
  }
  PFN_CreateEnv createEnv =
      (PFN_CreateEnv)GetProcAddress(loader, "CreateCoreWebView2EnvironmentWithOptions");
  if (!createEnv) {
    printf("   找不到 CreateCoreWebView2EnvironmentWithOptions\n");
    return false;
  }

  wchar_t udf[MAX_PATH] = {0};
  GetModuleFileNameW(nullptr, udf, MAX_PATH);
  wchar_t *slash = wcsrchr(udf, L'\\');
  if (slash) *slash = 0;
  wcscat(udf, L"\\wv2data");

  // 传 nullptr 作 options：需要自定义 browser 参数时再自己实现 ICoreWebView2EnvironmentOptions
  HRESULT hr = createEnv(nullptr, udf, nullptr, new EnvHandler());
  if (FAILED(hr)) {
    printf("   CreateCoreWebView2EnvironmentWithOptions 失败 0x%08lX\n",
           (unsigned long)hr);
    return false;
  }
  for (int i = 0; i < 200 && !g_envDone; i++) pump(100);
  if (!g_envDone || FAILED(g_envHr)) {
    printf("   环境创建未完成 hr=0x%08lX\n", (unsigned long)g_envHr);
    return false;
  }

  // 必须用 Environment3 的 CreateCoreWebView2CompositionController，
  // 用 CreateCoreWebView2Controller 建出来的对象 QI 不到 CompositionController
  // （实测返回 E_NOINTERFACE 0x80004002）。
  ICoreWebView2Environment3 *env3 = nullptr;
  hr = g_env->QueryInterface(kIID_Env3, (void **)&env3);
  if (FAILED(hr) || !env3) {
    printf("   QueryInterface(ICoreWebView2Environment3) 失败 0x%08lX\n",
           (unsigned long)hr);
    return false;
  }
  hr = env3->CreateCoreWebView2CompositionController(h, new CompCtrlHandler());
  env3->Release();
  if (FAILED(hr)) {
    printf("   CreateCoreWebView2CompositionController 失败 0x%08lX\n",
           (unsigned long)hr);
    return false;
  }
  for (int i = 0; i < 200 && !g_ctrlDone; i++) pump(100);
  if (!g_ctrlDone || FAILED(g_ctrlHr) || !g_comp || !g_ctrl) {
    printf("   Controller 创建未完成 hr=0x%08lX\n", (unsigned long)g_ctrlHr);
    return false;
  }

  // 关键一步：把 DComp visual 交出去
  hr = g_comp->put_RootVisualTarget(g_rootVisual);
  printf("   put_RootVisualTarget=%s (0x%08lX)\n", SUCCEEDED(hr) ? "OK" : "FAIL",
         (unsigned long)hr);
  if (g_dcomp) g_dcomp->Commit();

  // 背景透明（alpha 只能是 0 或 255）
  ICoreWebView2Controller2 *ctrl2 = nullptr;
  if (SUCCEEDED(g_ctrl->QueryInterface(kIID_Ctrl2, (void **)&ctrl2)) && ctrl2) {
    COREWEBVIEW2_COLOR bg;
    bg.A = 0;
    bg.R = 0;
    bg.G = 0;
    bg.B = 0;
    hr = ctrl2->put_DefaultBackgroundColor(bg);
    printf("   put_DefaultBackgroundColor(alpha=0)=%s (0x%08lX)\n",
           SUCCEEDED(hr) ? "OK" : "FAIL", (unsigned long)hr);
    ctrl2->Release();
  } else {
    printf("   拿不到 ICoreWebView2Controller2，背景透明设不了\n");
  }

  RECT rc;
  GetClientRect(h, &rc);
  g_ctrl->put_Bounds(rc);
  g_ctrl->put_IsVisible(TRUE);

  // 必须显式设 RasterizationScale。
  // 不设的话，挂 Progman 的子窗口会按 1.425 而不是 1.25 渲染
  // （实测包围盒 285x171 vs 期望 250x150），内容被整体放大并裁掉一圈。
  ICoreWebView2Controller3 *ctrl3 = nullptr;
  if (SUCCEEDED(g_ctrl->QueryInterface(kIID_Ctrl3, (void **)&ctrl3)) && ctrl3) {
    // 必须先关掉自动检测，否则 WebView2 会在导航后把 RasterizationScale
    // 重置回它自己算的值（实测 1.425），我们设的 1.25 被覆盖。
    ctrl3->put_ShouldDetectMonitorScaleChanges(FALSE);
    double want = (g_wantScale > 0) ? g_wantScale : (g_dpi / 96.0);
    double gotScale = 0;
    ctrl3->get_RasterizationScale(&gotScale);
    HRESULT h2 = ctrl3->put_RasterizationScale(want);
    double after = 0;
    ctrl3->get_RasterizationScale(&after);
    printf("   RasterizationScale：默认 %.3f → 设为 %.3f（写回 %.3f）%s\n", gotScale,
           want, after, SUCCEEDED(h2) ? "" : " 写入失败");
    ctrl3->Release();
  } else {
    printf("   拿不到 ICoreWebView2Controller3，缩放设不了\n");
  }

  EventRegistrationToken tok;
  g_webview->add_NavigationCompleted(new NavHandler(), &tok);
  hr = g_webview->Navigate(url);
  printf("   Navigate=%s (0x%08lX)\n", SUCCEEDED(hr) ? "OK" : "FAIL",
         (unsigned long)hr);
  return SUCCEEDED(hr);
}

// ---------------------------------------------------------------------------
// 页面
// ---------------------------------------------------------------------------

static void writePage(void) {
  const char *html =
      "<!doctype html><html><head><meta charset=\"utf-8\"><style>"
      "html,body{margin:0;padding:0;background:transparent;overflow:hidden}"
      "div.box{position:absolute;left:40px;top:40px;width:200px;height:120px;"
      "background:rgb(255,0,255)}"
      "</style></head><body><div class=\"box\"></div></body></html>";
  FILE *f = fopen("page.html", "wb");
  if (f) {
    fwrite(html, 1, strlen(html), f);
    fclose(f);
  }
}

// ---------------------------------------------------------------------------
// 单个用例
// ---------------------------------------------------------------------------

static int runCase(const char *mode, DWORD ex) {
  bool desktop = !lstrcmpA(mode, "desktop");
  printf("\n===== 用例：%s =====\n", desktop ? "桌面层（挂 Progman 子窗口）"
                                            : "置顶层（顶层窗口）");
  printf("   扩展样式=0x%08lX\n", (unsigned long)ex);

  // 桌面层要桌面可见才能读数
  if (desktop) {
    if (!pickFreeOrigin(&WIN_X, &WIN_Y)) {
      printf("!! 桌面被应用窗口盖住，无法测桌面层。\n");
      printf("!! 请按 Win+D 露出桌面，探针会自动继续…\n");
      bool got = false;
      for (int i = 0; i < 240; i++) {
        pump(500);
        if (pickFreeOrigin(&WIN_X, &WIN_Y)) {
          got = true;
          break;
        }
      }
      if (!got) {
        printf("!! 超时，中止。\n");
        return 2;
      }
      pump(800);
    }
  }

  g_hwnd = CreateWindowExW(ex, L"WV2ProbeWindow", L"WV2Probe", WS_POPUP, WIN_X,
                           WIN_Y, WIN_W, WIN_H, nullptr, nullptr,
                           GetModuleHandleW(nullptr), nullptr);
  if (!g_hwnd) {
    printf("   创建窗口失败 err=%lu\n", GetLastError());
    return 1;
  }
  SetLayeredWindowAttributes(g_hwnd, 0, 255, LWA_ALPHA);

  if (desktop) {
    HWND host = findIconHost();
    if (!host) {
      printf("   找不到图标宿主\n");
      return 1;
    }
    SetParent(g_hwnd, host);
    SetWindowPos(g_hwnd, HWND_TOP, WIN_X, WIN_Y, WIN_W, WIN_H,
                 SWP_NOACTIVATE | SWP_SHOWWINDOW);
    wchar_t cls[64] = {0};
    GetClassNameW(host, cls, 64);
    printf("   已挂到 %ls\n", cls);
  } else {
    ShowWindow(g_hwnd, SW_SHOWNOACTIVATE);
    SetWindowPos(g_hwnd, HWND_TOPMOST, WIN_X, WIN_Y, WIN_W, WIN_H,
                 SWP_NOACTIVATE | SWP_SHOWWINDOW);
  }
  pump(200);

  {
    RECT w2, c2;
    GetWindowRect(g_hwnd, &w2);
    GetClientRect(g_hwnd, &c2);
    int wdpi = 0;
    if (HMODULE u = GetModuleHandleW(L"user32.dll")) {
      typedef UINT(WINAPI * PFN_Dpi)(HWND);
      PFN_Dpi fn = (PFN_Dpi)GetProcAddress(u, "GetDpiForWindow");
      if (fn) wdpi = (int)fn(g_hwnd);
    }
    printf("   窗口矩形 %dx%d @ (%d,%d)，客户区 %dx%d，窗口 DPI=%d\n",
           w2.right - w2.left, w2.bottom - w2.top, w2.left, w2.top,
           c2.right - c2.left, c2.bottom - c2.top, wdpi);
  }

  // 背景比对基准：把窗口藏起来抓一张"没有本窗口"的图。
  // 顶层和桌面层都能这么做（藏起来就能露出后面的东西）。
  RECT wr;
  GetWindowRect(g_hwnd, &wr);
  Snap before;
  bool haveBefore = false;
  ShowWindow(g_hwnd, SW_HIDE);
  pump(400);
  haveBefore = grab(wr, &before);
  ShowWindow(g_hwnd, SW_SHOWNOACTIVATE);
  if (desktop) {
    // 重新显示会掉回 Progman 子窗口的底部，压回图标之上
    SetWindowPos(g_hwnd, HWND_TOP, WIN_X, WIN_Y, WIN_W, WIN_H,
                 SWP_NOACTIVATE | SWP_SHOWWINDOW);
  }
  pump(400);

  if (!setupDComp(g_hwnd)) return 1;
  printf("   DComp 挂载=OK\n");

  writePage();
  wchar_t url[MAX_PATH] = {0};
  GetModuleFileNameW(nullptr, url, MAX_PATH);
  wchar_t *slash = wcsrchr(url, L'\\');
  if (slash) *slash = 0;
  wcscat(url, L"\\page.html");
  wchar_t fullUrl[MAX_PATH + 16] = L"file:///";
  wcscat(fullUrl, url);

  if (!setupWebView2(g_hwnd, fullUrl)) return 1;

  for (int i = 0; i < 150 && !g_navDone; i++) {
    pump(100);
    if (g_dcomp) g_dcomp->Commit();
  }
  // 再等一会儿让首帧合成上来（期间持续 Commit，避免 DComp 批次没提交）
  for (int i = 0; i < 15; i++) {
    pump(100);
    if (g_dcomp) g_dcomp->Commit();
  }

  // 导航之后再看一次缩放，判断是不是被 WebView2 自己改回去了
  {
    ICoreWebView2Controller3 *c3 = nullptr;
    if (g_ctrl && SUCCEEDED(g_ctrl->QueryInterface(kIID_Ctrl3, (void **)&c3)) && c3) {
      double s = 0;
      c3->get_RasterizationScale(&s);
      printf("   导航后 RasterizationScale=%.3f\n", s);
      c3->Release();
    }
    if (g_ctrl) {
      double zf = 0;
      g_ctrl->get_ZoomFactor(&zf);
      printf("   页面 ZoomFactor=%.3f\n", zf);
    }
  }

  // 可测量性
  bool onTop = false;
  for (int i = 0; i < 25 && !onTop; i++) {
    onTop = isSelfOnTop(g_hwnd);
    if (!onTop) pump(200);
  }
  wchar_t cls[64] = {0};
  if (onTop) {
    HWND w = bottomHitAt({(wr.left + wr.right) / 2, (wr.top + wr.bottom) / 2});
    if (w) GetClassNameW(w, cls, 64);
  }
  printf("   该点最上层=%ls  可测量=%s\n", onTop ? cls : L"(被遮挡)",
         onTop ? "是" : "否");

  Snap after;
  bool got = grab(wr, &after);
  RECT bbox = {0, 0, 0, 0};
  int magenta = got ? countMagenta(after, &bbox) : -1;
  int changedAll = (got && haveBefore) ? diffPixels(before, after, false) : -1;
  int changedBg = (got && haveBefore) ? diffPixels(before, after, true) : -1;
  bool self = hitSelf(g_hwnd);

  printf("\n   —— 结果 ——\n");
  if (got) {
    // 采样几个点，方便判断"到底画了什么"
    int cx = after.w / 2, cy = after.h / 2;
    int qx = 140, qy = 100; // 洋红方块内部（页面坐标 40+200/2, 40+120/2）
    int i1 = (cy * after.w + cx) * 4;
    int i2 = (qy * after.w + qx) * 4;
    printf("   中心像素 RGB=(%d,%d,%d)   方块内像素 RGB=(%d,%d,%d)\n",
           after.px[i1 + 2], after.px[i1 + 1], after.px[i1 + 0], after.px[i2 + 2],
           after.px[i2 + 1], after.px[i2 + 0]);
  }
  if (!onTop) {
    printf("   页面渲染：不可判定（窗口被别的应用遮挡）\n");
  } else {
    printf("   页面渲染：洋红像素=%d，包围盒 %dx%d @ (%d,%d)\n", magenta,
           bbox.right - bbox.left + 1, bbox.bottom - bbox.top + 1, bbox.left,
           bbox.top);
    printf("             期望 200x120 CSS 像素 × 缩放 = %d × %d 物理像素\n",
           200 * g_dpi / 96, 120 * g_dpi / 96);
  }
  if (changedBg >= 0) {
    int total = after.w * after.h;
    printf("   背景透明：总变化=%d，其中方块以外变化=%d / %d（%.2f%%）%s\n",
           changedAll, changedBg, total, 100.0 * changedBg / total,
           changedBg < total * 0.01 ? "  ← 空白处原样透出桌面，透明成立"
                                    : "  ← 空白处被涂掉了，整窗不透明");
  } else {
    printf("   背景透明：无法比对（没抓到基准快照）\n");
  }
  printf("   点击穿透：命中自己=%s  %s\n", self ? "是" : "否",
         self ? "不穿透" : "穿透成立");
  reportMemory();

  // 收尾
  if (g_comp) g_comp->Release();
  if (g_webview) g_webview->Release();
  if (g_ctrl) g_ctrl->Close();
  if (g_ctrl) g_ctrl->Release();
  if (g_env) g_env->Release();
  if (g_rootVisual) g_rootVisual->Release();
  if (g_target) g_target->Release();
  if (g_dcomp) g_dcomp->Release();
  DestroyWindow(g_hwnd);
  g_hwnd = nullptr;
  pump(300);
  return 0;
}

// ---------------------------------------------------------------------------

int main(int argc, char **argv) {
  // DPI 感知级别必须和 Progman 一致，否则挂成子窗口后 DWM 会按
  // (Progman 的 DPI / 本进程 DPI) 把整个窗口位图拉伸一遍。
  // MinGW 8.1 没有这些声明，全部动态取。
  {
    HMODULE u = GetModuleHandleW(L"user32.dll");
    typedef BOOL(WINAPI * PFN_Ctx)(HANDLE);
    PFN_Ctx setCtx = u ? (PFN_Ctx)GetProcAddress(u, "SetProcessDpiAwarenessContext") : nullptr;
    BOOL ok = FALSE;
    if (setCtx) ok = setCtx((HANDLE)(LONG_PTR)-4); // PER_MONITOR_AWARE_V2
    if (!ok) {
      HMODULE sh = LoadLibraryW(L"shcore.dll");
      typedef HRESULT(WINAPI * PFN_Aware)(int);
      PFN_Aware aware = sh ? (PFN_Aware)GetProcAddress(sh, "SetProcessDpiAwareness") : nullptr;
      if (aware) ok = SUCCEEDED(aware(2)); // PER_MONITOR_DPI_AWARE
    }
    if (!ok) {
      typedef BOOL(WINAPI * PFN)(void);
      PFN fn = u ? (PFN)GetProcAddress(u, "SetProcessDPIAware") : nullptr;
      if (fn) fn();
    }
  }
  CoInitializeEx(nullptr, COINIT_APARTMENTTHREADED);
  registerClass();

  const char *mode = (argc > 1) ? argv[1] : "topmost";
  DWORD ex = WS_EX_NOREDIRECTIONBITMAP | WS_EX_LAYERED | WS_EX_TRANSPARENT;
  if (argc > 2) ex = (DWORD)strtoul(argv[2], nullptr, 16);
  if (argc > 3) g_wantScale = atof(argv[3]);

  printf("WebView2 Composition 探针  模式=%s\n", mode);
  {
    HDC dc = GetDC(nullptr);
    if (dc) {
      g_dpi = GetDeviceCaps(dc, LOGPIXELSX);
      ReleaseDC(nullptr, dc);
    }
  }
  printf("窗口 %dx%d，系统 DPI=%d（%d%%），页面里 200x120 洋红方块，body 透明\n",
         WIN_W, WIN_H, g_dpi, g_dpi * 100 / 96);

  // 把 DPI 事实全部打出来：进程感知级别、显示器实际 DPI。
  // 系统 DPI 与显示器 DPI 不一致时，系统 DPI 感知的进程会被 DWM 位图拉伸。
  {
    HMODULE u = GetModuleHandleW(L"user32.dll");
    typedef HANDLE(WINAPI * PFN_GetCtx)(void);
    typedef int(WINAPI * PFN_AwareFromCtx)(HANDLE);
    PFN_GetCtx getCtx = (PFN_GetCtx)GetProcAddress(u, "GetThreadDpiAwarenessContext");
    PFN_AwareFromCtx fromCtx =
        (PFN_AwareFromCtx)GetProcAddress(u, "GetAwarenessFromDpiAwarenessContext");
    const char *names[] = {"UNAWARE", "SYSTEM_AWARE", "PER_MONITOR_AWARE", "未知"};
    int aw = -1;
    if (getCtx && fromCtx) {
      HANDLE ctx = getCtx();
      if (ctx) aw = fromCtx(ctx);
    }
    printf("进程 DPI 感知=%s\n", (aw >= 0 && aw <= 3) ? names[aw] : "取不到");

    HMODULE sh = LoadLibraryW(L"shcore.dll");
    if (sh) {
      typedef HRESULT(WINAPI * PFN_Mon)(HMONITOR, int, UINT *, UINT *);
      PFN_Mon mon = (PFN_Mon)GetProcAddress(sh, "GetDpiForMonitor");
      if (mon) {
        POINT pt = {WIN_X + 10, WIN_Y + 10};
        HMONITOR hm = MonitorFromPoint(pt, MONITOR_DEFAULTTOPRIMARY);
        UINT dx = 0, dy = 0;
        if (SUCCEEDED(mon(hm, 0 /*MDT_EFFECTIVE_DPI*/, &dx, &dy))) {
          printf("显示器有效 DPI=%u（%.0f%%）\n", dx, dx * 100.0 / 96);
        }
      }
    }
  }
  int rc = runCase(mode, ex);

  CoUninitialize();
  printf("\n探针结束 rc=%d\n", rc);
  return rc;
}
