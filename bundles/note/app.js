// 便签组件。
//
// 这里没有一行代码碰窗口：拖动、缩放、置顶、关闭全部通过桥接请求宿主执行。
// 组件只负责"内容长什么样"和"用户输入怎么存"。
//
// 宿主给每个实例注入 window.__host，它提供 receive/onMessage/send 三个方法。

const host = window.__host;

let seq = 0;
const pending = new Map();

/** 发起一次请求，返回 Promise。 */
function call(method, params = {}) {
  return new Promise((resolve, reject) => {
    const id = ++seq;
    pending.set(id, { resolve, reject });
    host.send({ id, method, params });
  });
}

/** 单向通知，宿主不会回复。 */
function notify(method, params = {}) {
  host.send({ method, params });
}

function handleMessage(data) {
  // 宿主投递的是 JSON 对象字面量；兼容字符串形式，方便以后换传输层。
  let msg = data;
  if (typeof data === "string") {
    try {
      msg = JSON.parse(data);
    } catch {
      return;
    }
  }
  // 底层 WebView2 封装会把前端发出的消息原样回显一次，靠 from 字段区分。
  if (!msg || msg.from !== "host") return;

  if (msg.id && pending.has(msg.id)) {
    const entry = pending.get(msg.id);
    pending.delete(msg.id);
    if (msg.error) entry.reject(new Error(msg.error));
    else entry.resolve(msg.result);
    return;
  }
  if (msg.event) {
    document.dispatchEvent(new CustomEvent("host:" + msg.event, { detail: msg.data }));
  }
}

// ---------------------------------------------------------------------------
// 界面
// ---------------------------------------------------------------------------

const editor = document.getElementById("editor");
const statusEl = document.getElementById("status");
const dotEl = document.getElementById("dot");
const brandEl = document.getElementById("brand");
const closeBtn = document.getElementById("close");

const AUTOSAVE_DELAY = 700;
const STORAGE_KEY = "content";

let saveTimer = 0;
let dirty = false;
let resizable = true;

function setStatus(text, state = "idle") {
  statusEl.textContent = text;
  dotEl.dataset.state = state;
}

function stamp() {
  return new Date().toLocaleTimeString("zh-CN", { hour12: false });
}

async function flush() {
  clearTimeout(saveTimer);
  saveTimer = 0;
  if (!dirty) return;
  dirty = false;
  try {
    await call("storage.set", { key: STORAGE_KEY, value: editor.value });
    setStatus("已保存 " + stamp());
  } catch (err) {
    dirty = true;
    setStatus("保存失败：" + err.message, "error");
  }
}

function scheduleSave() {
  clearTimeout(saveTimer);
  saveTimer = setTimeout(flush, AUTOSAVE_DELAY);
}

editor.addEventListener("input", () => {
  dirty = true;
  setStatus("编辑中…", "busy");
  scheduleSave();
});

editor.addEventListener("blur", () => {
  if (dirty) flush();
});

// ---------------------------------------------------------------------------
// 无边框窗口：拖动
//
// 标题栏是页面内容，不在非客户区里，系统不知道那是标题栏，所以拖动必须
// 由前端发起：判落点 → 请宿主伪造 WM_NCLBUTTONDOWN(HTCAPTION)。
// 缩放不在这里做，见下面的说明。
// ---------------------------------------------------------------------------

// 缩放边框不在这里做：宿主在 WM_NCHITTEST 里回答 HTLEFT 这类值，
// 光标和缩放循环都由系统负责。
//
// 前端算边框这条路在这个引擎上走不通——CSS cursor 一旦被改成缩放箭头，
// 属性删掉之后引擎不会再发 WM_SETCURSOR，光标会永久卡在箭头上
// （实测 data-cursor 已清空，computed 仍报 w-resize）。
//
// 拖动只能前端发起：标题栏是页面内容，不在非客户区里，系统不知道那是标题栏。
document.addEventListener("mousedown", (e) => {
  if (e.button !== 0) return;
  // 按钮自己处理这次按下，别抢。
  if (e.target.closest("button")) return;
  if (e.target.closest(".titlebar")) {
    e.preventDefault();
    notify("window.beginDrag");
  }
});

// 置顶不再是便签自己的事：层级、穿透这些是**组**的属性，
// 用户在主界面的组设置里调，这里不提供按钮——一个组里的所有组件
// 行为必须一致，否则"同一个组"就没有意义了。

closeBtn.addEventListener("click", async () => {
  // 先落盘再关，避免最后一次输入还没到 700ms 就被关掉。
  await flush();
  notify("component.close");
});

// ---------------------------------------------------------------------------
// 变量
//
// 作者在 manifest 里声明了两个变量（标题、正文字号），值由用户在管理界面
// 按实例填。宿主在 ready 里给一次，之后每次改都在 host:vars 事件里推过来。
// 所以这里只需要一个 applyVars，两处共用。
// ---------------------------------------------------------------------------

const FONT_SIZE = { s: "13px", m: "15px", l: "18px", xl: "22px" };

// 组件名，由 ready 填上。标题变量留空时用它兜底。
let defaultBrand = "便签";

function applyVars(vars) {
  const v = vars || {};
  const title = typeof v.title === "string" ? v.title.trim() : "";
  // 留空就退回组件名：变量是配置，不该让窗口变成没名字的。
  brandEl.textContent = title || defaultBrand;
  editor.style.fontSize = FONT_SIZE[v.fontSize] || FONT_SIZE.m;
}

// ---------------------------------------------------------------------------
// 启动
// ---------------------------------------------------------------------------

async function boot() {
  if (!host) {
    setStatus("未连接宿主（浏览器预览模式）");
    return;
  }
  host.onMessage(handleMessage);
  // 用户在管理界面里改了变量，宿主会推一条 host:vars。
  document.addEventListener("host:vars", (e) => applyVars(e.detail));

  try {
    const ready = await call("component.ready", { at: Date.now() });
    const style = ready.style || {};

    resizable = style.resizable !== false;
    defaultBrand = (ready.instance && ready.instance.name) || "便签";
    applyVars(ready.vars);

    const saved = ready.storage && ready.storage[STORAGE_KEY];
    editor.value = typeof saved === "string" ? saved : "";

    // 不在这里 focus()：启动就抢键盘焦点不合适，空便签还会立刻顶着光标。
    // 用户点一下自然就进去了。
    setStatus("已就绪");
  } catch (err) {
    setStatus("加载失败：" + err.message, "error");
  }
}

boot();
