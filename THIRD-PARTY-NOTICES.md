# 第三方声明

本文件列出 **StickyNote 的二进制产物中包含的第三方代码**，以及各自适用的许可证。
本项目自身的代码采用 MIT License，见 [`LICENSE`](LICENSE)。

发出 exe 时请把 `LICENSE` 和本文件一起带上（见文末「再分发清单」）。

---

## 一览

| 组件 | 版本 | 许可证 | 版权 | 许可证原文 |
|---|---|---|---|---|
| Go 标准库与运行时 | go1.27.1 | BSD-3-Clause | The Go Authors | 本文件第一节 |
| `golang.org/x/sys` | v0.49.0 | BSD-3-Clause | The Go Authors | 本文件第一节（文本逐字节相同） |
| Microsoft Edge WebView2 SDK | 见下 | BSD-3-Clause | Microsoft Corporation | [`third_party/webview2/LICENSE`](third_party/webview2/LICENSE) |

`go.mod` 里只有一个直接依赖（`golang.org/x/sys`），但 **Go 二进制会静态链接 Go 运行时与标准库**，
所以实际进入 exe 的第三方代码是三份，不是一份。

---

## 一、Go 标准库与运行时 / golang.org/x/sys

两者的许可证文本**逐字节相同**（`$GOROOT/LICENSE` 与模块缓存里的 `golang.org/x/sys@v0.49.0/LICENSE`
做过 `cmp` 比对），版权人同为 The Go Authors，故合并列出。

```
Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

Go 另附一份**专利授权**（`$GOROOT/PATENTS`、`golang.org/x/sys@v0.49.0/PATENTS`）：
Google 承诺不对「实现」主张专利。那是额外的授权，不是附加义务，本项目据此受益。

---

## 二、Microsoft Edge WebView2 SDK

| 文件 | 来源 | 版权归属 |
|---|---|---|
| `third_party/webview2/include/WebView2.h` | Microsoft SDK（MIDL 生成） | Microsoft Corporation |
| `third_party/webview2/include/WebView2EnvironmentOptions.h` | Microsoft SDK | Microsoft Corporation（文件头已注明 BSD-style） |
| `third_party/webview2/x64/WebView2Loader.dll` | Microsoft SDK（可再分发） | Microsoft Corporation |
| `third_party/webview2/include/EventToken.h` | **本项目自写** | 本项目（MIT） |

许可证原文见 [`third_party/webview2/LICENSE`](third_party/webview2/LICENSE)
（BSD-3-Clause，`Copyright (C) Microsoft Corporation. All rights reserved.`）。
那份文本取自官方 NuGet 包 `Microsoft.Web.WebView2` 的 `LICENSE.txt`，**逐字节复制，未作改动**。

> `WebView2EnvironmentOptions.h` 的文件头写着「Use of this source code is governed by a
> BSD-style license that can be found in the LICENSE file.」——那个 LICENSE 就是现在放在
> 同目录的这份。在补上它之前，这个指路是断的。

`third_party/webview2/NOTICE.txt` 是该 NuGet 包自带的第三方声明，覆盖的是**打包工具链**
（`Antlr3.Runtime`、`StringTemplate4`，用于生成 SDK 包本身）。这些组件不进入本项目的二进制，
原样保留只为忠实传递上游材料。

**WebView2 Runtime 不随本程序分发**：它是系统级组件（Win11 预装，Win10 由 Edge 自动安装），
用户机器上已有。本项目只分发 SDK 里的 `WebView2Loader.dll`（加载器），不含浏览器本体。

---

## 三、本项目自有的部分

以下内容为本项目原创，适用 MIT License，**不属于第三方**：

- `cmd/`、`internal/` 下全部 Go 代码
- `internal/infrastructure/webview2/wrap.cpp` / `wrap.h`（自写的 cgo 包装，非 SDK 文件）
- `builtin/`（三种预设样式 + 主界面）与 `bundles/`（便签、时钟、弹幕）
- `cmd/stickynote/app.rc`、`rsrc.syso`（构建资源）
- `third_party/webview2/include/EventToken.h`（MinGW 8.1 缺这个头，自写的最小替身）

---

## 再分发清单

`build.bat` 已经把这几个文件自动拷进 `bin\`（分发目录）。发布时 `bin\` 长这样：

```
bin\
├── StickyNote.exe
├── WebView2Loader.dll
├── bundles\
├── LICENSE                    ← 本项目 MIT 正文
├── THIRD-PARTY-NOTICES.md     ← 本文件（Go / x/sys 的 BSD-3 全文在这里）
└── WebView2-SDK-LICENSE.txt   ← WebView2 SDK 的 BSD-3 全文
                              （源文件：third_party/webview2/LICENSE，只是换了个
                                在分发目录里更清楚的名字）
```

三条对应的义务：

| 许可证 | 条款 | 对本项目的含义 |
|---|---|---|
| MIT | 「The above copyright notice and this permission notice shall be included in all copies」 | 发 exe 时带上 `LICENSE` |
| BSD-3（Go / x/sys） | 「Redistributions in binary form must reproduce the above copyright notice … in the documentation and/or other materials provided with the distribution」 | 带上 BSD-3 全文（本文件第一节） |
| BSD-3（WebView2 SDK） | 同上，另有「不得用 Microsoft 名义背书」 | 带上 SDK 的 `LICENSE`；别在宣传里暗示微软背书 |

三条都不要求公开源码，也不要求以相同许可证发布衍生作品。
