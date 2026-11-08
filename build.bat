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

rem 许可证与第三方声明：MIT 要求随副本附带本声明，两份 BSD-3 要求在二进制分发的
rem 文档/材料里重现版权声明与免责条款。漏了不影响运行，但不合规。
copy /y LICENSE bin\LICENSE >nul
if errorlevel 1 echo 警告：复制 LICENSE 失败，分发前请手动补上
copy /y THIRD-PARTY-NOTICES.md bin\THIRD-PARTY-NOTICES.md >nul
if errorlevel 1 echo 警告：复制 THIRD-PARTY-NOTICES.md 失败
copy /y third_party\webview2\LICENSE bin\WebView2-SDK-LICENSE.txt >nul
if errorlevel 1 echo 警告：复制 WebView2 SDK LICENSE 失败

echo 已构建 bin\StickyNote.exe、bin\WebView2Loader.dll、bin\bundles\ 与许可证文件
