// WebView2 Composition 的 C++ 实现。
//
// 这个文件只做三件事：把页面接进宿主给的 IDCompositionVisual、
// 把宿主的输入转给引擎、把引擎的回调转回 Go。业务一律不在这里。
//
// 四个必须守住的点（都是探针里踩过的）：
//   1 必须用 Environment3 的 CreateCoreWebView2CompositionController，
//     用 CreateCoreWebView2Controller 建出来的对象 QI 不到 CompositionController。
//   2 DComp 要周期 Commit，不提交批次内容完全不出现，且没有错误码。
//   3 必须先 put_ShouldDetectMonitorScaleChanges(FALSE) 再设 RasterizationScale，
//     否则导航后它会被重置成自己算的值（实测 1.425），页面大 14%。
//   4 背景透明靠 put_DefaultBackgroundColor 的 alpha=0，且只能是 0 或 255。

#include <windows.h>
#include <d3d11.h>
#include <dxgi.h>
#include <objbase.h>
#include <stdio.h>
#include <string>

// MinGW 8.1 的 dcomp.h 在 IDCompositionTarget 里用到 IDCompositionVisual，
// 但它的定义在文件后面，会被解析成 int。补前向声明。
struct IDCompositionVisual;

#include <dcomp.h>
#include "WebView2.h"
#include "wrap.h"

// ---------------------------------------------------------------------------
// 由 Go 侧实现（//export），C++ 只声明
// ---------------------------------------------------------------------------

extern "C" void snGoMessage(uintptr_t user, const char *utf8);
extern "C" void snGoReady(uintptr_t user, int32_t kind);
extern "C" void snGoCursor(uintptr_t user, intptr_t hcursor);
extern "C" void snGoEval(uintptr_t user, uintptr_t token, int32_t ok,
                         const char *json);

// ---------------------------------------------------------------------------
// IID（GCC 不支持 __uuidof，从 MIDL_INTERFACE 抄）
// ---------------------------------------------------------------------------

static const GUID kIID_Env = {
    0xb96d755e, 0x0319, 0x4e92, {0xa2, 0x96, 0x23, 0x43, 0x6f, 0x46, 0xa1, 0xfc}};
static const GUID kIID_Env3 = {
    0x80a22ae3, 0xbe7c, 0x4ce2, {0xaf, 0xe1, 0x5a, 0x50, 0x05, 0x6c, 0xde, 0xeb}};
static const GUID kIID_CompCtrl = {
    0x3df9b733, 0xb9ae, 0x4a15, {0x86, 0xb4, 0xeb, 0x9e, 0xe9, 0x82, 0x64, 0x69}};
static const GUID kIID_Ctrl = {
    0x4d00c0d1, 0x9434, 0x4eb6, {0x80, 0x78, 0x86, 0x97, 0xa5, 0x60, 0x33, 0x4f}};
static const GUID kIID_Ctrl2 = {
    0xc979903e, 0xd4ca, 0x4228, {0x92, 0xeb, 0x47, 0xee, 0x3f, 0xa9, 0x6e, 0xab}};
static const GUID kIID_Ctrl3 = {
    0xf9614724, 0x5d2b, 0x41dc, {0xae, 0xf7, 0x73, 0xd6, 0x2b, 0x51, 0x54, 0x3b}};
static const GUID kIID_Web3 = {
    0xa0d6df20, 0x3b92, 0x416d, {0xaa, 0x0c, 0x43, 0x7a, 0x9c, 0x72, 0x78, 0x57}};

static const GUID kIID_EnvHandler = {
    0x4e8a3389, 0xc9d8, 0x4bd2, {0xb6, 0xb5, 0x12, 0x4f, 0xee, 0x6c, 0xc1, 0x4d}};
static const GUID kIID_CompCtrlHandler = {
    0x02fab84b, 0x1428, 0x4fb7, {0xad, 0x45, 0x1b, 0x2e, 0x64, 0x73, 0x61, 0x84}};
static const GUID kIID_NavHandler = {
    0xd33a35bf, 0x1c49, 0x4f98, {0x93, 0xab, 0x00, 0x6e, 0x05, 0x33, 0xfe, 0x1c}};
static const GUID kIID_MsgHandler = {
    0x57213f19, 0x00e6, 0x49fa, {0x8e, 0x07, 0x89, 0x8e, 0xa0, 0x1e, 0xcb, 0xd2}};
static const GUID kIID_CursorHandler = {
    0x9da43ccc, 0x26e1, 0x4dad, {0xb5, 0x6c, 0xd8, 0x96, 0x1c, 0x94, 0xc5, 0x71}};
static const GUID kIID_EvalHandler = {
    0x49511172, 0xcc67, 0x4bca, {0x99, 0x23, 0x13, 0x71, 0x12, 0xf4, 0xc4, 0xcc}};
static const GUID kIID_ScriptHandler = {
    0xb99369f3, 0x9b11, 0x47b5, {0xbc, 0x6f, 0x8e, 0x78, 0x95, 0xfc, 0xea, 0x17}};

// DComp / DXGI 的 IID：GCC 没有 __uuidof，只能手抄
static const GUID kIID_DCompDevice = {
    0xc37ea93a, 0xe7aa, 0x450d, {0xb1, 0x6f, 0x97, 0x46, 0xcb, 0x04, 0x07, 0xf3}};
static const GUID kIID_DXGIDevice = {
    0x54ec77fa, 0x1377, 0x44e6, {0x8c, 0x32, 0x88, 0xfd, 0x5f, 0x44, 0xc8, 0x4c}};

// 手写引用计数：MinGW 没有 WRL，用不上 ComPtr。
#define SN_REF_IMPL(IFACE, IID_CONST)                                       \
  ULONG ref = 1;                                                            \
  HRESULT STDMETHODCALLTYPE QueryInterface(REFIID riid, void **ppv) override { \
    if (!ppv) return E_POINTER;                                             \
    if (IsEqualIID(riid, IID_IUnknown) || IsEqualIID(riid, IID_CONST)) {    \
      *ppv = static_cast<IFACE *>(this);                                    \
      AddRef();                                                             \
      return S_OK;                                                          \
    }                                                                       \
    *ppv = nullptr;                                                         \
    return E_NOINTERFACE;                                                   \
  }                                                                         \
  ULONG STDMETHODCALLTYPE AddRef() override {                               \
    return (ULONG)InterlockedIncrement((LONG *)&ref);                       \
  }                                                                         \
  ULONG STDMETHODCALLTYPE Release() override {                              \
    LONG n = InterlockedDecrement((LONG *)&ref);                            \
    if (n == 0) delete this;                                                \
    return (ULONG)n;                                                        \
  }

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------

static std::string toUtf8(LPCWSTR w) {
  if (!w) return std::string();
  int n = WideCharToMultiByte(CP_UTF8, 0, w, -1, nullptr, 0, nullptr, nullptr);
  if (n <= 1) return std::string();
  std::string s((size_t)n - 1, '\0');
  WideCharToMultiByte(CP_UTF8, 0, w, -1, &s[0], n, nullptr, nullptr);
  return s;
}

static int dpiOfWindow(HWND hwnd) {
  typedef UINT(WINAPI * PFN)(HWND);
  static PFN fn = nullptr;
  static bool looked = false;
  if (!looked) {
    looked = true;
    HMODULE u = GetModuleHandleW(L"user32.dll");
    if (u) fn = (PFN)GetProcAddress(u, "GetDpiForWindow");
  }
  if (fn) {
    UINT d = fn(hwnd);
    if (d) return (int)d;
  }
  HDC dc = GetDC(nullptr);
  int d = dc ? GetDeviceCaps(dc, LOGPIXELSX) : 96;
  if (dc) ReleaseDC(nullptr, dc);
  return d ? d : 96;
}

// ---------------------------------------------------------------------------
// 句柄
// ---------------------------------------------------------------------------

struct SnWv2 {
  HWND hwnd = nullptr;
  uintptr_t user = 0;

  IDCompositionDevice *dcomp = nullptr;
  IDCompositionTarget *target = nullptr;
  IDCompositionVisual *root = nullptr;

  ICoreWebView2Controller *ctrl = nullptr;
  ICoreWebView2 *webview = nullptr;
  ICoreWebView2CompositionController *comp = nullptr;

  volatile LONG state = 0; // 0 进行中，1 就绪，-1 失败
  HRESULT hr = E_FAIL;
  double scale = 1.0;
};

// 环境是进程级单例：一个环境就是一整套浏览器进程，多开只会翻倍内存。
static ICoreWebView2Environment *g_env = nullptr;
static volatile LONG g_envState = 0;
static HRESULT g_envHr = E_FAIL;
static int g_debug = 0;
static wchar_t g_version[64] = {0};

// ---------------------------------------------------------------------------
// 环境创建
// ---------------------------------------------------------------------------

struct EnvHandler : ICoreWebView2CreateCoreWebView2EnvironmentCompletedHandler {
  SN_REF_IMPL(ICoreWebView2CreateCoreWebView2EnvironmentCompletedHandler,
              kIID_EnvHandler)
  HRESULT STDMETHODCALLTYPE Invoke(HRESULT hr,
                                   ICoreWebView2Environment *env) override {
    g_envHr = hr;
    if (SUCCEEDED(hr) && env) {
      g_env = env;
      g_env->AddRef();
      ICoreWebView2Environment *e = nullptr;
      if (SUCCEEDED(env->QueryInterface(kIID_Env, (void **)&e)) && e) {
        LPWSTR v = nullptr;
        if (SUCCEEDED(e->get_BrowserVersionString(&v)) && v) {
          lstrcpynW(g_version, v, 64);
          CoTaskMemFree(v);
        }
        e->Release();
      }
      InterlockedExchange(&g_envState, 1);
    } else {
      InterlockedExchange(&g_envState, -1);
    }
    return S_OK;
  }
};

// ---------------------------------------------------------------------------
// 控制器创建
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 页面回调
// ---------------------------------------------------------------------------

struct MsgHandler : ICoreWebView2WebMessageReceivedEventHandler {
  SN_REF_IMPL(ICoreWebView2WebMessageReceivedEventHandler, kIID_MsgHandler)
  uintptr_t user;
  explicit MsgHandler(uintptr_t u) : user(u) {}
  HRESULT STDMETHODCALLTYPE Invoke(
      ICoreWebView2 *sender,
      ICoreWebView2WebMessageReceivedEventArgs *args) override {
    if (!args) return S_OK;
    LPWSTR json = nullptr;
    if (SUCCEEDED(args->get_WebMessageAsJson(&json)) && json) {
      std::string s = toUtf8(json);
      CoTaskMemFree(json);
      snGoMessage(user, s.c_str());
    }
    return S_OK;
  }
};

struct NavHandler : ICoreWebView2NavigationCompletedEventHandler {
  SN_REF_IMPL(ICoreWebView2NavigationCompletedEventHandler, kIID_NavHandler)
  uintptr_t user;
  explicit NavHandler(uintptr_t u) : user(u) {}
  HRESULT STDMETHODCALLTYPE Invoke(
      ICoreWebView2 *sender,
      ICoreWebView2NavigationCompletedEventArgs *args) override {
    BOOL ok = FALSE;
    if (args) args->get_IsSuccess(&ok);
    snGoReady(user, ok ? 1 : 0);
    return S_OK;
  }
};

struct CursorHandler : ICoreWebView2CursorChangedEventHandler {
  SN_REF_IMPL(ICoreWebView2CursorChangedEventHandler, kIID_CursorHandler)
  uintptr_t user;
  explicit CursorHandler(uintptr_t u) : user(u) {}
  HRESULT STDMETHODCALLTYPE Invoke(
      ICoreWebView2CompositionController *sender, IUnknown *args) override {
    if (!sender) return S_OK;
    HCURSOR cur = nullptr;
    if (SUCCEEDED(sender->get_Cursor(&cur)) && cur) {
      snGoCursor(user, (intptr_t)cur);
    }
    return S_OK;
  }
};

struct EvalHandler : ICoreWebView2ExecuteScriptCompletedHandler {
  SN_REF_IMPL(ICoreWebView2ExecuteScriptCompletedHandler, kIID_EvalHandler)
  uintptr_t user;
  uintptr_t token;
  EvalHandler(uintptr_t u, uintptr_t t) : user(u), token(t) {}
  HRESULT STDMETHODCALLTYPE Invoke(HRESULT hr, LPCWSTR resultJson) override {
    std::string s = SUCCEEDED(hr) ? toUtf8(resultJson) : std::string();
    snGoEval(user, token, SUCCEEDED(hr) ? 1 : 0, s.c_str());
    return S_OK;
  }
};

struct ScriptHandler
    : ICoreWebView2AddScriptToExecuteOnDocumentCreatedCompletedHandler {
  SN_REF_IMPL(ICoreWebView2AddScriptToExecuteOnDocumentCreatedCompletedHandler,
              kIID_ScriptHandler)
  HRESULT STDMETHODCALLTYPE Invoke(HRESULT hr, LPCWSTR id) override {
    if (FAILED(hr) && g_debug) {
      OutputDebugStringW(L"[sn] AddScriptToExecuteOnDocumentCreated 失败\n");
    }
    return S_OK;
  }
};

struct CompCtrlHandler
    : ICoreWebView2CreateCoreWebView2CompositionControllerCompletedHandler {
  SN_REF_IMPL(ICoreWebView2CreateCoreWebView2CompositionControllerCompletedHandler,
              kIID_CompCtrlHandler)
  SnWv2 *h;
  explicit CompCtrlHandler(SnWv2 *handle) : h(handle) {}

  HRESULT STDMETHODCALLTYPE Invoke(
      HRESULT hr, ICoreWebView2CompositionController *comp) override {
    h->hr = hr;
    if (FAILED(hr) || !comp) {
      InterlockedExchange(&h->state, -1);
      return S_OK;
    }

    h->comp = comp;
    h->comp->AddRef();

    // CompositionController 不继承 ICoreWebView2Controller，要单独 QI
    if (FAILED(h->comp->QueryInterface(kIID_Ctrl, (void **)&h->ctrl)) || !h->ctrl) {
      InterlockedExchange(&h->state, -1);
      return S_OK;
    }
    h->ctrl->AddRef();
    h->ctrl->get_CoreWebView2(&h->webview);
    if (h->webview) h->webview->AddRef();

    // 把页面挂到宿主的视觉树上
    h->comp->put_RootVisualTarget(h->root);

    // 背景透明。alpha 只能 0 或 255，要半透明得自己在内容里做。
    ICoreWebView2Controller2 *c2 = nullptr;
    if (SUCCEEDED(h->ctrl->QueryInterface(kIID_Ctrl2, (void **)&c2)) && c2) {
      COREWEBVIEW2_COLOR bg;
      bg.A = 0;
      bg.R = 0;
      bg.G = 0;
      bg.B = 0;
      c2->put_DefaultBackgroundColor(bg);
      c2->Release();
    }

    // 缩放：必须先关自动检测，否则导航后会被引擎重置
    ICoreWebView2Controller3 *c3 = nullptr;
    if (SUCCEEDED(h->ctrl->QueryInterface(kIID_Ctrl3, (void **)&c3)) && c3) {
      c3->put_ShouldDetectMonitorScaleChanges(FALSE);
      c3->put_RasterizationScale(h->scale);
      c3->Release();
    }

    RECT rc;
    GetClientRect(h->hwnd, &rc);
    h->ctrl->put_Bounds(rc);
    h->ctrl->put_IsVisible(TRUE);

    if (h->webview) {
      ICoreWebView2Settings *st = nullptr;
      if (SUCCEEDED(h->webview->get_Settings(&st)) && st) {
        st->put_AreDefaultContextMenusEnabled(g_debug ? TRUE : FALSE);
        st->put_AreDevToolsEnabled(g_debug ? TRUE : FALSE);
        st->put_IsStatusBarEnabled(FALSE);
        st->put_IsZoomControlEnabled(FALSE);
        st->put_IsBuiltInErrorPageEnabled(FALSE);
        st->Release();
      }
      EventRegistrationToken tok;
      h->webview->add_WebMessageReceived(new MsgHandler(h->user), &tok);
      h->webview->add_NavigationCompleted(new NavHandler(h->user), &tok);
    }
    if (h->comp) {
      EventRegistrationToken tok;
      h->comp->add_CursorChanged(new CursorHandler(h->user), &tok);
    }

    if (h->dcomp) h->dcomp->Commit();
    InterlockedExchange(&h->state, 1);
    return S_OK;
  }
};

// ---------------------------------------------------------------------------
// 环境
// ---------------------------------------------------------------------------

typedef HRESULT(STDAPICALLTYPE *PFN_CreateEnv)(
    PCWSTR, PCWSTR, ICoreWebView2EnvironmentOptions *,
    ICoreWebView2CreateCoreWebView2EnvironmentCompletedHandler *);

static PFN_CreateEnv loadCreateEnv(void) {
  // 先按 exe 所在目录找，找不到再让系统按 PATH 找。
  wchar_t exe[MAX_PATH] = {0};
  GetModuleFileNameW(nullptr, exe, MAX_PATH);
  wchar_t *slash = wcsrchr(exe, L'\\');
  if (slash) *(slash + 1) = 0;
  lstrcatW(exe, L"WebView2Loader.dll");

  HMODULE loader = LoadLibraryW(exe);
  if (!loader) loader = LoadLibraryW(L"WebView2Loader.dll");
  if (!loader) return nullptr;
  return (PFN_CreateEnv)GetProcAddress(
      loader, "CreateCoreWebView2EnvironmentWithOptions");
}

extern "C" int32_t sn_wv2_start_env(const wchar_t *userDataDir, int32_t debug) {
  g_debug = debug ? 1 : 0;
  if (InterlockedCompareExchange(&g_envState, 0, 0) != 0) return S_OK;

  // WebView2 要求调用线程先初始化 COM，且必须是 STA：页面里的拖拽、
  // 剪贴板、文件选择都跑在单线程套间上。S_FALSE 表示这根线程已经初始化
  // 过（例如托盘、shell 对话框先动过手），不算错；只有 RPC_E_CHANGED_MODE
  // 才是真冲突（有人把它设成了 MTA），那种情况下面一定会失败，得报出来。
  HRESULT coHr = CoInitializeEx(nullptr, COINIT_APARTMENTTHREADED);
  if (coHr == RPC_E_CHANGED_MODE) {
    g_envHr = coHr;
    InterlockedExchange(&g_envState, -1);
    return coHr;
  }

  PFN_CreateEnv createEnv = loadCreateEnv();
  if (!createEnv) {
    InterlockedExchange(&g_envState, -1);
    return HRESULT_FROM_WIN32(ERROR_MOD_NOT_FOUND);
  }
  // 传 nullptr 作 options：要自定义 browser 参数得自己实现
  // ICoreWebView2EnvironmentOptions，而它的官方头文件依赖 WRL，MinGW 没有。
  HRESULT hr = createEnv(nullptr, userDataDir, nullptr, new EnvHandler());
  if (FAILED(hr)) {
    g_envHr = hr;
    InterlockedExchange(&g_envState, -1);
    return hr;
  }
  return S_OK;
}

extern "C" int32_t sn_wv2_env_state(void) {
  return (int32_t)InterlockedCompareExchange(&g_envState, 0, 0);
}

extern "C" int32_t sn_wv2_env_hr(void) { return (int32_t)g_envHr; }

extern "C" int32_t sn_wv2_runtime_version(wchar_t *buf, int32_t cap) {
  if (!buf || cap <= 0) return 0;
  int n = lstrlenW(g_version);
  if (n >= cap) n = cap - 1;
  for (int i = 0; i < n; i++) buf[i] = g_version[i];
  buf[n] = 0;
  return n;
}

// ---------------------------------------------------------------------------
// 每窗口
// ---------------------------------------------------------------------------

static bool setupDComp(SnWv2 *h) {
  ID3D11Device *d3d = nullptr;
  IDXGIDevice *dxgi = nullptr;
  HRESULT hr = D3D11CreateDevice(nullptr, D3D_DRIVER_TYPE_HARDWARE, nullptr,
                                 D3D11_CREATE_DEVICE_BGRA_SUPPORT, nullptr, 0,
                                 D3D11_SDK_VERSION, &d3d, nullptr, nullptr);
  if (FAILED(hr)) {
    h->hr = hr;
    return false;
  }
  hr = d3d->QueryInterface(kIID_DXGIDevice, (void **)&dxgi);
  d3d->Release();
  if (FAILED(hr)) {
    h->hr = hr;
    return false;
  }

  typedef HRESULT(WINAPI * PFN_Create)(IUnknown *, REFIID, void **);
  HMODULE dcompDll = LoadLibraryW(L"dcomp.dll");
  PFN_Create create =
      dcompDll ? (PFN_Create)GetProcAddress(dcompDll, "DCompositionCreateDevice")
               : nullptr;
  if (!create) {
    dxgi->Release();
    h->hr = E_FAIL;
    return false;
  }
  hr = create(dxgi, kIID_DCompDevice, (void **)&h->dcomp);
  dxgi->Release();
  if (FAILED(hr)) {
    h->hr = hr;
    return false;
  }

  hr = h->dcomp->CreateTargetForHwnd(h->hwnd, TRUE, &h->target);
  if (FAILED(hr)) {
    h->hr = hr;
    return false;
  }
  hr = h->dcomp->CreateVisual(&h->root);
  if (FAILED(hr)) {
    h->hr = hr;
    return false;
  }
  h->target->SetRoot(h->root);
  h->dcomp->Commit();
  return true;
}

extern "C" SN_WV2 sn_wv2_new(uintptr_t hwnd, uintptr_t user) {
  if (sn_wv2_env_state() != 1 || !g_env) return nullptr;

  SnWv2 *h = new SnWv2();
  h->hwnd = (HWND)hwnd;
  h->user = user;
  h->scale = (double)dpiOfWindow((HWND)hwnd) / 96.0;

  if (!setupDComp(h)) {
    h->state = -1;
    return h;
  }

  ICoreWebView2Environment3 *env3 = nullptr;
  HRESULT hr = g_env->QueryInterface(kIID_Env3, (void **)&env3);
  if (FAILED(hr) || !env3) {
    h->hr = hr;
    h->state = -1;
    return h;
  }
  // 关键：Composition 版控制器只能从这里建
  hr = env3->CreateCoreWebView2CompositionController(h->hwnd,
                                                     new CompCtrlHandler(h));
  env3->Release();
  if (FAILED(hr)) {
    h->hr = hr;
    h->state = -1;
  }
  return h;
}

extern "C" int32_t sn_wv2_ready(SN_WV2 h) {
  if (!h) return -1;
  return (int32_t)InterlockedCompareExchange(&((SnWv2 *)h)->state, 0, 0);
}

extern "C" int32_t sn_wv2_hr(SN_WV2 h) { return h ? (int32_t)((SnWv2 *)h)->hr : 0; }

extern "C" void sn_wv2_free(SN_WV2 handle) {
  SnWv2 *h = (SnWv2 *)handle;
  if (!h) return;
  if (h->ctrl) {
    h->ctrl->Close();
    h->ctrl->Release();
    h->ctrl = nullptr;
  }
  if (h->comp) {
    h->comp->Release();
    h->comp = nullptr;
  }
  if (h->webview) {
    h->webview->Release();
    h->webview = nullptr;
  }
  if (h->root) {
    h->root->Release();
    h->root = nullptr;
  }
  if (h->target) {
    h->target->Release();
    h->target = nullptr;
  }
  if (h->dcomp) {
    h->dcomp->Release();
    h->dcomp = nullptr;
  }
  delete h;
}

extern "C" int32_t sn_wv2_map_host(SN_WV2 handle, const wchar_t *host,
                                   const wchar_t *folder) {
  SnWv2 *h = (SnWv2 *)handle;
  if (!h || !h->webview) return E_FAIL;
  ICoreWebView2_3 *w3 = nullptr;
  HRESULT hr = h->webview->QueryInterface(kIID_Web3, (void **)&w3);
  if (FAILED(hr) || !w3) return hr;
  hr = w3->SetVirtualHostNameToFolderMapping(
      host, folder, COREWEBVIEW2_HOST_RESOURCE_ACCESS_KIND_DENY_CORS);
  w3->Release();
  return hr;
}

extern "C" int32_t sn_wv2_inject_script(SN_WV2 handle, const wchar_t *js) {
  SnWv2 *h = (SnWv2 *)handle;
  if (!h || !h->webview || !js) return E_FAIL;
  return h->webview->AddScriptToExecuteOnDocumentCreated(js, new ScriptHandler());
}

extern "C" int32_t sn_wv2_navigate(SN_WV2 handle, const wchar_t *url) {
  SnWv2 *h = (SnWv2 *)handle;
  if (!h || !h->webview || !url) return E_FAIL;
  return h->webview->Navigate(url);
}

extern "C" int32_t sn_wv2_post_json(SN_WV2 handle, const wchar_t *json) {
  SnWv2 *h = (SnWv2 *)handle;
  if (!h || !h->webview || !json) return E_FAIL;
  return h->webview->PostWebMessageAsJson(json);
}

extern "C" int32_t sn_wv2_eval(SN_WV2 handle, const wchar_t *js,
                               uintptr_t token) {
  SnWv2 *h = (SnWv2 *)handle;
  if (!h || !h->webview || !js) return E_FAIL;
  return h->webview->ExecuteScript(js, new EvalHandler(h->user, token));
}

extern "C" int32_t sn_wv2_resize(SN_WV2 handle) {
  SnWv2 *h = (SnWv2 *)handle;
  if (!h || !h->ctrl) return E_FAIL;
  RECT rc;
  GetClientRect(h->hwnd, &rc);
  HRESULT hr = h->ctrl->put_Bounds(rc);
  if (h->dcomp) h->dcomp->Commit();
  return hr;
}

extern "C" int32_t sn_wv2_set_zoom(SN_WV2 handle, double zoom) {
  SnWv2 *h = (SnWv2 *)handle;
  if (!h || !h->ctrl) return E_FAIL;
  ICoreWebView2Controller3 *c3 = nullptr;
  if (FAILED(h->ctrl->QueryInterface(kIID_Ctrl3, (void **)&c3)) || !c3)
    return E_NOINTERFACE;

  // 每次都重新关一次自动检测：引擎在排版/导航后会顺手把它打开，
  // 一旦打开，下一次 DPI 变化就会把 RasterizationScale 覆盖回它自己算的值。
  c3->put_ShouldDetectMonitorScaleChanges(FALSE);
  double s = h->scale * zoom;
  if (s < 0.1) s = 0.1;
  if (s > 6.0) s = 6.0;
  HRESULT hr = c3->put_RasterizationScale(s);
  c3->Release();
  return hr;
}

extern "C" int32_t sn_wv2_set_devtools_enabled(SN_WV2 handle, int32_t on) {
  SnWv2 *h = (SnWv2 *)handle;
  if (!h || !h->webview) return E_FAIL;
  ICoreWebView2Settings *st = nullptr;
  if (FAILED(h->webview->get_Settings(&st)) || !st) return E_FAIL;
  HRESULT hr = st->put_AreDevToolsEnabled(on ? TRUE : FALSE);
  st->Release();
  return hr;
}

extern "C" int32_t sn_wv2_send_mouse(SN_WV2 handle, int32_t kind,
                                     int32_t virtualKeys, int32_t mouseData,
                                     int32_t x, int32_t y) {
  SnWv2 *h = (SnWv2 *)handle;
  if (!h || !h->comp) return E_FAIL;
  POINT pt;
  pt.x = x;
  pt.y = y;
  return h->comp->SendMouseInput((COREWEBVIEW2_MOUSE_EVENT_KIND)kind,
                                 (COREWEBVIEW2_MOUSE_EVENT_VIRTUAL_KEYS)virtualKeys,
                                 (UINT32)mouseData, pt);
}

extern "C" int32_t sn_wv2_focus(SN_WV2 handle) {
  SnWv2 *h = (SnWv2 *)handle;
  if (!h || !h->ctrl) return E_FAIL;
  return h->ctrl->MoveFocus(COREWEBVIEW2_MOVE_FOCUS_REASON_PROGRAMMATIC);
}
