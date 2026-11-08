// 测试弹幕。
//
// 只做一件事：把 index.html 里那行字复制一份，滚动交给 CSS 动画。
//
// 位移不需要 JS 量：轨道里是两份完全相同的文字，所以 translate(-50%,0)
// 正好是一份的宽度，动画结束时第二份顶到第一份的起始位置，看不出接缝。
// （早先是靠 getBoundingClientRect 量宽度再写进 --shift，那套已经删掉了：
//  首轮量到的宽度不可靠，得靠重试才补得上，纯属自找麻烦。）
//
// 拖动和别的组件一样，走桥接请求宿主，组件自己不碰窗口。

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
// 无缝循环：复制一份文字
// ---------------------------------------------------------------------------

const track = document.getElementById("track");
const item = document.getElementById("item");

// 第二份文字：只为衔接，对读屏软件隐藏。
const copy = item.cloneNode(true);
copy.removeAttribute("id");
copy.setAttribute("aria-hidden", "true");
track.appendChild(copy);

// ---------------------------------------------------------------------------
// 变量
//
// 作者在 manifest 里声明了三个变量（弹幕文字、滚动速度、多行弹幕），值由用户
// 在管理界面里填。宿主在 ready 里给一次，之后每次改都推一条 host:vars。
//
// 注意两份文字都要改：无缝循环靠的是"轨道里两份一模一样的文字"，
// 只改第一份的话接缝处会突然换内容。
// ---------------------------------------------------------------------------

const SPEED = { slow: "28s", normal: "18s", fast: "10s" };

/**
 * 从 `lines`（列表式变量）里取出要滚的文字。
 *
 * 作者声明的是 `type: "list"`，用户在管理界面里**一行一条**地填，
 * 宿主把它包成 `[{"text":"第一条"}]` 下发——这里拿到的就是真正的数组，
 * 不需要 JSON.parse。每条取 `text` 字段，拼成一行；空数组表示"没用这个变量"。
 */
function linesText(list) {
  if (!Array.isArray(list)) return "";
  return list
    .map((o) => {
      if (typeof o === "string") return o.trim();
      return o && typeof o.text === "string" ? o.text.trim() : "";
    })
    .filter(Boolean)
    .join(" · ");
}

function applyVars(vars) {
  const v = vars || {};

  // 列表优先：用户填了多行就用多行，否则退回单行文字。
  // 两个变量都要处理——用户可能把列表清空、只留单行文字。
  const many = linesText(v.lines);
  const one = typeof v.text === "string" ? v.text : "";
  const text = (many || one).replace(/\s+/g, " ").trim();
  if (text) {
    item.textContent = text;
    copy.textContent = text;
  }

  const speed = SPEED[v.speed];
  if (speed) track.style.setProperty("--sn-toast-speed", speed);
}

// ---------------------------------------------------------------------------
// 启动
// ---------------------------------------------------------------------------

async function boot() {
  if (!host) {
    return;
  }
  host.onMessage(handleMessage);
  // 用户在管理界面里改了变量，宿主会推一条 host:vars。
  document.addEventListener("host:vars", (e) => applyVars(e.detail));

  try {
    const ready = await call("component.ready", { at: Date.now() });
    const name = (ready.instance && ready.instance.name) || "测试弹幕";
    document.title = name;
    applyVars(ready.vars);

    // 自检：日志里能对上"滚的是哪一行、一份文字多宽、速度是不是样式给的那个"。
    // animation-duration 取自 --sn-toast-speed：如果还是兜底值 18s，
    // 而样式声明的是别的值，就说明样式的 cssVars 没传到组件里。
    // 延 400ms 再报：脚本首轮 getBoundingClientRect 恒为 0。
    setTimeout(() => {
      const dur = getComputedStyle(track).animationDuration;
      const width = Math.round(item.getBoundingClientRect().width);
      host.send({
        method: "host.log",
        params: {
          level: width > 0 ? "info" : "warn",
          message: `测试弹幕就绪：一份文字 ${width > 0 ? width + "px" : "(未算出)"}` +
            ` · 速度=${dur} · 文字 ${item.textContent.trim().length} 字` +
            ` · 组=${ready.style && ready.style.groupName}`,
        },
      });
    }, 400);
  } catch (err) {
    host.send({
      method: "host.log",
      params: { level: "error", message: "测试弹幕初始化失败：" + err.message },
    });
  }
}

boot();
