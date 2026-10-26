/* SVG Master · 前端主逻辑（原生 ES2020，无框架、无外部依赖） */
(() => {
  'use strict';

  // ── 基础工具 ────────────────────────────────────────────
  const $ = (id) => document.getElementById(id);
  const TOKEN = new URLSearchParams(location.search).get('k') || window.__APP_TOKEN__ || '';
  const MAX_SIDE = 1568;

  const el = (tag, cls, txt) => {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (txt != null) n.textContent = txt;
    return n;
  };
  const clamp = (v, a, b) => Math.min(b, Math.max(a, v));
  const fmtNum = (n) => {
    n = Number(n) || 0;
    if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
    if (n >= 1e3) return (n / 1e3).toFixed(1) + 'k';
    return String(Math.round(n));
  };
  const nowTs = () => {
    const d = new Date();
    const p = (x) => String(x).padStart(2, '0');
    return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
  };
  const stamp = () => {
    const d = new Date();
    const p = (x) => String(x).padStart(2, '0');
    return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`;
  };
  const rand = () => Math.random().toString(36).slice(2, 10);
  const debounce = (fn, ms) => {
    let t = 0;
    return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); };
  };

  let toastTimer = 0;
  function toast(msg, isErr) {
    const t = $('toast');
    t.textContent = msg;
    t.classList.toggle('err', !!isErr);
    t.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => t.classList.remove('show'), 2600);
  }

  // ── 主题 ────────────────────────────────────────────────
  const THEMES = ['system', 'dark', 'light'];
  const THEME_LABEL = { system: '跟随系统', dark: '深色', light: '浅色' };
  // 三态图标：跟随系统 = 半明半暗圆；深色 = 实心月牙；浅色 = 太阳
  const ICON_ATTR = 'width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"';
  const THEME_ICON = {
    system:
      `<svg ${ICON_ATTR}><circle cx="8" cy="8" r="6"/>` +
      `<path d="M8 2a6 6 0 0 0 0 12z" fill="currentColor" stroke="none"/></svg>`,
    dark:
      `<svg ${ICON_ATTR}><path d="M13.5 9.6A5.9 5.9 0 0 1 6.4 2.5a6 6 0 1 0 7.1 7.1z" fill="currentColor" stroke="none"/></svg>`,
    light:
      `<svg ${ICON_ATTR}><circle cx="8" cy="8" r="3.1" fill="currentColor" stroke="none"/>` +
      `<path d="M8 1.2v1.6M8 13.2v1.6M1.2 8h1.6M13.2 8h1.6` +
      `M3.2 3.2l1.1 1.1M11.7 11.7l1.1 1.1M12.8 3.2l-1.1 1.1M4.3 11.7l-1.1 1.1"/></svg>`,
  };
  function themeMode() {
    try { return localStorage.getItem('svgmaster.theme') || 'system'; } catch { return 'system'; }
  }
  function applyTheme(mode) {
    const sysDark = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
    const dark = mode === 'dark' || (mode === 'system' && sysDark);
    const root = document.documentElement;
    root.setAttribute('data-theme', dark ? 'dark' : 'light');
    root.setAttribute('data-theme-mode', mode);
    try { localStorage.setItem('svgmaster.theme', mode); } catch { /* ignore */ }
    const b = $('btnTheme');
    if (b) {
      b.innerHTML = THEME_ICON[mode] || THEME_ICON.system;
      b.title = '主题：' + THEME_LABEL[mode] + '（点击切换）';
    }
  }
  applyTheme(themeMode());
  $('btnTheme').addEventListener('click', () => {
    const cur = themeMode();
    const next = THEMES[(THEMES.indexOf(cur) + 1) % THEMES.length];
    applyTheme(next);
    toast('主题：' + THEME_LABEL[next]);
  });
  if (window.matchMedia) {
    const mq = window.matchMedia('(prefers-color-scheme: dark)');
    const onChange = () => { if (themeMode() === 'system') applyTheme('system'); };
    if (mq.addEventListener) mq.addEventListener('change', onChange);
    else if (mq.addListener) mq.addListener(onChange);
  }

  // ── HTTP ────────────────────────────────────────────────
  async function api(path, opts = {}) {
    const o = Object.assign({}, opts);
    const headers = Object.assign({ 'X-App-Token': TOKEN }, o.headers || {});
    if (o.body != null && typeof o.body !== 'string') {
      o.body = JSON.stringify(o.body);
      headers['Content-Type'] = 'application/json';
    }
    o.headers = headers;
    const res = await fetch(path, o);
    const ct = res.headers.get('content-type') || '';
    let data;
    try { data = ct.includes('json') ? await res.json() : await res.text(); }
    catch { data = ''; }
    if (!res.ok) {
      const msg = (data && typeof data === 'object' && data.error) ? data.error
        : (typeof data === 'string' && data ? data : `HTTP ${res.status}`);
      const e = new Error(msg);
      e.status = res.status;
      throw e;
    }
    return data;
  }

  // ── 全局状态 ────────────────────────────────────────────
  const S = {
    settings: null,
    protocols: [],
    canvases: [],
    prompts: [],
    categories: [],
    defaultPrompt: '',
    version: '',
    dataDir: '',
    images: [],          // [{mime,data,thumb}]
    svg: '',             // 当前 SVG 文本
    canvas: null,        // 当前画布预设
    running: false,
    clearing: false,
    stopping: false,       // 已发出中止请求，等流断开
    runSeq: 0,             // 每次运行 +1：旧的 SSE 回调据此自我作废
    streamReader: null,    // 当前 SSE reader，停止时直接 cancel
    streamAbort: null,
    runID: '',
    session: '',
    mode: 'continue',     // 对话模式：历史已落在工程里，始终连续
    liveSession: '',
    project: null,        // 当前工程快照（/api/project）
    projectMeta: null,    // 模式列表 / 最近工程 / 默认根目录
    pickedLayer: '',       // 图层面板选中项
    zoom: 1,
    fitScale: 1,
    panX: 0,
    panY: 0,
    paused: false,
    pausedBig: false,
    rawText: '',         // 正文全文（累加，供字数与复制）
    thinkText: '',       // 思维链全文（累加，供字数与复制）
    errText: '',         // 错误全文
    flow: [],            // 右栏「模型输出」分段流：[{role:'think'|'text'|'err', text}]
    lastStats: null,
    // 代码视图
    codeOpen: false,
    codeLines: [],       // [{indent,text,foldable,delta}]
    folds: new Set(),    // 已折叠的行号
    wrap: false,
    lastReport: null,    // 最近一次校验报告
    livePreview: false,  // 本次是否已进入实时预览（决定 setSVG 是否重排视图）
    // 视图缓存：stats 只在 setSVG 时算一次；avail 只在容器尺寸变化时量一次
    stats: { viewBox: '—', size: '—', elements: 0, bytes: 0, w: 0, h: 0 },
    availW: 0,
    availH: 0,
    fitW: 0,
    fitH: 0,
    // 生成方式：三档开关的状态（genMode 是唯一真源）
    //   genLocked 一旦点过「开始生成」即为 true；在此之前三档可以随便来回切。
    genMode: 'layers',
    genLocked: false,
    // 动图播放器状态
    anim: {
      frames: 0,        // 帧分组数量（0 表示不是帧分组动图）
      fps: 8,
      userFps: false,   // 用户手动改过帧率后，不再被 data-fps 覆盖
      source: '',       // 'frames' | 'smil' | 'static' | ''
      playing: false,
      index: 0,
      timer: 0,
      busy: false,      // 正在导出 GIF
    },
  };

  // ── 日志 ────────────────────────────────────────────────
  const MAX_LOG = 1600;
  function makeLogRow(level, text) {
    level = (level || 'LOG').toUpperCase();
    const row = el('div', 'logline ' + level);
    row.appendChild(el('span', 'ts', '[' + nowTs() + ']'));
    row.appendChild(el('span', 'tag', level));
    row.appendChild(el('span', 'msg', text == null ? '' : String(text)));
    return row;
  }
  function appendToBig(node) {
    const big = $('bigLog');
    big.appendChild(node);
    while (big.childElementCount > MAX_LOG) big.removeChild(big.firstElementChild);
    if (!S.pausedBig) big.scrollTop = big.scrollHeight;
  }
  // 流式正文 / 思维链只进「摘要」行：全文在右侧「模型输出」里看，
  // 这里始终只保留一行、实时更新开头 90 字与累计字数，避免日志被碎片刷满。
  const SUMMARY_LEVELS = { TEXT: 1, THINK: 1 };
  function summaryOf(s) {
    s = String(s == null ? '' : s).replace(/\s+/g, ' ').trim();
    return s.length > 90 ? s.slice(0, 90) + '…' : s;
  }
  function addLog(level, text) {
    const lv = (level || 'LOG').toUpperCase();
    const body = text == null ? '' : String(text);
    if (SUMMARY_LEVELS[lv]) {
      const big = $('bigLog');
      let row = big.lastElementChild;
      if (!row || !row.classList.contains(lv)) {
        row = makeLogRow(lv, '');
        appendToBig(row);
      }
      const msg = row.querySelector('.msg');
      const cnt = row.querySelector('.cnt') || row.appendChild(el('span', 'cnt', ''));
      if (msg) msg.textContent = summaryOf(body);
      cnt.textContent = body ? `· ${body.length} 字` : '';
      if (!S.pausedBig) big.scrollTop = big.scrollHeight;
      return;
    }
    appendToBig(makeLogRow(lv, body));
    if (lv === 'ERROR') S.errText += `[${nowTs()}] ${body}\n`;
  }
  function pushRawLogNode(node) {
    appendToBig(node.cloneNode(true));
  }

  // ── 提示 ────────────────────────────────────────────────
  function banner(msg) {
    if (!msg) { $('banner').classList.add('hidden'); return; }
    $('bannerText').textContent = msg;
    $('banner').classList.remove('hidden');
  }
  $('bannerClose').addEventListener('click', () => $('banner').classList.add('hidden'));

  // ── 弹窗通用（开合都带 120–160ms 过渡） ────────────────
  const modalTimers = new Map();
  // 启动时「选择打开方式」是必答题：选完之前不允许被遮罩点击 / ESC / resize 弄没。
  let bootPickOpen = false;
  function openModal(id) {
    const m = $(id);
    if (!m) return;
    clearTimeout(modalTimers.get(id));
    m.classList.remove('hidden', 'closing');
    m.classList.add('opening');
    // 兜底：动画帧被节流/丢弃时也要把 .opening 撤掉，否则弹窗卡在 opacity:0。
    const reveal = () => m.classList.remove('opening');
    requestAnimationFrame(() => requestAnimationFrame(reveal));
    clearTimeout(modalTimers.get('reveal:' + id));
    modalTimers.set('reveal:' + id, setTimeout(reveal, 220));
  }
  function finalizeClose(id) {
    const m = $(id);
    if (!m) return;
    clearTimeout(modalTimers.get(id));
    modalTimers.delete(id);
    m.classList.add('hidden');
    m.classList.remove('closing', 'opening');
  }
  function closeModal(id, force) {
    const m = $(id);
    if (!m) return;
    // 启动选择窗是必答题，只有真正选完（force）才允许关
    if (id === 'modalProject' && bootPickOpen && !force) return;
    // 正在 openModal 的两帧过渡里（.opening 未撤）就直接关：撤定时器、立即隐藏，
    // 否则那两帧回调会把 .opening 去掉，弹窗卡在半透明不可交互的状态里。
    if (m.classList.contains('opening') || m.classList.contains('hidden')) {
      finalizeClose(id);
      return;
    }
    m.classList.add('closing');
    clearTimeout(modalTimers.get(id));
    modalTimers.set(id, setTimeout(() => finalizeClose(id), 150));
  }
  document.querySelectorAll('[data-close]').forEach((b) => {
    b.addEventListener('click', (e) => {
      // 只认元素自己的监听器：确认框里点「开始」后把它关掉，冒泡到 document
      // 时若被当成「点了遮罩」，会把底下的「选择打开方式」一起关掉。
      if (e && e.currentTarget !== b) return;
      const m = b.closest('.modal');
      if (!m) return;
      // 退出询问弹窗：取消按钮要回执给 chooseExit（否则窗口一直等）。
      // 顺序很关键：必须先关自己再回执。回执里可能立刻弹出下一层确认框，
      // 反过来的话，回执弹窗刚 openModal 就会被这里的 closeModal 一把关掉。
      if (m.id === 'modalConfirm') {
        closeModal(m.id);
        document.dispatchEvent(new CustomEvent('dsh-confirm-cancel'));
        return;
      }
      closeModal(m.id);
    });
  });
  document.querySelectorAll('.modal').forEach((m) => {
    m.addEventListener('mousedown', (e) => {
      if (e.target !== m) return;
      if (m.dataset.noClose === '1') return; // 退出询问：必须做出选择
      closeModal(m.id);
    });
  });

  // ── 顶栏标签页（预览 / 代码 / 日志流） ──────────────────
  const TAB_OF = { paneGen: 'gen', paneCode: 'code', paneStream: 'stream' };
  function showPane(id) {
    ['paneGen', 'paneCode', 'paneStream'].forEach((p) => $(p).classList.toggle('hidden', p !== id));
    const tab = TAB_OF[id] || 'gen';
    document.querySelectorAll('#tabs .tab').forEach((x) => x.classList.toggle('active', x.dataset.tab === tab));
    S.codeOpen = id === 'paneCode';
    if (id === 'paneCode') renderCode();
    else if (id === 'paneGen') requestAnimationFrame(recomputeFit);
  }
  function setCodeView(on) { showPane(on ? 'paneCode' : 'paneGen'); }
  $('tabs').addEventListener('click', (e) => {
    const b = e.target.closest('.tab');
    if (!b) return;
    const tab = b.dataset.tab;
    if (tab === 'code') showPane('paneCode');
    else if (tab === 'stream') showPane('paneStream');
    else showPane('paneGen');
  });

  // ── 左栏折叠 ────────────────────────────────────────────
  $('leftToggle').addEventListener('click', () => {
    const left = $('left');
    const collapsed = left.classList.toggle('collapsed');
    $('leftToggle').textContent = collapsed ? '›' : '‹';
    $('leftToggle').classList.toggle('collapsed', collapsed);
    requestAnimationFrame(recomputeFit);
  });

  // ── 右栏区块折叠（Agent 默认折叠） ─────────────────────
  // 状态存在 localStorage：重开窗口保持上次的布局习惯。
  const RBLOCKS = ['blkUsage', 'blkQuota', 'layerBlock', 'blkAgent', 'blkRaw'];
  const RBLOCK_DEFAULT = { blkUsage: false, blkQuota: false, layerBlock: false, blkAgent: true, blkRaw: false };
  function rblockState() {
    let saved = null;
    try { saved = JSON.parse(localStorage.getItem('svg.rblocks') || 'null'); } catch { saved = null; }
    const out = Object.assign({}, RBLOCK_DEFAULT);
    if (saved && typeof saved === 'object') {
      RBLOCKS.forEach((id) => { if (typeof saved[id] === 'boolean') out[id] = saved[id]; });
    }
    return out;
  }
  function applyRBlock(id, collapsed) {
    const blk = $(id);
    if (!blk) return;
    blk.classList.toggle('collapsed', !!collapsed);
    const chev = blk.querySelector('.rhead .rchev');
    if (chev) chev.textContent = collapsed ? '▸' : '▾';
    const head = blk.querySelector('.rhead');
    if (head) head.title = collapsed ? '点击展开' : '点击折叠';
  }
  function setRBlock(id, collapsed, save) {
    applyRBlock(id, collapsed);
    if (save !== false) {
      const st = rblockState();
      st[id] = !!collapsed;
      try { localStorage.setItem('svg.rblocks', JSON.stringify(st)); } catch { /* 忽略 */ }
    }
    // 折叠会改变画布可用高度 → 重新适配
    requestAnimationFrame(recomputeFit);
  }
  function initRBlocks() {
    const st = rblockState();
    RBLOCKS.forEach((id) => applyRBlock(id, st[id]));
  }
  document.querySelectorAll('#right .rhead[data-rblock]').forEach((head) => {
    head.addEventListener('click', (e) => {
      // 标题栏上的小按钮（新建图层等）不触发折叠
      if (e.target.closest('button')) return;
      const id = head.dataset.rblock;
      const blk = $(id);
      if (!blk) return;
      setRBlock(id, !blk.classList.contains('collapsed'));
    });
  });
  initRBlocks();

  // 「模型输出」整块的滚动目标（内容 + 底部工具条同一个滚动区）
  function flowScrollEl() {
    const blk = $('blkRaw');
    return (blk && blk.querySelector(':scope > .rbody')) || $('flowBody');
  }

  // ── 启动 ────────────────────────────────────────────────
  async function boot() {
    addLog('LOG', '正在读取 /api/state …');
    try {
      const st = await api('/api/state');
      S.settings = st.settings;
      S.protocols = st.protocols || [];
      S.canvases = st.canvases || [];
      S.prompts = st.prompts || [];
      S.categories = st.categories || [];
      S.defaultPrompt = st.defaultPrompt || '';
      S.version = st.version || '';
      S.dataDir = st.dataDir || '';
      S.canvas = (S.settings && S.settings.canvas) || S.canvases[0] || null;

      if (st.warn) banner('配置加载警告：' + st.warn);
      initSels();
      renderCanvasSelect();
      applySettingsToLeft();
      renderPromptCats();
      renderPromptGrid();
      renderFlow();
      setRunningUI(false);      // 先画出按钮初始态
      await bootProject();      // 启动模式：快速自动进 / 工程自动开上次的
      addLog('LOG', `就绪 · v${S.version} · ${S.dataDir || ''}`);
    } catch (e) {
      addLog('ERROR', '初始化失败：' + e.message);
      banner('初始化失败：' + e.message + '（请确认 URL 里带 ?k= 令牌）');
    }
  }

  // ── 自定义下拉（界面上所有下拉框统一走这里，不用原生 select） ──
  const SELS = {};
  function initSel(id, onChange) {
    const root = $(id);
    if (!root) return;
    const btn = root.querySelector('.sel-btn');
    const menu = root.querySelector('.sel-menu');
    const st = { id, value: '', options: [], onChange, open: false };
    SELS[id] = st;
    let closeTimer = 0;
    const close = () => {
      if (!st.open) return;
      st.open = false;
      root.classList.remove('open');
      menu.classList.remove('entering');
      menu.classList.add('closing');
      clearTimeout(closeTimer);
      closeTimer = setTimeout(() => {
        menu.classList.remove('closing');
        menu.classList.add('hidden');
      }, 130);
    };
    const open = () => {
      closeAllSels(id);
      clearTimeout(closeTimer);
      st.open = true;
      root.classList.add('open');
      menu.classList.remove('closing', 'hidden');
      menu.classList.add('entering');
      placeSelMenu(root, btn, menu);
      // 下一帧移除 entering，让 transition 跑起来（同一帧内改类不会触发过渡）
      requestAnimationFrame(() => {
        requestAnimationFrame(() => { if (st.open) menu.classList.remove('entering'); });
      });
      const act = menu.querySelector('.sel-opt.active');
      if (act && act.scrollIntoView) act.scrollIntoView({ block: 'nearest' });
    };
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      if (st.open) close(); else open();
    });
    menu.addEventListener('click', (e) => {
      const o = e.target.closest('.sel-opt');
      if (!o) return;
      e.stopPropagation();
      setSelValue(id, o.dataset.value);
      close();
      if (st.onChange) st.onChange(st.value);
    });
    root._sel = { close, open };
    document.addEventListener('mousedown', (e) => {
      if (!root.contains(e.target)) close();
    });
  }
  function closeAllSels(except) {
    Object.keys(SELS).forEach((k) => {
      if (k === except) return;
      const r = $(k);
      if (r && r._sel) r._sel.close();
    });
  }
  function anySelOpen() {
    return Object.keys(SELS).some((k) => SELS[k].open);
  }
  // 下拉菜单用 fixed 定位，按按钮实际位置摆放，避免被左栏 overflow 裁掉或压到画布上。
  function placeSelMenu(root, btn, menu) {
    const r = btn.getBoundingClientRect();
    const vh = window.innerHeight || 800;
    const vw = window.innerWidth || 1200;
    const width = Math.max(r.width, 200);
    menu.style.width = width + 'px';
    menu.style.left = Math.max(6, Math.min(r.left, vw - width - 6)) + 'px';
    // 量高度时用 visibility:hidden 而不是挪到 -9999px，避免位置跳一下造成的闪烁
    const prevVis = menu.style.visibility;
    menu.style.visibility = 'hidden';
    menu.style.top = r.bottom + 4 + 'px';
    const h = menu.offsetHeight || 240;
    const below = vh - r.bottom - 8;
    const top = below >= Math.min(h, 200) || r.top < h + 8 ? r.bottom + 4 : r.top - h - 4;
    menu.style.top = Math.max(6, Math.min(top, vh - h - 6)) + 'px';
    menu.style.visibility = prevVis || '';
  }
  function setSelOptions(id, options, keepValue) {
    const st = SELS[id];
    if (!st) return;
    const root = $(id);
    const menu = root.querySelector('.sel-menu');
    st.options = options || [];
    menu.innerHTML = '';
    st.options.forEach((o) => {
      const d = el('div', 'sel-opt');
      d.dataset.value = o.value;
      d.title = o.title || o.label || '';
      d.appendChild(el('span', 'sel-opt-label', o.label != null ? o.label : o.value));
      if (o.sub) d.appendChild(el('span', 'sel-opt-sub', o.sub));
      menu.appendChild(d);
    });
    if (!st.options.length) menu.appendChild(el('div', 'sel-empty', '暂无可选项'));
    if (!keepValue) {
      const hit = st.options.find((o) => o.value === st.value) || st.options[0];
      st.value = hit ? hit.value : '';
    }
    paintSel(id);
  }
  function paintSel(id) {
    const st = SELS[id];
    if (!st) return;
    const root = $(id);
    const hit = st.options.find((o) => o.value === st.value);
    const label = root.querySelector('.sel-label');
    label.textContent = hit ? (hit.label != null ? hit.label : hit.value) : '—';
    label.title = hit ? (hit.title || hit.label || '') : '';
    root.querySelectorAll('.sel-opt').forEach((n) => {
      n.classList.toggle('active', n.dataset.value === st.value);
    });
    root.classList.toggle('empty', !hit);
  }
  function selValue(id) {
    const st = SELS[id];
    return st ? st.value : '';
  }
  function setSelValue(id, v) {
    const st = SELS[id];
    if (!st) return;
    st.value = v == null ? '' : String(v);
    paintSel(id);
  }

  function activeProvider() {
    const st = S.settings;
    if (!st || !st.providers || !st.providers.length) return null;
    return st.providers.find((p) => p.id === st.activeProviderId) || st.providers[0];
  }
  function protoMeta(key) {
    return S.protocols.find((p) => p.key === key) || { key, label: key, sub: '', baseUrl: '', hint: '' };
  }
  // 模型 id → 展示名（pi-browser-agent 的 autoModelName 规则）：
  // 去 org/ 前缀取最后一段，按 -/_ 切分；gpt 与 o+数字 转大写；纯数字版本号原样；其余首字母大写。
  function autoModelName(id) {
    if (!id) return '';
    const tail = id.split('/').pop() || id;
    return tail.split(/[-_]+/).filter(Boolean).map((seg) => {
      if (/^gpt$/i.test(seg)) return 'GPT';
      if (/^o\d+$/i.test(seg)) return seg.toUpperCase();
      if (/^\d+(\.\d+)*$/.test(seg)) return seg; // 4.1 / 5 / 2.5 原样
      return seg.charAt(0).toUpperCase() + seg.slice(1);
    }).join(' ');
  }
  // 徽标/菜单显示名：优先供应商模型表里的 name，手填 id 回落到 autoModelName
  function displayModelName(id) {
    if (!id) return '';
    const p = activeProvider();
    const m = p && p.models && p.models.find((x) => x && x.id === id);
    if (m && m.name && m.name !== m.id) return m.name;
    return autoModelName(id);
  }

  // ── 设置 → 左栏 ─────────────────────────────────────────
  function applySettingsToLeft() {
    const st = S.settings;
    if (!st) return;
    renderThinkingOptions();
    setSelValue('thinkingSel', st.thinking || 'off');
    $('maxSteps').value = st.agent?.maxSteps || 40;
    $('autoValidate').checked = !!st.agent?.autoValidate;
    $('compressOn').checked = !!st.agent?.compressOn;
    $('streamOff').checked = !!st.agent?.streamDisabled;
    $('compressAt').value = st.agent?.compressAt || 75;
    $('compressAtLabel').textContent = ($('compressAt').value) + '%';
    // 生成方式：genMode 是唯一真源；老设置只有 multiStep 时按它推断一次并回写。
    st.agent = st.agent || {};
    S.genMode = normalizeGenMode(st.agent.genMode, st.agent.multiStep);
    st.agent.genMode = S.genMode;
    st.agent.multiStep = S.genMode === 'layers';
    syncGenSwitch();
    if (!S.svg && $('prompt').value === '') $('prompt').value = S.defaultPrompt;
    S.canvas = st.canvas || S.canvas;
    syncCanvasSelect();
    updateBadge();
  }

  function updateBadge() {
    syncQuotaProvider();
    const st = S.settings;
    const model = st?.activeModel || '';
    const p = activeProvider();
    renderFastMode();
    $('modelBadgeText').textContent = (p && p.authMode === 'chatgpt' ? 'ChatGPT 订阅 · ' : '') + (model ? displayModelName(model) : '未选择模型');
    $('modelBadge').title = model
      ? `${p ? p.name : ''} · ${displayModelName(model)}${displayModelName(model) !== model ? ` (${model})` : ''}（点击切换模型）`
      : '尚未选择模型（点击打开模型列表）';
    const dot = $('badgeDot');
    if (S.running) dot.className = 'dot busy';
    else if (model && p && (p.apiKey || p.authMode === 'chatgpt')) dot.className = 'dot ok';
    else dot.className = 'dot idle';
  }

  // ── 模型菜单（点击徽标弹出；显示所有分组，当前分组展开、其余折叠） ──
  const MM = { open: false, timer: 0 };
  function closeModelMenu() {
    if (!MM.open) return;
    MM.open = false;
    const box = $('modelMenu');
    box.classList.remove('show');
    box.classList.add('hide');
    clearTimeout(MM.timer);
    MM.timer = setTimeout(() => {
      box.classList.add('hidden');
      box.classList.remove('hide');
    }, 130);
  }
  // 菜单是 position:fixed：右边缘对齐徽标右边缘，顶部贴徽标下沿 +6，宽度夹在 300–420 与视口内
  function placeModelMenu() {
    const box = $('modelMenu');
    const badge = $('modelBadge');
    if (!box || !badge) return;
    const r = badge.getBoundingClientRect();
    const vw = window.innerWidth || 1200;
    const vh = window.innerHeight || 800;
    const width = clamp(r.width + 120, 300, 420);
    box.style.width = width + 'px';
    const right = clamp(vw - r.right, 8, Math.max(8, vw - width - 8));
    box.style.left = (vw - right - width) + 'px';
    box.style.right = 'auto';
    box.style.top = Math.max(6, r.bottom + 6) + 'px';
    // 展开多个分组时菜单可以更高
    const vh4 = vh * 0.72;
    box.style.maxHeight = Math.max(264, vh4) + 'px';
    // 底边超出视口时上移
    const h = box.offsetHeight || 0;
    if (h && box.getBoundingClientRect().bottom > vh - 8) {
      box.style.top = Math.max(6, vh - h - 8) + 'px';
    }
  }
  function openModelMenu() {
    const st = S.settings;
    const box = $('modelMenu');
    box.innerHTML = '';
    const provs = (st && st.providers) || [];
    const activeId = (st && st.activeProviderId) || '';
    const cur = (st && st.activeModel) || '';

    if (!provs.length) {
      const head = el('div', 'mm-head');
      head.appendChild(el('b', '', '无供应商'));
      const manage = el('button', 'mini', '管理');
      manage.type = 'button';
      manage.addEventListener('click', () => { closeModelMenu(); openProviders(); });
      head.appendChild(manage);
      box.appendChild(head);
      box.appendChild(el('div', 'mm-empty', '还没有供应商，点右上「管理」新建一个。'));
    }

    provs.forEach((p) => {
      const isCur = p.id === activeId;
      // ── 分组头（当前供应商高亮 + 可折叠）──
      const head = el('div', 'mm-head' + (isCur ? ' on' : ''));
      const caret = el('span', 'mm-caret', isCur ? '▾' : '▸');
      caret.title = isCur ? '收起该分组' : '展开该分组';
      head.appendChild(caret);
      head.appendChild(el('b', '', p.name || p.id));
      if (isCur) head.appendChild(el('span', 'mm-tag', '当前'));
      const manage = el('button', 'mini', '管理');
      manage.type = 'button';
      manage.addEventListener('click', () => { closeModelMenu(); openProviders(); });
      head.appendChild(manage);
      box.appendChild(head);
      if (!isCur) head.classList.add('collapsed');
      // 点击分组头：切换当前供应商（从而展开该组）
      head.addEventListener('click', (e) => {
        if (e.target.closest('.mini')) return;
        if (!S.settings) return;
        if (!isCur) {
          S.settings.activeProviderId = p.id;
          // 切组后默认选该组第一个模型
          const first = (p.models && p.models[0] && p.models[0].id) || '';
          S.settings.activeModel = first;
          if (!first && S.settings.activeModel && (!p.models || !p.models.some((m) => m.id === S.settings.activeModel))) {
            S.settings.activeModel = '';
          }
          updateBadge();
          persist();
          openModelMenu(); // 重展开：现在它是当前组
          toast('供应商：' + (p.name || p.id) + (first ? ' · ' + first : ''));
        }
      });

      // ── 分组体（仅当前供应商展开模型行）──
      const list = p.models || [];
      if (!isCur) return; // 折叠组到此为止
      const capsOf = (id) => (p.modelCaps && p.modelCaps[id]) || {};
      if (!list.length) {
        box.appendChild(el('div', 'mm-empty', '该供应商还没有模型，先去「管理」里获取模型列表。'));
      }
      list.forEach((m) => {
        const row = el('div', 'mm-item' + (m.id === cur ? ' active' : ''));
        row.appendChild(el('span', 'nm', displayModelName(m.id)));
        const c = capsOf(m.id);
        if (c.vision === true) row.appendChild(el('span', 'tagline', '视觉'));
        if (c.reasoning === true) row.appendChild(el('span', 'tagline', '推理'));
        if (m.id === cur) row.appendChild(el('span', 'tick', '✓'));
        row.title = m.id;
        row.addEventListener('click', () => {
          if (!S.settings) return;
          S.settings.activeModel = m.id;
          updateBadge();
          persist();
          closeModelMenu();
          toast('模型：' + (m.name || m.id));
        });
        box.appendChild(row);
      });

      // 手填的模型 id 不在列表里时也要能看见当前选中项
      if (cur && !list.some((m) => m.id === cur)) {
        box.appendChild(el('div', 'mm-sep'));
        const row = el('div', 'mm-item active');
        row.appendChild(el('span', 'nm', displayModelName(cur)));
        row.appendChild(el('span', 'tagline', '手填'));
        row.appendChild(el('span', 'tick', '✓'));
        box.appendChild(row);
      }
    });

    clearTimeout(MM.timer);
    box.classList.remove('hidden', 'hide');
    box.classList.add('entering');
    MM.open = true;
    placeModelMenu();
    requestAnimationFrame(() => {
      requestAnimationFrame(() => { if (MM.open) box.classList.remove('entering'); });
    });
  }
  $('modelBadge').addEventListener('click', (e) => {
    e.stopPropagation();
    if (MM.open) closeModelMenu();
    else openModelMenu();
  });
  document.addEventListener('mousedown', (e) => {
    if (!MM.open) return;
    if (e.target.closest('#modelMenu') || e.target.closest('#modelBadge')) return;
    closeModelMenu();
  });

  // ── 保存设置（防抖，可强制 flush） ──────────────────────
  function fastModeAvailable() {
    const p = activeProvider();
    return !!p && p.protocol === 'openai-responses';
  }
  function renderFastMode() {
    const available = fastModeAvailable();
    $('fastModeField').classList.toggle('hidden', !available);
    $('fastMode').checked = !!S.settings?.fastMode;
    $('fastMode').disabled = S.running || !available;
    if (!S.settings?.fastMode) $('fastModeStatus').textContent = '';
  }
  $('fastMode').addEventListener('change', () => {
    if (!S.settings || S.running) return;
    S.settings.fastMode = $('fastMode').checked;
    $('fastModeStatus').textContent = S.settings.fastMode ? '下次生成将请求 Fast 加速' : '';
    persist();
  });
  let persistTimer = 0;
  let persistPending = false;
  async function persistNow() {
    clearTimeout(persistTimer);
    persistPending = false;
    if (!S.settings) return;
    try {
      const r = await api('/api/settings', { method: 'PUT', body: S.settings });
      if (r && r.settings) S.settings = r.settings;
      updateBadge();
    } catch (e) {
      toast('保存设置失败：' + e.message, true);
      throw e;
    }
  }
  function persist() {
    persistPending = true;
    clearTimeout(persistTimer);
    persistTimer = setTimeout(() => { persistNow().catch(() => {}); }, 450);
  }
  async function flushPersist() {
    if (!persistPending) return;
    await persistNow();
  }

  // 把左栏当前可见值同步进 S.settings（不依赖 change 事件是否触发过）
  function syncInputsToSettings() {
    const st = S.settings;
    if (!st) return;
    st.thinking = selValue('thinkingSel') || st.thinking;
    st.fastMode = $('fastMode').checked;
    if (st.agent) {
      st.agent.maxSteps = clamp(parseInt($('maxSteps').value, 10) || st.agent.maxSteps || 40, 1, 200);
      st.agent.autoValidate = $('autoValidate').checked;
      st.agent.compressOn = $('compressOn').checked;
      st.agent.streamDisabled = $('streamOff').checked;
      st.agent.compressAt = clamp(parseInt($('compressAt').value, 10) || st.agent.compressAt || 75, 1, 95);
      st.agent.genMode = S.genMode;
      st.agent.multiStep = S.genMode === 'layers';
    }
    st.layers = st.layers || {};
    // 三档下图层文件都在，图层面板始终可用；multiStep 只表示「多图层模式」。
    st.layers.enabled = true;
    if (S.canvas) st.canvas = S.canvas;
    persist();
  }

  // ── 左栏输入绑定（连接信息已全部移入供应商弹窗） ──────────
  $('maxSteps').addEventListener('change', () => {
    if (!S.settings) return;
    S.settings.agent.maxSteps = clamp(parseInt($('maxSteps').value, 10) || 40, 1, 200);
    $('maxSteps').value = S.settings.agent.maxSteps;
    persist();
  });
  $('autoValidate').addEventListener('change', () => {
    if (!S.settings) return;
    S.settings.agent.autoValidate = $('autoValidate').checked;
    persist();
  });
  $('compressOn').addEventListener('change', () => {
    if (!S.settings) return;
    S.settings.agent.compressOn = $('compressOn').checked;
    persist();
  });
  $('streamOff').addEventListener('change', () => {
    if (!S.settings) return;
    S.settings.agent.streamDisabled = $('streamOff').checked;
    persist();
  });
  $('compressAt').addEventListener('input', () => {
    $('compressAtLabel').textContent = $('compressAt').value + '%';
  });
  $('compressAt').addEventListener('change', () => {
    if (!S.settings) return;
    S.settings.agent.compressAt = parseInt($('compressAt').value, 10) || 75;
    persist();
  });

  // ── 画布比例 ────────────────────────────────────────────
  // 思考档位：默认 4 档；开启「扩展思考」后追加 xhigh / max 两档
  const THINK_BASE = [
    { value: 'off', label: 'off — 不思考' },
    { value: 'low', label: 'low — 轻量' },
    { value: 'medium', label: 'medium — 中等' },
    { value: 'high', label: 'high — 深入' },
  ];
  const THINK_EXT = [
    { value: 'xhigh', label: 'xhigh — 极致' },
    { value: 'max', label: 'max — 最大' },
  ];
  function extendedThinking() {
    return !!(S.settings && S.settings.extendedThinking);
  }
  function renderThinkingOptions() {
    const ext = extendedThinking();
    const opts = ext ? THINK_BASE.concat(THINK_EXT) : THINK_BASE.slice();
    // 关掉扩展思考时，xhigh/max 回落到 high（与后端 Normalize 的降级规则一致）
    const cur = selValue('thinkingSel');
    setSelOptions('thinkingSel', opts, true);
    if (!ext && (cur === 'xhigh' || cur === 'max')) setSelValue('thinkingSel', 'high');
    const root = $('thinkingSel');
    if (root) root.title = ext ? '6 档（含 xhigh / max）' : '4 档（可在供应商管理里开启扩展思考）';
  }
  function initSels() {
    initSel('thinkingSel', (v) => {
      if (!S.settings) return;
      S.settings.thinking = v || 'off';
      persist();
    });
    initSel('canvasSel', (v) => { applyCanvasIndex(parseInt(v, 10)); });
    setSelOptions('thinkingSel', THINK_BASE, true);
  }
  function renderCanvasSelect() {
    const opts = S.canvases.map((c, i) => ({
      value: String(i),
      label: c.name,
      sub: `${c.width}×${c.height}`,
      title: c.viewBox ? `viewBox ${c.viewBox}` : '',
    }));
    setSelOptions('canvasSel', opts, true);
    syncCanvasSelect();
  }
  function syncCanvasSelect() {
    if (!S.canvas) { setSelValue('canvasSel', ''); return; }
    let idx = S.canvases.findIndex((c) =>
      c.name === S.canvas.name || (c.width === S.canvas.width && c.height === S.canvas.height));
    if (idx < 0) idx = 0;
    setSelValue('canvasSel', String(idx));
    $('canvasHint').textContent = `${S.canvas.width}×${S.canvas.height}`;
  }
  async function applyCanvasIndex(idx) {
    const c = S.canvases[idx];
    if (!c) return;
    S.canvas = { name: c.name, width: c.width, height: c.height, viewBox: c.viewBox };
    $('canvasHint').textContent = `${c.width}×${c.height}`;
    if (S.settings) S.settings.canvas = S.canvas;
    persist();
    if (!S.svg) { resetView(); return; }
    // 后端不再改写 SVG：换画布只更新尺寸规范，下一次生成按新画布出图。
    resetView();
    addLog('LOG', `画布切到 ${c.name}（现有 SVG 保持原样，不做重排）`);
  }

  // ── 图片输入 ────────────────────────────────────────────
  const drop = $('drop');
  $('fileInput').addEventListener('change', (e) => {
    handleFiles(Array.from(e.target.files || []));
    e.target.value = '';
  });
  drop.addEventListener('click', () => $('fileInput').click());
  ['dragenter', 'dragover'].forEach((ev) => {
    drop.addEventListener(ev, (e) => { e.preventDefault(); drop.classList.add('over'); });
  });
  ['dragleave', 'drop'].forEach((ev) => {
    drop.addEventListener(ev, (e) => { e.preventDefault(); drop.classList.remove('over'); });
  });
  drop.addEventListener('drop', (e) => {
    const files = Array.from(e.dataTransfer?.files || []);
    handleFiles(files);
  });
  const scrollEl = $('canvasScroll');
  ['dragenter', 'dragover'].forEach((ev) => {
    scrollEl.addEventListener(ev, (e) => {
      if (!e.dataTransfer || !Array.from(e.dataTransfer.types || []).includes('Files')) return;
      e.preventDefault();
      scrollEl.style.outline = '2px dashed var(--accent)';
      scrollEl.style.outlineOffset = '-6px';
    });
  });
  ['dragleave', 'drop'].forEach((ev) => {
    scrollEl.addEventListener(ev, () => {
      scrollEl.style.outline = '';
      scrollEl.style.outlineOffset = '';
    });
  });
  scrollEl.addEventListener('dragover', (e) => e.preventDefault());
  scrollEl.addEventListener('drop', (e) => {
    e.preventDefault();
    handleFiles(Array.from(e.dataTransfer?.files || []));
  });
  document.addEventListener('paste', (e) => {
    const items = Array.from(e.clipboardData?.items || []);
    const files = items.filter((i) => i.kind === 'file').map((i) => i.getAsFile()).filter(Boolean);
    if (files.length) { e.preventDefault(); handleFiles(files); }
  });

  async function handleFiles(files) {
    const imgs = files.filter((f) => f && f.type && f.type.startsWith('image/'));
    if (!imgs.length) return;
    for (const f of imgs) {
      try {
        const img = await loadImage(f);
        const dataUrl = toPngScaled(img);
        S.images.push({ mime: 'image/png', data: dataUrl.split(',')[1], thumb: dataUrl, name: f.name || 'pasted' });
        addLog('LOG', `图片已载入：${f.name || '剪贴板'} (${img.naturalWidth}×${img.naturalHeight} → ≤${MAX_SIDE}px)`);
      } catch (err) {
        addLog('ERROR', '图片处理失败：' + (err.message || err));
      }
    }
    renderThumbs();
  }
  function loadImage(file) {
    return new Promise((res, rej) => {
      const url = URL.createObjectURL(file);
      const img = new Image();
      img.onload = () => { URL.revokeObjectURL(url); res(img); };
      img.onerror = () => { URL.revokeObjectURL(url); rej(new Error('无法解码图片')); };
      img.src = url;
    });
  }
  function toPngScaled(img) {
    let w = img.naturalWidth || img.width;
    let h = img.naturalHeight || img.height;
    const scale = Math.min(1, MAX_SIDE / Math.max(w, h));
    w = Math.max(1, Math.round(w * scale));
    h = Math.max(1, Math.round(h * scale));
    const cv = document.createElement('canvas');
    cv.width = w; cv.height = h;
    const cx = cv.getContext('2d');
    cx.fillStyle = '#ffffff';
    cx.fillRect(0, 0, w, h);
    cx.drawImage(img, 0, 0, w, h);
    return cv.toDataURL('image/png');
  }
  function renderThumbs() {
    const box = $('thumbs');
    box.innerHTML = '';
    S.images.forEach((im, i) => {
      const t = el('div', 'thumb');
      const img = el('img');
      img.src = im.thumb;
      img.alt = im.name || '';
      t.appendChild(img);
      const x = el('button', 'x', '×');
      x.type = 'button';
      x.title = '移除';
      x.addEventListener('click', () => { S.images.splice(i, 1); renderThumbs(); });
      t.appendChild(x);
      box.appendChild(t);
    });
  }

  // ── SVG 渲染 ────────────────────────────────────────────
  function sanitizeSVG(svg) {
    try {
      const doc = new DOMParser().parseFromString(svg, 'image/svg+xml');
      if (doc.querySelector('parsererror')) return svg;
      doc.querySelectorAll('script').forEach((n) => n.remove());
      doc.querySelectorAll('*').forEach((n) => {
        for (const a of Array.from(n.attributes)) {
          if (/^on/i.test(a.name)) n.removeAttribute(a.name);
        }
        if (n.hasAttribute('href') && /^\s*javascript:/i.test(n.getAttribute('href'))) n.removeAttribute('href');
      });
      const out = new XMLSerializer().serializeToString(doc.documentElement);
      return out.startsWith('<svg') ? out : svg;
    } catch { return svg; }
  }

  function parseStats(svg) {
    const st = { viewBox: '—', size: '—', elements: 0, bytes: svg ? new Blob([svg]).size : 0, w: 0, h: 0 };
    if (!svg) return st;
    try {
      const doc = new DOMParser().parseFromString(svg, 'image/svg+xml');
      if (doc.querySelector('parsererror')) return st;
      const root = doc.documentElement;
      const vb = root.getAttribute('viewBox') || '';
      st.viewBox = vb || '—';
      const w = root.getAttribute('width') || '';
      const h = root.getAttribute('height') || '';
      st.size = (w && h) ? `${parseFloat(w)}×${parseFloat(h)}` : '—';
      st.w = parseFloat(w) || 0;
      st.h = parseFloat(h) || 0;
      if (!st.w || !st.h) {
        const parts = vb.trim().split(/[\s,]+/).map(Number);
        if (parts.length === 4) { st.w = parts[2]; st.h = parts[3]; }
      }
      st.elements = root.querySelectorAll('*').length;
    } catch { /* ignore */ }
    return st;
  }

  // 把「尚未闭合」的 SVG 片段补成合法文档：截到最后完整标签，再按标签栈补闭合。
  function completeSVG(part) {
    let s = String(part || '');
    const at = s.indexOf('<svg');
    if (at < 0) return null;
    s = s.slice(at);
    const gt = s.lastIndexOf('>');
    if (gt < 0) return null;
    s = s.slice(0, gt + 1);
    const stack = [];
    const re = /<(\/?)([a-zA-Z][\w:.-]*)((?:"[^"]*"|'[^']*'|[^>"'])*)(\/?)>/g;
    let m;
    while ((m = re.exec(s))) {
      const name = m[2].toLowerCase();
      if (m[1] === '/') {
        for (let i = stack.length - 1; i >= 0; i--) {
          if (stack[i] === name) { stack.length = i; break; }
        }
      } else if (m[4] !== '/' && !VOID_TAGS[name]) {
        stack.push(name);
      }
    }
    let out = s;
    for (let i = stack.length - 1; i >= 0; i--) out += '</' + stack[i] + '>';
    return out;
  }

  // 实时预览：模型还在吐 emit_svg 的参数时就逐步上屏（补全渲染，不重排视图）。
  let previewSVG = '';
  function setSVGPreview(part) {
    const full = completeSVG(part);
    if (!full || full === previewSVG) return;
    const first = !previewSVG;
    previewSVG = full;
    S.livePreview = true;
    const host = $('svgHost');
    const clean = sanitizeSVG(full);
    host.innerHTML = clean;
    const node = host.querySelector('svg');
    if (node) {
      node.setAttribute('width', '100%');
      node.setAttribute('height', '100%');
      node.style.display = 'block';
      node.style.width = '100%';
      node.style.height = '100%';
      if (!node.getAttribute('preserveAspectRatio')) node.setAttribute('preserveAspectRatio', 'xMidYMid meet');
    }
    host.classList.add('live');
    const st = parseStats(clean);
    S.stats = st;
    $('statViewBox').textContent = st.viewBox;
    $('statSize').textContent = st.size;
    $('statElements').textContent = String(st.elements);
    $('statBytes').textContent = fmtNum(st.bytes) + ' B';
    if (first) recomputeFit();
    else applyTransform();
  }

  function setSVG(svg, why) {
    // 空工程也要走一遍：清掉上一幅图、把统计归位，别留着旧画面当新工程。
    if (!svg) { resetWorkspaceView(); S.svg = ''; return; }
    S.svg = svg;
    previewSVG = '';
    const host = $('svgHost');
    host.classList.remove('live');
    const clean = sanitizeSVG(svg);
    host.innerHTML = clean;
    const node = host.querySelector('svg');
    if (node) {
      node.setAttribute('width', '100%');
      node.setAttribute('height', '100%');
      node.style.display = 'block';
      node.style.width = '100%';
      node.style.height = '100%';
      if (!node.getAttribute('preserveAspectRatio')) node.setAttribute('preserveAspectRatio', 'xMidYMid meet');
    }
    const st = parseStats(clean);
    S.stats = st;
    $('statViewBox').textContent = st.viewBox;
    $('statSize').textContent = st.size;
    $('statElements').textContent = String(st.elements);
    $('statBytes').textContent = fmtNum(st.bytes) + ' B';
    // 实时预览过的这一轮不再重排视图，避免中途缩放被清掉
    if (!S.livePreview) resetView();
    S.livePreview = false;
    scheduleValidate();
    if (S.codeOpen) renderCode();
    // 动图模式下换了新画面：重新识别帧分组 / SMIL，并接着播。
    animOnSVG();
    if (why !== 'silent') addLog('SVG', `画布更新 · ${st.elements} 元素 · ${fmtNum(st.bytes)} B`);
  }

  // ── 视图：缩放 + 平移 ───────────────────────────────────
  // 性能约定：
  //  · S.stats 只在 setSVG 时解析一次，fitBox 不再 new DOMParser
  //  · S.availW/H 只在 recomputeFit（容器尺寸变化）时量一次，fitBox 纯算术
  //  · 拖动 / 滚轮只改 S.panX/panY/zoom 并 scheduleView()，真正的样式写入合并到一帧一次
  function measureAvail() {
    const box = $('canvasScroll');
    if (!box) return;
    S.availW = Math.max(40, box.clientWidth - 48);
    S.availH = Math.max(40, box.clientHeight - 48);
  }
  function fitBox() {
    const st = S.stats;
    let uw = st.w, uh = st.h;
    if (!(uw > 0 && uh > 0)) {
      uw = (S.canvas && S.canvas.width) || 1024;
      uh = (S.canvas && S.canvas.height) || 1024;
    }
    const availW = S.availW || 40;
    const availH = S.availH || 40;
    const aspect = uw / uh;
    let fw, fh;
    if (aspect >= 1) {
      fw = availW; fh = availW / aspect;
      if (fh > availH) { fh = availH; fw = availH * aspect; }
    } else {
      fh = availH; fw = availH * aspect;
      if (fw > availW) { fw = availW; fh = availW / aspect; }
    }
    return { fw, fh, uw, uh };
  }
  function recomputeFit() {
    measureAvail();
    const b = fitBox();
    S.fitW = Math.max(16, b.fw);
    S.fitH = Math.max(16, b.fh);
    S.fitScale = b.fw / b.uw; // CSS px / 用户单位
    applyLayout();
    applyTransform();
  }
  function resetView() {
    S.zoom = 1;
    S.panX = 0;
    S.panY = 0;
    recomputeFit();
  }
  // 只写尺寸：仅在 fit 结果变化（窗口 / 比例 / SVG 换图）时调用，带 140ms 过渡
  function applyLayout() {
    const host = $('svgHost');
    if (!host) return;
    const w = Math.round(S.fitW || 16);
    const h = Math.round(S.fitH || 16);
    if (host._w !== w) { host.style.width = w + 'px'; host._w = w; }
    if (host._h !== h) { host.style.height = h + 'px'; host._h = h; }
  }
  // 只写 transform + 缩放标签：拖动 / 滚轮每帧只走这里，不碰 width/height
  function applyTransform() {
    const host = $('svgHost');
    if (!host) return;
    host.style.transform = `translate(${S.panX}px, ${S.panY}px) scale(${S.zoom})`;
    const uw = S.stats.w || (S.canvas && S.canvas.width) || 1024;
    const real = ((S.fitW || 16) * S.zoom / uw) * 100;
    const label = $('zoomLabel');
    if (label) label.textContent = (real >= 10 ? Math.round(real) : real.toFixed(1)) + '%';
  }
  let viewRaf = 0;
  function scheduleView() {
    if (viewRaf) return;
    viewRaf = requestAnimationFrame(() => {
      viewRaf = 0;
      applyTransform();
    });
  }

  // 滚轮缩放：以光标位置为锚点，缩放时保持该点在屏幕上的位置
  scrollEl.addEventListener('wheel', (e) => {
    if (!S.svg) return;
    e.preventDefault();
    const rect = scrollEl.getBoundingClientRect();
    const cx = e.clientX - rect.left - rect.width / 2;
    const cy = e.clientY - rect.top - rect.height / 2;
    const before = S.zoom;
    const factor = Math.exp(-e.deltaY * 0.0016);
    // SVG 是矢量：放大由浏览器按最终分辨率重新栅格化，不糊——上限放到 64×（相对适应窗口）
    S.zoom = clamp(S.zoom * factor, 0.05, 64);
    const k = S.zoom / before;
    // 平移量按缩放比同步放大，锚点位置不动
    S.panX = cx - (cx - S.panX) * k;
    S.panY = cy - (cy - S.panY) * k;
    scheduleView();
  }, { passive: false });

  // 左键拖动平移：拖动期间只累加 panX/panY，样式写入交给 rAF 合并
  let drag = null;
  scrollEl.addEventListener('mousedown', (e) => {
    if (e.button !== 0) return;
    if (e.target.closest('button')) return;
    drag = { x: e.clientX, y: e.clientY, px: S.panX, py: S.panY };
    scrollEl.classList.add('grabbing', 'dragging');
    e.preventDefault();
  });
  window.addEventListener('mousemove', (e) => {
    if (!drag) return;
    S.panX = drag.px + (e.clientX - drag.x);
    S.panY = drag.py + (e.clientY - drag.y);
    scheduleView();
  });
  window.addEventListener('mouseup', () => {
    if (!drag) return;
    drag = null;
    scrollEl.classList.remove('grabbing', 'dragging');
  });
  // 双击回到居中
  scrollEl.addEventListener('dblclick', (e) => {
    if (!S.svg) return;
    if (e.target.closest('button')) return;
    resetView();
  });

  $('btnFit').addEventListener('click', () => { resetView(); });
  $('btnOne').addEventListener('click', () => {
    S.zoom = clamp(1 / (S.fitScale || 1), 0.05, 64);
    S.panX = 0;
    S.panY = 0;
    applyTransform();
  });
  window.addEventListener('resize', debounce(() => {
    recomputeFit();
    // 启动选择窗必答：窗口尺寸变化把弹窗挤掉（首帧重排 / 误触关闭）就补回来
    if (!bootPickOpen) return;
    const m = $('modalProject');
    if (!m) return;
    if (m.classList.contains('hidden')) openModal('modalProject');
    else m.classList.remove('opening');
  }, 120));
  if (window.ResizeObserver) new ResizeObserver(() => { recomputeFit(); }).observe(scrollEl);

  // ── 校验 ────────────────────────────────────────────────
  const scheduleValidate = debounce(async () => {
    if (!S.svg) return;
    const seq = S.runSeq;
    const source = S.svg;
    try {
      const rep = await api('/api/validate', { method: 'POST', body: { svg: source } });
      if (seq !== S.runSeq || source !== S.svg) return;
      renderReport(rep);
      if (S.codeOpen) renderReport(rep, 'codeIssues');
    } catch (e) {
      if (seq !== S.runSeq || source !== S.svg) return;
      $('issues').innerHTML = '';
      $('issues').appendChild(el('div', 'issue error', '校验失败：' + e.message));
      if (S.codeOpen) {
        $('codeIssues').innerHTML = '';
        $('codeIssues').appendChild(el('div', 'issue error', '校验失败：' + e.message));
      }
    }
  }, 300);

  function renderReport(rep, targetId) {
    if (!rep) return;
    S.lastReport = rep;
    if (rep.viewBox) $('statViewBox').textContent = rep.viewBox;
    if (rep.width && rep.height) $('statSize').textContent = `${rep.width}×${rep.height}`;
    if (rep.elements) $('statElements').textContent = String(rep.elements);
    if (rep.bytes) $('statBytes').textContent = fmtNum(rep.bytes) + ' B';
    const box = $(targetId || 'issues');
    if (!box) return;
    box.innerHTML = '';
    const issues = rep.issues || [];
    if (!issues.length) {
      box.appendChild(el('div', 'issue info', rep.ok ? '校验通过，无问题。' : '未返回问题。'));
      return;
    }
    const order = { error: 0, warn: 1, info: 2 };
    issues.slice().sort((a, b) => (order[a.level] ?? 3) - (order[b.level] ?? 3)).forEach((it) => {
      const row = el('div', 'issue ' + (it.level || 'info'));
      row.appendChild(el('span', 'lvl', it.level || 'info'));
      if (it.code) row.appendChild(el('span', 'code', it.code));
      row.appendChild(el('span', 'txt', it.message || ''));
      box.appendChild(row);
    });
  }

  // ── 模型输出流（右栏：思维链与正文按到达顺序交替） ────────
  // 每一段是 { role: 'think' | 'text' | 'err', text }，相邻同 role 合并，不同 role 另起一段。
  const ROLE_META = {
    think: { cls: 'fseg-think', label: '思维链', foldable: true },
    text: { cls: 'fseg-text', label: '回复', foldable: false },
    err: { cls: 'fseg-err', label: '错误', foldable: false },
  };

  // ── 轻量 Markdown 渲染（纯 DOM 构建，不走 innerHTML）────────
  // 支持：标题 / 粗斜体 / 行内码 / 删除线 / 链接 / 围栏代码块 / 列表 / 引用 / 分隔线。
  // 单换行按换行显示（聊天风格）；模型输出里字面量 \n 也归一成真空行。
  const MD_INLINE_RE = /(\*\*([^*\n]+)\*\*)|(\*([^*\n]+)\*)|(`([^`\n]+)`)|(~~([^~\n]+)~~)|(\[([^\]]+)\]\(([^)\s]+)\))/g;
  function mdInline(parent, str) {
    let last = 0;
    const re = new RegExp(MD_INLINE_RE.source, 'g');
    let m;
    while ((m = re.exec(str)) !== null) {
      if (m.index > last) parent.appendChild(document.createTextNode(str.slice(last, m.index)));
      if (m[2] != null) { const n = el('strong'); mdInline(n, m[2]); parent.appendChild(n); }
      else if (m[4] != null) { const n = el('em'); mdInline(n, m[4]); parent.appendChild(n); }
      else if (m[6] != null) { parent.appendChild(el('code', 'md-code', m[6])); }
      else if (m[8] != null) { parent.appendChild(el('del', 'md-del', m[8])); }
      else if (m[10] != null) {
        const url = m[11];
        if (/^(https?:|mailto:)/i.test(url)) {
          const a = el('a', 'md-a', m[10]);
          a.href = url;
          a.title = url;
          a.addEventListener('click', (e) => {
            e.preventDefault();
            e.stopPropagation();
            if (navigator.clipboard && navigator.clipboard.writeText) {
              navigator.clipboard.writeText(url).then(() => toast('链接已复制')).catch(() => toast(url));
            } else toast(url);
          });
          parent.appendChild(a);
        } else {
          parent.appendChild(document.createTextNode(m[10]));
        }
      }
      last = m.index + m[0].length;
    }
    if (last < str.length) parent.appendChild(document.createTextNode(str.slice(last)));
  }
  function mdBlockParent(frag, tag, cls) {
    const n = el(tag, cls);
    frag.appendChild(n);
    return n;
  }
  function renderMarkdown(src) {
    const frag = document.createDocumentFragment();
    const text0 = String(src == null ? '' : src).replace(/\r\n?/g, '\n');
    // 有些网关把换行转义成字面量 \n 返回：整段没有任何真换行时才归一，避免误伤代码块里的 "\n"
    const text = (!text0.includes('\n') && /\\r\\n|\\n/.test(text0))
      ? text0.replace(/\\r\\n/g, '\n').replace(/\\n/g, '\n')
      : text0;
    const lines = text.split('\n');
    const para = []; // 当前段落行
    const flushPara = () => {
      if (!para.length) return;
      const p = el('p', 'md-p');
      para.forEach((ln, k) => {
        if (k) p.appendChild(el('br'));
        mdInline(p, ln);
      });
      frag.appendChild(p);
      para.length = 0;
    };
    let i = 0;
    while (i < lines.length) {
      const line = lines[i];
      // 围栏代码块（流式期间未闭合也要能显示）
      const fence = /^\s*```([\w+-]*)\s*$/.exec(line);
      if (fence) {
        flushPara();
        const buf = [];
        i++;
        while (i < lines.length && !/^\s*```\s*$/.test(lines[i])) { buf.push(lines[i]); i++; }
        if (i < lines.length) i++; // 吃掉闭合围栏
        const pre = el('pre', 'md-pre');
        pre.dataset.lang = fence[1] || '';
        pre.textContent = buf.join('\n');
        frag.appendChild(pre);
        continue;
      }
      const h = /^(#{1,6})\s+(.*)$/.exec(line);
      if (h) {
        flushPara();
        const n = Math.min(h[1].length, 4); // 最多到 h4 尺寸
        const hn = el('h' + n, 'md-h md-h' + n);
        mdInline(hn, h[2]);
        frag.appendChild(hn);
        i++;
        continue;
      }
      if (/^\s*(\*\*\*+|---+|___+)\s*$/.test(line)) {
        flushPara();
        frag.appendChild(el('hr', 'md-hr'));
        i++;
        continue;
      }
      const q = /^\s*>\s?(.*)$/.exec(line);
      if (q) {
        flushPara();
        const bq = mdBlockParent(frag, 'blockquote', 'md-quote');
        const buf = [];
        while (i < lines.length) {
          const m2 = /^\s*>\s?(.*)$/.exec(lines[i]);
          if (!m2) break;
          buf.push(m2[1]);
          i++;
        }
        buf.forEach((ln, k) => {
          if (k) bq.appendChild(el('br'));
          mdInline(bq, ln);
        });
        continue;
      }
      const ul = /^(\s*)[-*+]\s+(.*)$/.exec(line);
      const ol = /^(\s*)(\d+)[.)]\s+(.*)$/.exec(line);
      if (ul || ol) {
        flushPara();
        const ordered = !!ol;
        const list = mdBlockParent(frag, ordered ? 'ol' : 'ul', 'md-list' + (ordered ? ' md-olist' : ''));
        while (i < lines.length) {
          const mu = /^(\s*)[-*+]\s+(.*)$/.exec(lines[i]);
          const mo = ordered ? /^(\s*)(\d+)[.)]\s+(.*)$/.exec(lines[i]) : null;
          const hit = ordered ? mo : mu;
          if (!hit || (ordered && mu)) break;
          const li = el('li', 'md-li');
          mdInline(li, ordered ? hit[3] : hit[2]);
          list.appendChild(li);
          i++;
        }
        continue;
      }
      if (!line.trim()) { flushPara(); i++; continue; }
      para.push(line);
      i++;
    }
    flushPara();
    return frag;
  }

  function pushFlow(role, chunk) {
    if (!chunk) return;
    const last = S.flow[S.flow.length - 1];
    if (last && last.role === role) last.text += chunk;
    else S.flow.push({ role, text: chunk, folded: role === 'think' ? false : undefined });
    scheduleFlow();
  }
  // 生成结束后调用：把所有思维链段自动折叠（点头部可再展开）
  function foldThinking() {
    let n = 0;
    S.flow.forEach((seg) => { if (seg.role === 'think' && !seg.folded) { seg.folded = true; n++; } });
    if (n) scheduleFlow();
    return n;
  }
  // 模型输出的渲染状态。流式期间只重画最后一段：整块重建会把滚动位置打回顶部，
  // 这正是「自动滚动失灵、手动滚动被拽回」的根因。
  const flowRS = { count: -1, lastFolded: undefined };

  // 距底部 tol 像素内算「贴底」——只有贴底时才跟随新内容
  function nearBottom(sc, tol) {
    if (!sc) return true;
    return sc.scrollHeight - sc.scrollTop - sc.clientHeight <= (tol == null ? 24 : tol);
  }

  function fillSegBody(pre, seg) {
    if (seg.role === 'err') { pre.textContent = seg.text; return; }
    pre.innerHTML = '';
    pre.appendChild(renderMarkdown(seg.text));
  }

  // 造一段（思维链 / 正文 / 错误）。展开态思维链自带限高滚动条，位置记在 seg 上。
  function flowSegNode(seg, i) {
    const meta = ROLE_META[seg.role] || ROLE_META.text;
    const wrap = el('div', 'fseg ' + meta.cls);
    wrap.dataset.i = String(i);
    if (seg.role === 'think' && seg.folded) wrap.classList.add('folded');
    const head = el('div', 'fseg-head');
    head.appendChild(el('span', 'fh-label', meta.label));
    if (seg.text.length > 40) head.appendChild(el('span', 'fh-cnt', seg.text.length + ' 字'));
    if (meta.foldable) {
      const f = el('span', 'fh-fold', seg.folded ? '▸' : '▾');
      f.title = seg.folded ? '展开思维链' : '折叠思维链';
      f.addEventListener('click', (e) => {
        e.stopPropagation();
        seg.folded = !seg.folded;
        renderFlow(true); // 折叠态变了：必须整块重排
      });
      head.appendChild(f);
    }
    wrap.appendChild(head);
    if (meta.foldable && seg.folded) {
      head.addEventListener('click', () => { seg.folded = false; renderFlow(true); });
      head.title = '展开思维链';
    }
    const pre = el('div', 'fseg-body md');
    fillSegBody(pre, seg);
    // 记下用户是否把这条思维链滚到了底部：只有贴底才继续跟随
    if (seg._pinned === undefined) seg._pinned = true;
    pre.addEventListener('scroll', () => {
      seg._bodyTop = pre.scrollTop;
      seg._pinned = nearBottom(pre, 8);
    });
    wrap.appendChild(pre);
    return wrap;
  }

  function renderFlow(force) {
    const box = $('flowBody');
    if (!box) return;
    const sc = flowScrollEl();
    const auto = !$('flowAuto') || $('flowAuto').checked;
    const wasNear = nearBottom(sc);
    const prevTop = sc ? sc.scrollTop : 0;

    if (!S.flow.length) {
      box.innerHTML = '';
      box.appendChild(el('div', 'flow-empty', '（尚无输出）'));
      flowRS.count = 0;
      const m0 = $('rawMeta');
      if (m0) m0.textContent = '0 字';
      return;
    }

    const last = S.flow[S.flow.length - 1];
    const canPatch = !force
      && flowRS.count === S.flow.length
      && flowRS.lastFolded === last.folded
      && box.querySelectorAll(':scope > .fseg').length === S.flow.length;
    if (canPatch) {
      // 增量：原地换掉最后一段的正文，其余段不动，滚动位置天然保住
      const node = box.querySelector(':scope > .fseg:last-child');
      if (node) {
        const pre = node.querySelector(':scope > .fseg-body');
        const head = node.querySelector(':scope > .fseg-head');
        if (pre) fillSegBody(pre, last);
        if (head) {
          let cnt = head.querySelector('.fh-cnt');
          if (last.text.length > 40) {
            if (!cnt) {
              cnt = el('span', 'fh-cnt', '');
              head.insertBefore(cnt, head.querySelector('.fh-fold'));
            }
            cnt.textContent = last.text.length + ' 字';
          } else if (cnt) {
            cnt.remove();
          }
        }
        if (pre && last.role === 'think' && !last.folded) {
          // 思维链自带限高滚动条：贴底就跟随，否则回到用户原来的位置
          if (auto && last._pinned !== false) pre.scrollTop = pre.scrollHeight;
          else if (last._bodyTop) pre.scrollTop = last._bodyTop;
        }
      }
    } else {
      box.innerHTML = '';
      const frag = document.createDocumentFragment();
      S.flow.forEach((seg, i) => frag.appendChild(flowSegNode(seg, i)));
      box.appendChild(frag);
      // 挂载后恢复各思维链自己的滚动位置（挂载前写 scrollTop 是空操作）
      box.querySelectorAll(':scope > .fseg-think:not(.folded) > .fseg-body').forEach((b) => {
        const seg = S.flow[Number(b.parentNode.dataset.i)];
        if (seg && seg._bodyTop) b.scrollTop = seg._bodyTop;
      });
    }

    flowRS.count = S.flow.length;
    flowRS.lastFolded = last.folded;

    const m = $('rawMeta');
    if (m) {
      const n = S.rawText.length + S.thinkText.length + S.errText.length;
      m.textContent = `${S.flow.length} 段 · ${n} 字`;
    }

    // 滚动策略：勾了「自动滚动」且原本就贴底 → 跟随最新；否则原地不动（用户在看历史）
    if (sc) sc.scrollTop = (auto && wasNear) ? sc.scrollHeight : prevTop;
    if (!canPatch && auto && wasNear) {
      box.querySelectorAll(':scope > .fseg-think:not(.folded) > .fseg-body').forEach((b) => {
        b.scrollTop = b.scrollHeight;
      });
    }
  }
  let flowTimer = 0;
  function scheduleFlow() {
    if (flowTimer) return;
    // 48ms 合并：流式 token 到达远比这密集，每帧重排会把滚动挤得来回跳
    flowTimer = setTimeout(() => {
      flowTimer = 0;
      requestAnimationFrame(() => renderFlow());
    }, 48);
  }
  // 勾回「自动滚动」时立刻贴底（取消勾选不打断当前阅读位置）
  if ($('flowAuto')) {
    $('flowAuto').addEventListener('change', () => {
      if (!$('flowAuto').checked) return;
      const sc = flowScrollEl();
      if (sc) sc.scrollTop = sc.scrollHeight;
      $('flowBody').querySelectorAll(':scope > .fseg-think:not(.folded) > .fseg-body')
        .forEach((b) => { b.scrollTop = b.scrollHeight; });
    });
  }
  $('btnClearRaw').addEventListener('click', () => {
    S.flow = [];
    S.rawText = '';
    S.thinkText = '';
    S.errText = '';
    renderFlow(true);
  });
  // 出错时滚到右栏最底部（错误段总在末尾），不切页签
  function showRawTab() {
    // 折叠状态下先展开，否则用户看不到错误
    const blk = $('blkRaw');
    if (blk && blk.classList.contains('collapsed')) setRBlock('blkRaw', false);
    const box = flowScrollEl();
    if (box) box.scrollTop = box.scrollHeight;
    scheduleFlow();
  }

  // ── 日志暂停 ────────────────────────────────────────────
  $('pauseBig').addEventListener('change', () => {
    S.pausedBig = $('pauseBig').checked;
    if (!S.pausedBig) $('bigLog').scrollTop = $('bigLog').scrollHeight;
  });

  // ── 用量 ────────────────────────────────────────────────
  function updateUsage(st) {
    if (!st) return;
    S.lastStats = st;
    const used = st.contextUsed || 0;
    const max = st.contextMax || 0;
    const pct = max > 0 ? clamp((used / max) * 100, 0, 100) : 0;
    $('usagePct').textContent = pct.toFixed(0) + '%';
    $('usageBar').style.width = pct + '%';
    $('usageBar').style.background = pct >= 90 ? 'var(--err)' : pct >= 70 ? 'var(--warn)' : 'var(--ok)';
    $('usageUsed').textContent = `${fmtNum(used)} / ${fmtNum(max)}`;
    $('usageBlocks').textContent = String(st.blocks || 0);
    $('usageTps').textContent = (st.tps || 0).toFixed(1);
    $('usageTokens').textContent = fmtNum(st.tokens || 0);
    $('usageSteps').textContent = `${st.modelSteps || 0} / ${st.toolCalls || 0}`;
  }

  // ── 工具调用摘要（只进日志流，不再有独立卡片列表） ──────
  function argsSummary(args) {
    if (args == null) return '';
    let s;
    if (typeof args === 'string') s = args;
    else {
      try { s = JSON.stringify(args); } catch { s = String(args); }
    }
    s = s.replace(/\s+/g, ' ');
    return s.length > 110 ? s.slice(0, 110) + '…' : s;
  }
  function toolStart(ev) {
    addLog('TOOL', `▶ ${ev.tool || 'tool'} ${argsSummary(ev.args)}`);
  }
  function toolEnd(ev) {
    const failed = !!(ev.error && ev.error.length);
    const res = ev.result || '';
    addLog('TOOL', `■ ${ev.tool || 'tool'} ${failed ? '失败：' + ev.error : '完成' + (res ? ' · ' + res.replace(/\s+/g, ' ').slice(0, 160) : '')}`);
  }

  // ── ask 卡片 ────────────────────────────────────────────
  function askCard(ev) {
    const wrap = el('div', 'tcard');
    const head = el('div', 'tcard-head');
    head.appendChild(el('span', 'nm', 'ask_user'));
    head.appendChild(el('span', 'args', ev.question || ''));
    wrap.appendChild(head);
    const body = el('div', 'tcard-body');
    body.textContent = ev.question || '';
    const opts = el('div', '');
    opts.style.display = 'flex';
    opts.style.flexWrap = 'wrap';
    opts.style.gap = '4px';
    opts.style.marginTop = '5px';
    (ev.options || []).forEach((o) => {
      const b = el('button', 'mini', o);
      b.type = 'button';
      b.addEventListener('click', () => {
        addLog('ASK', `选择：${o}`);
        opts.querySelectorAll('button').forEach((x) => { x.disabled = true; });
      });
      opts.appendChild(b);
    });
    if (!(ev.options || []).length) opts.appendChild(el('span', 'muted tiny', '（无选项）'));
    body.appendChild(opts);
    wrap.appendChild(body);
    pushRawLogNode(wrap);
    addLog('ASK', ev.question || '需要澄清');
  }

  // ── SSE 消费 ────────────────────────────────────────────
  function dispatchEvent(ev) {
    if (!ev || !ev.kind) return;
    switch (ev.kind) {
      case 'text': {
        const t = ev.text || '';
        if (!t) break;
        S.rawText += t;
        pushFlow('text', t);
        addLog('TEXT', S.rawText);
        break;
      }
      case 'thinking': {
        const t = ev.text || '';
        if (!t) break;
        S.thinkText += t;
        pushFlow('think', t);
        addLog('THINK', S.thinkText);
        break;
      }
      case 'log': {
        const lv = (ev.level || 'info').toLowerCase();
        const map = { error: 'ERROR', warn: 'NUDGE', tool: 'TOOL', info: 'LOG', debug: 'LOG' };
        addLog(map[lv] || 'LOG', ev.text || '');
        break;
      }
      case 'svg': {
        if (ev.svg) setSVG(ev.svg, 'stream');
        break;
      }
      case 'tool_delta': {
        if (ev.text) setSVGPreview(ev.text);
        break;
      }
      case 'canvas': {
        if (ev.canvas) {
          S.canvas = ev.canvas;
          if (S.settings) S.settings.canvas = ev.canvas;
          syncCanvasSelect();
          resetView();
          addLog('LOG', `画布：${ev.canvas.name || ''} ${ev.canvas.width}×${ev.canvas.height}`);
        }
        break;
      }
      case 'tool_start': toolStart(ev); break;
      case 'layers': if (Array.isArray(ev.layers)) { S.project = S.project || {}; S.project.layers = ev.layers; renderLayers(); } break;
      case 'tool_end': toolEnd(ev); break;
      case 'usage': updateUsage(ev.usage); break;
      case 'service_tier':
        if (S.settings?.fastMode && fastModeAvailable()) {
          const tier = ev.text || '';
          $('fastModeStatus').textContent = (tier === 'priority' || tier === 'fast') ? '本轮已使用 Fast 加速'
            : tier === 'default' ? '本轮按标准档位处理（已请求 Fast）' : '已请求 Fast，服务端未确认加速档位';
        }
        break;
      case 'nudge': addLog('NUDGE', ev.text || '上下文接近上限，建议压缩'); break;
      case 'ask': askCard(ev); break;
      case 'done': {
        updateUsage(ev.usage);
        if (ev.session) S.session = ev.session; // 对话历史跟着工程走，记住会话 id
        refreshProject(); // 模型可能改了图层文件，重读一次
        finishRun('DONE', '任务完成' + (ev.usage ? ` · ${ev.usage.modelSteps} 轮 / ${ev.usage.toolCalls} 工具 / ${(ev.usage.elapsedMs / 1000).toFixed(1)}s` : ''));
        break;
      }
      case 'error': {
        if (ev.usage) updateUsage(ev.usage);
        const msg = ev.error || ev.text || '未知错误';
        addLog('ERROR', msg);   // addLog 内部已把错误写进 S.errText，不再重复追加
        pushFlow('err', msg);
        finishRun('ERROR', '任务失败');
        showRawTab('err');
        break;
      }
      default:
        addLog('LOG', `事件 ${ev.kind}${ev.text ? '：' + ev.text : ''}`);
    }
  }

  function finishRun(level, note) {
    refreshQuota();
    S.running = false;
    setRunningUI(false); // 有会话时按钮是「发送」，否则是「开始生成」
    addLog(level === 'ERROR' ? 'ERROR' : 'DONE', note || '结束');
    updateBadge();
    const folded = foldThinking();
    if (folded) addLog('LOG', `已自动折叠 ${folded} 段思维链`);
    renderFlow(true);
    if (S.svg) scheduleValidate();
  }
  // ── 工程 / 图层（对话历史属于工程，不再有「新建/连续」二选一） ──
  function setMode() { setRunningUI(S.running); }

  // 当前工程里是否有可延续的历史
  function hasSession() { return !!(S.project && S.project.open && S.session); }

  const HINTS = {
    multiOn: '<b>多步修改</b>：模型可分层创建、读取、编辑，逐步细化。',
    multiOff: '<b>单步生成</b>：模型一次交付整幅 SVG（无图层工具）。',
    noProj: '<b>尚未打开工程</b>：点击顶部「工程」选择模式。',
  };

  // ── 生成方式：三级开关（快速 / 多图层 / 动图） ────────────
  //
  // 状态：S.genMode 三档之一；S.genLocked 一旦点过「开始生成」就为 true。
  // 锁定规则（需求原文「已经开始后无法再切换回去，只有快速模式支持二次切换」）：
  //   - 未锁定：三档随便切；
  //   - 已锁定且当前停在快速模式：还允许再切一次，切走之后永久锁死；
  //   - 已锁定且当前不是快速模式：三档全部不可点、不可拖。
  // 新建 / 打开 / 关闭工程会解锁。
  const GEN_MODES = ['quick', 'layers', 'anim'];
  const GEN_LABEL = { quick: '快速模式', layers: '多图层模式', anim: '动图模式' };

  // normalizeGenMode 兼容老设置：只有 multiStep 时按它推断。
  function normalizeGenMode(mode, multiStep) {
    if (GEN_MODES.indexOf(mode) >= 0) return mode;
    return multiStep === false ? 'quick' : 'layers';
  }

  function layerCount() {
    const ls = (S.project && S.project.layers) || [];
    return ls.length;
  }

  // genSwitchAllowed 当前是否还有切换资格。
  //   没点过「开始生成」→ 三档之间随便来回切，包括从动图切回快速 / 多图层；
  //   点过之后 → 只有仍停在快速模式时才留最后一次机会，切走即封死；
  //   开始生成时若正停在动图模式，动图是单向门，直接封死。
  function genSwitchAllowed() {
    if (!S.genLocked) return true;
    return S.genMode === 'quick';
  }

  // genBlockReason 当前被封死的原因（给 toast / tooltip 用）。
  function genBlockReason() {
    if (S.genMode === 'anim') {
      return '动图模式已开始生成，无法切回其他模式（点「快速生成」可重新选择）';
    }
    return '已经开始生成，无法再切换生成方式（点「快速生成」可重新选择）';
  }

  // canSwitchTo 能否切到目标档，返回 null 表示可以，否则返回拒绝原因。
  function canSwitchTo(target) {
    if (target === S.genMode) return null;
    if (GEN_MODES.indexOf(target) < 0) return '未知的生成方式';
    if (!genSwitchAllowed()) return genBlockReason();
    const n = layerCount();
    if (target !== 'layers' && n > 1) {
      return `当前有 ${n} 个图层，先把多余的图层删到 1 个才能切到${GEN_LABEL[target]}`;
    }
    return null;
  }

  // syncGenSwitch 把 S.genMode / S.genLocked 渲染到开关上。
  function syncGenSwitch() {
    const track = $('genTrack');
    const thumb = $('genThumb');
    const wrap = $('genSwitch');
    if (!track || !thumb) return;
    const mode = GEN_MODES.indexOf(S.genMode) >= 0 ? S.genMode : 'layers';
    S.genMode = mode;
    const idx = GEN_MODES.indexOf(mode);

    track.querySelectorAll('.gen-opt').forEach((btn) => {
      const on = btn.dataset.mode === mode;
      btn.classList.toggle('on', on);
      btn.setAttribute('aria-checked', on ? 'true' : 'false');
      // 锁定后：只有「还能切」的档位保持可点，其余置灰
      const allowed = on || genSwitchAllowed();
      btn.classList.toggle('allowed', allowed);
      btn.disabled = !allowed;
    });
    thumb.style.transform = `translateX(${idx * 100}%)`;
    if (wrap) {
      const frozen = !genSwitchAllowed();
      wrap.classList.toggle('locked', frozen);
      wrap.classList.toggle('sealed', frozen && S.genMode === 'anim');
      wrap.title = frozen
        ? genBlockReason()
        : '拖动或点击切换：快速模式一次成图；多图层模式分步按图层改造；动图模式单图层输出可播放动图';
    }
    renderGenHint();
    syncAnimBar();
  }

  // renderGenHint 开关下方的一句话说明（锁定原因 / 当前档位的含义）。
  function renderGenHint() {
    const el = $('genHint');
    if (!el) return;
    let text = '';
    let warn = false;
    if (!S.genLocked) {
      // 还没开始生成：三档自由切换，只说明当前档位干什么。
      if (S.genMode === 'anim') text = '单图层输出可播放的动图，支持导出 GIF。';
      else if (S.genMode === 'layers') text = '模型分多步、按图层改造画面。';
      else text = '模型一次性交付整幅 SVG，最快。';
    } else if (S.genMode === 'anim') {
      text = '动图模式已开始生成：单向锁定，无法切回快速/多图层（点「快速生成」可重选）。';
      warn = true;
    } else if (S.genMode === 'quick') {
      text = '已开始生成：仍是快速模式，可以切换最后一次，之后锁定。';
      warn = true;
    } else {
      text = '已锁定：本次会话不再允许切换生成方式（新建或打开工程可解锁）。';
      warn = true;
    }
    el.textContent = text;
    el.classList.toggle('warn', warn);
  }

  // setGenMode 切换生成方式（唯一入口：点击、拖动、键盘都走这里）。
  function setGenMode(target, opts) {
    const why = canSwitchTo(target);
    if (why) {
      if (!(opts && opts.silent)) toast(why, true);
      syncGenSwitch();
      return false;
    }
    if (target === S.genMode) return true;
    const prev = S.genMode;
    S.genMode = target;
    if (S.settings) {
      S.settings.agent = S.settings.agent || {};
      S.settings.agent.genMode = target;
      S.settings.agent.multiStep = target === 'layers';
      persist();
    }
    syncGenSwitch();
    if (target === 'anim') animMount();
    else if (prev === 'anim') animUnmount();
    if (!(opts && opts.silent)) addLog('LOG', `生成方式：${GEN_LABEL[target]}`);
    return true;
  }

  // genLock 点「开始生成」时调用：锁死切换（快速模式仍留一次机会）。
  function genLock() {
    if (S.genLocked) return;
    S.genLocked = true;
    syncGenSwitch();
  }

  // genUnlock 换工程时只解「开始生成」那把锁。
  //   动图是单向门，换工程也不解除 —— 只有「快速生成」（清空重来）才回到快速模式。
  function genUnlock() {
    if (!S.genLocked) return;
    S.genLocked = false;
    syncGenSwitch();
  }

  // genResetToQuick 只有「快速生成」这一个入口能解动图单向门：
  //   它本来就是清空画板重开，语义上等于换了一个快速模式的新会话。
  function genResetToQuick() {
    const prev = S.genMode;
    S.genLocked = false;
    S.genMode = 'quick';
    if (S.settings) {
      S.settings.agent = S.settings.agent || {};
      S.settings.agent.genMode = 'quick';
      S.settings.agent.multiStep = false;
      persist();
    }
    if (prev === 'anim') animUnmount();
    syncGenSwitch();
  }

  function renderHints() {
    // 左栏说明文字已移除，只保留开关状态同步。
    syncGenSwitch();
  }

  // ── 动图模式：帧识别 / 播放 / 导出 GIF ────────────────────
  //
  // 动图模式仍然只有「一个图层」，动起来靠 SVG 自己表达，分三种来源：
  //   frames — 根下按 data-frame="0/1/2…" 分组的帧（提示词里的首选交付格式）；
  //   smil   — <animate>/<animateTransform>/<set> 等 SMIL 动画；
  //   static — 既没帧分组也没动画，仍然可以导出单帧 GIF。
  // 播放：frames 直接切换分组的 display，最省事也最稳；smil 用 SVG 自己的
  // unpauseAnimations()。导出 GIF：把每一帧序列化成独立 SVG → <img> → canvas
  // → GIFEncoder（gif.js，零依赖）。
  const ANIM_TAGS = 'animate, animateTransform, animateMotion, animateColor, set';

  function animRoot() {
    const host = $('svgHost');
    return host ? host.querySelector('svg') : null;
  }

  function animFpsFromRoot(root) {
    const v = parseFloat(root && root.getAttribute('data-fps'));
    return Number.isFinite(v) && v > 0 ? clamp(Math.round(v), 1, 30) : 8;
  }

  // animGroups 取根下的帧分组，按 data-frame 的数值排序。
  function animGroups(root) {
    if (!root) return [];
    return Array.from(root.querySelectorAll('[data-frame]')).sort((a, b) => {
      const x = parseFloat(a.getAttribute('data-frame')) || 0;
      const y = parseFloat(b.getAttribute('data-frame')) || 0;
      return x - y;
    });
  }

  // animAnalyze 重新判定当前画面的动图来源，并把帧数 / 帧率同步进状态。
  function animAnalyze() {
    const a = S.anim;
    const root = animRoot();
    if (!root) { a.frames = 0; a.source = ''; a.index = 0; return; }
    const groups = animGroups(root);
    if (groups.length > 1) {
      a.frames = groups.length;
      a.source = 'frames';
      a.index = clamp(a.index, 0, a.frames - 1);
      if (!a.userFps) a.fps = animFpsFromRoot(root);
      return;
    }
    a.frames = 0;
    a.index = 0;
    a.source = root.querySelector(ANIM_TAGS) ? 'smil' : 'static';
    if (!a.userFps) a.fps = 8;
  }

  // animShowFrame 只显示第 idx 帧（其余分组 display:none），保留嵌套与祖先变换。
  function animShowFrame(root, idx) {
    const groups = animGroups(root);
    groups.forEach((g, i) => {
      if (i === idx) g.style.removeProperty('display');
      else g.style.display = 'none';
    });
  }

  function animShowAll(root) {
    animGroups(root).forEach((g) => g.style.removeProperty('display'));
  }

  function animStop() {
    const a = S.anim;
    if (a.timer) { clearInterval(a.timer); a.timer = 0; }
    // SMIL 走浏览器时间轴：暂停必须显式调用 pauseAnimations，
    // 否则「暂停」只是改了按钮图标，画面照转。
    if (a.source === 'smil') {
      const root = animRoot();
      if (root && typeof root.pauseAnimations === 'function') root.pauseAnimations();
    }
    a.playing = false;
  }

  function animPos() {
    const a = S.anim;
    if (a.source === 'frames') return `${a.index + 1} / ${a.frames}`;
    if (a.source === 'smil') return 'SMIL 动画';
    if (a.source === 'static') return '静态（单帧）';
    return '—';
  }

  // syncAnimBar 把动图状态画到工具条上；非动图模式直接隐藏。
  function syncAnimBar() {
    const bar = $('animBar');
    if (!bar) return;
    const on = S.genMode === 'anim';
    bar.classList.toggle('hidden', !on);
    if (!on) { animStop(); return; }
    animAnalyze();
    const a = S.anim;
    const fps = $('animFps');
    if (fps && String(fps.value) !== String(a.fps)) fps.value = String(a.fps);
    const pos = $('animPos');
    if (pos) pos.textContent = animPos();
    const play = $('btnAnimPlay');
    if (play) {
      const canPlay = a.source === 'frames' || a.source === 'smil';
      play.textContent = a.playing ? '❚❚' : '▶';
      play.disabled = !canPlay;
      play.title = canPlay ? '播放 / 暂停' : '当前画面不是动图';
    }
    const gif = $('btnGif');
    if (gif) {
      gif.disabled = a.busy || !S.svg;
      gif.textContent = a.busy ? '导出中…' : '导出 GIF';
      gif.title = S.svg ? '把当前动图导出为 GIF' : '还没有可导出的画面';
    }
  }

  function animPlay() {
    const a = S.anim;
    const root = animRoot();
    if (!root) return;
    if (a.source === 'frames') {
      animShowFrame(root, a.index);
      a.timer = setInterval(() => {
        const r = animRoot();
        if (!r) { animStop(); return; }
        a.index = (a.index + 1) % Math.max(1, a.frames);
        animShowFrame(r, a.index);
        const pos = $('animPos');
        if (pos) pos.textContent = animPos();
      }, Math.round(1000 / clamp(a.fps, 1, 30)));
      a.playing = true;
    } else if (a.source === 'smil') {
      if (typeof root.unpauseAnimations === 'function') root.unpauseAnimations();
      a.playing = true;
    }
    syncAnimBar();
  }

  function animToggle() {
    if (S.anim.playing) { animStop(); syncAnimBar(); return; }
    animPlay();
  }

  // animMount 进入动图模式：显示工具条、从第 0 帧开始播。
  function animMount() {
    const a = S.anim;
    a.index = 0;
    a.userFps = false;
    syncAnimBar();
    const root = animRoot();
    if (root && a.source === 'frames') animShowFrame(root, 0);
    if (a.source === 'frames' || a.source === 'smil') animPlay();
  }

  // animUnmount 离开动图模式：停表、把被藏起来的帧放回去。
  function animUnmount() {
    animStop();
    const root = animRoot();
    if (root) animShowAll(root);
  }

  // animOnSVG 画布换了新图时调用：重新判定来源，动图模式下自动接着播。
  function animOnSVG() {
    if (S.genMode !== 'anim') return;
    const a = S.anim;
    animStop();
    a.index = 0;
    a.userFps = false;
    syncAnimBar();
    const root = animRoot();
    if (root && a.source === 'frames') animShowFrame(root, 0);
    if (a.source === 'frames' || a.source === 'smil') animPlay();
  }

  // ── GIF 导出 ────────────────────────────────────────────
  //
  // 关键点：SVG 元素不能直接 drawImage，必须先序列化成 Blob 交给 <img> 解码。
  // 所以每一帧都构造一份「只有这一帧可见」的 SVG 文本，再光栅化取像素。

  function parseDur(v) {
    if (!v) return 0;
    const s = String(v).trim();
    if (!s || /indefinite/i.test(s)) return 0;
    const m = /^([\d.]+)\s*(ms|s|min|h)?$/i.exec(s);
    if (!m) return 0;
    const n = parseFloat(m[1]);
    if (!Number.isFinite(n)) return 0;
    const unit = (m[2] || 's').toLowerCase();
    if (unit === 'ms') return n / 1000;
    if (unit === 'min') return n * 60;
    if (unit === 'h') return n * 3600;
    return n;
  }

  // smilDuration 取所有 SMIL 动画里最长的 dur，兜底 2 秒。
  function smilDuration(root) {
    let max = 0;
    root.querySelectorAll(ANIM_TAGS).forEach((n) => {
      const d = parseDur(n.getAttribute('dur'));
      if (d > max) max = d;
    });
    return max > 0 ? Math.min(max, 10) : 2;
  }

  // animTargetSize GIF 的输出尺寸：长边压到 400（帧多时 320），控制体积。
  function animTargetSize() {
    const st = S.stats || {};
    let w = st.w || (S.canvas && S.canvas.width) || 512;
    let h = st.h || (S.canvas && S.canvas.height) || 512;
    const cap = (S.anim.frames > 16 ? 320 : 400);
    const s = Math.min(1, cap / Math.max(w, h));
    return { w: Math.max(1, Math.round(w * s)), h: Math.max(1, Math.round(h * s)) };
  }

  // svgTextForFrame 构造只含第 idx 帧的 SVG 文本（idx < 0 表示整幅原样）。
  function svgTextForFrame(svgText, idx, w, h) {
    const doc = new DOMParser().parseFromString(svgText, 'image/svg+xml');
    if (doc.querySelector('parsererror')) return svgText;
    const root = doc.documentElement;
    const groups = Array.from(root.querySelectorAll('[data-frame]'));
    if (idx >= 0 && groups.length > 1) {
      groups.forEach((g, i) => {
        if (i === idx) g.removeAttribute('display');
        else g.setAttribute('display', 'none');
      });
    }
    root.setAttribute('width', String(w));
    root.setAttribute('height', String(h));
    if (!root.getAttribute('xmlns')) root.setAttribute('xmlns', 'http://www.w3.org/2000/svg');
    if (!root.getAttribute('xmlns:xlink')) root.setAttribute('xmlns:xlink', 'http://www.w3.org/1999/xlink');
    return new XMLSerializer().serializeToString(root);
  }

  // svgTextAtTime SMIL 采样：把所有动画的 begin 前置一个负偏移，
  // 让它在 t=0 时就已经播了 t 秒——<img> 里没法 setCurrentTime，只能这样定格。
  function svgTextAtTime(svgText, t, w, h) {
    const doc = new DOMParser().parseFromString(svgText, 'image/svg+xml');
    if (doc.querySelector('parsererror')) return svgText;
    const root = doc.documentElement;
    root.querySelectorAll(ANIM_TAGS).forEach((n) => {
      const begin = (n.getAttribute('begin') || '').trim();
      // 只挪时间型 begin；click / indefinite 之类保持原样，挪了会坏。
      if (begin && !/^\s*-?[\d.]+(ms|s|min|h)?\s*$/i.test(begin.split(';')[0])) return;
      n.setAttribute('begin', `-${t}s` + (begin ? ';' + begin : ''));
    });
    root.setAttribute('width', String(w));
    root.setAttribute('height', String(h));
    if (!root.getAttribute('xmlns')) root.setAttribute('xmlns', 'http://www.w3.org/2000/svg');
    if (!root.getAttribute('xmlns:xlink')) root.setAttribute('xmlns:xlink', 'http://www.w3.org/1999/xlink');
    return new XMLSerializer().serializeToString(root);
  }

  // rasterizeSVG 把一段 SVG 文本画进 canvas，返回 ImageData。
  function rasterizeSVG(svgText, w, h) {
    return new Promise((resolve, reject) => {
      const blob = new Blob([svgText], { type: 'image/svg+xml;charset=utf-8' });
      const url = URL.createObjectURL(blob);
      const img = new Image();
      img.decoding = 'sync';
      img.onload = () => {
        URL.revokeObjectURL(url);
        try {
          const cv = document.createElement('canvas');
          cv.width = w;
          cv.height = h;
          const ctx = cv.getContext('2d', { willReadFrequently: true });
          ctx.clearRect(0, 0, w, h);
          ctx.drawImage(img, 0, 0, w, h);
          resolve(ctx.getImageData(0, 0, w, h));
        } catch (e) { reject(e); }
      };
      img.onerror = () => { URL.revokeObjectURL(url); reject(new Error('这一帧渲染失败')); };
      img.src = url;
    });
  }

  function downloadBlob(blob, name) {
    const url = URL.createObjectURL(blob);
    const a = el('a');
    a.href = url;
    a.download = name;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 4000);
  }

  // exportGIF 按当前来源逐帧光栅化，编码成 GIF89a 下载。
  async function exportGIF() {
    const a = S.anim;
    if (a.busy) return;
    if (!S.svg) { toast('画布还是空的', true); return; }
    if (typeof window.GIFEncoder !== 'function') { toast('GIF 编码器没有加载，无法导出', true); return; }
    a.busy = true;
    const wasPlaying = a.playing;
    animStop();
    syncAnimBar();
    try {
      animAnalyze();
      const size = animTargetSize();
      const fps = clamp(a.fps, 1, 30);
      const delayCs = Math.max(2, Math.round(100 / fps));
      const enc = new window.GIFEncoder(size.w, size.h, { loop: 0 });
      let made = 0;

      if (a.source === 'frames' && a.frames > 1) {
        for (let i = 0; i < a.frames; i++) {
          const pos = $('animPos');
          if (pos) pos.textContent = `导出中 ${i + 1} / ${a.frames}`;
          const text = svgTextForFrame(S.svg, i, size.w, size.h);
          const data = await rasterizeSVG(text, size.w, size.h);
          if (enc.addFrame(data.data, delayCs)) made++;
        }
      } else if (a.source === 'smil') {
        const root = animRoot();
        const dur = root ? smilDuration(root) : 2;
        const total = clamp(Math.round(dur * fps), 2, 48);
        for (let i = 0; i < total; i++) {
          const pos = $('animPos');
          if (pos) pos.textContent = `导出中 ${i + 1} / ${total}`;
          const t = (i / total) * dur;
          const text = svgTextAtTime(S.svg, t, size.w, size.h);
          const data = await rasterizeSVG(text, size.w, size.h);
          if (enc.addFrame(data.data, delayCs)) made++;
        }
      } else {
        const pos = $('animPos');
        if (pos) pos.textContent = '导出中 1 / 1';
        const text = svgTextForFrame(S.svg, -1, size.w, size.h);
        const data = await rasterizeSVG(text, size.w, size.h);
        if (enc.addFrame(data.data, 100)) made++;
      }

      if (!made) { toast('没有可导出的帧', true); return; }
      const blob = enc.finish();
      if (!blob) { toast('GIF 编码失败', true); return; }
      const name = `svg-${stamp()}.gif`;
      downloadBlob(blob, name);
      addLog('LOG', `已导出 ${name} · ${made} 帧 · ${fmtNum(blob.size)} B`);
      toast(`GIF 已导出（${made} 帧，${fmtNum(blob.size)} B）`);
    } catch (e) {
      addLog('ERR', '导出 GIF 失败：' + e.message);
      toast('导出失败：' + e.message, true);
    } finally {
      a.busy = false;
      syncAnimBar();
      if (wasPlaying) animPlay();
    }
  }

  // 动图工具条的三个控件
  $('btnAnimPlay')?.addEventListener('click', animToggle);
  $('animFps')?.addEventListener('change', () => {
    const v = parseInt($('animFps').value, 10);
    S.anim.fps = Number.isFinite(v) ? clamp(v, 1, 30) : 8;
    S.anim.userFps = true;
    $('animFps').value = String(S.anim.fps);
    if (S.anim.playing) { animStop(); animPlay(); }
    syncAnimBar();
  });
  $('btnGif')?.addEventListener('click', () => { exportGIF(); });

  // 应用工程快照到界面（图层树 + 画布）
  function applyProject(pj, opts) {
    if (!pj) return;
    if (!pj.open) {
      S.project = pj;
      renderLayers();
      renderHints();
      updateProjectBadge();
      return;
    }
    const prevActive = S.project && S.project.activeLayer;
    S.project = pj;
    if (!S.pickedLayer || !pj.layers.some((l) => l.id === S.pickedLayer)) {
      S.pickedLayer = pj.activeLayer || (pj.layers[0] && pj.layers[0].id) || '';
    }
    if (pj.canvas && pj.canvas.width) {
      S.canvas = pj.canvas;
      if (S.settings) S.settings.canvas = pj.canvas;
      syncCanvasSelect();
    }
    if (!(opts && opts.keepCanvas)) setSVG(pj.svg || '', 'project');
    renderLayers();
    renderHints();
    updateProjectBadge();
    if (prevActive !== pj.activeLayer) resetView();
  }

  // 顶部「工程」按钮文案：名称 + 模式
  function updateProjectBadge() {
    updateSaveTitle();
    const b = $('btnProject');
    if (!b) return;
    const pj = S.project;
    if (!pj || !pj.open) {
      b.textContent = '工程：未打开';
      b.title = '点击选择快速模式或打开工程文件夹';
      return;
    }
    const tag = pj.mode === 'quick' ? '快速' : '工程';
    b.textContent = `工程：${pj.name || '未命名'}（${tag}）`;
    b.title = pj.dir || '';
  }

  // 启动流程：快速模式直接进临时工程；工程模式自动打开上次的工程，没有则弹窗。
  async function bootProject() {
    // 每次启动都先弹窗让用户选：快速生成 / 新建项目 / 打开项目。
    await loadProjectMeta();
    renderHints();
    renderLayers();
    openProjectModal({ boot: true });
  }

  // ── 图层树 ──────────────────────────────────────────────
  const layerState = { busy: false };
  function renderLayers() {
    const box = $('layerList');
    if (!box) return;
    box.innerHTML = '';
    const pj = S.project;
    const on = !!(S.settings && S.settings.layers && S.settings.layers.enabled);
    $('layerBlock').classList.toggle('hidden', !on);
    if (!pj || !pj.open) {
      box.appendChild(el('div', 'flow-empty', '（尚未打开工程）'));
      $('layerMeta').textContent = '—';
      $('layerActive').textContent = '当前：—';
      return;
    }
    const list = (pj.layers || []).slice().sort((a, b) => b.index - a.index); // 顶层在上
    if (!list.length) box.appendChild(el('div', 'flow-empty', '（没有图层）'));
    $('layerMeta').textContent = `${pj.layers.length} 层`;
    const activeName = (pj.layers.find((l) => l.id === pj.activeLayer) || {}).name || '—';
    $('layerActive').textContent = `当前：${activeName}`;

    list.forEach((l) => {
      const row = el('div', 'layer-row' + (l.id === pj.activeLayer ? ' active' : '') + (l.visible ? '' : ' hidden-layer'));
      row.dataset.id = l.id;

      // 眼睛：可见度
      const eye = el('span', 'eye' + (l.visible ? '' : ' off'), l.visible ? '◉' : '○');
      eye.title = l.visible ? '隐藏图层' : '显示图层';
      eye.addEventListener('click', (ev) => {
        ev.stopPropagation();
        layerOp('visible', { id: l.id, visible: !l.visible });
      });

      // 锁：锁定后模型不能改这一层
      const lock = el('span', 'lock' + (l.locked ? ' on' : ''), l.locked ? '🔒' : '🔓');
      lock.title = l.locked ? '解锁（解锁后模型才能改这一层）' : '锁定（禁止模型修改这一层）';
      lock.addEventListener('click', (ev) => {
        ev.stopPropagation();
        layerOp('locked', { id: l.id, locked: !l.locked });
      });

      const nm = el('span', 'nm', l.name || l.id);
      nm.title = `${l.file || ''} · ${l.elements || 0} 元素 · ${l.bytes || 0} 字节（双击改名）`;
      nm.addEventListener('dblclick', (ev) => {
        ev.stopPropagation();
        startRenameLayer(row, l);
      });

      const blend = el('span', 'blend', l.blend && l.blend !== 'normal' ? l.blend : '');
      const idx = el('span', 'idx', String(l.index));

      row.append(eye, lock, nm, blend, idx);
      row.addEventListener('click', () => {
        S.pickedLayer = l.id;
        if (l.id !== pj.activeLayer) layerOp('active', { id: l.id });
      });
      box.appendChild(row);
    });
  }

  // 双击改名
  function startRenameLayer(row, l) {
    const nm = row.querySelector('.nm');
    if (!nm || nm.querySelector('input')) return;
    const input = el('input');
    input.value = l.name || '';
    nm.textContent = '';
    nm.appendChild(input);
    input.focus();
    input.select();
    let done = false;
    const commit = (okFlag) => {
      if (done) return;
      done = true;
      const v = input.value.trim();
      if (okFlag && v && v !== l.name) layerOp('rename', { id: l.id, name: v });
      else renderLayers();
    };
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') { e.preventDefault(); commit(true); }
      else if (e.key === 'Escape') { e.preventDefault(); commit(false); }
    });
    input.addEventListener('blur', () => commit(true));
  }

  // 图层操作统一入口
  async function layerOp(op, payload) {
    if (layerState.busy) return;
    layerState.busy = true;
    try {
      const r = await api('/api/layers/ops', { method: 'POST', body: Object.assign({ op }, payload) });
      if (r && r.project) applyProject(r.project);
    } catch (e) {
      toast('图层操作失败：' + e.message, true);
    } finally {
      layerState.busy = false;
    }
  }

  // 当前选中图层
  function pickedLayer() {
    if (!S.project || !S.project.open) return null;
    const id = S.pickedLayer || S.project.activeLayer;
    return (S.project.layers || []).find((l) => l.id === id) || null;
  }

  // 刷新工程快照（模型改过图层后调用）
  async function refreshProject() {
    const seq = S.runSeq;
    try {
      const r = await api('/api/layers');
      if (seq !== S.runSeq) return;
      applyProject(r, { keepCanvas: true });
    } catch { /* 工程可能尚未打开 */ }
  }

  // 图层按钮
  $('btnAddLayer').addEventListener('click', async () => {
    if (!S.project || !S.project.open) { toast('请先打开工程', true); return; }
    await layerOp('add', { name: '新图层' });
    addLog('LOG', '新建图层');
  });
  $('btnLayerDel').addEventListener('click', () => {
    const l = pickedLayer();
    if (!l) { toast('没有选中图层', true); return; }
    confirmBox({
      title: '删除图层',
      text: `确定删除「${l.name}」？该图层的文件会一并删除。`,
      ok: '删除',
      onOk: async () => { await layerOp('delete', { id: l.id }); addLog('LOG', `删除图层：${l.name}`); },
    });
  });
  $('btnLayerUp').addEventListener('click', async () => {
    const l = pickedLayer();
    if (!l) return;
    await layerOp('index', { id: l.id, index: l.index + 1 });
  });
  $('btnLayerDown').addEventListener('click', async () => {
    const l = pickedLayer();
    if (!l) return;
    await layerOp('index', { id: l.id, index: Math.max(1, l.index - 1) });
  });

  // ── 生成方式开关：点击 / 拖动 / 键盘 ─────────────────────
  //
  // 拖动：pointerdown 抓住轨道，thumb 实时跟随指针，松手吸附到最近一档；
  // 若目标档不被允许，thumb 弹回原位并提示原因。
  // endAt 记录本次拖动结束时刻：click 抑制只在紧随其后的那一次点击生效，
  // 否则指针捕获/pointercancel 之类没派发 click 的情况下，moved 会一直为 true
  // 把用户下一次真实点击吞掉。
  const genDrag = { active: false, startX: 0, moved: false, pointerId: 0, width: 0, endAt: 0 };

  function genOpts() {
    const track = $('genTrack');
    return track ? Array.from(track.querySelectorAll('.gen-opt')) : [];
  }

  // genIndexFromX 把指针位置换算成最近的档位下标。
  function genIndexFromX(clientX) {
    const track = $('genTrack');
    if (!track) return 0;
    const r = track.getBoundingClientRect();
    const inner = Math.max(1, r.width - 4); // 减掉左右 padding
    const ratio = clamp((clientX - r.left - 2) / inner, 0, 0.9999);
    return Math.floor(ratio * GEN_MODES.length);
  }

  function genThumbToIndex(idx, animate) {
    const thumb = $('genThumb');
    if (!thumb) return;
    const track = $('genTrack');
    if (track) track.classList.toggle('dragging', !animate);
    thumb.style.transform = `translateX(${idx * 100}%)`;
  }

  $('genTrack')?.addEventListener('pointerdown', (ev) => {
    if (ev.button !== 0) return;
    const track = $('genTrack');
    if (!track) return;
    // 锁定到不可切换时，直接给出提示，不进入拖动
    if (!genSwitchAllowed()) {
      toast(genBlockReason(), true);
      return;
    }
    genDrag.active = true;
    genDrag.moved = false;
    genDrag.startX = ev.clientX;
    genDrag.pointerId = ev.pointerId;
    genDrag.width = track.getBoundingClientRect().width;
    try { track.setPointerCapture(ev.pointerId); } catch (_) { /* 老浏览器忽略 */ }
    track.classList.add('dragging');
    ev.preventDefault();
  });

  $('genTrack')?.addEventListener('pointermove', (ev) => {
    if (!genDrag.active || ev.pointerId !== genDrag.pointerId) return;
    if (Math.abs(ev.clientX - genDrag.startX) > 2) genDrag.moved = true;
    const track = $('genTrack');
    const thumb = $('genThumb');
    if (!track || !thumb) return;
    // thumb 宽度 = 轨道内宽 / 档位数；让 thumb 中心跟着指针走。
    // thumb 中心位置 = p * w + w/2，指针位置 = r * 3w  ⇒  p = 3r - 0.5
    const r = track.getBoundingClientRect();
    const inner = Math.max(1, r.width - 4);
    const ratio = clamp((ev.clientX - r.left - 2) / inner, 0, 1);
    const pos = clamp(ratio * GEN_MODES.length - 0.5, 0, GEN_MODES.length - 1);
    thumb.style.transform = `translateX(${pos * 100}%)`;
  });

  function genDragEnd(ev) {
    if (!genDrag.active) return;
    const track = $('genTrack');
    genDrag.active = false;
    genDrag.endAt = Date.now();
    // 真实浏览器会在 pointerup 后立刻派发 click，那一次要抑制；
    // 但 pointercancel / 指针移出元素等情况不会派发 click，
    // 所以这里再排一个 0ms 定时器兜底清掉 moved，避免吞掉用户的下一次点击。
    setTimeout(() => { genDrag.moved = false; }, 0);
    if (track) {
      track.classList.remove('dragging');
      try { track.releasePointerCapture(genDrag.pointerId); } catch (_) { /* 忽略 */ }
    }
    if (ev && ev.pointerId !== undefined && ev.pointerId !== genDrag.pointerId) return;
    const moved = genDrag.moved;
    const idx = genIndexFromX(ev ? ev.clientX : genDrag.startX);
    if (!moved) {
      // 没拖动 → 当作点击：用指针落点换算档位
      setGenMode(GEN_MODES[idx]);
      return;
    }
    if (!setGenMode(GEN_MODES[idx])) {
      // 被拒绝：thumb 弹回当前档
      genThumbToIndex(GEN_MODES.indexOf(S.genMode), true);
    }
  }

  $('genTrack')?.addEventListener('pointerup', genDragEnd);
  $('genTrack')?.addEventListener('pointercancel', genDragEnd);

  // 点击档位按钮（指针事件已处理，这里兜住键盘与辅助技术的 click）
  genOpts().forEach((btn) => {
    btn.addEventListener('click', (ev) => {
      // 拖动结束后的那一次 click 不重复处理（moved 由 genDragEnd 的定时器兜底清空）。
      if (genDrag.moved && Date.now() - genDrag.endAt < 500) return;
      ev.preventDefault();
      setGenMode(btn.dataset.mode);
    });
    btn.addEventListener('keydown', (ev) => {
      const i = GEN_MODES.indexOf(btn.dataset.mode);
      let next = -1;
      if (ev.key === 'ArrowLeft' || ev.key === 'ArrowUp') next = Math.max(0, i - 1);
      else if (ev.key === 'ArrowRight' || ev.key === 'ArrowDown') next = Math.min(GEN_MODES.length - 1, i + 1);
      else if (ev.key === 'Home') next = 0;
      else if (ev.key === 'End') next = GEN_MODES.length - 1;
      if (next < 0 || next === i) return;
      ev.preventDefault();
      setGenMode(GEN_MODES[next]);
      const opts = genOpts();
      if (opts[next]) opts[next].focus();
    });
  });

  // ── 工程弹窗：三个方框（快速生成 / 新建项目 / 打开项目） ───
  // 打开、新建都直接弹系统文件夹选择器，没有输入框、没有默认路径。
  function openProjectModal(opts) {
    if (!S.settings) return;
    const boot = !!(opts && opts.boot);
    bootPickOpen = boot;
    const m = $('modalProject');
    if (m) m.dataset.noClose = boot ? '1' : ''; // 遮罩点击不关，必须先选一项
    const title = $('projTitle');
    if (title) title.textContent = boot ? '选择打开方式' : '工程';
    const closeBtn = $('projClose');
    if (closeBtn) closeBtn.classList.toggle('hidden', boot);
    $('projPath').textContent = (S.project && S.project.dir) ? S.project.dir : '—';
    openModal('modalProject');
  }

  // 系统文件夹选择器：内置窗口走宿主对话框，浏览器调试模式回落 <input webkitdirectory>。
  function pickFolder(title) {
    return new Promise((resolve) => {
      api('/api/project/browse', { method: 'POST', body: { title } })
        .then((r) => {
          if (r && !r.fallback) { resolve(r.path || ''); return; }
          resolve(fallbackPickFolder());
        })
        .catch(() => resolve(fallbackPickFolder()));
    });
  }
  function fallbackPickFolder() {
    return new Promise((resolve) => {
      const input = $('dirPicker');
      let done = false;
      const finish = (v) => {
        if (done) return;
        done = true;
        input.value = '';
        resolve(v || '');
      };
      input.onchange = () => {
        const f = input.files && input.files[0];
        const rel = f && f.webkitRelativePath ? f.webkitRelativePath.split('/')[0] : '';
        finish(rel);
      };
      input.oncancel = () => finish('');
      input.click();
    });
  }

  async function projectAction(path, body, note) {
    try {
      const r = await api(path, { method: 'POST', body });
      if (r && r.project) applyProject(r.project);
      addLog('LOG', note || '工程已更新');
      bootPickOpen = false;
      closeModal('modalProject', true);
      closeModal('modalNewProj');
      await loadProjectMeta();
      // 换了工程就解锁生成方式：新工程的会话是新的，切换资格也该重置。
      // 「快速生成」额外解动图单向门（清空重来 = 新的快速会话）；其余入口不解。
      if (body && body.mode === 'quick') genResetToQuick();
      else genUnlock();
      return r;
    } catch (e) {
      toast('操作失败：' + e.message, true);
      return null;
    }
  }
  async function loadProjectMeta() {
    try {
      const r = await api('/api/project');
      S.projectMeta = r;
      if (r.project) applyProject(r.project, { keepCanvas: true });
      return r;
    } catch { return null; }
  }

  $('btnProject').addEventListener('click', () => openProjectModal());
  $('projClose').addEventListener('click', () => closeModal('modalProject'));

  // 方框一：快速生成 → 清掉临时工程，空画板直接开工
  // 工程本来就是空的（无图层内容、无对话历史）时不必多问一句，直接进。
  $('btnQuickNew').addEventListener('click', () => {
    const pj = S.project;
    const blank = !pj || !pj.open || pj.empty === true;
    const go = async () => {
      const r = await projectAction('/api/project/open', { mode: 'quick', fresh: true }, '已进入快速生成');
      if (r) {
        S.session = '';
        resetWorkspaceView();
      }
    };
    if (blank) {
      bootPickOpen = false;
      closeModal('modalProject', true);
      go();
      return;
    }
    confirmBox({
      title: '快速生成',
      text: '将清空临时工程的图层与上下文，从空画板重新开始。确定继续？',
      ok: '开始',
      onOk: go,
    });
  });

  // 方框二：新建项目 → 只填名字，确认后弹文件夹选择器选位置
  $('btnProjNew').addEventListener('click', () => {
    $('projNameInput').value = '';
    openModal('modalNewProj');
    setTimeout(() => $('projNameInput').focus(), 30);
  });
  $('projNameInput').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); $('btnProjCreate').click(); }
  });
  $('btnProjCreate').addEventListener('click', async () => {
    const name = $('projNameInput').value.trim();
    if (!name) {
      toast('请填写项目名', true);
      $('projNameInput').focus();
      return;
    }
    const dir = await pickFolder('选择「' + name + '」的存放位置');
    if (!dir) return; // 取消选择就留在原地
    projectAction('/api/project/create', { name, dir }, '已新建项目 ' + name);
  });

  // 方框三：打开项目 → 直接弹文件夹选择器
  $('btnProjOpen').addEventListener('click', async () => {
    const dir = await pickFolder('选择项目文件夹');
    if (!dir) return;
    projectAction('/api/project/open', { mode: 'project', dir }, '已打开项目 ' + dir);
  });

  // 清空画板视图（不动工程文件）
  function resetWorkspaceView() {
    S.rawText = '';
    S.thinkText = '';
    S.errText = '';
    S.flow = [];
    S.svg = '';
    S.folds.clear();
    S.codeLines = [];
    S.lastReport = null;
    animStop();
    S.anim.frames = 0;
    S.anim.source = '';
    S.anim.index = 0;
    S.anim.userFps = false;
    renderFlow();
    renderCode();
    const host = $('svgHost');
    host.innerHTML = '';
    host.appendChild(el('div', 'empty', '尚未生成 SVG'));
    ['statViewBox', 'statSize', 'statElements', 'statBytes'].forEach((id) => { $(id).textContent = '—'; });
    S.stats = { viewBox: '—', size: '—', elements: 0, bytes: 0, w: 0, h: 0 };
    $('issues').innerHTML = '';
    $('issues').appendChild(el('div', 'issue info', '等待生成…'));
    resetView();
  }

  // ── 开始 / 停止（单按钮切换） ───────────────────────────
  function setRunningUI(on) {
    $('fastMode').disabled = !!on || !fastModeAvailable();
    const b = $('btnRun');
    if (!b) return;
    b.classList.toggle('running', !!on);
    b.classList.toggle('primary', !on);
    b.classList.toggle('danger', !!on);
    if (on) {
      b.textContent = '停止生成';
      b.title = '点击中止当前生成（Esc）';
    } else if (hasSession()) {
      b.textContent = '发送';
      b.title = '发送消息，继续当前对话（Ctrl+Enter）';
    } else {
      b.textContent = '开始生成';
      b.title = '开始生成（Ctrl+Enter）';
    }
    b.disabled = S.clearing;
    // 停止阶段：按钮进入「正在停止」，任何点击都直接再打一次 abort。
    b.classList.toggle('stopping', !!on && S.stopping);
    if (on && S.stopping) {
      b.textContent = '正在停止…';
      b.title = '已发出中止请求，正在断开流';
    }
  }
  // 立即切到「已停止」：不等后端回执，先把 UI 与本地状态收干净。
  // 后端的 abort 是 context.Cancel，流会随即结束；剩下的 SSE 帧全部丢弃。
  function haltRunLocal() {
    S.running = false;
    S.stopping = false;
    S.runSeq++;
    if (S.streamAbort) {
      S.streamAbort.abort();
      S.streamAbort = null;
    }
    if (S.streamReader) {
      try { S.streamReader.cancel().catch(() => {}); } catch { /* 忽略 */ }
      S.streamReader = null;
    }
    setRunningUI(false);
    updateBadge();
  }
  async function startRun() {
    if (S.running || S.clearing) return;
    const pendingSeq = S.runSeq;
    if (!S.settings) { toast('设置尚未加载', true); return; }
    syncInputsToSettings();
    const prompt = $('prompt').value.trim();
    if (!prompt) { $('prompt').focus(); toast('请先填写提示词', true); return; }
    const p = activeProvider();
    if (!p) { toast('没有可用供应商', true); return; }
    const model = (S.settings.activeModel || '').trim();
    if (!model) { toast('请先在右上角选择模型', true); return; }
    if (!p.apiKey && p.authMode !== 'chatgpt') { toast('该供应商还没有填写 API Key', true); return; }
    if (!S.canvas) { toast('请选择比例预设', true); return; }
    if (!S.project || !S.project.open) {
      toast('请先选择打开方式（快速生成 / 新建项目 / 打开项目）', true);
      openProjectModal();
      return;
    }

    try {
      await flushPersist();
    } catch (e) {
      toast('设置保存失败：' + e.message, true);
      return;
    }
    if (S.running || S.clearing || pendingSeq !== S.runSeq) return;

    S.running = true;
    const controller = new AbortController();
    S.streamAbort = controller;
    S.runID = 'r' + rand();
    S.stopping = false;
    const seq = ++S.runSeq;
    // 一旦开始生成就锁定生成方式：当前若停在快速模式，还留最后一次切换机会。
    genLock();
    // 快速模式：工程目录是临时的，开新会话前丢掉上一轮上下文。
    // 工程模式：沿用工程的会话 id（历史随工程持久化）。
    if (S.project.mode === 'quick' && !S.session) {
      S.session = 's' + rand();
    } else if (!S.session) {
      S.session = 's' + rand();
    }
    S.rawText = '';
    S.thinkText = '';
    S.errText = '';
    S.flow = [];
    previewSVG = '';
    S.livePreview = false;
    renderFlow();
    setRunningUI(true);
    updateBadge();
    addLog('LOG', `开始生成 · ${p.name} / ${model} · ${S.canvas.name}`);
    if (S.settings.fastMode && fastModeAvailable()) {
      $('fastModeStatus').textContent = '已请求 Fast，等待服务端确认…';
      addLog('LOG', '已请求 Fast 加速；可能增加额度或费用消耗');
    }
    if (S.images.length) addLog('LOG', `附带 ${S.images.length} 张图片`);

    const body = {
      prompt,
      images: S.images.map((i) => ({ mime: i.mime, data: i.data })),
      canvas: S.canvas,
      model,
      providerId: p.id,
      thinking: selValue('thinkingSel') || 'off',
      fastMode: !!S.settings.fastMode && fastModeAvailable(),
      session: S.session,
      runId: S.runID,
      layer: S.pickedLayer || '',
      genMode: S.genMode,
      multiStep: S.genMode === 'layers',
    };

    try {
      const res = await fetch('/api/chat', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-App-Token': TOKEN },
        body: JSON.stringify(body),
        signal: controller.signal,
      });
      if (!res.ok) {
        const ct = res.headers.get('content-type') || '';
        let msg = `HTTP ${res.status}`;
        try {
          const d = ct.includes('json') ? await res.json() : null;
          if (d && d.error) msg = d.error;
        } catch { /* ignore */ }
        throw new Error(msg);
      }
      if (!res.body) throw new Error('当前浏览器不支持流式读取');

      const reader = res.body.getReader();
      if (seq !== S.runSeq) { await reader.cancel(); return; }
      S.streamReader = reader;
      const dec = new TextDecoder();
      let buf = '';
      let finished = false;
      // seq 变了说明用户点了停止（或又开了新一轮）：这个循环立刻作废，不再碰 UI。
      const stale = () => seq !== S.runSeq;
      while (true) {
        const { done, value } = await reader.read();
        if (done || stale()) break;
        buf += dec.decode(value, { stream: true });
        buf = buf.replace(/\r\n/g, '\n');
        let idx;
        while ((idx = buf.indexOf('\n\n')) >= 0) {
          const frame = buf.slice(0, idx);
          buf = buf.slice(idx + 2);
          if (stale()) break;
          handleFrame(frame);
        }
        if (stale()) break;
      }
      if (S.streamReader === reader) S.streamReader = null;
      // 用户主动停止：已由 haltRunLocal 收尾，这里不再报「连接意外结束」。
      if (stale()) return;
      if (buf.trim()) handleFrame(buf);
      if (!finished && S.running) {
        finishRun('ERROR', '连接意外结束');
        showRawTab('err');
      }
      function handleFrame(frame) {
        for (const line of frame.split('\n')) {
          if (!line || line.startsWith(':')) continue;
          if (!line.startsWith('data:')) continue;
          const payload = line.slice(5).trim();
          if (!payload) continue;
          let ev;
          try { ev = JSON.parse(payload); } catch { continue; }
          if (ev.kind === 'done' || ev.kind === 'error') finished = true;
          dispatchEvent(ev);
        }
      }
    } catch (e) {
      if (seq !== S.runSeq) return; // 已停止，错误无意义
      addLog('ERROR', '请求失败：' + e.message);
      finishRun('ERROR', '请求失败');
      showRawTab('err');
    } finally {
      if (S.streamAbort === controller) S.streamAbort = null;
    }
  }

  // 立即切断：先把前端收干净（按钮/流/状态），再异步通知后端。
  // 后端 Abort 是 context.Cancel，SSE 会随即断；前端不再等它。
  function stopRunNow() {
    if (!S.running) return;
    const session = S.session;
    const runId = S.runID;
    S.stopping = true;
    haltRunLocal();
    const stoppedSeq = S.runSeq;
    addLog('LOG', '已中止生成');
    toast('已中止生成');
    // 不 await：网络慢也不该卡住界面。
    api('/api/chat/abort', { method: 'POST', body: { session, runId } })
      .then((r) => { if (stoppedSeq === S.runSeq && (!r || r.ok === false)) addLog('LOG', '后端已无运行中的任务'); })
      .catch((e) => { if (stoppedSeq === S.runSeq) addLog('ERROR', '中止通知失败：' + e.message); });
  }

  // 停止是即时取消，保留已有作品。
  function stopRun() {
    if (!S.running) { setRunningUI(false); return; }
    stopRunNow();
  }

  $('btnRun').addEventListener('click', () => { if (S.running) stopRun(); else startRun(); });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      startRun();
    } else if (e.key === '`' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      setCodeView(!S.codeOpen);
    } else if (e.key === 'Escape') {
      if (MM.open) { closeModelMenu(); return; }
      if (anySelOpen()) { closeAllSels(''); return; }
      if (document.querySelector('.modal:not(.hidden)')) {
        document.querySelectorAll('.modal:not(.hidden)').forEach((m) => {
          // 退出询问弹窗按 ESC = 取消退出，必须把选择回执给 chooseExit。
          if (m.id === 'modalConfirm' && m.dataset.noClose === '1') {
            document.dispatchEvent(new CustomEvent('dsh-confirm-cancel'));
          }
          if (m.dataset.noClose !== '1') closeModal(m.id);
        });
        return;
      }
      if (S.running) { e.preventDefault(); stopRun(); }
    }
  });

  // ── 代码视图：把 SVG 排版成可折叠的行 ──────────────────
  // 折叠只按「同一行内的开合标签」判断：行内既有 < 又有 > 且不含成对闭合，视为可折叠起点。
  const VOID_TAGS = { br: 1, img: 1, hr: 1, input: 1, path: 1, circle: 1, rect: 1, line: 1, ellipse: 1, polygon: 1, polyline: 1, stop: 1, use: 1 };
  function formatSVG(svg) {
    const src = String(svg || '').replace(/\r\n/g, '\n');
    const lines = [];
    // 在标签边界切分：<tag ...>、</tag>、<tag/>、文本
    const tokens = src.split(/(<[^>]*>)/).filter((t) => t !== '');
    let depth = 0;
    const push = (text, delta, foldable) => {
      lines.push({ indent: depth, text, foldable: !!foldable, delta });
    };
    tokens.forEach((tok) => {
      const t = tok.trim();
      if (!t) return;
      if (t.startsWith('</')) {
        depth = Math.max(0, depth - 1);
        push(t, -1, false);
        return;
      }
      if (t.startsWith('<')) {
        const selfClose = /\/>$/.test(t);
        const m = t.match(/^<([a-zA-Z][\w:.-]*)/);
        const name = m ? m[1].toLowerCase() : '';
        // 同一 token 内已经闭合（如 <g></g>）就不增加深度
        const closedInline = new RegExp('</' + (name || 'x') + '>$').test(t);
        if (selfClose || VOID_TAGS[name] || closedInline) {
          push(t, 0, false);
        } else {
          push(t, 1, true);
          depth += 1;
        }
        return;
      }
      // 纯文本节点
      push(t, 0, false);
    });
    return lines;
  }
  function renderCode() {
    const box = $('codeView');
    if (!box) return;
    const raw = S.svg || '';
    const lines = raw ? formatSVG(raw) : [];
    S.codeLines = lines;
    box.innerHTML = '';
    box.classList.toggle('wrap', !!S.wrap);
    $('codeLines').textContent = lines.length ? String(lines.length) : '—';
    $('codeBytes').textContent = raw ? fmtNum(new Blob([raw]).size) + ' B' : '—';
    $('codeFolds').textContent = String(S.folds.size);
    if (!lines.length) {
      box.appendChild(el('div', 'code-empty muted', '尚未生成 SVG'));
      $('codeIssues').innerHTML = '';
      $('codeIssues').appendChild(el('div', 'issue info', '尚未生成 SVG'));
      return;
    }
    const frag = document.createDocumentFragment();
    lines.forEach((ln, i) => {
      const row = el('div', 'cline' + (S.folds.has(i) ? ' folded' : ''));
      row.dataset.i = String(i);
      const no = el('span', 'ln', String(i + 1));
      const fold = el('span', 'fold' + (ln.foldable ? '' : ' ghost'), ln.foldable ? '▾' : '');
      if (ln.foldable) {
        fold.title = S.folds.has(i) ? '展开' : '折叠';
        fold.addEventListener('click', () => {
          if (S.folds.has(i)) S.folds.delete(i); else S.folds.add(i);
          renderCode();
        });
      }
      const code = el('span', 'cd');
      code.textContent = ln.text;
      row.style.paddingLeft = (10 + ln.indent * 14) + 'px';
      row.appendChild(no);
      row.appendChild(fold);
      row.appendChild(code);
      frag.appendChild(row);
    });
    box.appendChild(frag);
    applyFolds();
    // 校验报告也复用到代码视图底部
    const rep = S.lastReport;
    if (rep) renderReport(rep, 'codeIssues');
  }
  function applyFolds() {
    const box = $('codeView');
    if (!box) return;
    const rows = Array.from(box.querySelectorAll('.cline'));
    const lines = S.codeLines;
    // 折叠：把某行之后的缩进更深的所有行隐藏
    rows.forEach((row) => {
      const i = Number(row.dataset.i);
      let hidden = false;
      S.folds.forEach((fi) => {
        if (fi >= i) return;
        if (lines[i] && lines[i].indent > (lines[fi] ? lines[fi].indent : 0)) hidden = true;
      });
      row.classList.toggle('folded', hidden);
    });
  }
  $('btnWrap').addEventListener('click', () => {
    S.wrap = !S.wrap;
    $('codeView').classList.toggle('wrap', S.wrap);
    $('btnWrap').classList.toggle('primary', S.wrap);
  });
  $('btnFoldAll').addEventListener('click', () => {
    S.codeLines.forEach((ln, i) => { if (ln.foldable) S.folds.add(i); });
    renderCode();
  });
  $('btnUnfoldAll').addEventListener('click', () => {
    S.folds.clear();
    renderCode();
  });
  $('btnCopyCode').addEventListener('click', async () => {
    if (!S.svg) { toast('画布还是空的', true); return; }
    try {
      await navigator.clipboard.writeText(S.svg);
      toast('SVG 代码已复制');
    } catch (e) {
      toast('复制失败：' + e.message, true);
    }
  });

  // ── 清理（确认后清空上下文与画板） ──────────────────────
  let confirmCleanup = null;
  function confirmBox(opts) {
    // 上一次确认框的监听器必须先撤掉：否则连点两次「快速生成」会叠两个 handler，
    // 两个 handler 各自 cleanup 后只关窗，真正干活的 onOk 一个都没跑。
    if (confirmCleanup) { confirmCleanup(); confirmCleanup = null; }
    $('confirmTitle').textContent = opts.title || '确认操作';
    $('confirmIcon').textContent = opts.icon || '!';
    $('confirmText').textContent = opts.text || '';
    const ok = $('confirmOk');
    ok.textContent = opts.ok || '确定';
    // noClose：必须做出选择（退出询问用），点遮罩/ESC 不关闭。
    const dialog = $('modalConfirm');
    dialog.dataset.noClose = opts.noClose ? '1' : '';
    openModal('modalConfirm');
    const cleanup = () => {
      confirmCleanup = null;
      ok.removeEventListener('click', handler);
      ok.removeEventListener('click', onCancel);
      document.removeEventListener('dsh-confirm-cancel', onCancel);
    };
    confirmCleanup = cleanup;
    const handler = async () => {
      cleanup();
      closeModal('modalConfirm');
      if (opts.onOk) await opts.onOk();
    };
    const onCancel = async () => {
      cleanup();
      closeModal('modalConfirm');
      if (opts.onCancel) await opts.onCancel();
    };
    ok.addEventListener('click', handler);
    // 取消按钮沿用 modalConfirm 里已有的 data-close 行为，这里补一层钩子。
    document.addEventListener('dsh-confirm-cancel', onCancel);
  }
  // ── 清空全部：停止任务、重置作品及对话、清空本地显示 ─────
  $('btnClear').addEventListener('click', () => {
    if (S.clearing) return;
    confirmBox({
      title: '清空全部',
      icon: '!',
      text: '将停止当前生成，并清空画布、全部图层内容、对话上下文、日志、原始输出和已加载图片。',
      ok: '清空全部',
      onOk: async () => {
        S.clearing = true;
        try {
          if (S.running) stopRunNow();
          haltRunLocal();
          const result = await api('/api/session/drop', { method: 'POST', body: { session: S.session || '' } });
          haltRunLocal();
          if (result && result.project) applyProject(result.project, { keepCanvas: true });
        } catch (e) {
          S.clearing = false;
          setRunningUI(S.running);
          toast('清空失败：' + e.message, true);
          return;
        }
        S.session = '';
        S.liveSession = '';
        S.runID = '';
        S.images = [];
        $('prompt').value = '';
        previewSVG = '';
        S.livePreview = false;
        resetWorkspaceView();
        genUnlock();
        updateUsage({});
        renderThumbs();
        $('bigLog').innerHTML = '';
        S.clearing = false;
        setRunningUI(false);
        updateBadge();
        toast('已清空全部内容与上下文');
      },
    });
  });

  // ── 退出询问（宿主关窗口前调 appClosing，这里问「另存为工程 / 直接丢弃」） ──
  // 返回 false = 用户取消退出，拦住这次关闭。
  window.dshSaveChoice = async function dshSaveChoice() {
    if (!(S.project && S.project.open && S.project.mode === 'quick')) return true;
    const choice = await chooseExit();
    if (choice === 'cancel') return false;
    if (choice === 'keep') {
      // 另存为工程：临时工程复制成正式工程，之后照常退出。
      await projectAction('/api/project/saveas', { dir: `data/projects/svg-${stamp()}` }, '已另存为工程');
    } else {
      await api('/api/project/close', { method: 'POST', body: { purgeQuick: true } }).catch(() => {});
    }
    return true;
  };

  // 三选一：另存 / 丢弃 / 取消。用 confirm 弹窗两次以避免新增 DOM。
  function chooseExit() {
    return new Promise((resolve) => {
      confirmBox({
        title: '保存这次的作品？',
        icon: '!',
        text: '当前是快速模式（临时文件夹）。选择「另存为工程」会把内容存成正式工程文件夹；点「取消」可返回继续编辑。',
        ok: '另存为工程',
        onOk: () => resolve('keep'),
        onCancel: () => {
          confirmBox({
            title: '直接丢弃？',
            icon: '!',
            text: '不保存就退出，临时工程里的图层和对话会被清空。',
            ok: '丢弃并退出',
            onOk: () => resolve('discard'),
            onCancel: () => resolve('cancel'),
          });
        },
        noClose: true,
      });
    });
  }

  // ── 复制 / 保存 ─────────────────────────────────────────
  $('btnCopy').addEventListener('click', async () => {
    if (!S.svg) { toast('画布还是空的', true); return; }
    try {
      await navigator.clipboard.writeText(S.svg);
      toast('SVG 已复制到剪贴板');
      addLog('LOG', '已复制 SVG');
    } catch (e) {
      const ta = el('textarea');
      ta.value = S.svg;
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      try { document.execCommand('copy'); toast('SVG 已复制'); } catch { toast('复制失败：' + e.message, true); }
      ta.remove();
    }
  });
  $('btnSave').addEventListener('click', async (ev) => {
    // 见下：Alt+点击 = 导出单文件
    // 工程模式下「保存」= 落盘（图层文件 + project.json）；
    // 按住 Alt（或快速模式）时退化为导出单文件 SVG。
    if (!ev.altKey && S.project && S.project.open && S.project.mode === 'project') {
      try {
        const r = await api('/api/project/save', { method: 'POST', body: {} });
        addLog('LOG', `工程已保存${r && r.files ? `（${r.files} 个文件）` : ''}`);
        toast('工程已保存');
      } catch (e) {
        toast('保存失败：' + e.message, true);
      }
      return;
    }
    if (!S.svg) { toast('画布还是空的', true); return; }
    const blob = new Blob([S.svg], { type: 'image/svg+xml;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const a = el('a');
    a.href = url;
    a.download = `svg-${stamp()}.svg`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 4000);
    addLog('LOG', '已导出 ' + a.download);
  });

  // 保存按钮的提示随工程模式变化
  function updateSaveTitle() {
    const b = $('btnSave');
    if (!b) return;
    const pm = S.project && S.project.open && S.project.mode === 'project';
    b.title = pm ? '保存工程（Alt+点击导出单文件 SVG）' : '导出 SVG';
    b.textContent = pm ? '保存工程' : '保存';
  }

  // ── 订阅额度：与生成使用同一个 Codex 登录 ────────────────
  const Q = { id: '', seq: 0, timer: 0, loading: false, data: null };
  function quotaPath(action = '') {
    return '/api/chatgpt/' + (action ? action.replace(/^\//, '') : 'quota') + '?providerId=' + encodeURIComponent(Q.id);
  }
  function syncQuotaProvider() {
    const p = activeProvider();
    const id = p?.authMode === 'chatgpt' ? p.id : '';
    $('blkQuota').classList.toggle('hidden', !id);
    if (Q.id === id) return;
    clearTimeout(Q.timer);
    Q.id = id; Q.seq++; Q.data = null; Q.loading = false;
    renderQuota();
    if (id) refreshQuota();
  }
  function renderQuota() {
    const d = Q.data || {}, w = d.window;
    const plan = d.planType === 'plus' ? 'Plus' : /^(?:prolite|pro(?:_.*)?)$/i.test(d.planType || '') ? 'Pro' : 'ChatGPT';
    $('quotaPlan').textContent = plan + (d.period ? ' · ' + d.period : '');
    $('quotaPlan').title = d.email ? 'Codex 账号：' + d.email : '';
    const valid = !!w && typeof w.usedPercent === 'number' && w.usedPercent >= 0 && w.usedPercent <= 100;
    $('quotaDetails').classList.toggle('hidden', !valid);
    if (valid) {
      const remaining = 100 - w.usedPercent;
      const pct = new Intl.NumberFormat('zh-CN', {maximumFractionDigits: 1}).format(remaining);
      $('quotaPeriod').textContent = d.period + ' 剩余';
      $('quotaRemaining').textContent = pct + '%';
      $('quotaBar').style.width = remaining + '%';
      $('quotaBar').style.background = remaining <= 10 ? 'var(--err)' : remaining <= 30 ? 'var(--warn)' : 'var(--ok)';
      $('quotaMeter').setAttribute('aria-valuenow', String(remaining));
      $('quotaMeter').setAttribute('aria-valuetext', d.period + ' 剩余 ' + pct + '%');
      const reset = w.resetsAt ? new Date(w.resetsAt * 1000) : null;
      $('quotaReset').textContent = reset ? reset.toLocaleString('zh-CN', {month:'2-digit', day:'2-digit', hour:'2-digit', minute:'2-digit', hour12:false}) : '暂未提供';
      $('quotaReset').title = reset ? reset.toLocaleString() : '';
    }
    $('quotaMessage').textContent = d.pending ? '请在浏览器中完成 Codex 登录…' : d.message || (Q.loading && !Q.data ? '正在读取额度…' : '');
    $('btnQuotaLogin').classList.toggle('hidden', !!d.connected || !!d.pending);
    $('btnQuotaCancel').classList.toggle('hidden', !d.pending);
    $('btnQuotaLogout').classList.toggle('hidden', !d.connected || !!d.pending);
    $('btnQuotaRefresh').classList.toggle('hidden', !Q.data || !!d.pending);
    for (const id of ['btnQuotaLogin', 'btnQuotaRefresh', 'btnQuotaLogout', 'btnQuotaCancel']) $(id).disabled = Q.loading;
    $('quotaNote').textContent = d.updatedAt ? 'Codex 账号额度 · 更新于 ' + new Date(d.updatedAt * 1000).toLocaleTimeString('zh-CN', {hour12:false}) : '生成与额度共用 Codex 登录';
  }
  async function refreshQuota() {
    if (!Q.id || Q.loading) return;
    clearTimeout(Q.timer);
    const seq = Q.seq;
    Q.loading = true; renderQuota();
    try {
      const d = await api(quotaPath());
      if (seq !== Q.seq) return;
      Q.data = d;
    } catch (e) {
      if (seq !== Q.seq) return;
      Q.data = {message:e.message};
    } finally {
      if (seq === Q.seq) {
        Q.loading = false; renderQuota();
        Q.timer = setTimeout(() => { if (!document.hidden || Q.data?.pending) refreshQuota(); }, Q.data?.pending ? 1500 : 60000);
      }
    }
  }
  async function quotaAction(action) {
    if (!Q.id || Q.loading) return;
    clearTimeout(Q.timer);
    const seq = Q.seq;
    Q.loading = true; renderQuota();
    try {
      await api(quotaPath('/' + action), {method:'POST', body:{}});
      if (seq !== Q.seq) return;
      Q.data = null;
    } catch (e) {
      if (seq !== Q.seq) return;
      Q.data = Object.assign({}, Q.data, {message:e.message});
      Q.loading = false; renderQuota(); return;
    } finally {
      if (seq === Q.seq) Q.loading = false;
    }
    if (seq === Q.seq) {
      refreshQuota();
      if (P.curId === Q.id && P.draft?.authMode === 'chatgpt') {
        clearTimeout(chatgptPoll);
        refreshChatGPTStatus(Q.id, action === 'login');
      }
    }
  }
  $('btnQuotaLogin').addEventListener('click', () => quotaAction('login'));
  $('btnQuotaCancel').addEventListener('click', () => quotaAction('cancel'));
  $('btnQuotaLogout').addEventListener('click', () => quotaAction('logout'));
  $('btnQuotaRefresh').addEventListener('click', refreshQuota);
  $('btnQuotaUsage').addEventListener('click', async () => {
    if (!Q.id) return;
    try { await api('/api/chatgpt/usage?providerId=' + encodeURIComponent(Q.id), {method:'POST',body:{}}); }
    catch (e) { toast(e.message, true); }
  });
  document.addEventListener('visibilitychange', () => { if (!document.hidden) refreshQuota(); });

  // ── 供应商管理弹窗 ──────────────────────────────────────
  const P = { curId: '', draft: null };
	let chatgptPoll = 0;
	function chatgptPath(action, id = P.curId) {
	  return '/api/chatgpt/' + action + '?providerId=' + encodeURIComponent(id);
	}
	function renderChatGPTAccount() {
	  clearTimeout(chatgptPoll);
	  const plan = P.draft && P.draft.authMode === 'chatgpt';
	  $('chatgptAccount').hidden = !plan;
	  [$('pfProto').closest('.fld'), $('pfBase').closest('.fld'), $('pfKey').closest('.fld')].forEach(n => n.classList.toggle('chatgpt-hidden', !!plan));
	  if (!plan) return;
	  $('chatgptStatus').textContent = '正在读取 Codex 登录状态…';
	  $('btnChatGPTLogin').disabled = false;
	  $('btnChatGPTLogin').hidden = false;
	  $('btnChatGPTCancel').hidden = true;
	  $('btnChatGPTLogout').hidden = true;
	  const saved = (S.settings.providers || []).find(x => x.id === P.curId);
	  if (saved && saved.authMode === 'chatgpt') refreshChatGPTStatus(P.curId);
	  else $('chatgptStatus').textContent = '点击登录将保存供应商并打开官方授权页';
	}
	async function refreshChatGPTStatus(id, afterLogin = false) {
	  try {
	    const state = await api(chatgptPath('status', id));
	    if (P.curId !== id || P.draft?.authMode !== 'chatgpt') return;
	    $('btnChatGPTLogin').disabled = state.pending;
	    $('btnChatGPTLogin').hidden = state.connected && !state.pending;
	    $('btnChatGPTCancel').hidden = !state.pending;
	    $('btnChatGPTLogout').hidden = !state.connected;
	    $('chatgptStatus').textContent = state.pending ? '请在系统浏览器中完成 Codex 登录…' : state.message || (state.connected ? `${state.email || '已连接账号'} · 已登录 Codex，生成与额度共用此账号` : '尚未登录');
	    if (state.pending) {
	      chatgptPoll = setTimeout(() => refreshChatGPTStatus(id, afterLogin), 1500);
	    } else if (afterLogin && state.connected) {
	      refreshQuota();
	      $('btnFetchProv').click();
	    }
	  } catch (e) {
	    if (P.curId === id) { $('chatgptStatus').textContent = e.message; $('btnChatGPTLogin').disabled = false; }
	  }
	}
	$('pfAuthMode').addEventListener('change', () => {
	  if (!P.draft) return;
	  P.draft.authMode = $('pfAuthMode').value;
	  if (P.draft.authMode === 'chatgpt') {
	    P.draft.protocol = 'openai-responses'; P.draft.baseUrl = 'https://api.openai.com/v1'; P.draft.apiKey = '';
	    P.draft.models = []; P.draft.modelCaps = {};
	    $('pfBase').value = P.draft.baseUrl; $('pfKey').value = '';
	    renderProtoGrid(); renderProvModels(); renderCapsTable();
	  }
	  renderChatGPTAccount();
	});
	$('btnChatGPTLogin').addEventListener('click', async () => {
	  const id = P.curId;
	  if (!await saveProvider() || P.curId !== id) return;
	  $('btnChatGPTLogin').disabled = true;
	  try {
	    await api(chatgptPath('login', id), {method:'POST', body:{}});
	    clearTimeout(chatgptPoll); await refreshChatGPTStatus(id, true);
	  } catch (e) { $('chatgptStatus').textContent = e.message; $('btnChatGPTLogin').disabled = false; }
	});
	$('btnChatGPTCancel').addEventListener('click', async () => {
	  try { await api(chatgptPath('cancel'), {method:'POST', body:{}}); clearTimeout(chatgptPoll); await refreshChatGPTStatus(P.curId); }
	  catch(e) { toast(e.message, true); }
	});
	$('btnChatGPTLogout').addEventListener('click', async () => {
	  try { await api(chatgptPath('logout'), {method:'POST', body:{}}); toast('已退出 Codex'); await refreshChatGPTStatus(P.curId); refreshQuota(); }
	  catch(e) { toast(e.message, true); }
	});
	$('btnChatGPTUsage').addEventListener('click', async () => {
	  try { await api(chatgptPath('usage'), {method:'POST', body:{}}); } catch(e) { toast(e.message, true); }
	});

  function openProviders() {
    if (!S.settings) return;
    openModal('modalProviders');
    P.curId = S.settings.activeProviderId || (S.settings.providers[0] || {}).id || '';
    renderProvList();
    selectProvider(P.curId);
    $('provHint').textContent = `${S.settings.providers.length} 个供应商 · 保存后立即生效`;
  }
  function renderProvList() {
    const box = $('provList');
    box.innerHTML = '';
    (S.settings.providers || []).forEach((p) => {
      const row = el('div', 'prov-item' + (p.id === P.curId ? ' active' : ''));
      row.appendChild(el('span', 'pname', p.name || p.id));
      row.appendChild(el('span', 'pproto', p.authMode === 'chatgpt' ? 'ChatGPT 订阅' : (protoMeta(p.protocol).label || p.protocol)));
      row.addEventListener('click', () => selectProvider(p.id));
      box.appendChild(row);
    });
  }
  function selectProvider(id) {
    const p = (S.settings.providers || []).find((x) => x.id === id);
    if (!p) return;
    P.curId = id;
    P.draft = JSON.parse(JSON.stringify(p));
    P.draft.modelCaps = P.draft.modelCaps || {};
    renderProvList();
    $('pfName').value = P.draft.name || '';
    $('pfBase').value = P.draft.baseUrl || '';
    $('pfKey').value = P.draft.apiKey || '';
    $('pfKey').type = 'password';
    $('pfModelInput').value = '';
    $('pfExtended').checked = extendedThinking();
	$('pfAuthMode').value = P.draft.authMode || '';
	renderChatGPTAccount();
    renderProtoGrid();
    renderProvModels();
    renderCapsTable();
  }
  function renderProtoGrid() {
    const box = $('pfProto');
    box.innerHTML = '';
    S.protocols.forEach((m) => {
      const card = el('button', 'proto-card' + (P.draft && P.draft.protocol === m.key ? ' active' : ''));
      card.type = 'button';
      card.appendChild(el('b', '', m.label));
      card.appendChild(el('small', '', m.sub));
      card.addEventListener('click', () => {
        if (!P.draft) return;
        const prevDefault = protoMeta(P.draft.protocol).baseUrl;
        P.draft.protocol = m.key;
        const cur = ($('pfBase').value || '').trim();
        if (!cur || cur === prevDefault) {
          P.draft.baseUrl = m.baseUrl;
          $('pfBase').value = m.baseUrl;
        }
        renderProtoGrid();
        $('provHint').textContent = m.hint || '';
      });
      box.appendChild(card);
    });
  }
  function renderProvModels() {
    const box = $('pfModels');
    box.innerHTML = '';
    const models = (P.draft && P.draft.models) || [];
    if (!models.length) { box.appendChild(el('div', 'model-empty', '暂无模型')); return; }
    models.forEach((m, i) => {
      const row = el('div', 'model-row');
      row.appendChild(el('span', '', m.id));
      const x = el('button', '', '×');
      x.type = 'button';
      x.title = '移除';
      x.addEventListener('click', () => {
        P.draft.models.splice(i, 1);
        renderProvModels();
        renderCapsTable();
      });
      row.appendChild(x);
      box.appendChild(row);
    });
  }
  function renderCapsTable() {
    const t = $('pfCaps');
    t.innerHTML = '';
    const thead = el('thead');
    const hr = el('tr');
    ['模型', 'contextWindow', 'maxTokens', 'reasoning', 'vision'].forEach((h) => hr.appendChild(el('th', '', h)));
    thead.appendChild(hr);
    t.appendChild(thead);
    const tb = el('tbody');
    const models = (P.draft && P.draft.models) || [];
    if (!models.length) {
      const tr = el('tr');
      const td = el('td', 'muted', '先添加模型');
      td.colSpan = 5;
      tr.appendChild(td);
      tb.appendChild(tr);
    }
    models.forEach((m) => {
      const caps = (P.draft.modelCaps && P.draft.modelCaps[m.id]) || {};
      const tr = el('tr');
      tr.appendChild(el('td', 'mid', m.id));
      const mkNum = (key) => {
        const td = el('td');
        const inp = el('input');
        inp.type = 'number';
        inp.min = '0';
        inp.placeholder = 'auto';
        if (caps[key] != null) inp.value = String(caps[key]);
        inp.addEventListener('input', () => {
          const v = parseInt(inp.value, 10);
          if (!inp.value.trim() || !v || v <= 0) delete caps[key];
          else caps[key] = v;
          P.draft.modelCaps[m.id] = caps;
        });
        td.appendChild(inp);
        return td;
      };
      tr.appendChild(mkNum('contextWindow'));
      tr.appendChild(mkNum('maxTokens'));
      const mkBool = (key) => {
        const td = el('td');
        const sel = el('select');
        sel.style.width = '74px';
        [['', '自动'], ['1', '支持'], ['0', '不支持']].forEach(([v, label]) => {
          const o = el('option', '', label);
          o.value = v;
          sel.appendChild(o);
        });
        sel.value = caps[key] === true ? '1' : caps[key] === false ? '0' : '';
        sel.addEventListener('change', () => {
          if (sel.value === '') delete caps[key];
          else caps[key] = sel.value === '1';
          P.draft.modelCaps[m.id] = caps;
        });
        td.appendChild(sel);
        return td;
      };
      tr.appendChild(mkBool('reasoning'));
      tr.appendChild(mkBool('vision'));
      tb.appendChild(tr);
    });
    t.appendChild(tb);
  }
  $('pfName').addEventListener('input', () => { if (P.draft) P.draft.name = $('pfName').value; });
  $('pfBase').addEventListener('input', () => { if (P.draft) P.draft.baseUrl = $('pfBase').value.trim(); });
  $('pfKey').addEventListener('input', () => { if (P.draft) P.draft.apiKey = $('pfKey').value.trim(); });
  $('pfEye').addEventListener('click', () => {
    const f = $('pfKey');
    f.type = f.type === 'password' ? 'text' : 'password';
  });
  // 扩展思考：顶层设置，勾选后思考档位从 4 档变 6 档，随 PUT /api/settings 一起提交
  $('pfExtended').addEventListener('change', () => {
    if (!S.settings) return;
    S.settings.extendedThinking = $('pfExtended').checked;
    renderThinkingOptions();
    $('provHint').textContent = S.settings.extendedThinking
      ? '已开启扩展思考：思考档位新增 xhigh — 极致 / max — 最大'
      : '已关闭扩展思考：xhigh / max 会降级为 high';
  });
  function addModelId(id) {
    if (!P.draft || !id) return;
    id = id.trim();
    if (!id) return;
    if (!P.draft.models) P.draft.models = [];
    if (P.draft.models.some((m) => m.id === id)) { toast('已存在该模型'); return; }
    P.draft.models.push({ id, name: id });
    renderProvModels();
    renderCapsTable();
  }
  $('btnAddModel').addEventListener('click', () => {
    addModelId($('pfModelInput').value);
    $('pfModelInput').value = '';
  });
  $('pfModelInput').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); addModelId($('pfModelInput').value); $('pfModelInput').value = ''; }
  });
  $('btnClearModels').addEventListener('click', () => {
    if (!P.draft) return;
    const n = (P.draft.models || []).length;
    if (!n) { toast('当前没有备选模型'); return; }
    confirmBox({
      title: '清空备选模型',
      icon: '!',
      text: `将移除「${P.draft.name}」下的 ${n} 个备选模型（能力配置一并清除），供应商本身保留。`,
      ok: '清空',
      onOk: () => {
        P.draft.models = [];
        P.draft.modelCaps = {};
        renderProvModels();
        renderCapsTable();
        $('provHint').textContent = `已清空 ${n} 个备选模型`;
        toast('已清空备选模型');
      },
    });
  });
  $('btnFetchProv').addEventListener('click', async () => {
    if (!P.draft) return;
	if (P.draft.authMode === 'chatgpt' && !await saveProvider()) return;
    const b = $('btnFetchProv');
    b.disabled = true;
    b.textContent = '…';
    try {
      const r = await api('/api/models', {
        method: 'POST',
        body: {
          providerId: P.draft.id,
          protocol: P.draft.protocol,
          baseUrl: $('pfBase').value.trim(),
          apiKey: $('pfKey').value.trim(),
        },
      });
      const fetched = r.models || [];
      let added = 0;
      const liveIds = new Set();
      fetched.forEach((m) => {
        liveIds.add(m.id);
        if (!P.draft.models.some((x) => x.id === m.id)) { P.draft.models.push({ id: m.id, name: m.name || m.id }); added++; }
        if (m.caps) {
          P.draft.modelCaps[m.id] = {
            contextWindow: m.caps.contextWindow || undefined,
            maxTokens: m.caps.maxTokens || undefined,
            reasoning: !!m.caps.reasoning,
            vision: !!m.caps.vision,
          };
        }
      });
      renderProvModels();
      renderCapsTable();
      $('provHint').textContent = `拉取 ${fetched.length} 个模型，新增 ${added} 个`;
      toast(`新增 ${added} 个模型`);
      // 接口没返回的备选模型视为已失效（下架/改名），问一句是否删掉。
      const stale = (P.draft.models || []).filter((m) => !liveIds.has(m.id));
      if (fetched.length && stale.length) {
        const names = stale.map((m) => m.id);
        const shown = names.slice(0, 6).join('、') + (names.length > 6 ? ` 等 ${names.length} 个` : '');
        confirmBox({
          title: '发现已失效模型',
          icon: '?',
          text: `本次拉取未返回 ${stale.length} 个已配置模型：${shown}。是否从备选列表删除它们？（保留则仍可手动选用）`,
          ok: '删除失效模型',
          onOk: () => {
            const dead = new Set(names);
            P.draft.models = P.draft.models.filter((m) => !dead.has(m.id));
            dead.forEach((id) => { delete P.draft.modelCaps[id]; });
            renderProvModels();
            renderCapsTable();
            $('provHint').textContent = `已删除 ${dead.size} 个失效模型`;
            toast(`已删除 ${dead.size} 个失效模型`);
          },
          onCancel: () => {
            $('provHint').textContent = `保留 ${stale.length} 个未返回模型`;
          },
        });
      }
    } catch (e) {
      $('provHint').textContent = '拉取失败：' + e.message;
      toast('拉取失败：' + e.message, true);
    } finally {
      b.disabled = false;
      b.textContent = '获取模型列表';
    }
  });
  $('btnNewProv').addEventListener('click', () => {
    const meta = S.protocols[0] || { key: 'openai-completions', label: '新供应商', baseUrl: '' };
    const p = {
      id: 'p' + Date.now().toString(36),
      name: '新供应商',
      protocol: meta.key,
      baseUrl: meta.baseUrl || '',
      apiKey: '',
      models: [],
      modelCaps: {},
    };
    S.settings.providers.push(p);
    renderProvList();
    selectProvider(p.id);
    $('pfName').focus();
    $('pfName').select();
  });
  $('btnDelProv').addEventListener('click', () => {
    if (!S.settings.providers.length) return;
    if (S.settings.providers.length <= 1) { toast('至少保留一个供应商', true); return; }
    const idx = S.settings.providers.findIndex((p) => p.id === P.curId);
    if (idx < 0) return;
    const name = S.settings.providers[idx].name;
    S.settings.providers.splice(idx, 1);
    if (S.settings.activeProviderId === P.curId) {
      S.settings.activeProviderId = S.settings.providers[0].id;
    }
    P.curId = S.settings.providers[0].id;
    renderProvList();
    selectProvider(P.curId);
    addLog('LOG', `已删除供应商 ${name}`);
  });
  async function saveProvider() {
    if (!P.draft) return;
    const idx = S.settings.providers.findIndex((p) => p.id === P.draft.id);
    const clean = JSON.parse(JSON.stringify(P.draft));
    clean.name = ($('pfName').value || '').trim() || clean.name;
    clean.baseUrl = ($('pfBase').value || '').trim();
    clean.apiKey = ($('pfKey').value || '').trim();
    clean.models = (clean.models || []).filter((m) => m && m.id);
    const caps = clean.modelCaps || {};
    Object.keys(caps).forEach((k) => {
      if (!clean.models.some((m) => m.id === k)) { delete caps[k]; return; }
      const c = caps[k];
      const empty = (c.contextWindow == null) && (c.maxTokens == null) && (c.reasoning == null) && (c.vision == null);
      if (empty) delete caps[k];
    });
    if (idx >= 0) S.settings.providers[idx] = clean;
    else S.settings.providers.push(clean);
    if (!S.settings.activeProviderId) S.settings.activeProviderId = clean.id;
    // 当前模型不在该供应商的列表里时，自动落到第一个模型，避免选了个不存在的模型
    if (S.settings.activeProviderId === clean.id && clean.models.length
        && !clean.models.some((m) => m.id === S.settings.activeModel)) {
      S.settings.activeModel = clean.models[0].id;
    }
    try {
      const r = await api('/api/settings', { method: 'PUT', body: S.settings });
      if (r && r.settings) S.settings = r.settings;
      applySettingsToLeft();
      renderProvList();
      selectProvider(clean.id);
      toast('供应商已保存');
      addLog('LOG', `供应商已保存：${clean.name}`);
	  return true;
    } catch (e) {
      toast('保存失败：' + e.message, true);
      addLog('ERROR', '供应商保存失败：' + e.message);
	  return false;
    }
  }
  $('btnProvSave').addEventListener('click', saveProvider);
  $('btnProviders').addEventListener('click', openProviders);

  // ── 提示词库弹窗 ────────────────────────────────────────
  const PR = { cat: '', q: '' };
  function openPrompts() {
    openModal('modalPrompts');
    PR.cat = '';
    PR.q = '';
    $('promptSearch').value = '';
    renderPromptCats();
    renderPromptGrid();
  }
  function renderPromptCats() {
    const box = $('promptCats');
    box.innerHTML = '';
    const cats = ['', ...(S.categories || [])];
    cats.forEach((c) => {
      const b = el('button', 'cat' + (PR.cat === c ? ' active' : ''), c || '全部');
      b.type = 'button';
      b.addEventListener('click', () => { PR.cat = c; renderPromptCats(); renderPromptGrid(); });
      box.appendChild(b);
    });
  }
  function filteredPrompts() {
    const q = PR.q.toLowerCase();
    return (S.prompts || []).filter((p) => {
      if (PR.cat && p.category !== PR.cat) return false;
      if (!q) return true;
      return [p.title, p.category, p.template, p.note].join(' ').toLowerCase().includes(q);
    });
  }
  function renderPromptGrid() {
    const grid = $('promptGrid');
    grid.innerHTML = '';
    const list = filteredPrompts();
    if (!list.length) {
      grid.appendChild(el('div', 'muted tiny pad', '没有匹配的提示词（提示词库可能尚未加载）'));
      return;
    }
    list.forEach((p) => {
      const card = el('div', 'pcard');
      const top = el('div', 'pt');
      top.appendChild(el('b', '', p.title || p.id));
      if (p.ratio) top.appendChild(el('span', 'ratio', p.ratio));
      card.appendChild(top);
      if (p.note) card.appendChild(el('div', 'pnote', p.note));
      if (p.category) card.appendChild(el('div', 'pcat', p.category));
      card.addEventListener('click', () => applyPromptTemplate(p));
      grid.appendChild(card);
    });
  }
  function applyPromptTemplate(p) {
    const ta = $('prompt');
    const cur = ta.value;
    const subject = extractSubject(cur);
    let text = (p.template || '').split('{{subject}}').join(subject);
    ta.value = text;
    closeModal('modalPrompts');
    ta.focus();
    if (!subject) {
      ta.setSelectionRange(0, ta.value.length);
      toast('已填入模板，请把主题补进提示词');
    } else {
      toast('已填入模板（主题：' + subject + '）');
    }
    addLog('LOG', `提示词模板：${p.title || p.id}`);
  }
  function extractSubject(text) {
    if (!text) return '';
    let t = text.trim();
    let raw = '';
    const m = t.match(/generate an svg of ([^.,\n]+)/i) || t.match(/svg of ([^.,\n]+)/i);
    if (m) raw = m[1].trim();
    else if (t.length <= 60 && !/svg/i.test(t)) raw = t;
    return raw.replace(/^(?:a|an|the)\s+/i, '').replace(/[.。!！?？,，;；:：]+$/, '').trim();
  }
  $('btnPrompts').addEventListener('click', openPrompts);
  $('promptSearch').addEventListener('input', debounce(() => {
    PR.q = $('promptSearch').value.trim();
    renderPromptGrid();
  }, 120));

  // ── 收尾 ────────────────────────────────────────────────
  updateBadge();
  boot();
})();
