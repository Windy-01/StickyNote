package webview

// baseCSS 是所有窗口（主界面与所有组件）共用的底座样式。
//
// 存在的理由：每个组件各写一遍 reset 就一定会各踩一遍同样的坑，
// 所以统一放在内核里，由宿主在文档创建时注入。
//
// 引擎换成 WebView2（Chromium）之后，这里只剩真正的底座语义，
// 原先那些绕 Sciter 子集的补丁（align-self 居中、hidden 反射、
// inline-flex 包含块）全部删掉了——WebKit 里它们本来就是对的。
const baseCSS = `
html,body{margin:0;padding:0;height:100%;}
*{box-sizing:border-box;}

/* 页面默认透明：窗口内容由 DComp 直接合成，一张白底会把"背景透明"
   变成摆设。注意这只是**兜底**——它排在最前面，组件样式表里的
   body{background:var(--sn-background)} 会盖掉它，那是刻意的：
   要什么底色由样式包决定，不是内核决定。 */
html,body{background:transparent;}

body{
  font-family:"Microsoft YaHei UI","Segoe UI",system-ui,sans-serif;
  font-size:13px;
  line-height:1.5;
  -webkit-font-smoothing:antialiased;
}

/* 组件窗口没有浏览器滚动条，也不该出现选中蓝框。 */
::selection{background:rgba(64,132,255,.28);}
`
