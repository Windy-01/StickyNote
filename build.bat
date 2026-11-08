@echo off
rem 构建脚本：产出无控制台窗口的 exe，并把运行时依赖拷到 bin\。
setlocal
cd /d "%~dp0"

if not exist bin mkdir bin

rem 重建前先杀掉旧进程：exe 被占用时 go build 会失败，
rem 而后面几条命令照跑，看起来像"改了没生效"。
taskkill /F /IM StickyNote.exe >nul 2>&1

go build -ldflags "-H windowsgui -s -w" -o bin\StickyNote.exe .\cmd\stickynote
if errorlevel 1 (
    echo 构建失败
    exit /b 1
)

rem 渲染引擎换成 WebView2：运行时是系统自带的 Edge WebView2 Runtime，
rem 只有加载器 dll 需要跟 exe 放一起（wrap.cpp 用 LoadLibraryW 从 exe 同目录找）。
if not exist third_party\webview2\x64\WebView2Loader.dll (
    echo 缺少 third_party\webview2\x64\WebView2Loader.dll
    exit /b 1
)
copy /y third_party\webview2\x64\WebView2Loader.dll bin\WebView2Loader.dll >nul
if errorlevel 1 (
    echo 复制 WebView2Loader.dll 失败
    exit /b 1
)

rem 预置插件放在 exe 旁边，不打进 exe：
rem 漏了这一步程序也能起来，但只有一个空桌面（没有便签、没有时钟）。
if exist bundles (
    xcopy bundles bin\bundles /E /I /Y /Q >nul
    if errorlevel 1 (
        echo 复制 bundles 失败
        exit /b 1
    )
)

echo 已构建 bin\StickyNote.exe、bin\WebView2Loader.dll 与 bin\bundles\
