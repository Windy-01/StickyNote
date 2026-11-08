// 管理界面（内核 UI）。
//
// 和组件一样，这里没有一行代码碰窗口：拖动、缩放、最小化、关闭
// 全部通过桥接请求宿主执行。区别在于它请求的是 manager.* 命名空间——
// 那是只有内核 UI 才能调的方法，组件拿不到。
//
// 数据模型只有三个，且**归属落在实例上**：
//   - 组（group）：窗口开关 + 外观。一个组管的是一批窗口，不是一批组件。
//   - 组件（component）：静态定义，本身没有组也没有窗口。
//   - 实例（instance）：一次运行，**唯一的管理单位**。它属于哪个组、在哪儿、
//     显示与否，都是它自己的事——同一个组件可以开两个窗口，一个贴桌面层、
//     一个浮在最上面。
//
// 侧边栏因此有两个视角，看的是同一批实例：
//   「组」按窗口形态归类（改外观与层级从这儿进）；
//   「组件」按来源归类（看一个组件到底开了几个窗口，逐个改它们的组）。

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
// DOM
// ---------------------------------------------------------------------------

const groupListEl = document.getElementById("groupList");
const sideTabsEl = document.getElementById("sideTabs");
const boardEl = document.getElementById("board");
const boardWrapEl = document.getElementById("boardWrap");
const boardFootEl = document.getElementById("boardFoot");
const boardTitleEl = document.getElementById("boardTitle");
const boardHintEl = document.getElementById("boardHint");
const legendEl = document.getElementById("legend");
const showAllBtn = document.getElementById("showAll");
const screenSizeEl = document.getElementById("screenSize");
const statusEl = document.getElementById("status");
const pathsEl = document.getElementById("paths");
const toastEl = document.getElementById("toast");
const drawerEl = document.getElementById("drawer");
const modalEl = document.getElementById("modal");
const modalTitleEl = document.getElementById("modalTitle");
const modalTextEl = document.getElementById("modalText");
const newGroupBtn = document.getElementById("newGroup");
const installBtn = document.getElementById("install");
const reloadBtn = document.getElementById("reload");
const openPluginsBtn = document.getElementById("openPlugins");
const minimizeBtn = document.getElementById("minimize");
const closeBtn = document.getElementById("close");

let state = { components: [], groups: [], instances: [], pluginRoot: "", screen: null, scale: 1 };

/** 侧边栏当前视角："groups" | "components"。 */
let sideTab = "groups";

/**
 * 当前抽屉。kind：
 *   "group"     组设置
 *   "component" 组件设置（含它名下全部实例）
 *   "instance"  单个实例（改它属于哪个组）
 *   "pick"      往某个组里挑组件新建实例
 * id 是主体的 id；pick 用 groupId。
 */
let drawer = null;

/**
 * 布局板的范围：空串表示「全部组」，否则是某一个组的 id。
 *
 * 位置是绝对屏幕坐标，所以"按组调整"只能在只显示它的前提下做——
 * 全都摊在一起时，拖一个方块到底改的是哪个组会变得含糊。
 */
let boardScope = "";

/**
 * 折叠起来的节点，键形如 "g:<组id>" / "c:<组件id>"。
 *
 * 只存在内存里：这是"我现在想看什么"的临时状态，不是配置。
 * 存到磁盘会让下次启动的界面和用户关窗口那一刻绑死，反而更难找东西。
 */
const collapsed = new Set();

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

function esc(v) {
  return String(v == null ? "" : v).replace(/[&<>"']/g, (ch) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  }[ch]));
}

function setHidden(el, on) {
  if (!el) return;
  if (on) el.setAttribute("hidden", "");
  else el.removeAttribute("hidden");
}

function groups() {
  return state.groups || [];
}

function components() {
  return state.components || [];
}

function instances() {
  return state.instances || [];
}

function componentByID(id) {
  return components().find((c) => c.id === id) || null;
}

function groupByID(id) {
  return groups().find((g) => g.id === id) || null;
}

function findInstance(id) {
  return instances().find((i) => i.id === id) || null;
}

function instancesOfGroup(groupId) {
  return instances().filter((i) => i.groupId === groupId);
}

function instancesOfComponent(componentId) {
  return instances().filter((i) => i.componentId === componentId);
}

const LAYER_LABEL = { normal: "常规", topmost: "置顶", desktop: "置底" };
const SIZE_LABEL = { fixed: "固定", user: "可调", fitContent: "随内容" };
const VAR_TYPE_LABEL = {
  text: "单行文本",
  textarea: "长文本",
  select: "选项",
  number: "数字",
  bool: "开关",
  list: "列表",
  group: "记录表",
};

function layerLabel(v) {
  return LAYER_LABEL[v] || v || "常规";
}

/** 实例的状态短句，侧边栏和抽屉共用。 */
function instanceStateText(i) {
  if (!i.running) return "未运行";
  return i.visible ? "显示中" : "已隐藏";
}

/** 组色转成"淡底"：同色加一点透明度，方块才能既透出桌面又带上组色。 */
function softColor(hex) {
  if (!/^#[0-9a-fA-F]{6}$/.test(hex || "")) return "rgba(255, 255, 255, 0.9)";
  return hex + "2e"; // #RRGGBBAA，约 18% 不透明度
}

/** 组色写成 CSS 变量，供 .group / .box / .legend__item 共用。 */
function groupVars(g) {
  const c = g && g.color ? g.color : "#cfc9bd";
  return `--g:${esc(c)};--g-soft:${esc(softColor(c))}`;
}

/** 组下拉选项。selected 是当前值，找不到对应组时选第一个。 */
function groupOptions(selected) {
  const list = groups();
  if (!list.length) return "";
  const has = list.some((g) => g.id === selected);
  return list
    .map((g) => {
      const on = has ? g.id === selected : g.id === list[0].id;
      return `<option value="${esc(g.id)}" ${on ? "selected" : ""}>${esc(g.name)}</option>`;
    })
    .join("");
}

const chevSVG = `<svg viewBox="0 0 12 12" aria-hidden="true"><path d="M2.5 4.5l3.5 3.5 3.5-3.5"/></svg>`;

// ---------------------------------------------------------------------------
// 侧边栏：组视角
// ---------------------------------------------------------------------------

/** 一个实例行。
 *
 * 侧边栏按**实例**列，不按组件类型列：用户面前摆的是一个个窗口，
 * "便签有 2 个窗口"和"有 2 个便签组件"是两件事，前者才是他要管的。
 *
 * withGroup 为真时右侧显示"属于哪个组"的下拉（组件视角用）；
 * 组视角里组是隐含的，改显示实例 id——那是删实例时要对上的东西。
 */
function instanceRow(i, withGroup) {
  const c = componentByID(i.componentId);
  const name = c ? c.name : i.componentId;
  const g = groupByID(i.groupId);
  const locked = !c || !c.enabled;
  const on = i.running && i.visible;

  const why = `${name} · ${i.id} · ${instanceStateText(i)} · ${i.width}×${i.height}` +
    (g ? ` · 组 ${g.name}` : " · 所属组不存在");

  const right = withGroup
    ? `<select class="item__group" data-act="instanceGroup" data-instance="${esc(i.id)}"
        title="这个窗口属于哪个组（换组会重建窗口）">${groupOptions(i.groupId)}</select>`
    : `<span class="item__meta">${esc(i.id)}</span>`;

  return `
  <div class="item ${locked || !i.visible ? "is-off" : ""}"
    data-act="instanceOpen" data-instance="${esc(i.id)}" title="${esc(why)}">
    <span class="item__dot" data-on="${on ? "1" : "0"}"></span>
    <span class="item__name">${esc(name)}</span>
    ${right}
    <label class="switch" title="${locked ? "组件已停用" : i.visible ? "隐藏这个窗口" : "显示这个窗口"}">
      <input type="checkbox" data-act="instanceVisible" data-instance="${esc(i.id)}"
        ${i.visible ? "checked" : ""} ${locked ? "disabled" : ""}>
      <span class="switch__track"></span>
    </label>
  </div>`;
}

/** 一个组卡片：折叠箭头 + 色点 + 名字 + 层级/穿透徽标 + 实例列表。 */
function groupCard(g) {
  const rows = instancesOfGroup(g.id);
  const isCollapsed = collapsed.has("g:" + g.id);

  // 徽标只留「层级」和「穿透」：再加一条"固定尺寸"会在 251px 的侧边栏里
  // 把组名挤成省略号，而尺寸模式在设置里看得到。
  const badges = [`<span class="badge badge--layer">${esc(layerLabel(g.layer))}</span>`];
  if (g.clickThrough) badges.push(`<span class="badge badge--through">穿透</span>`);

  const body = rows.length
    ? rows.map((i) => instanceRow(i, false)).join("")
    : `<div class="group__empty">这个组还没有实例。点右上角 + 选一个组件放进来。</div>`;

  return `
  <article class="group ${boardScope === g.id ? "is-active" : ""}"
    data-group="${esc(g.id)}" style="${groupVars(g)}">
    <div class="group__head" data-act="groupFocus" data-id="${esc(g.id)}"
      title="${boardScope === g.id ? "回到全部组" : "只看这个组，可以拖动它里面的方块"}">
      <button type="button" class="group__chev ${isCollapsed ? "is-collapsed" : ""}"
        data-act="groupToggle" data-id="${esc(g.id)}" title="${isCollapsed ? "展开" : "折叠"}">${chevSVG}</button>
      <span class="group__dot"></span>
      <span class="group__name">${esc(g.name)}</span>
      <span class="group__count">${rows.length}</span>
      <span class="group__badges">${badges.join("")}</span>
      <span class="group__acts">
        <button type="button" class="group__add" data-act="pickComponent" data-group="${esc(g.id)}"
          title="选一个组件放进这个组">+</button>
        <button type="button" class="group__gear" data-act="groupOpen" data-id="${esc(g.id)}"
          title="组设置">⋯</button>
      </span>
    </div>
    <div class="group__members" ${isCollapsed ? "hidden" : ""}>${body}</div>
  </article>`;
}

function renderGroups() {
  const list = groups();
  if (!list.length) {
    groupListEl.innerHTML = `
      <div class="empty">
        <div class="empty__title">还没有组</div>
        <div>组决定一批窗口的层级、穿透和外观。</div>
        <div>点右上角 + 造一个。</div>
      </div>`;
    return;
  }

  // 重渲染会重置滚动位置，先存后还原，否则每次开关都跳回顶部。
  const top = groupListEl.scrollTop;
  let html = list.map(groupCard).join("");

  // 归属不明的实例单独列出来。正常流程下不会出现（内核每次启动都会把
  // 指向已消失组的实例挪进兜底组），但数据被外部改坏时得让人看见，
  // 而不是"窗口凭空消失了"。
  const known = new Set(list.map((g) => g.id));
  const orphans = instances().filter((i) => !known.has(i.groupId));
  if (orphans.length) {
    html += `
    <article class="group">
      <div class="group__head">
        <span class="group__dot"></span>
        <span class="group__name">未分组</span>
        <span class="group__count">${orphans.length}</span>
        <span class="group__badges"><span class="badge badge--through">异常</span></span>
      </div>
      <div class="group__members">${orphans.map((i) => instanceRow(i, true)).join("")}</div>
    </article>`;
  }

  groupListEl.innerHTML = html;
  groupListEl.scrollTop = top;
}

// ---------------------------------------------------------------------------
// 侧边栏：组件视角
//
// 「组件存入独立的组件列表」：这里按组件列，一个组件一张卡，卡里是它名下
// 的全部实例。同一组件的两个窗口可以分属不同的组，所以每行都带组下拉。
// ---------------------------------------------------------------------------

function componentCard(c) {
  const rows = instancesOfComponent(c.id);
  const isCollapsed = collapsed.has("c:" + c.id);
  const sub = `${c.id}${c.builtin ? " · 内置" : ""}`;

  const body = rows.length
    ? rows.map((i) => instanceRow(i, true)).join("")
    : `<div class="group__empty">还没有实例。点右上角 ⋯ 进设置新建一个。</div>`;

  return `
  <article class="comp ${c.enabled ? "" : "is-off"}" data-comp="${esc(c.id)}">
    <div class="comp__head">
      <button type="button" class="group__chev ${isCollapsed ? "is-collapsed" : ""}"
        data-act="compToggle" data-id="${esc(c.id)}" title="${isCollapsed ? "展开" : "折叠"}">${chevSVG}</button>
      <span class="comp__name" data-act="componentOpen" data-id="${esc(c.id)}"
        title="${esc(sub)}">${esc(c.name)}</span>
      <span class="group__count">${rows.length}</span>
      <label class="switch" title="${c.enabled ? "停用这个组件" : "启用这个组件"}">
        <input type="checkbox" data-act="toggle" data-id="${esc(c.id)}" ${c.enabled ? "checked" : ""}>
        <span class="switch__track"></span>
      </label>
      <button type="button" class="group__gear" data-act="componentOpen" data-id="${esc(c.id)}"
        title="组件设置">⋯</button>
    </div>
    <div class="group__members" ${isCollapsed ? "hidden" : ""}>${body}</div>
  </article>`;
}

function renderComponents() {
  const list = components();
  if (!list.length) {
    groupListEl.innerHTML = `
      <div class="empty">
        <div class="empty__title">还没有组件</div>
        <div>点下面的「安装组件…」挑一个文件夹。</div>
      </div>`;
    return;
  }
  const top = groupListEl.scrollTop;
  groupListEl.innerHTML = list.map(componentCard).join("");
  groupListEl.scrollTop = top;
}

function renderSideTabs() {
  for (const b of sideTabsEl.querySelectorAll(".seg__btn")) {
    b.classList.toggle("is-on", b.dataset.tab === sideTab);
  }
  // 新建组只在组视角有意义。
  setHidden(newGroupBtn, sideTab !== "groups");
}

function renderSidebar() {
  renderSideTabs();
  if (sideTab === "components") renderComponents();
  else renderGroups();
}

// ---------------------------------------------------------------------------
// 布局板
//
// 把桌面按比例画成一块板子，每个实例是一个方块，拖方块就等于拖窗口。
// 坐标一律用宿主给的**物理像素**，这里只做等比换算——前端不碰 DPI，
// 也不做逻辑像素换算，否则 125% 缩放下方块会和窗口对不上。
// ---------------------------------------------------------------------------

/** 当前是否正在拖方块。拖动期间不重渲染，否则刚抓起来的元素会被换掉。 */
let dragging = null;

function clamp(v, lo, hi) {
  if (hi < lo) return lo;
  return Math.min(Math.max(v, lo), hi);
}

// sizeTries 是"量不到就再试"的计数。
//
// 刚显示那一刻布局还没算完，经常读到 0。用 setTimeout 重试而不是等下一帧：
// 这里要的是"过一会儿再看"，不需要跟帧，重试几次总能读到。
// 不重试的后果很具体：板子停在 2x2，所有方块被压成小点堆在左上角。
let sizeTries = 0;

function sizeBoard() {
  const wrap = boardWrapEl;
  const s = state.screen;
  if (!wrap || !s || !s.w || !s.h) return;
  if (wrap.clientWidth < 8 || wrap.clientHeight < 8) {
    if (sizeTries < 25) {
      sizeTries++;
      setTimeout(sizeBoard, 20);
    }
    return;
  }
  sizeTries = 0;
  // 扣掉内边距再算：clientWidth 含 padding，不扣的话板子会比可用空间大 32px，
  // 在窄窗口里直接溢出、被裁掉右边。
  const cs = getComputedStyle(wrap);
  const availW = wrap.clientWidth - (parseFloat(cs.paddingLeft) || 0) - (parseFloat(cs.paddingRight) || 0);
  const availH = wrap.clientHeight - (parseFloat(cs.paddingTop) || 0) - (parseFloat(cs.paddingBottom) || 0);
  if (availW < 8 || availH < 8) {
    if (sizeTries < 25) {
      sizeTries++;
      setTimeout(sizeBoard, 20);
    }
    return;
  }
  const k = Math.min(availW / s.w, availH / s.h);
  boardEl.style.width = `${Math.round(s.w * k)}px`;
  boardEl.style.height = `${Math.round(s.h * k)}px`;
  // 方块尺寸用像素写，不用百分比：绝对定位子元素的百分比高度算不出来，
  // 会被压成 min-height。板子尺寸一变就要重算一次，所以放在这里。
  for (const b of boardEl.querySelectorAll(".box")) {
    const bw = Number(b.dataset.w || 0);
    const bh = Number(b.dataset.h || 0);
    if (bw > 0) b.style.width = `${Math.round(bw * k)}px`;
    if (bh > 0) b.style.height = `${Math.round(bh * k)}px`;
  }
}

/** 组图例：只在「全部组」视图出现，点一下切到那个组。 */
function renderLegend(all) {
  const list = groups();
  if (boardScope || !list.length) {
    legendEl.innerHTML = "";
    setHidden(legendEl, true);
    return;
  }
  legendEl.innerHTML = list
    .map((g) => {
      const n = all.filter((i) => i.groupId === g.id).length;
      return `
      <button type="button" class="legend__item ${n ? "" : "is-empty"}"
        data-act="groupFocus" data-id="${esc(g.id)}" style="${groupVars(g)}"
        title="${esc(g.name)} · ${n} 个方块">
        <span class="legend__dot"></span>
        <span>${esc(g.name)}</span>
        <span class="legend__n">${n}</span>
      </button>`;
    })
    .join("");
  setHidden(legendEl, false);
}

function renderBoard() {
  // 范围指向的组已经没了（被删掉）就退回全部，否则板子会一直是空的。
  if (boardScope && !groupByID(boardScope)) boardScope = "";
  const scope = boardScope ? groupByID(boardScope) : null;

  const all = instances();
  const items = boardScope ? all.filter((i) => i.groupId === boardScope) : all;
  const readonly = !boardScope;
  // 能不能拖边框改尺寸，只看组的「尺寸」档位。
  //
  // **刻意不看 `resizable`**：那个开关说的是"用户能不能直接拖窗口边框"，
  // 而穿透窗口和桌面层窗口根本收不到鼠标，它们永远是 false——桌面层和浮层
  // 这两个内置组就是这样。拿它去挡布局板，等于这两个组永远改不了尺寸，
  // 而布局板恰恰是它们唯一的尺寸入口。
  const canResize = !!scope && scope.sizeMode !== "fixed";

  boardEl.classList.toggle("is-readonly", readonly);
  boardTitleEl.textContent = scope ? scope.name : "全部组";
  const resizeHint = !scope
    ? ""
    : canResize
      ? " · 拖边角改大小"
      : " · 尺寸档位是「固定」，改不了大小";
  boardHintEl.textContent = scope
    ? `${layerLabel(scope.layer)}${scope.clickThrough ? " · 穿透" : ""} · 拖动方块改位置${resizeHint} · 双击居中`
    : "按组区分颜色 · 点方块或下面的组名进入该组调整位置与大小";
  setHidden(showAllBtn, !boardScope);
  renderLegend(all);

  const s = state.screen;
  if (!s || !s.w || !s.h) {
    boardEl.innerHTML = "";
    boardFootEl.textContent = "读不到屏幕尺寸，布局板暂时画不出来。";
    screenSizeEl.textContent = "";
    return;
  }

  boardEl.innerHTML = items
    .map((i) => {
      const off = !i.running || !i.visible;
      const g = groupByID(i.groupId);
      const c = componentByID(i.componentId);
      const cls = ["box"];
      if (off) cls.push("box--off");
      if (g && g.clickThrough) cls.push("box--through");
      const left = ((i.x - s.x) / s.w) * 100;
      const top = ((i.y - s.y) / s.h) * 100;
      // 只读视图里方块可点，点它就进到它所属的组；组视图里方块只用来拖。
      const focus = readonly
        ? `data-act="boxFocus" data-group="${esc(i.groupId)}"`
        : "";
      const label = c ? c.name : i.componentId;
      // 八个手柄：四角 + 四边。用绝对定位贴在方块边缘，比在 mousedown 里
      // 算"离边几像素"可靠——后者在方块被缩到很小时会误判。
      const grips = canResize
        ? ["nw", "n", "ne", "e", "se", "s", "sw", "w"]
            .map((d) => `<span class="grip grip--${d}" data-edge="${d}"></span>`)
            .join("")
        : "";
      return `
      <div class="${cls.join(" ")}" data-instance="${esc(i.id)}"
        data-w="${i.w}" data-h="${i.h}" ${focus}
        style="left:${left}%;top:${top}%;${groupVars(g)}"
        title="${esc(label)} ${esc(i.id)} · ${i.x}, ${i.y} · ${i.w}×${i.h} · ${esc(g ? g.name : "组不存在")}">
        <span class="box__label">${esc(label)}</span>${grips}
      </div>`;
    })
    .join("");

  const groupCount = new Set(items.map((i) => i.groupId)).size;
  boardFootEl.textContent = !items.length
    ? scope
      ? "这个组还没有实例。点组名右上角的 + 选一个组件放进来。"
      : "还没有实例。在侧边栏的组里点 + 新建一个。"
    : scope
      ? `${s.w} × ${s.h}（物理像素）· 本组 ${items.length} 个方块`
      : `${s.w} × ${s.h}（物理像素）· ${items.length} 个方块，分属 ${groupCount} 个组`;
  screenSizeEl.textContent = `${s.w} × ${s.h} @${Math.round((state.scale || 1) * 100)}%`;
  sizeBoard();
}

/** 把实例坐标写到方块上（拖动时每帧调用，不重渲染整块板子）。 */
function placeBox(box, inst, s) {
  box.style.left = `${((inst.x - s.x) / s.w) * 100}%`;
  box.style.top = `${((inst.y - s.y) / s.h) * 100}%`;
}

// 拖动过程中的位置更新：**不节流**。
//
// 原来用 setTimeout(16ms) 合并消息，实测拖动时 mousemove 太密，定时器
// 一直排不上队，结果只在松手时发了一次位置——视觉上就是"方块从起点直接
// 跳到终点"，中间过程全丢。
//
// 改成每条 mousemove 直接发，靠"位置没变就不发"去重。频率等于鼠标采样率
// （几十 Hz），进程内 JSON 消息完全吃得下。
let lastMove = { id: "", x: 0, y: 0 };

function sendMove(inst, commit) {
  if (!commit && lastMove.id === inst.id && lastMove.x === inst.x && lastMove.y === inst.y) {
    return;
  }
  lastMove = { id: inst.id, x: inst.x, y: inst.y };
  call("manager.moveInstance", {
    instanceId: inst.id,
    x: inst.x,
    y: inst.y,
    commit,
  }).catch((err) => toast({ ok: false, text: err.message || String(err) }));
}

// 拖动帧计数。排查"拖拽一跳到位"时用来区分是前端没更新还是宿主没挪窗：
//   raw 少            → 引擎/系统没把移动送来（窗口侧的问题）
//   raw 多、frames 少 → 坐标没变或换算写错（脚本侧的问题）
let dragFrames = 0;
let dragRaw = 0;

boardEl.addEventListener("mousedown", (e) => {
  if (e.button !== 0) return;
  // 全部组视图是只读总览：位置只能进到某个组里改，否则"拖的是谁"会含糊。
  if (!boardScope) return;

  // 边框手柄优先：手柄压在方块边缘上，不先判断就会被当成"拖位置"。
  const grip = e.target.closest(".grip");
  const box = e.target.closest(".box");
  if (!box) return;
  const inst = findInstance(box.dataset.instance);
  const s = state.screen;
  if (!inst || !s) return;
  const rect = boardEl.getBoundingClientRect();
  if (rect.width <= 0) return;

  e.preventDefault();

  if (grip) {
    startResize(box, inst, grip.dataset.edge, e, s, rect);
    return;
  }

  dragFrames = 0;
  dragRaw = 0;
  box.classList.add("is-dragging");
  dragging = {
    box,
    inst,
    // 物理像素 / CSS 像素。板子缩放多少，这个比值就补回多少。
    k: s.w / rect.width,
    startX: e.clientX,
    startY: e.clientY,
    originX: inst.x,
    originY: inst.y,
    moved: false,
  };
});

// ---------------------------------------------------------------------------
// 拖边框改尺寸
//
// 和拖位置一样：坐标全在**物理像素**里算，前端只做等比换算。
// 拖动中每帧只改窗口（commit=false），松手才落盘——一次拖拽几十上百帧，
// 每帧写一遍 layout.json 没有意义。
//
// 缩放不动：内容缩放（实例抽屉里的那个）改的是页面大小，和窗口尺寸是两个
// 正交维度，这里只改窗口。
// ---------------------------------------------------------------------------

/** 正在改尺寸。为 null 表示没在改。 */
let resizing = null;

function startResize(box, inst, edge, e, s, rect) {
  // 夹取下限用内核给的最小尺寸（物理像素）：前端先按它夹，方块就不会
  // 缩到比窗口真能到的最小值还小，松手后再被弹回来。
  //
  // 再跟当前尺寸取小：万一存盘的尺寸本来就小于声明的下限（手改 layout.json
  // 或者作者改过 manifest），按下手柄的第一帧会被"夹"到下限，窗口凭空变大
  // 一截。宁可让它先拖不动，也不要替用户改尺寸。
  const floorW = Math.min(inst.minW || inst.w, inst.w);
  const floorH = Math.min(inst.minH || inst.h, inst.h);
  const minW = Math.max(24, Math.round(floorW * (rect.width / s.w)));
  const minH = Math.max(24, Math.round(floorH * (rect.height / s.h)));
  box.classList.add("is-resizing");
  resizing = {
    box,
    inst,
    edge,
    // 物理像素 / CSS 像素，和拖动那条一致。
    k: s.w / rect.width,
    // CSS 像素 / 物理像素：写回方块尺寸时用。
    css: rect.width / s.w,
    startX: e.clientX,
    startY: e.clientY,
    originX: inst.x,
    originY: inst.y,
    originW: inst.w,
    originH: inst.h,
    minW,
    minH,
    moved: false,
    frames: 0,
  };
}

/** 把尺寸写到方块上（CSS 像素），并记进 dataset 让 sizeBoard 能重算。 */
function sizeBox(box, w, h, css) {
  box.dataset.w = String(w);
  box.dataset.h = String(h);
  box.style.width = `${Math.round(w * css)}px`;
  box.style.height = `${Math.round(h * css)}px`;
}

function sendResize(inst, commit) {
  // 尺寸按逻辑像素送：layout.json 里存的就是逻辑像素，
  // 换到别的 DPI 缩放比例下才对得上。
  const scale = state.scale || 1;
  const w = Math.round(inst.w / scale);
  const h = Math.round(inst.h / scale);
  if (!commit && lastResize.id === inst.id && lastResize.w === w && lastResize.h === h &&
      lastResize.x === inst.x && lastResize.y === inst.y) {
    return;
  }
  lastResize = { id: inst.id, x: inst.x, y: inst.y, w, h };
  call("manager.setInstanceBounds", {
    instanceId: inst.id,
    x: inst.x,
    y: inst.y,
    w,
    h,
    commit,
  }).catch((err) => toast({ ok: false, text: err.message || String(err) }));
}

let lastResize = { id: "", x: 0, y: 0, w: 0, h: 0 };

document.addEventListener("mousemove", (e) => {
  if (resizing) {
    moveResize(e);
    return;
  }
  if (!dragging) return;
  dragRaw++;
  const { box, inst, k, startX, startY, originX, originY } = dragging;
  const s = state.screen;

  // 夹在桌面范围内：窗口不能挪到屏幕外，否则用户再也找不回来。
  const nx = clamp(Math.round(originX + (e.clientX - startX) * k), s.x, s.x + s.w - inst.w);
  const ny = clamp(Math.round(originY + (e.clientY - startY) * k), s.y, s.y + s.h - inst.h);
  if (nx === inst.x && ny === inst.y) return;

  inst.x = nx;
  inst.y = ny;
  dragging.moved = true;
  dragFrames++;
  placeBox(box, inst, s);

  boardFootEl.textContent = `${inst.name} → ${nx}, ${ny}`;
  sendMove(inst, false);
});

function endDrag(commit) {
  if (!dragging) return;
  const { box, inst, moved } = dragging;
  box.classList.remove("is-dragging");
  dragging = null;
  // 自检：拖动一共更新了几帧。0 或 1 说明前端这条链断了；
  // 数字正常而窗口没跟上，就说明问题在宿主侧。
  if (host) {
    host.send({
      method: "host.log",
      params: {
        level: "info",
        message: `拖动结束：收到 mousemove ${dragRaw} 条，位置更新 ${dragFrames} 帧，落到 ${inst.x},${inst.y}`,
      },
    });
  }
  if (moved && commit) sendMove(inst, true);
}

function moveResize(e) {
  const r = resizing;
  const s = state.screen;
  if (!s) return;
  const dx = Math.round((e.clientX - r.startX) * r.k);
  const dy = Math.round((e.clientY - r.startY) * r.k);

  let { originX: x, originY: y, originW: w, originH: h } = r;
  const edge = r.edge;

  // 拖左边/上边时尺寸和位置一起变，右边（下边）固定不动。
  // 先按"对边固定"把尺寸夹好，再由尺寸反推位置——反过来的话，
  // 窗口贴到屏幕边缘时会出现"宽度被夹小、左边还在往外跑"的错位。
  if (edge.indexOf("w") >= 0) {
    w = clamp(r.originW - dx, r.minW, r.originX + r.originW - s.x);
    x = r.originX + r.originW - w;
  } else if (edge.indexOf("e") >= 0) {
    w = clamp(r.originW + dx, r.minW, s.x + s.w - r.originX);
  }
  if (edge.indexOf("n") >= 0) {
    h = clamp(r.originH - dy, r.minH, r.originY + r.originH - s.y);
    y = r.originY + r.originH - h;
  } else if (edge.indexOf("s") >= 0) {
    h = clamp(r.originH + dy, r.minH, s.y + s.h - r.originY);
  }

  const inst = r.inst;
  if (x === inst.x && y === inst.y && w === inst.w && h === inst.h) return;
  inst.x = x;
  inst.y = y;
  inst.w = w;
  inst.h = h;
  r.moved = true;
  r.frames++;
  placeBox(r.box, inst, s);
  sizeBox(r.box, w, h, r.css);

  boardFootEl.textContent = `${inst.name} → ${inst.x}, ${inst.y} · ${w}×${h}`;
  sendResize(inst, false);
}

function endResize(commit) {
  if (!resizing) return;
  const { box, inst, moved, frames } = resizing;
  box.classList.remove("is-resizing");
  resizing = null;
  if (host) {
    host.send({
      method: "host.log",
      params: {
        level: "info",
        message: `改尺寸结束：更新 ${frames} 帧，落到 ${inst.x},${inst.y} ${inst.w}×${inst.h}（物理像素）`,
      },
    });
  }
  if (moved && commit) sendResize(inst, true);
}

document.addEventListener("mouseup", () => {
  endResize(true);
  endDrag(true);
});

// 双击居中：穿透窗口和桌面层窗口都不好拖，这里给一个确定的落点。
boardEl.addEventListener("dblclick", (e) => {
  if (!boardScope) return;
  const box = e.target.closest(".box");
  if (!box) return;
  const inst = findInstance(box.dataset.instance);
  const s = state.screen;
  if (!inst || !s) return;
  inst.x = s.x + Math.round((s.w - inst.w) / 2);
  inst.y = s.y + Math.round((s.h - inst.h) / 2);
  placeBox(box, inst, s);
  sendMove(inst, true);
});

window.addEventListener("resize", sizeBoard);

// ---------------------------------------------------------------------------
// 抽屉：通用小件
// ---------------------------------------------------------------------------

function segRow(act, values, current) {
  const btns = values
    .map(([v, label]) => `
      <button type="button" class="seg__btn ${v === current ? "is-on" : ""}"
        data-value="${esc(v)}">${esc(label)}</button>`)
    .join("");
  return `<div class="seg" data-act="${esc(act)}">${btns}</div>`;
}

function switchRow(label, act, on, title) {
  return `
  <div class="field field--row">
    <label class="field__label">${esc(label)}</label>
    <label class="switch" title="${esc(title || label)}">
      <input type="checkbox" data-act="${esc(act)}" ${on ? "checked" : ""}>
      <span class="switch__track"></span>
    </label>
  </div>`;
}

/**
 * 缩放标记。只在不是 100% 时出现：绝大多数实例都是原始大小，
 * 每条都挂一个「100%」只会把真正调过的那一个埋掉。
 */
function zoomTag(i) {
  const z = Math.round((i.zoom || 1) * 100);
  return z === 100 ? "" : ` · ${z}%`;
}

/**
 * 抽屉里的一行实例。
 *
 * mode：
 *   "select" 显示"属于哪个组"的下拉——这是**实例级**改组的入口，
 *            改完宿主只重建这一个窗口，同组件的其他实例不受影响；
 *   "tag"    只显示组标签（不需要在这里改的时候）；
 *   "none"   什么都不显示（在组设置里，组是隐含的）。
 */
function memberRow(i, mode) {
  const c = componentByID(i.componentId);
  const name = c ? c.name : i.componentId;
  const g = groupByID(i.groupId);

  let where = "";
  if (mode === "select") {
    where = `<select class="input input--sm" data-act="instanceGroup" data-instance="${esc(i.id)}"
      title="换组会重建这个窗口">${groupOptions(i.groupId)}</select>`;
  } else if (mode === "tag") {
    where = `<span class="member__tag" style="${groupVars(g)}"><span class="member__dot"></span>${esc(g ? g.name : "组不存在")}</span>`;
  }

  return `
  <div class="member">
    <span class="member__name">${esc(name)}</span>
    <span class="member__id">${esc(i.id)} · ${esc(instanceStateText(i))} · ${i.width}×${i.height}${zoomTag(i)}</span>
    <span class="member__acts">
      ${where}
      <button type="button" class="btn btn--ghost btn--sm"
        data-act="instanceVisible" data-instance="${esc(i.id)}"
        data-visible="${i.visible ? "0" : "1"}"
        ${i.running ? "" : "disabled"}>${i.visible ? "隐藏" : "显示"}</button>
      <button type="button" class="btn btn--danger btn--sm"
        data-act="instanceRemove" data-instance="${esc(i.id)}">关闭</button>
    </span>
  </div>`;
}

function memberList(rows, mode, emptyHint) {
  if (!rows.length) return `<div class="field__hint">${esc(emptyHint)}</div>`;
  return `<div class="member-list">${rows.map((i) => memberRow(i, mode)).join("")}</div>`;
}

function drawerHead(title, sub) {
  return `
  <div class="drawer__head">
    <div>
      <div class="drawer__title">${esc(title)}</div>
      <div class="drawer__sub">${esc(sub)}</div>
    </div>
    <button type="button" class="btn btn--ghost btn--sm drawer__close" data-act="drawerClose">关闭</button>
  </div>`;
}

// ---------------------------------------------------------------------------
// 抽屉：组设置
//
// 组设置改的是**组本身**：一次改动，组里所有实例一起变。这是这套模型的核心，
// 界面上不能出现"给某个实例单独调个色"这种入口。
// ---------------------------------------------------------------------------

function renderGroupDrawer(g) {
  const isHex = /^#[0-9a-fA-F]{6}$/.test(g.background || "");
  const bgHex = isHex ? g.background : "#ffffff";
  const rows = instancesOfGroup(g.id);

  drawerEl.innerHTML = `
  ${drawerHead("组设置", g.id)}

  <div class="drawer__body">
    <div class="field">
      <label class="field__label">名称</label>
      <input class="input" data-act="groupName" value="${esc(g.name)}">
    </div>

    <div class="field">
      <label class="field__label">识别色</label>
      <div class="field__row">
        <input type="color" data-act="groupColor" value="${esc(g.color || "#cfc9bd")}">
        <input class="input" data-act="groupColorText" value="${esc(g.color || "")}"
          placeholder="#c08a1e">
      </div>
      <div class="field__hint">只用来在侧边栏和布局板上区分组，不影响窗口外观。</div>
    </div>

    <div class="field">
      <label class="field__label">窗口层级</label>
      ${segRow("groupLayer", [["normal", "常规"], ["topmost", "置顶"], ["desktop", "置底"]], g.layer)}
      <div class="field__hint">常规会被其他窗口盖住；置顶浮在所有窗口之上；置底贴在桌面图标之上、应用窗口之下，Win+D 时跟随桌面显隐。</div>
    </div>

    ${switchRow("点击穿透", "groupClickThrough", g.clickThrough, "开启后鼠标事件穿过窗口，点不到它")}
    <div class="field__hint">穿透的窗口收不到鼠标，因此也无法被激活——这一点由内核自动保证。</div>

    ${switchRow("可缩放", "groupResizable", g.resizable, "用户能否直接拖窗口边框改尺寸")}
    <div class="field__hint">只影响"直接拖窗口自己的边框"。布局板上的拖边角改大小由下面的「尺寸」档位决定——穿透窗口和桌面层窗口收不到鼠标，布局板是它们唯一的尺寸入口。</div>
    ${switchRow("标题栏", "groupTitlebar", g.titlebar, "是否给组件页显示标题栏")}

    <div class="field">
      <label class="field__label">尺寸</label>
      ${segRow("groupSizeMode", [["fixed", "固定"], ["user", "可调"], ["fitContent", "随内容"]], g.sizeMode)}
      <div class="field__hint">「固定」= 谁都不能改，布局板上也不给手柄；其余档位可以在布局板上拖方块的边角改大小。</div>
    </div>

    <div class="field">
      <label class="field__label">留白</label>
      <div class="field__row">
        <input type="range" min="0" max="40" step="1" data-act="groupPadding" value="${g.padding}">
        <span class="field__val" data-val="groupPadding">${g.padding}</span>
      </div>
    </div>

    <div class="field">
      <label class="field__label">圆角</label>
      <div class="field__row">
        <input type="range" min="0" max="32" step="1" data-act="groupRadius" value="${g.radius}">
        <span class="field__val" data-val="groupRadius">${g.radius}</span>
      </div>
    </div>

    <div class="field">
      <label class="field__label">底色</label>
      <div class="field__row">
        <input type="color" data-act="backgroundSwatch" value="${esc(bgHex)}">
        <input class="input" data-act="groupBackground" value="${esc(g.background || "")}"
          placeholder="rgba(255,255,255,.9)">
      </div>
    </div>

    <div class="field">
      <label class="field__label">阴影</label>
      <input class="input" data-act="groupShadow" value="${esc(g.shadow || "")}"
        placeholder="0 8px 24px rgba(0,0,0,.18)">
      <div class="field__hint">留空表示不要投影。Win11 下投影与窗口圆角由同一个系统开关控制，两者要么都有、要么都没有。</div>
    </div>

    <div class="field">
      <label class="field__label">实例（${rows.length}）</label>
      ${memberList(rows, "none", "这个组还没有实例。点下面的按钮挑一个组件放进来。")}
      <button type="button" class="btn btn--sm" data-act="pickComponent" data-group="${esc(g.id)}">
        + 选组件新建实例
      </button>
      <div class="field__hint">这里列的是「窗口」。同一个组件的两个窗口可以一个在这儿、一个在别的组。</div>
    </div>
  </div>

  <div class="drawer__foot">
    <button type="button" class="btn btn--danger btn--sm" data-act="groupDelete">删除组</button>
    <span class="statusbar__spacer"></span>
    <span class="field__hint">改动即时生效</span>
  </div>`;
}

// ---------------------------------------------------------------------------
// 抽屉：组件设置
//
// 组件是静态定义，本身没有组。它的每个实例各自选组——这是"每个实例独立
// 管控所属分组"的落点。
// ---------------------------------------------------------------------------

function renderComponentDrawer(c) {
  const rows = instancesOfComponent(c.id);

  const reveal = c.builtin
    ? `<button type="button" class="btn btn--sm" disabled title="内置组件住在缓存目录里，没有独立目录可打开">打开目录</button>`
    : `<button type="button" class="btn btn--sm" data-act="componentReveal" data-id="${esc(c.id)}">打开目录</button>`;
  const uninstall = c.builtin
    ? `<button type="button" class="btn btn--danger btn--sm" disabled title="内置组件随程序发布，不能卸载">卸载</button>`
    : `<button type="button" class="btn btn--danger btn--sm" data-act="componentUninstall" data-id="${esc(c.id)}">卸载</button>`;

  // 新建实例要先选组：组件本身不带组，得告诉内核这个窗口该长什么样。
  const defaultGid = groups().length ? groups()[0].id : "";
  // 单例组件已经有一个实例时，新建这条路是关的。
  const canAdd = groups().length > 0 && !(c.singleton && rows.length > 0);

  // 变量声明：这里只列出来（作者要什么），填值在实例抽屉里——
  // 值是实例的，不是组件的。
  const vars = c.variables || [];
  const varList = vars.length
    ? `
    <div class="field">
      <label class="field__label">变量（${vars.length}）</label>
      <div class="var-list">
        ${vars
          .map(
            (v) => `
          <div class="var-item">
            <span class="var-item__name">${esc(v.label)}</span>
            <span class="var-item__key">${esc(v.key)}</span>
            <span class="var-item__type">${esc(VAR_TYPE_LABEL[v.type] || v.type)}</span>
          </div>`,
          )
          .join("")}
      </div>
      <div class="field__hint">值按实例保存：打开某个实例的设置就能填。这里只是作者声明的清单。</div>
    </div>`
    : "";

  drawerEl.innerHTML = `
  ${drawerHead(c.name, `${c.id}${c.author ? " · " + c.author : ""}`)}

  <div class="drawer__body">
    <div class="field">
      <div class="field__hint">${esc(c.description || "（作者没写说明）")}</div>
    </div>

    <div class="field field--row">
      <label class="field__label">启用</label>
      <label class="switch" title="${c.enabled ? "停用这个组件" : "启用这个组件"}">
        <input type="checkbox" data-act="toggle" data-id="${esc(c.id)}" ${c.enabled ? "checked" : ""}>
        <span class="switch__track"></span>
      </label>
    </div>
    <div class="field__hint">停用会关掉它的所有窗口；实例记录和私有数据都留着，重新启用时原样回来。</div>

    ${varList}

    <div class="field">
      <label class="field__label">实例（${rows.length}）</label>
      ${memberList(rows, "select", "还没有实例。选一个组，点下面的按钮新建。")}
      <div class="field__row">
        <select class="input" data-act="newInstanceGroup" ${canAdd ? "" : "disabled"}>
          ${groupOptions(defaultGid)}
        </select>
        <button type="button" class="btn btn--sm" data-act="instanceNew" data-id="${esc(c.id)}"
          ${canAdd ? "" : "disabled"}>+ 新建</button>
      </div>
      <div class="field__hint">${
        c.singleton && rows.length > 0
          ? "这是单例组件，只能有一个实例。"
          : "每个实例独立选组：同一个组件可以一个窗口贴桌面、另一个浮在最上面。"
      }</div>
    </div>
  </div>

  <div class="drawer__foot">
    ${reveal}
    <span class="statusbar__spacer"></span>
    ${uninstall}
  </div>`;
}

// ---------------------------------------------------------------------------
// 抽屉：单个实例
// ---------------------------------------------------------------------------

/**
 * 把一个变量的值渲染成控件里的文本。
 *
 * 文本类变量的值一定是 JSON 字符串，直接放；`list` 类型的值是对象数组
 * `[{"text":"第一条"}]`，取出每项里声明的字段（默认 text）拼成多行文本——
 * 用户在界面上看到的永远是"一行一条"，不是 JSON。
 *
 * `number` / `bool` 是标量，`group` 走卡片式表单（见 varField），
 * 都不从这里出文本。
 *
 * 注意判断依据是**声明的类型**而不是值的类型：界面这一侧不猜形状，
 * 清单说是什么类型就按什么类型摊开。
 */
function varText(v, value) {
  const raw = value === undefined ? v.default : value;
  if (raw === undefined || raw === null) return "";
  if (v.type === "list") {
    if (!Array.isArray(raw)) return "";
    const field = v.item || "text";
    return raw
      .map((o) => {
        if (typeof o === "string") return o;
        return o && typeof o[field] === "string" ? o[field] : "";
      })
      .filter((s) => s !== "")
      .join("\n");
  }
  if (v.type === "group") return "";
  return String(raw);
}

// ---------------------------------------------------------------------------
// 记录表（group）：一条记录 = 一块纵向排列的字段
// ---------------------------------------------------------------------------

/** 一个字段在界面上的初值：记录里没这个键时用字段声明的 default。 */
function fieldText(f, rec) {
  const raw = rec && Object.prototype.hasOwnProperty.call(rec, f.key) ? rec[f.key] : f.default;
  if (raw === undefined || raw === null) return "";
  return String(raw);
}

/** 一个字段的输入控件。`rec` 是这条记录的值（可能为空对象）。 */
function recFieldControl(v, f, rec) {
  const key = esc(v.key);
  const fk = esc(f.key);
  const ph = esc(f.placeholder || "");
  const common = `data-rec-field="${fk}" data-rec-type="${esc(f.type)}"`;

  if (f.type === "bool") {
    const on = fieldText(f, rec) === "true" || rec[f.key] === true;
    return `<input type="checkbox" class="rec__check" ${common} ${on ? "checked" : ""}>`;
  }
  if (f.type === "select") {
    const cur = fieldText(f, rec);
    const opts = (f.options || [])
      .map((o) => `<option value="${esc(o.value)}" ${o.value === cur ? "selected" : ""}>${esc(o.label)}</option>`)
      .join("");
    // 空选项让"没选"也能表达：清空之后回到字段默认值。
    return `<select class="input" ${common}><option value="">（不填）</option>${opts}</select>`;
  }
  if (f.type === "number") {
    return `<input type="number" class="input" ${common} value="${esc(fieldText(f, rec))}" placeholder="${ph}">`;
  }
  if (f.type === "textarea") {
    return `<textarea class="input input--area" ${common} rows="${f.rows || 3}"
      placeholder="${ph}">${esc(fieldText(f, rec))}</textarea>`;
  }
  return `<input class="input" ${common} value="${esc(fieldText(f, rec))}" placeholder="${ph}">`;
}

/** 一条记录的内部结构（不含外层 .rec 包裹）。 */
function recBody(v, rec, index) {
  const rows = (v.fields || [])
    .map(
      (f) => `<div class="rec__row">
        <label class="rec__label">${esc(f.label)}<span class="rec__key">${esc(f.key)}</span></label>
        ${recFieldControl(v, f, rec)}
      </div>`,
    )
    .join("");
  return `<div class="rec__head">
      <span class="rec__no">第 ${index + 1} 条</span>
      <button type="button" class="btn btn--sm btn--ghost" data-act="recDel">删除</button>
    </div>${rows}`;
}

/**
 * 记录表控件：每条一张卡片，底部一个「+ 添加一条」。
 *
 * 容器自己带 `data-var`，里面的输入框带 `data-rec-field`——读值时
 * `readVarValues` 按容器分流：普通控件读 value，记录表把卡片拼成 JSON。
 */
function recList(v, value) {
  const recs = Array.isArray(value) ? value : [];
  const cards = recs
    .map((rec, i) => `<div class="rec">${recBody(v, rec, i)}</div>`)
    .join("");
  return `<div class="recs" data-var="${esc(v.key)}" data-records="1">${cards}</div>
    <button type="button" class="btn btn--sm" data-act="recAdd" data-add="${esc(v.key)}">+ 添加一条</button>`;
}

/**
 * 一个变量的输入控件。
 *
 * 声明来自组件（c.variables），值来自实例（i.vars）——同一个组件的两个
 * 实例共用一份声明、各持一份值，界面上就体现为"两个抽屉、同样的表单、
 * 不同的内容"。
 *
 * 七种类型对应四种控件：单行（`text`/`number`）、下拉（`select`）、
 * 开关（`bool`）、多行（`textarea`/`list`）、卡片表单（`group`）。
 * `list` 与 `group` 的区别只是"一条里一个字段"还是"一条里几个字段"，
 * 拆成对象数组都是宿主的事，用户不用碰 JSON。
 */
function varField(v, value) {
  const text = varText(v, value);
  const req = v.required ? `<span class="var__req">必填</span>` : "";
  const ph = esc(v.placeholder || "");

  let control;
  if (v.type === "select") {
    const opts = (v.options || [])
      .map(
        (o) => `<option value="${esc(o.value)}" ${o.value === text ? "selected" : ""}>${esc(o.label)}</option>`,
      )
      .join("");
    control = `<select class="input" data-var="${esc(v.key)}">${opts}</select>`;
  } else if (v.type === "bool") {
    const on = text === "true";
    control = `<label class="switch-row">
      <input type="checkbox" data-var="${esc(v.key)}" data-bool="1" ${on ? "checked" : ""}>
      <span>${on ? "开" : "关"}</span></label>`;
  } else if (v.type === "group") {
    control = recList(v, value === undefined ? v.default : value);
  } else if (v.type === "textarea" || v.type === "list") {
    control = `<textarea class="input input--area"
      data-var="${esc(v.key)}"${v.type === "list" ? ' data-list="1"' : ""}
      rows="${v.rows || 4}" placeholder="${ph}">${esc(text)}</textarea>`;
  } else if (v.type === "number") {
    control = `<input type="number" class="input" data-var="${esc(v.key)}" value="${esc(text)}" placeholder="${ph}">`;
  } else {
    control = `<input class="input" data-var="${esc(v.key)}" value="${esc(text)}" placeholder="${ph}">`;
  }

  return `
  <div class="field">
    <label class="field__label">${esc(v.label)}${req}
      <span class="var__key">${esc(v.key)}</span>
    </label>
    ${control}
    ${v.type === "list" ? `<div class="field__hint">一行一条，空行会被忽略。</div>` : ""}
    ${v.type === "group" ? `<div class="field__hint">一条记录一张卡片，可以增删；没填的字段不会写进值里。</div>` : ""}
    ${v.description ? `<div class="field__hint">${esc(v.description)}</div>` : ""}
  </div>`;
}

/**
 * 一个变量算不算"没填"。
 *
 * `list` 要单独判断：空数组在 JS 里是 truthy，直接看真假会把"一条都没有"
 * 当成已填，必填提示就永远不出现了。
 */
function varIsEmpty(v, value) {
  if (value === undefined || value === null || value === "") return true;
  if (v.type === "list" || v.type === "group") return !Array.isArray(value) || value.length === 0;
  return false;
}

/** 变量声明里"必填但当前是空的"那一条的提示文字（没有就返回空串）。 */
function varMissingHint(list, values) {
  const miss = list.filter((v) => v.required && varIsEmpty(v, values && values[v.key]));
  if (!miss.length) return "";
  return `还差 ${miss.map((v) => esc(v.label)).join("、")} 没填——组件可能显示不出内容。`;
}

function renderInstanceDrawer(i) {
  const c = componentByID(i.componentId);
  const g = groupByID(i.groupId);
  const name = c ? c.name : i.componentId;
  const vars = (c && c.variables) || [];
  const missing = varMissingHint(vars, i.vars);

  const varSection = vars.length
    ? `
    <div class="field field--vars">
      <label class="field__label">变量（${vars.length}）</label>
      <div class="field__hint">这些值只属于这个窗口：同一个组件的另一个实例可以填别的。</div>
      ${missing ? `<div class="field__warn">${missing}</div>` : ""}
      ${vars.map((v) => varField(v, i.vars ? i.vars[v.key] : v.default)).join("")}
    </div>`
    : "";

  drawerEl.innerHTML = `
  ${drawerHead(name, i.id)}

  <div class="drawer__body">
    <div class="field">
      <label class="field__label">所属组</label>
      <select class="input" data-act="instanceGroup" data-instance="${esc(i.id)}">
        ${groupOptions(i.groupId)}
      </select>
      <div class="field__hint">${
        g
          ? `外观与窗口行为来自「${esc(g.name)}」：${esc(layerLabel(g.layer))}${g.clickThrough ? " · 穿透" : ""}。换组会重建这个窗口，其他实例不受影响。`
          : "所属组不存在，这个窗口现在解析不出外观。"
      }</div>
    </div>

    ${varSection}

    <div class="field">
      <label class="field__label">内容缩放</label>
      <div class="field__row">
        <input type="range" min="50" max="300" step="5"
          data-act="instanceZoom" data-instance="${esc(i.id)}"
          value="${Math.round((i.zoom || 1) * 100)}">
        <span class="field__val" data-val="instanceZoom">${Math.round((i.zoom || 1) * 100)}%</span>
        <button type="button" class="btn btn--ghost btn--sm"
          data-act="instanceZoomReset" data-instance="${esc(i.id)}">复位</button>
      </div>
      <div class="field__hint">只放大窗口里的页面内容，窗口本身多大不变。想改窗口大小去布局页拖方块的边角——两件事互不影响。</div>
    </div>

    <div class="field">
      <label class="field__label">状态</label>
      <div class="field__hint">${esc(instanceStateText(i))} · ${i.width}×${i.height} 逻辑像素 · 屏幕 ${i.x}, ${i.y}</div>
    </div>

    <div class="field">
      <label class="field__label">组件</label>
      <div class="field__hint">${esc(i.componentId)}${c && c.enabled ? "" : "（已停用，窗口不会显示）"}</div>
    </div>
  </div>

  <div class="drawer__foot">
    <button type="button" class="btn btn--sm" data-act="instanceVisible" data-instance="${esc(i.id)}"
      data-visible="${i.visible ? "0" : "1"}" ${i.running ? "" : "disabled"}>
      ${i.visible ? "隐藏" : "显示"}
    </button>
    <span class="statusbar__spacer"></span>
    <button type="button" class="btn btn--danger btn--sm" data-act="instanceRemove"
      data-instance="${esc(i.id)}">关闭实例</button>
  </div>`;
}

// ---------------------------------------------------------------------------
// 抽屉：往组里挑组件
//
// 「每个分组内可以通过选择组件新建下层实例」的落点。组是给定的，
// 用户只挑组件；建出来的实例直接属于这个组。
// ---------------------------------------------------------------------------

function renderPickDrawer(groupId) {
  const g = groupByID(groupId);
  if (!g) return closeDrawer();

  const list = components();
  const rows = list.length
    ? list
        .map((c) => {
          const n = instancesOfComponent(c.id).length;
          // 单例组件已经有实例了就别再提供这个选项：点了必然收到
          // 一句"这是单例"，等于让用户白点一次。
          const full = c.singleton && n > 0;
          const off = !c.enabled;
          const blocked = off || full;
          const meta = off
            ? "已停用，先启用"
            : full
              ? "单例，只能有一个"
              : n
                ? `已有 ${n} 个实例`
                : "还没有实例";
          return `
        <button type="button" class="pick ${blocked ? "is-off" : ""}"
          data-act="pickCreate" data-id="${esc(c.id)}" data-group="${esc(groupId)}"
          ${blocked ? "disabled" : ""} title="${esc(c.name)} · ${esc(c.id)}">
          <span class="pick__name">${esc(c.name)}</span>
          <span class="pick__id">${esc(c.id)}</span>
          <span class="pick__meta">${esc(meta)}</span>
        </button>`;
        })
        .join("")
    : `<div class="field__hint">还没有装任何组件。</div>`;

  drawerEl.innerHTML = `
  ${drawerHead("选组件", `放进「${g.name}」`)}
  <div class="drawer__body">
    <div class="field">
      <div class="field__hint">选一个组件，它会在「${esc(g.name)}」里开一个新窗口。新窗口沿用这个组的层级与外观。</div>
    </div>
    <div class="pick-list">${rows}</div>
  </div>
  <div class="drawer__foot">
    <span class="field__hint">同一组件可以反复选，开多个窗口。</span>
  </div>`;
}

// ---------------------------------------------------------------------------
// 抽屉开关
// ---------------------------------------------------------------------------

function openDrawer(next) {
  drawer = next;
  renderDrawer();
  setHidden(drawerEl, false);
  renderSidebar();
}

function renderDrawer() {
  if (!drawer) return;
  if (drawer.kind === "group") {
    const g = groupByID(drawer.id);
    if (!g) return closeDrawer();
    renderGroupDrawer(g);
  } else if (drawer.kind === "component") {
    const c = componentByID(drawer.id);
    if (!c) return closeDrawer();
    renderComponentDrawer(c);
  } else if (drawer.kind === "instance") {
    const i = findInstance(drawer.id);
    // 实例刚被删掉：抽屉没有可显示的东西，直接收起来。
    if (!i) return closeDrawer();
    renderInstanceDrawer(i);
  } else if (drawer.kind === "pick") {
    renderPickDrawer(drawer.groupId);
  }
}

function openGroupDrawer(id) {
  const g = groupByID(id);
  if (!g) return;
  // 打开设置就把布局板切到这个组：改层级、穿透、留白时能立刻看到是哪些窗口在变。
  boardScope = id;
  openDrawer({ kind: "group", id });
  renderBoard();
}

function openComponentDrawer(id) {
  if (!componentByID(id)) return;
  openDrawer({ kind: "component", id });
}

function openInstanceDrawer(id) {
  if (!findInstance(id)) return;
  openDrawer({ kind: "instance", id });
}

function openPickDrawer(groupId) {
  if (!groupByID(groupId)) return;
  boardScope = groupId;
  openDrawer({ kind: "pick", groupId });
  renderBoard();
}

function closeDrawer() {
  drawer = null;
  setHidden(drawerEl, true);
  drawerEl.innerHTML = "";
  renderSidebar();
}

/** 状态更新后，如果抽屉开着就跟着重画一遍。 */
function refreshDrawer() {
  if (!drawer) return;
  // 正在往变量输入框里打字时不要重渲染：重建 DOM 会把焦点和光标位置一起
  // 丢掉，表现为"每敲一个字就跳出输入框"。值本来就在框里，等失焦之后的
  // 下一次状态推送再刷新即可。
  //
  // 记录表的输入框带的是 `data-rec-field` 而不是 `data-var`，同样要跳过。
  const act = document.activeElement;
  if (act && act.dataset && (act.dataset.var || act.dataset.recField) && drawerEl.contains(act)) return;
  renderDrawer();
}

/** 从一条记录的各字段控件里读出值，按声明的类型给成 JS 值。 */
function readRecord(card) {
  const rec = {};
  for (const el of card.querySelectorAll("[data-rec-field]")) {
    const key = el.dataset.recField;
    const type = el.dataset.recType;
    if (type === "bool") {
      // 布尔总是写进去：`false` 也是一个明确的取值，不写会让组件分不清
      // "用户关掉了"和"这条根本没有这个字段"。
      rec[key] = !!el.checked;
      continue;
    }
    const raw = el.value;
    if (raw.trim() === "") continue;
    if (type === "number") {
      const n = Number(raw);
      if (!Number.isNaN(n)) rec[key] = n;
      continue;
    }
    rec[key] = type === "textarea" ? raw : raw.trim();
  }
  return rec;
}

/** 一条记录是不是"什么都没填"——全是空串或 false 就丢掉，别存空卡片。 */
function recordEmpty(rec) {
  return Object.values(rec).every((v) => v === "" || v === false || v === null || v === undefined);
}

/**
 * 把记录表里所有卡片读成数组。
 *
 * 这是唯一一处"界面把值拼成 JSON"的地方：多字段没法用"一行一条"表达，
 * 只能由控件自己序列化。用户看到的始终是一张张卡片，不是 JSON。
 */
function readRecords(box) {
  const recs = [];
  for (const card of box.querySelectorAll(".rec")) {
    const rec = readRecord(card);
    if (!recordEmpty(rec)) recs.push(rec);
  }
  return recs;
}

/** 从实例抽屉里读出全部变量值（每个 `[data-var]` 一个，都是原始文本）。 */
function readVarValues() {
  const out = {};
  for (const el of drawerEl.querySelectorAll("[data-var]")) {
    if (el.dataset.records) {
      out[el.dataset.var] = JSON.stringify(readRecords(el));
    } else if (el.type === "checkbox") {
      out[el.dataset.var] = el.checked ? "true" : "false";
    } else {
      out[el.dataset.var] = el.value;
    }
  }
  return out;
}

/** 重新给卡片编号（删掉中间一条之后，后面的要往前挪）。 */
function renumberRecs(box) {
  const cards = box.querySelectorAll(".rec");
  cards.forEach((card, i) => {
    const no = card.querySelector(".rec__no");
    if (no) no.textContent = `第 ${i + 1} 条`;
  });
}

/** 按变量 key 找到它在当前实例抽屉里的声明。 */
function varDecl(key) {
  if (!drawer || drawer.kind !== "instance") return null;
  const inst = findInstance(drawer.id);
  const comp = inst ? componentByID(inst.componentId) : null;
  if (!comp || !comp.variables) return null;
  return comp.variables.find((v) => v.key === key) || null;
}

/**
 * 提交实例变量。
 *
 * 合并 400ms：变量可能是整段文字或一长串条目，每敲一下就写一次文件没有意义。
 * 但也不能只在失焦时存——用户可能填完直接点关闭。
 *
 * 这里不再有"JSON 合法性"这一关：界面上根本没有 JSON 输入框，`list` 类型
 * 就是若干行文字，拆成对象数组是宿主 `Coerce` 的事。用户在文本框里怎么敲
 * 都是合法输入，不该被打断。
 */
let varSaveTimer = 0;

function saveVars(immediate) {
  if (!drawer || drawer.kind !== "instance") return;
  const id = drawer.id;
  const values = readVarValues();
  const send = () => {
    call("manager.setVars", { instanceId: id, values })
      .then((next) => {
        if (next) apply(next);
      })
      .catch((err) => toast({ ok: false, text: err.message || String(err) }));
  };
  clearTimeout(varSaveTimer);
  if (immediate) {
    send();
    return;
  }
  varSaveTimer = setTimeout(send, 400);
}

/** 从抽屉里的控件读出完整的一组参数。 */
function readGroupParams() {
  const q = (sel) => drawerEl.querySelector(sel);
  const seg = (sel) => {
    const on = q(`${sel} .seg__btn.is-on`);
    return on ? on.dataset.value : "";
  };
  return {
    id: drawer.id,
    name: q('[data-act="groupName"]').value.trim(),
    color: q('[data-act="groupColorText"]').value.trim(),
    layer: seg('[data-act="groupLayer"]'),
    clickThrough: q('[data-act="groupClickThrough"]').checked,
    resizable: q('[data-act="groupResizable"]').checked,
    sizeMode: seg('[data-act="groupSizeMode"]'),
    titlebar: q('[data-act="groupTitlebar"]').checked,
    padding: Number(q('[data-act="groupPadding"]').value),
    radius: Number(q('[data-act="groupRadius"]').value),
    background: q('[data-act="groupBackground"]').value,
    shadow: q('[data-act="groupShadow"]').value,
  };
}

let groupSaveTimer = 0;

/** 回填抽屉里某个控件的值（取色器和文本框互相同步时用）。 */
function setFieldValue(sel, v) {
  const el = drawerEl.querySelector(sel);
  if (el) el.value = v;
}

/**
 * 提交组设置。
 *
 * 滑杆这类控件会连发 change，合并 120ms 再发：每一次提交都会让宿主判断
 * "要不要重建窗口"，连发只会让窗口反复拆建。合并之后拖动滑杆是改一次外观，
 * 而不是改十次。
 */
function saveGroup(immediate) {
  if (!drawer || drawer.kind !== "group") return;
  const p = readGroupParams();
  const send = () => {
    call("manager.updateGroup", p).catch((err) => toast({ ok: false, text: err.message || String(err) }));
  };
  clearTimeout(groupSaveTimer);
  if (immediate) {
    send();
    return;
  }
  groupSaveTimer = setTimeout(send, 120);
}

// ---------------------------------------------------------------------------
// 实例的内容缩放
//
// 和组设置里的留白/圆角滑杆不同，这个必须**边拖边看**：缩放值好不好看
// 只能靠眼睛判断，松手才生效等于让人闭着眼睛调。
//
// 所以拖动中每格都发一次（commit=false，只改页面不落盘），松手才落盘。
// 40ms 防抖是为了别把鼠标采样率原样转成桥接消息。
// ---------------------------------------------------------------------------

let zoomTimer = 0;

function sendZoom(commit) {
  if (!drawer || drawer.kind !== "instance") return;
  const el = drawerEl.querySelector('[data-act="instanceZoom"]');
  const inst = findInstance(drawer.id);
  if (!el || !inst) return;
  const zoom = Number(el.value) / 100;
  // 本地先记账：状态行和侧边栏读的是 state 里这份，
  // 不等宿主回推就已经是对的。
  inst.zoom = zoom;
  call("manager.setInstanceZoom", { instanceId: inst.id, zoom, commit }).catch((err) =>
    toast({ ok: false, text: err.message || String(err) }),
  );
}

// ---------------------------------------------------------------------------
// 确认框
// ---------------------------------------------------------------------------

let modalResolve = null;

function ask(title, text, okText) {
  return new Promise((resolve) => {
    modalTitleEl.textContent = title;
    modalTextEl.textContent = text;
    document.getElementById("modalOk").textContent = okText || "确定";
    setHidden(modalEl, false);
    modalResolve = (v) => {
      setHidden(modalEl, true);
      modalResolve = null;
      resolve(v);
    };
  });
}

document.getElementById("modalOk").addEventListener("click", () => {
  if (modalResolve) modalResolve(true);
});

document.getElementById("modalCancel").addEventListener("click", () => {
  if (modalResolve) modalResolve(false);
});

// ---------------------------------------------------------------------------
// 渲染总入口
// ---------------------------------------------------------------------------

function render() {
  // 范围指向的组可能刚被删掉：先归一化，免得侧边栏和布局板各说各话。
  if (boardScope && !groupByID(boardScope)) boardScope = "";

  renderSidebar();
  // 拖动/改尺寸过程中不重渲染布局：否则刚抓起来的方块会被换成新元素，
  // 拖拽就断了（改尺寸这条尤其明显——尺寸还在算，方块已经被换掉了）。
  if (!dragging && !resizing) renderBoard();
  refreshDrawer();

  const comps = components();
  statusEl.textContent = `组件 ${comps.length} · 组 ${groups().length} · 实例 ${instances().length}`;
  pathsEl.textContent = state.pluginRoot || "";
  pathsEl.title = state.pluginRoot || "";
}

function apply(next) {
  if (!next || !Array.isArray(next.components)) return;
  state = next;
  // 老宿主可能还没下发扁平清单：从组件的嵌套结构里拼一份，界面不至于空着。
  if (!Array.isArray(state.instances)) {
    state.instances = [];
    for (const c of state.components) {
      for (const i of c.instances || []) state.instances.push(i);
    }
  }
  render();
}

// ---------------------------------------------------------------------------
// 提示条
// ---------------------------------------------------------------------------

let toastTimer = 0;

function toast(t) {
  if (!t) return;
  setHidden(toastEl, false);
  toastEl.dataset.ok = t.ok === false ? "0" : "1";
  toastEl.textContent = t.text || "";
  // 先显示再上过渡类，否则 hidden 元素上的过渡不会触发。
  setTimeout(() => toastEl.classList.add("is-on"), 0);

  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    toastEl.classList.remove("is-on");
    setTimeout(() => setHidden(toastEl, true), 220);
  }, 3200);
}

// ---------------------------------------------------------------------------
// 操作
// ---------------------------------------------------------------------------

/** 统一的错误处理：任何请求失败都变成一条红提示，不让界面卡死。 */
async function guard(fn) {
  try {
    await fn();
  } catch (err) {
    toast({ ok: false, text: err.message || String(err) });
  }
}

/** 切换布局板的范围：传同一个组就是"取消"，回到全部组。 */
function focusGroup(id) {
  if (!id) return;
  boardScope = boardScope === id ? "" : id;
  render();
}

async function toggleComponent(id, enabled) {
  // 会让宿主建/拆窗口，而窗口不能在前端消息的回调里建（宿主会跑嵌套
  // 消息循环等 WebView2 控制器就绪，撞上运行时的锁就死锁）。
  // 所以它只回一句 accepted，真正的新状态由 manager:state 事件推回来。
  await guard(() => call("manager.setEnabled", { componentId: id, enabled }));
}

async function setInstanceVisible(id, visible) {
  await guard(async () => {
    apply(await call("manager.setVisible", { instanceId: id, visible }));
  });
}

/** 改单个实例所属的组。宿主只重建这一个窗口。 */
async function setInstanceGroup(id, groupId) {
  if (!groupId) return;
  await guard(() => call("manager.setInstanceGroup", { instanceId: id, groupId }));
}

/** 新建实例。groupId 为空时由宿主落兜底组。 */
async function createInstance(componentId, groupId) {
  await guard(() => call("manager.createInstance", { componentId, groupId }));
}

async function removeInstance(id) {
  const ok = await ask(
    "关闭这个实例？",
    `实例 ${id} 的记录与它保存的数据都会被删除，无法撤销。\n只想让它暂时消失的话，用「隐藏」。`,
    "关闭实例"
  );
  if (!ok) return;
  await guard(() => call("manager.removeInstance", { instanceId: id }));
}

async function deleteGroup(id) {
  const g = groupByID(id);
  if (!g) return;
  const n = instancesOfGroup(id).length;
  if (n) {
    toast({ ok: false, text: `「${g.name}」里还有 ${n} 个实例，先把它们移到别的组` });
    return;
  }
  const ok = await ask("删除这个组？", `组「${g.name}」会被删除。组里没有实例，所以不会影响任何窗口。`, "删除");
  if (!ok) return;
  await guard(async () => {
    closeDrawer();
    apply(await call("manager.deleteGroup", { groupId: id }));
  });
}

async function uninstallComponent(id) {
  const ok = await ask(
    "卸载这个组件？",
    `插件文件会被删除；组件的私有数据（比如便签正文）会保留。`,
    "卸载"
  );
  if (!ok) return;
  await guard(async () => {
    closeDrawer();
    apply(await call("manager.uninstall", { kind: "component", id }));
    toast({ ok: true, text: `已卸载 ${id}` });
  });
}

// ---------------------------------------------------------------------------
// 事件绑定
// ---------------------------------------------------------------------------

// 开关用 change：click 在复选框上会先变值再冒泡，容易和重渲染抢时序。
document.addEventListener("change", (e) => {
  const el = e.target;
  const act = el.dataset ? el.dataset.act : "";

  // 变量下拉 / 记录表里的下拉与开关：选中即提交，不用等合并窗口。
  if (el.dataset && (el.dataset.var || el.dataset.recField) && (el.tagName === "SELECT" || el.type === "checkbox")) {
    saveVars(true);
    return;
  }

  switch (act) {
    case "toggle":
      toggleComponent(el.dataset.id, el.checked);
      return;

    // 侧边栏里每个实例一个显隐开关。组件停用时它是 disabled 的，
    // 拨不动也就不会走到这里。
    case "instanceVisible":
      setInstanceVisible(el.dataset.instance, el.checked);
      return;

    // 实例改组：侧边栏（组件视角）、组件设置抽屉、实例抽屉都有这个下拉。
    case "instanceGroup":
      setInstanceGroup(el.dataset.instance, el.value);
      return;

    // 离散改动：立刻提交。
    case "groupName":
    case "groupClickThrough":
    case "groupResizable":
    case "groupTitlebar":
    case "groupBackground":
    case "groupShadow":
      saveGroup(true);
      return;

    // 识别色：取色器和文本框说的是同一件事，互相回填。
    case "groupColor":
      setFieldValue('[data-act="groupColorText"]', el.value);
      saveGroup(true);
      return;
    case "groupColorText": {
      const hex = el.value.trim();
      if (/^#[0-9a-fA-F]{6}$/.test(hex)) setFieldValue('[data-act="groupColor"]', hex);
      saveGroup(true);
      return;
    }

    // 底色取色器同理，回填到右边的文本框。
    case "backgroundSwatch":
      setFieldValue('[data-act="groupBackground"]', el.value);
      saveGroup(false);
      return;

    // 滑杆：拖动中只更新数字，提交走 change（合并 120ms）。
    case "groupPadding":
    case "groupRadius": {
      const out = drawerEl.querySelector(`[data-val="${act}"]`);
      if (out) out.textContent = el.value;
      saveGroup(false);
      return;
    }

    // 内容缩放：松手才落盘。拖动中的预览由 input 那条负责。
    case "instanceZoom":
      clearTimeout(zoomTimer);
      sendZoom(true);
      renderSidebar();
      return;

    default:
      return;
  }
});

// 滑杆拖动过程中只更新数字，不提交：提交要走 change。
// 变量输入框相反：每敲一下都算一次改动，合并 400ms 后再提交。
document.addEventListener("input", (e) => {
  const el = e.target;
  const act = el.dataset ? el.dataset.act : "";
  if (el.dataset && (el.dataset.var || el.dataset.recField)) {
    saveVars(false);
    return;
  }
  // 缩放要边拖边看：立刻推给宿主（只改页面，不落盘）。
  if (act === "instanceZoom") {
    const out = drawerEl.querySelector('[data-val="instanceZoom"]');
    if (out) out.textContent = `${el.value}%`;
    clearTimeout(zoomTimer);
    zoomTimer = setTimeout(() => sendZoom(false), 40);
    return;
  }
  if (act !== "groupPadding" && act !== "groupRadius") return;
  const out = drawerEl.querySelector(`[data-val="${act}"]`);
  if (out) out.textContent = e.target.value;
});

// 变量输入框失焦就立刻落盘：合并窗口里可能还压着没发出去的值，
// 而用户接下来很可能就去点"关闭实例"了。
document.addEventListener("focusout", (e) => {
  const el = e.target;
  if (el && el.dataset && (el.dataset.var || el.dataset.recField)) saveVars(true);
});

document.addEventListener("click", (e) => {
  // 开关自己处理，别被行点击抢走。
  if (e.target.closest(".switch")) return;
  // 下拉也是自己处理：点它不该顺带打开抽屉。
  if (e.target.closest("select")) return;

  const el = e.target.closest("[data-act]");
  if (!el) {
    // 点空白处收起抽屉：抽屉没有遮罩，靠这条让"点别处"符合直觉。
    if (drawer && !e.target.closest(".drawer")) closeDrawer();
    return;
  }

  switch (el.dataset.act) {
    case "sideTab":
      sideTab = el.dataset.tab === "components" ? "components" : "groups";
      renderSidebar();
      break;
    case "groupOpen":
      openGroupDrawer(el.dataset.id);
      break;
    case "groupFocus":
    case "boxFocus":
      // 再点一次同一个组就回到「全部组」。
      focusGroup(el.dataset.id || el.dataset.group);
      break;
    case "groupToggle": {
      const key = "g:" + el.dataset.id;
      if (collapsed.has(key)) collapsed.delete(key);
      else collapsed.add(key);
      renderSidebar();
      break;
    }
    case "compToggle": {
      const key = "c:" + el.dataset.id;
      if (collapsed.has(key)) collapsed.delete(key);
      else collapsed.add(key);
      renderSidebar();
      break;
    }
    case "componentOpen":
      openComponentDrawer(el.dataset.id);
      break;
    case "instanceOpen":
      openInstanceDrawer(el.dataset.instance);
      break;
    case "pickComponent":
      openPickDrawer(el.dataset.group);
      break;
    case "pickCreate":
      // 建完就收起抽屉：用户接下来通常要去看新窗口，而不是再挑一个。
      guard(async () => {
        await createInstance(el.dataset.id, el.dataset.group);
        closeDrawer();
      });
      break;
    case "drawerClose":
      closeDrawer();
      break;
    // 记录表：加一条 / 删一条。
    //
    // 加一条不提交——空卡片不该写进磁盘，用户填了字之后 `input` 事件
    // 会照常把它存下去。删一条要立刻提交，否则下一次状态推送又会把它画回来。
    case "recAdd": {
      const box = drawerEl.querySelector(`.recs[data-var="${el.dataset.add}"]`);
      const v = varDecl(el.dataset.add);
      if (!box || !v) break;
      const card = document.createElement("div");
      card.className = "rec";
      card.innerHTML = recBody(v, {}, box.querySelectorAll(".rec").length);
      box.appendChild(card);
      renumberRecs(box);
      const first = card.querySelector("[data-rec-field]");
      if (first) first.focus();
      break;
    }
    case "recDel": {
      const card = el.closest(".rec");
      const box = card ? card.parentElement : null;
      if (!card || !box) break;
      card.remove();
      renumberRecs(box);
      saveVars(true);
      break;
    }
    case "groupDelete":
      deleteGroup(drawer ? drawer.id : "");
      break;
    case "groupLayer":
    case "groupSizeMode": {
      // 三选一：先在本地点亮，再整组提交。
      for (const b of el.querySelectorAll(".seg__btn")) {
        b.classList.toggle("is-on", b === e.target.closest(".seg__btn"));
      }
      saveGroup(true);
      break;
    }
    case "instanceNew": {
      // 组件设置抽屉里：组由旁边的下拉给；组视角里由 data-group 给。
      const sel = drawerEl.querySelector('[data-act="newInstanceGroup"]');
      const gid = el.dataset.group || (sel ? sel.value : "");
      createInstance(el.dataset.id, gid);
      break;
    }
    case "instanceVisible":
      setInstanceVisible(el.dataset.instance, el.dataset.visible === "1");
      break;
    case "instanceZoomReset": {
      const slider = drawerEl.querySelector('[data-act="instanceZoom"]');
      const out = drawerEl.querySelector('[data-val="instanceZoom"]');
      if (slider) slider.value = "100";
      if (out) out.textContent = "100%";
      clearTimeout(zoomTimer);
      sendZoom(true);
      renderSidebar();
      break;
    }
    case "instanceRemove":
      removeInstance(el.dataset.instance);
      break;
    case "componentReveal":
      guard(() => call("manager.reveal", { kind: "component", id: el.dataset.id }));
      break;
    case "componentUninstall":
      uninstallComponent(el.dataset.id);
      break;
    default:
      break;
  }
});

newGroupBtn.addEventListener("click", () => {
  guard(async () => {
    const g = await call("manager.createGroup", { name: "新组" });
    if (g && g.id) openGroupDrawer(g.id);
  });
});

showAllBtn.addEventListener("click", () => {
  boardScope = "";
  render();
});

installBtn.addEventListener("click", () => {
  // 宿主会先回包再去弹文件夹对话框：对话框是模态的，
  // 在消息处理里直接弹会重入消息循环。
  guard(() => call("manager.install"));
});

reloadBtn.addEventListener("click", () => {
  guard(async () => {
    apply(await call("manager.reload"));
    toast({ ok: true, text: "已重新扫描插件目录" });
  });
});

openPluginsBtn.addEventListener("click", () => {
  guard(() => call("manager.openPluginDir"));
});

minimizeBtn.addEventListener("click", () => {
  guard(() => call("window.minimize"));
});

closeBtn.addEventListener("click", () => {
  // 关闭是单向通知：宿主收到就把窗口收掉，不需要等回复。
  notify("manager.close");
});

// ---------------------------------------------------------------------------
// 无边框窗口：拖动
//
// 缩放边框不在这里做。宿主在 WM_NCHITTEST 里回答 HTLEFT 这类值，
// 光标和缩放循环都由系统负责——前端算边框这条路走不通：
// 一旦让前端去设光标，属性撤掉之后光标会永久卡在缩放箭头上。
//
// 拖动只能前端发起：标题栏是页面内容，不在非客户区里，系统不知道那是标题栏。
// ---------------------------------------------------------------------------

document.addEventListener("mousedown", (e) => {
  if (e.button !== 0) return;
  // 方块拖拽已经在 boardEl 上处理过了（它先冒泡到这里），别抢。
  if (dragging) return;
  // 按钮自己处理这次按下。
  if (e.target.closest("button")) return;
  if (e.target.closest(".titlebar")) {
    e.preventDefault();
    notify("window.beginDrag");
  }
});

// ---------------------------------------------------------------------------
// 宿主事件
// ---------------------------------------------------------------------------

// 状态可能被管理界面之外的动作改变（托盘菜单、组件自己关闭窗口）。
document.addEventListener("host:manager:state", (e) => apply(e.detail));
document.addEventListener("host:manager:toast", (e) => toast(e.detail));

// ---------------------------------------------------------------------------
// 启动
// ---------------------------------------------------------------------------

// 前端异常统一送回内核落盘。
// WebView2 不会把 JS 错误告诉宿主，没有这条通道，界面白屏时日志里什么都没有。
window.addEventListener("error", (e) => {
  if (!host) return;
  host.send({
    method: "host.log",
    params: { level: "error", message: `UI 异常: ${e.message} @${e.filename}:${e.lineno}` },
  });
});

window.addEventListener("unhandledrejection", (e) => {
  if (!host) return;
  const reason = e.reason && e.reason.message ? e.reason.message : String(e.reason);
  host.send({ method: "host.log", params: { level: "error", message: `未处理的 Promise: ${reason}` } });
});

async function boot() {
  if (!host) {
    statusEl.textContent = "未连接宿主（浏览器预览模式）";
    return;
  }
  host.onMessage(handleMessage);

  await guard(async () => {
    apply(await call("manager.state"));

    // 自检：确认 DOM 真的被建出来了。日志里能对上数量，就不用靠截图
    // 判断"界面到底渲染没渲染"。
    //
    // 开关状态从 DOM 里数一遍：组件的 enabled 字段一旦没下发，所有开关
    // 会集体变成"关"（undefined 是假值），光看数量发现不了。
    // 按 checked **属性**数，不按 :checked 伪类数——面板刚显示时复选框
    // 可能还没初始化，:checked 会少算。
    const onSwitches = document.querySelectorAll(".switch input[checked]").length;
    host.send({
      method: "host.log",
      params: {
        level: "info",
        message:
          `管理界面就绪：组 ${document.querySelectorAll("#groupList .group").length} 个，` +
          `组件 ${components().length} 个，实例 ${instances().length} 个，` +
          `侧边栏行 ${document.querySelectorAll("#groupList .item").length} 行，` +
          `布局方块 ${document.querySelectorAll(".box").length} 个，` +
          `开关 ${onSwitches} 个打开，` +
          `板子 ${boardEl.style.width || "0"}×${boardEl.style.height || "0"}`,
      },
    });
  });
}

boot();
