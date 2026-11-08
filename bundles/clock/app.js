// 数字时钟。
//
// 一个"只看不点"的组件：贴在桌面图标层之上（样式 layer=desktop），
// 窗口是点击穿透的（WS_EX_TRANSPARENT）——鼠标落在表盘上会直接落到下面的
// 桌面图标上，所以组件里没有任何鼠标交互，也不需要 window.drag 权限。
// 位置改由管理界面的「布局」页拖动调整。
//
// 唯一的状态就是"现在几点"，所以没有 storage：每次启动现算。

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
// 时间
// ---------------------------------------------------------------------------

const hmEl = document.getElementById("hm");
const secEl = document.getElementById("sec");
const dateEl = document.getElementById("date");

const WEEK = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"];

function pad(n) {
  return n < 10 ? "0" + n : String(n);
}

// 只在文本真的变了才写 DOM：秒针每秒一次，但时和日一分钟/一天才变一次，
// 无谓的赋值会让浏览器每次都走一遍布局。
let shownHM = "";
let shownSec = "";
let shownDate = "";

function render() {
  const d = new Date();

  const hm = pad(d.getHours()) + ":" + pad(d.getMinutes());
  if (hm !== shownHM) {
    shownHM = hm;
    hmEl.textContent = hm;
  }

  const sec = ":" + pad(d.getSeconds());
  if (sec !== shownSec) {
    shownSec = sec;
    secEl.textContent = sec;
  }

  const day = d.getFullYear() + "-" + d.getMonth() + "-" + d.getDate();
  if (day !== shownDate) {
    shownDate = day;
    dateEl.textContent =
      `${d.getFullYear()}年${d.getMonth() + 1}月${d.getDate()}日 ${WEEK[d.getDay()]}`;
  }
}

let timer = 0;

// 对齐到整秒再触发，而不是简单的 setInterval(1000)：
// 后者会从上一次触发的时刻累加，跑久了会和系统时钟漂开。
function schedule() {
  clearTimeout(timer);
  const delay = 1000 - (Date.now() % 1000) + 8;
  timer = setTimeout(() => {
    render();
    schedule();
  }, delay);
}

// 窗口隐藏时定时器会被浏览器降频，回来时立刻补一次，别显示成旧时间。
document.addEventListener("visibilitychange", () => {
  if (document.hidden) {
    clearTimeout(timer);
    return;
  }
  render();
  schedule();
});

// ---------------------------------------------------------------------------
// 启动
// ---------------------------------------------------------------------------

async function boot() {
  render();
  schedule();

  if (!host) {
    return;
  }
  host.onMessage(handleMessage);

  try {
    const ready = await call("component.ready", { at: Date.now() });
    const name = (ready.instance && ready.instance.name) || "数字时钟";
    document.title = name;
    // 自检：日志里能对上"时钟起来了、第一屏显示什么、样式有没有真的注进来"，
    // 就不用靠截图判断界面到底渲染没渲染。
    // 底色取自 --sn-background：如果是兜底值而不是 desktop 样式的 #141416，
    // 说明样式没接上。
    const bg = getComputedStyle(document.body).backgroundColor;
    host.send({
      method: "host.log",
      params: {
        level: "info",
        message: `数字时钟就绪：${hmEl.textContent}${secEl.textContent} ${dateEl.textContent}` +
          ` · 组=${ready.style && ready.style.groupName} 底色=${bg}`,
      },
    });
  } catch (err) {
    host.send({
      method: "host.log",
      params: { level: "error", message: "数字时钟初始化失败：" + err.message },
    });
  }
}

boot();
