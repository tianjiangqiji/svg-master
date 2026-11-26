(() => {
  'use strict';

  const palettes = {
    pico: '000000 1d2b53 7e2553 008751 ab5236 5f574f c2c3c7 fff1e8 ff004d ffa300 ffec27 00e436 29adff 83769c ff77a8 ffccaa',
    endesga: 'be4a2f d77643 ead4aa e4a672 b86f50 733e39 3e2731 a22633 e43b44 f77622 feae34 fee761 63c74d 3e8948 265c42 193c3e 124e89 0099db 2ce8f5 ffffff c0cbdc 8b9bb4 5a6988 3a4466 262b44 181425 ff0044 68386c b55088 f6757a e8b796 c28569',
    db: '000000 222034 45283c 663931 8f563b df7126 d9a066 eec39a fbf236 99e550 6abe30 37946e 4b692f 524b24 323c39 3f3f74 306082 5b6ee1 639bff 5fcde4 cbdbfc ffffff 9badb7 847e87 696a6a 595652 76428a ac3232 d95763 d77bba 8f974a 8a6f30',
    gb: '0f380f 306230 8bac0f 9bbc0f',
  };
  const shortcuts = {
    b: 'pencil', e: 'eraser', g: 'fill', i: 'picker', l: 'line', r: 'rectangle',
    o: 'ellipse', m: 'select', w: 'wand', v: 'move', h: 'pan',
  };
  const shapes = ['line', 'rectangle', 'ellipse'];
  const zero = [0, 0, 0, 0];
  const undoBudget = 128 * 1024 * 1024;
  const tileSize = 128;
  const partialUploadBytes = 2 * 1024 * 1024;
  const maxCanvasSize = 2048;
  const maxAnimationSize = 512;
  const maxFrameCount = 32;
  const minZoom = 0.125;
  const maxZoom = 64;
  const $ = (id) => document.getElementById(id);
  const clamp = (value, low, high) => Math.min(high, Math.max(low, value));
  const toHex = (rgba) => '#' + [...rgba].slice(0, rgba[3] === 255 ? 3 : 4)
    .map((value) => value.toString(16).padStart(2, '0')).join('');
  const toRGBA = (value) => (value.length === 7 ? value + 'ff' : value)
    .slice(1).match(/../g).map((byte) => parseInt(byte, 16));
  const validColor = (value) => /^#[\da-f]{6}(?:[\da-f]{2})?$/i.test(value);
  const area = (a, b) => ({
    x: Math.min(a.x, b.x), y: Math.min(a.y, b.y),
    w: Math.abs(a.x - b.x) + 1, h: Math.abs(a.y - b.y) + 1,
  });
  const union = (a, b) => area(
    { x: Math.min(a.x, b.x), y: Math.min(a.y, b.y) },
    { x: Math.max(a.x + a.w, b.x + b.w) - 1, y: Math.max(a.y + a.h, b.y + b.h) - 1 },
  );
  const encodePNG = (canvas) => new Promise((resolve, reject) => {
    canvas.toBlob((blob) => blob ? resolve(blob) : reject(new Error('无法编码图层 PNG')), 'image/png');
  });
  const canvas2D = (w, h) => {
    const canvas = document.createElement('canvas');
    canvas.width = w;
    canvas.height = h;
    return canvas;
  };
  const byte64 = (bytes) => {
    const chunks = [];
    for (let i = 0; i < bytes.length; i += 8192) {
      chunks.push(String.fromCharCode(...bytes.subarray(i, i + 8192)));
    }
    return btoa(chunks.join(''));
  };
  function loadImage(src) {
    return new Promise((resolve, reject) => {
      const img = new Image();
      img.onload = () => resolve(img);
      img.onerror = () => reject(new Error('无法读取图像'));
      img.src = src;
    });
  }
  function drawLine(a, b, plot) {
    const dx = Math.abs(b.x - a.x);
    const dy = -Math.abs(b.y - a.y);
    const sx = a.x < b.x ? 1 : -1;
    const sy = a.y < b.y ? 1 : -1;
    let x = a.x;
    let y = a.y;
    let error = dx + dy;
    for (;;) {
      plot(x, y);
      if (x === b.x && y === b.y) return;
      const twice = 2 * error;
      if (twice >= dy) { error += dy; x += sx; }
      if (twice <= dx) { error += dx; y += sy; }
    }
  }
  function drawShape(tool, start, end, filled, stroke, dot) {
    if (tool === 'line') return drawLine(start, end, stroke);
    const r = area(start, end);
    const inShape = tool === 'rectangle' ? () => true : (x, y) => {
      const dx = (x - (r.x + (r.w - 1) / 2)) / (r.w / 2);
      const dy = (y - (r.y + (r.h - 1) / 2)) / (r.h / 2);
      return dx * dx + dy * dy <= 1;
    };
    const inside = (x, y) => x >= r.x && x < r.x + r.w && y >= r.y && y < r.y + r.h && inShape(x, y);
    for (let y = r.y; y < r.y + r.h; y++) {
      for (let x = r.x; x < r.x + r.w; x++) {
        if (!inside(x, y)) continue;
        if (filled) dot(x, y);
        else if (!inside(x - 1, y) || !inside(x + 1, y) || !inside(x, y - 1) || !inside(x, y + 1)) stroke(x, y);
      }
    }
  }

  window.createPixelStudio = ({ api, toast, applyProject, isActive, openProject, confirm, token }) => {
    let project;
    let layers = new Map();
    let boardWidth = 0;
    let boardHeight = 0;
    let generation = 0;
    let running = false;
    let syncing = false;
    let busy = false;
    let loading = false;
    let syncTimer = 0;
    let paintFrame = 0;
    let scrollFrame = 0;
    let compositeDirty = true;
    let compositeRegion = null;
    let assetKey = '';
    let overlayImage = null;
    let focusSlot = 'main';
    let selection = null;
    let cursor = null;
    let stroke = null;
    let spaceDown = false;
    let tool = 'pan';
    let zoom = 1;
    let fit = true;
    let wheelFrame = 0;
    let wheelDelta = 0;
    let wheelAnchor;
    let editingZoom = false;
    let undo = [];
    let redo = [];
    let pending = [];
    let serialized = Promise.resolve();
    let compare = false;
    let lastHighlight = null;
    let activeID = '';
    let playing = false;
    let playbackID = '';
    let playbackTimer = 0;
    let exportingGIF = false;
    let primary = '#ffffff';
    let secondary = '#000000';
    let colorsDirty = false;
    let remoteError = '';
    let turnBefore = '';
    const previewVisibility = new Map();
    const timelineCards = new Map();
    let timelineShown = '';
    const stage = $('pixelStage');
    const viewport = $('pixelViewport');
    const sheet = $('pixelSheet');
    const zoomInput = $('pixelZoom');
    const composite = $('pixelCanvas');
    const compositeCtx = composite.getContext('2d', { willReadFrequently: true });
    const decoration = $('pixelDecoration');
    const overlay = $('pixelOverlay');
    const onion = $('pixelOnion');
    const buttons = [...document.querySelectorAll('[data-pixel-tool]')];
    const dimensions = () => [Number($('pixelWidth').value), Number($('pixelHeight').value)];
    const hasBoard = () => Boolean(project?.open);
    const animation = (board = project) => board?.pixel?.anim;
    const isAnimated = () => project?.pixel?.mode === 'anim';
    const entries = (board = project) => animation(board)?.frames || board?.layers || [];
    const layerMetadata = (id = activeID) => entries().find((layer) => layer.id === id);
    const ownsEdits = () => !running && !busy && !loading && !playing;
    const visibleForPreview = (layer) => previewVisibility.get(layer.id) ?? layer.visible;
    const projectKey = () => project?.projectId || '';
    const requestWithRevision = (body = {}) => ({ projectId: projectKey(), revision: project?.revision, ...body });
    const displayError = (error) => {
      remoteError = error.message || String(error);
      updateControls();
      toast(remoteError, true);
    };
    const listen = (id, event, handler) => $(id).addEventListener(event, handler);
    const click = (id, handler) => listen(id, 'click', handler);
    const encodeColor = (id) => '#' + $(id).value.slice(1, 7);
    const loadResult = (result, options) => {
      if (result?.project) applyProject(result.project, options);
      return result;
    };
    const serialize = (fn) => {
      const task = serialized.then(fn);
      serialized = task.catch(() => {});
      return task;
    };

    function updateControls() {
      const isOpen = hasBoard();
      const animated = isAnimated();
      $('pixelSheet').classList.toggle('hidden', !isOpen);
      $('pixelEmpty').classList.toggle('hidden', isOpen);
      const editable = isOpen && ownsEdits();
      $('pixelUndo').disabled = !editable || !undo.length;
      $('pixelRedo').disabled = !editable || !redo.length;
      $('pixelClearSelection').disabled = !selection || running;
      $('pixelRestrictField').classList.toggle('hidden', !selection);
      $('pixelQuantize').disabled = !editable || !project.pixel.mainImage;
      $('pixelResize').disabled = !editable;
      $('pixelDraft').disabled = !editable;
      const mode = $('pixelMode');
      mode.disabled = !editable || !!project?.pixel?.modeLocked;
      mode.title = project?.pixel?.modeLocked ? '开始 AI 动图后，清空或新建工程即可切换形态' : '';
      mode.value = animated ? 'anim' : 'static';
      const maxSize = animated ? maxAnimationSize : maxCanvasSize;
      $('pixelWidth').max = $('pixelHeight').max = String(maxSize);
      for (const option of $('pixelSizePreset').options) option.disabled = parseInt(option.value, 10) > maxSize;
      $('pixelOverlayField').classList.toggle('hidden', !project?.pixel?.mainImage);
      $('pixelCompare').disabled = !project?.pixel?.mainImage && !project?.pixel?.refImages?.length;
      $('pixelMainDrop').disabled = !editable;
      $('pixelRefDrop').disabled = !editable || (project?.pixel?.refImages?.length || 0) >= 8;
      for (const id of ['pixelExportPNG', 'pixelExportSVG', 'pixelSave', 'pixelPalettePreset', 'pixelPaletteAdd', 'pixelPaletteExtract']) {
        $(id).disabled = !editable;
      }
      $('pixelExportPNG').textContent = animated ? '导出当前帧 PNG' : '导出 PNG';
      $('pixelExportSVG').classList.toggle('hidden', animated);
      $('pixelExportGIF').classList.toggle('hidden', !animated);
      $('pixelExportGIF').disabled = !editable || !visibleFrames().length;
      $('pixelExportGIF').textContent = exportingGIF ? '正在编码…' : '导出 GIF';
      const timeline = $('pixelTimeline');
      if (timeline) {
        timeline.classList.toggle('hidden', !animated);
        $('pixelAnimBar')?.classList.add('hidden');
      } else $('pixelAnimBar')?.classList.toggle('hidden', !animated);
      $('pixelAnimPlay').disabled = !animated || running || busy || loading || !visibleFrames().length;
      $('pixelAnimFPS').disabled = $('pixelAnimLoop').disabled = !editable;
      $('pixelAnimOnion').disabled = !animated || playing || running;
      const delayInput = $('pixelFrameDelay');
      if (delayInput) {
        delayInput.disabled = !editable || !animated || !activeID;
        if (document.activeElement !== delayInput) delayInput.value = layerMetadata()?.delayMult || 1;
      }
      const play = $('pixelAnimPlay');
      play.textContent = playing ? '❚❚' : '▶';
      play.setAttribute('aria-label', playing ? '暂停' : '播放');
      for (const option of $('pixelExportScale').options) {
        const scale = parseInt(option.value, 10);
        const width = boardWidth * scale;
        const height = boardHeight * scale;
        option.disabled = animated
          ? scale > 8 || Math.max(width, height) > 640
          : Math.max(width, height) > 8192 || width * height > 64 * 1024 * 1024;
      }
      if ($('pixelExportScale').selectedOptions[0]?.disabled) $('pixelExportScale').value = '1×';
      renderTimeline();
      if (animated) {
        const shown = playbackID || activeID;
        const order = playing ? visibleFrames() : [...entries()].sort((a, b) => a.index - b.index);
        const index = order.findIndex((frame) => frame.id === shown);
        $('pixelAnimPosition').textContent = (index + 1) + ' / ' + order.length;
        updateTimelineButtons(editable);
      }
      $('pixelSelectionInfo').textContent = selection
        ? `选区 ${selection.x},${selection.y} · ${selection.w}×${selection.h}` : '';
      const layer = layerMetadata();
      $('pixelStatus').textContent = remoteError || (running ? 'AI 正在绘制，可以平移缩放。'
        : loading ? '正在加载图层…' : syncing ? '正在同步笔触…' : pending.length ? '笔触等待同步'
          : layer?.locked ? `「${layer.name}」已锁定` : '');
      const canPan = tool === 'pan' || spaceDown || running || playing;
      viewport.style.cursor = stroke?.tool === 'pan' ? 'grabbing' : canPan ? 'grab' : '';
      stage.style.cursor = viewport.style.cursor || (tool === 'move' ? 'move' : 'crosshair');
    }

    function setTool(next) {
      tool = next;
      for (const button of buttons) {
        button.setAttribute('aria-pressed', String(button.dataset.pixelTool === tool));
      }
      $('pixelShapeFill').classList.toggle('hidden', !['rectangle', 'ellipse'].includes(tool));
      $('pixelBrushSize').classList.toggle('hidden', !['pencil', 'eraser', ...shapes].includes(tool));
      $('pixelToleranceField').classList.toggle('hidden', !['fill', 'wand'].includes(tool));
      $('pixelContiguousField').classList.toggle('hidden', !['fill', 'wand'].includes(tool));
      updateControls();
    }

    function visibleFrames() {
      return (animation()?.frames || []).filter((frame) => visibleForPreview(frame)).sort((a, b) => a.index - b.index);
    }

    function orderedFrames() {
      return [...entries()].sort((a, b) => a.index - b.index);
    }

    function selectFrame(frameId) {
      if (!frameId || frameId === activeID) return;
      mutate('/api/pixel/frames', { action: 'active', frameId });
    }

    function stepFrame(delta) {
      const order = orderedFrames();
      if (!order.length) return;
      const index = order.findIndex((frame) => frame.id === (playbackID || activeID));
      const next = order[clamp(index + delta, 0, order.length - 1)];
      if (next) selectFrame(next.id);
    }

    function moveFrame(delta) {
      if (!ownsEdits()) return;
      const frame = layerMetadata();
      if (!frame) return;
      const order = orderedFrames();
      const index = order.findIndex((item) => item.id === frame.id);
      const target = index + delta;
      if (index < 0 || target < 0 || target >= order.length) return;
      mutate('/api/pixel/frames', { action: 'reorder', frameId: frame.id, index: target + 1 });
    }

    function drawTimelineThumb(card, raster) {
      const scale = 40 / Math.max(boardWidth, boardHeight, 1);
      const w = Math.max(1, Math.round(boardWidth * scale));
      const h = Math.max(1, Math.round(boardHeight * scale));
      const thumb = card.thumb;
      if (thumb.width !== w || thumb.height !== h) { thumb.width = w; thumb.height = h; }
      thumb.style.width = w + 'px';
      thumb.style.height = h + 'px';
      const context = thumb.getContext('2d');
      context.clearRect(0, 0, w, h);
      context.imageSmoothingEnabled = false;
      const source = raster && !raster.failed ? rasterImage(raster) : null;
      if (source) context.drawImage(source, 0, 0, w, h);
    }

    function renderTimeline() {
      const list = $('pixelTimelineFrames');
      if (!list) return;
      if (!isAnimated()) {
        timelineCards.clear();
        timelineShown = '';
        list.replaceChildren();
        return;
      }
      const frames = orderedFrames();
      const alive = new Set(frames.map((frame) => frame.id));
      for (const [id, card] of timelineCards) {
        if (!alive.has(id)) { card.button.remove(); timelineCards.delete(id); }
      }
      const boardKey = boardWidth + '×' + boardHeight;
      frames.forEach((frame, position) => {
        let card = timelineCards.get(frame.id);
        if (!card) {
          const id = frame.id;
          const button = document.createElement('button');
          button.type = 'button';
          button.className = 'pixel-timeline-frame';
          button.dataset.frameId = id;
          const indexLabel = document.createElement('span');
          indexLabel.className = 'pixel-timeline-index';
          const thumb = document.createElement('canvas');
          thumb.className = 'pixel-timeline-thumb';
          thumb.style.imageRendering = 'pixelated';
          const delayLabel = document.createElement('span');
          delayLabel.className = 'pixel-timeline-delay';
          const stateLabel = document.createElement('span');
          stateLabel.className = 'pixel-timeline-state';
          button.append(indexLabel, thumb, delayLabel, stateLabel);
          button.addEventListener('click', () => selectFrame(id));
          card = { button, indexLabel, thumb, delayLabel, stateLabel, key: '', raster: null, rasterVersion: -1, boardKey: '' };
          timelineCards.set(id, card);
        }
        const visible = visibleForPreview(frame);
        const key = [position + 1, visible ? 1 : 0, frame.delayMult || 1, frame.name || '', frame.locked ? 1 : 0].join('|');
        if (card.key !== key) {
          card.key = key;
          card.indexLabel.textContent = String(position + 1);
          card.delayLabel.textContent = '×' + (frame.delayMult || 1);
          card.button.classList.toggle('is-hidden', !visible);
          card.button.title = '帧 ' + (position + 1) + (frame.name ? ' · ' + frame.name : '')
            + ' · 时长 ×' + (frame.delayMult || 1) + (visible ? '' : ' · 已隐藏') + (frame.locked ? ' · 已锁定' : '');
          card.button.setAttribute('aria-label', card.button.title);
        }
        const isActive = frame.id === activeID;
        const isPlaying = playing && frame.id === playbackID;
        card.button.classList.toggle('active', isActive);
        card.button.classList.toggle('playing', isPlaying);
        card.button.setAttribute('aria-pressed', String(isActive));
        const state = (isActive ? '当前' : '') + (isPlaying ? (isActive ? ' · 播放' : '播放') : '') + (!visible ? ((isActive || isPlaying) ? ' · 隐藏' : '隐藏') : '');
        if (card.stateLabel.textContent !== state) card.stateLabel.textContent = state;
        // 缩略图复用 raster 缓存：仅当缓存版本变化、画布尺寸变化或当前帧被编辑时重绘，避免整表重复解码。
        const raster = layers.get(frame.id);
        const live = isActive && raster?.canvas && !raster.loading;
        if (card.raster !== raster || card.rasterVersion !== (raster?.version || 0) || card.boardKey !== boardKey || live) {
          card.raster = raster;
          card.rasterVersion = raster?.version || 0;
          card.boardKey = boardKey;
          drawTimelineThumb(card, raster);
        }
        const current = list.children[position];
        if (current !== card.button) list.insertBefore(card.button, current || null);
      });
      const shown = playbackID || activeID;
      if (timelineShown !== shown) {
        timelineShown = shown;
        timelineCards.get(shown)?.button.scrollIntoView({ block: 'nearest', inline: 'nearest' });
      }
    }

    function updateTimelineButtons(editable) {
      const order = orderedFrames();
      const shown = playbackID || activeID;
      const index = order.findIndex((frame) => frame.id === shown);
      const activeIndex = order.findIndex((frame) => frame.id === activeID);
      const full = order.length >= maxFrameCount;
      const set = (id, disabled) => { const node = $(id); if (node) node.disabled = disabled; };
      set('pixelAnimPrev', !editable || index <= 0);
      set('pixelAnimNext', !editable || index < 0 || index >= order.length - 1);
      set('pixelFrameAdd', !editable || full);
      set('pixelFrameDup', !editable || !activeID || full);
      set('pixelFrameDel', !editable || !activeID);
      set('pixelFrameLeft', !editable || activeIndex <= 0);
      set('pixelFrameRight', !editable || activeIndex < 0 || activeIndex >= order.length - 1);
    }

    function scheduleNextFrame() {
      const order = visibleFrames();
      const index = order.findIndex((frame) => frame.id === playbackID);
      const frame = order[index];
      if (!playing || !frame) return stopPlayback();
      const { fps, loop } = animation();
      playbackTimer = setTimeout(() => {
        if (index + 1 >= order.length && !loop) { stopPlayback(true); return; }
        playbackID = order[(index + 1) % order.length].id;
        updateControls();
        schedulePaint();
        scheduleNextFrame();
      }, 1000 / fps * (frame.delayMult || 1));
    }

    function stopPlayback(hold = false) {
      clearTimeout(playbackTimer);
      if (!playing && !playbackID) return;
      playing = false;
      if (!hold) playbackID = '';
      updateControls();
      schedulePaint();
    }

    async function togglePlayback() {
      if (playing) {
        const frameId = playbackID;
        stopPlayback(true);
        if (frameId !== activeID) await mutate('/api/pixel/frames', { action: 'active', frameId }, 'POST', { keepPlayback: true });
        return;
      }
      if (!ownsEdits()) return;
      try { await flush(); } catch (error) { displayError(error); return; }
      const frame = visibleFrames()[0];
      if (!isActive() || !isAnimated() || !frame || running) return;
      playbackID = frame.id;
      playing = true;
      updateControls();
      schedulePaint();
      scheduleNextFrame();
    }

    function viewWindow() {
      const box = stage.getBoundingClientRect();
      const view = viewport.getBoundingClientRect();
      const left = Math.max(0, Math.floor(view.left - box.left));
      const top = Math.max(0, Math.floor(view.top - box.top));
      const right = Math.min(box.width, Math.ceil(view.left + viewport.clientWidth - box.left));
      const bottom = Math.min(box.height, Math.ceil(view.top + viewport.clientHeight - box.top));
      return { box, view, left, top, width: Math.max(0, right - left), height: Math.max(0, bottom - top),
        scaleX: box.width / boardWidth, scaleY: box.height / boardHeight };
    }

    function drawRulers(region) {
      const { box, view, left, top, width, height, scaleX, scaleY } = region;
      const sheetBounds = sheet.getBoundingClientRect();
      const color = getComputedStyle(viewport).getPropertyValue('--muted').trim();
      const step = [1, 2, 4, 8, 16, 32, 64, 128, 256].find((n) => n * zoom >= 30) || 256;
      const rw = $('pixelRulerX');
      const rh = $('pixelRulerY');
      rw.width = width;
      rw.height = 24;
      rh.width = 24;
      rh.height = height;
      rw.style.left = box.left - sheetBounds.left + left + 'px';
      rw.style.top = Math.max(view.top, box.top - 24) - sheetBounds.top + 'px';
      rh.style.left = Math.max(view.left, box.left - 24) - sheetBounds.left + 'px';
      rh.style.top = box.top - sheetBounds.top + top + 'px';
      const xctx = rw.getContext('2d');
      const yctx = rh.getContext('2d');
      if (!width || !height) return;
      xctx.font = yctx.font = '9px monospace';
      xctx.fillStyle = yctx.fillStyle = color || '#888';
      for (let x = Math.ceil(left / scaleX / step) * step; x < boardWidth && x * scaleX <= left + width; x += step) {
        const dx = Math.round(x * scaleX - left);
        xctx.fillRect(dx, 18, 1, 6);
        xctx.fillText(String(x), dx + 2, 15);
      }
      for (let y = Math.ceil(top / scaleY / step) * step; y < boardHeight && y * scaleY <= top + height; y += step) {
        const dy = Math.round(y * scaleY - top);
        yctx.fillRect(18, dy, 6, 1);
        yctx.fillText(String(y), 1, dy + 10);
      }
    }

    function refreshViewport() {
      if (scrollFrame) return;
      scrollFrame = requestAnimationFrame(() => {
        scrollFrame = 0;
        if (!hasBoard() || !isActive() || !viewport.clientWidth) return;
        const region = viewWindow();
        drawRulers(region);
        drawDecoration(region);
      });
    }

    function viewportCenter() {
      const bounds = viewport.getBoundingClientRect();
      return { x: bounds.left + viewport.clientWidth / 2, y: bounds.top + viewport.clientHeight / 2 };
    }

    function positionCanvas(x, y, anchor) {
      const bounds = stage.getBoundingClientRect();
      viewport.scrollLeft += bounds.left + x * bounds.width - anchor.x;
      viewport.scrollTop += bounds.top + y * bounds.height - anchor.y;
    }

    function showZoom() {
      if (!editingZoom) zoomInput.value = String(Number((zoom * 100).toFixed(2)));
    }

    function layout() {
      if (!hasBoard() || !isActive() || !viewport.clientWidth || !viewport.clientHeight) return;
      // Keep enough room on every side to anchor zoom and drag a fitted canvas.
      sheet.style.setProperty('--pixel-pad-x', Math.ceil(viewport.clientWidth / 2) + 'px');
      sheet.style.setProperty('--pixel-pad-y', Math.ceil(viewport.clientHeight / 2) + 'px');
      if (fit) {
        const ratio = Math.min((viewport.clientWidth - 50) / boardWidth, (viewport.clientHeight - 50) / boardHeight);
        zoom = clamp(ratio, minZoom, maxZoom);
      }
      const pixelWidth = Math.max(1, Math.round(boardWidth * zoom));
      const pixelHeight = Math.max(1, Math.round(boardHeight * zoom));
      stage.style.width = pixelWidth + 'px';
      stage.style.height = pixelHeight + 'px';
      showZoom();
      if (fit) positionCanvas(0.5, 0.5, viewportCenter());
      refreshViewport();
    }

    function changeZoom(value, anchor = viewportCenter()) {
      if (!hasBoard()) return;
      const oldBox = stage.getBoundingClientRect();
      if (!oldBox.width || !oldBox.height) return;
      const x = (anchor.x - oldBox.left) / oldBox.width;
      const y = (anchor.y - oldBox.top) / oldBox.height;
      fit = false;
      zoom = clamp(value, minZoom, maxZoom);
      layout();
      positionCanvas(x, y, anchor);
    }

    function finishZoomInput() {
      if (editingZoom) {
        editingZoom = false;
        const text = zoomInput.value.trim().replace(/[%％]$/, '').trim().replace(',', '.');
        if (/^(?:\d+(?:\.\d*)?|\.\d+)$/.test(text)) {
          const value = Number(text);
          if (Number.isFinite(value) && value > 0) changeZoom(value / 100);
        }
      }
      showZoom();
    }

    function cancelWheelZoom() {
      cancelAnimationFrame(wheelFrame);
      wheelFrame = 0;
      wheelDelta = 0;
    }

    function drawOverlay() {
      const context = overlay.getContext('2d');
      context.clearRect(0, 0, boardWidth, boardHeight);
      if (!overlayImage || !boardWidth || !boardHeight) return;
      const scale = Math.min(boardWidth / overlayImage.naturalWidth, boardHeight / overlayImage.naturalHeight);
      const mode = $('pixelSamplingFit').value;
      const cover = Math.max(boardWidth / overlayImage.naturalWidth, boardHeight / overlayImage.naturalHeight);
      const renderScale = mode === 'cover' ? cover : scale;
      const width = mode === 'stretch' ? boardWidth : Math.max(1, Math.round(overlayImage.naturalWidth * renderScale));
      const height = mode === 'stretch' ? boardHeight : Math.max(1, Math.round(overlayImage.naturalHeight * renderScale));
      context.imageSmoothingEnabled = false;
      context.drawImage(overlayImage, Math.floor((boardWidth - width) / 2), Math.floor((boardHeight - height) / 2), width, height);
      overlay.style.opacity = String(Number($('pixelOverlayOpacity').value) / 100);
    }

    function drawDecoration({ left, top, width, height, scaleX: dx, scaleY: dy } = viewWindow()) {
      if (decoration.width !== width) decoration.width = width;
      if (decoration.height !== height) decoration.height = height;
      decoration.style.left = left + 'px';
      decoration.style.top = top + 'px';
      decoration.style.width = width + 'px';
      decoration.style.height = height + 'px';
      if (!width || !height) return;
      const context = decoration.getContext('2d');
      context.clearRect(0, 0, decoration.width, decoration.height);
      context.save();
      context.translate(-left, -top);
      if ($('pixelGrid').checked && zoom >= 6) {
        context.beginPath();
        context.strokeStyle = 'rgba(120,130,150,.4)';
        for (let x = Math.max(1, Math.ceil(left / dx)); x < boardWidth && x * dx <= left + width; x++) {
          context.moveTo(x * dx, top);
          context.lineTo(x * dx, top + height);
        }
        for (let y = Math.max(1, Math.ceil(top / dy)); y < boardHeight && y * dy <= top + height; y++) {
          context.moveTo(left, y * dy);
          context.lineTo(left + width, y * dy);
        }
        context.stroke();
      }
      if (selection) {
        const { x, y, w, h, mask } = selection;
        context.beginPath();
        if (!mask) context.rect(x * dx, y * dy, w * dx, h * dy);
        else {
          for (let j = Math.max(0, Math.floor(top / dy) - y); j < Math.min(h, Math.ceil((top + height) / dy) - y); j++) {
            for (let i = Math.max(0, Math.floor(left / dx) - x); i < Math.min(w, Math.ceil((left + width) / dx) - x); i++) {
              const k = j * w + i;
              if (!mask[k]) continue;
              const a = (x + i) * dx;
              const b = (y + j) * dy;
              if (j === 0 || !mask[k - w]) { context.moveTo(a, b); context.lineTo(a + dx, b); }
              if (i === 0 || !mask[k - 1]) { context.moveTo(a, b); context.lineTo(a, b + dy); }
              if (j + 1 === h || !mask[k + w]) { context.moveTo(a, b + dy); context.lineTo(a + dx, b + dy); }
              if (i + 1 === w || !mask[k + 1]) { context.moveTo(a + dx, b); context.lineTo(a + dx, b + dy); }
            }
          }
        }
        context.setLineDash([]);
        context.strokeStyle = '#161b22';
        context.lineWidth = 2;
        context.stroke();
        context.setLineDash([4, 4]);
        context.strokeStyle = '#ffffff';
        context.lineWidth = 1;
        context.stroke();
        context.setLineDash([]);
      }
      if (lastHighlight) {
        const { x, y, w, h } = lastHighlight;
        context.strokeStyle = '#2f81f7';
        context.lineWidth = 2;
        context.strokeRect(x * dx, y * dy, w * dx, h * dy);
      }
      if (cursor && !running && !stroke && zoom >= 4 && ['pencil', 'eraser', ...shapes].includes(tool)) {
        context.strokeStyle = tool === 'eraser' ? '#ffffff' : primary.slice(0, 7);
        context.lineWidth = 1;
        context.strokeRect(cursor.x * dx, cursor.y * dy, dx, dy);
      }
      context.restore();
    }

    function schedulePaint(updateComposite = true, region = null) {
      if (updateComposite) {
        compositeRegion = compositeDirty && compositeRegion && region ? union(compositeRegion, region)
          : compositeDirty ? null : region;
        compositeDirty = true;
      }
      if (paintFrame) return;
      paintFrame = requestAnimationFrame(() => {
        paintFrame = 0;
        if (!boardWidth || !boardHeight || !isActive()) return;
        if (compositeDirty) {
          compositeDirty = false;
          const rect = compositeRegion || { x: 0, y: 0, w: boardWidth, h: boardHeight };
          compositeRegion = null;
          compositeCtx.clearRect(rect.x, rect.y, rect.w, rect.h);
          compositeCtx.imageSmoothingEnabled = false;
          if (isAnimated()) {
            const current = layers.get(playbackID || activeID);
            if (rasterImage(current)) drawRaster(compositeCtx, rasterImage(current), rect);
            drawOnion(rect);
          } else for (const layer of [...entries()].sort((a, b) => a.index - b.index)) {
            const raster = layers.get(layer.id);
            if (!visibleForPreview(layer) || !rasterImage(raster)) continue;
            compositeCtx.globalAlpha = layer.opacity ?? 1;
            compositeCtx.globalCompositeOperation = layer.blend && layer.blend !== 'normal' ? layer.blend : 'source-over';
            drawRaster(compositeCtx, rasterImage(raster), rect);
          }
          compositeCtx.globalAlpha = 1;
          compositeCtx.globalCompositeOperation = 'source-over';
        }
        drawDecoration();
        if (cursor) {
          const sample = compositeCtx.getImageData(cursor.x, cursor.y, 1, 1).data;
          $('pixelSampledColor').textContent = toHex(sample);
        }
      });
    }

    function drawRaster(context, canvas, rect) {
      const { x, y, w, h } = rect;
      context.drawImage(canvas, x, y, w, h, x, y, w, h);
    }

    function drawOnion(rect) {
      const context = onion.getContext('2d');
      context.clearRect(rect.x, rect.y, rect.w, rect.h);
      if (playing || running || !$('pixelAnimOnion').checked) return;
      const current = layerMetadata(playbackID || activeID);
      const previousFrames = visibleFrames().filter((frame) => frame.index < current?.index);
      const previous = previousFrames[previousFrames.length - 1];
      const raster = layers.get(previous?.id);
      if (rasterImage(raster)) drawRaster(context, rasterImage(raster), rect);
    }

    function rasterImage(raster) {
      return raster?.canvas || raster?.image;
    }

    function editableRaster(raster) {
      if (!raster.canvas) {
        raster.canvas = canvas2D(boardWidth, boardHeight);
        raster.context = raster.canvas.getContext('2d', { willReadFrequently: true });
        if (raster.image) raster.context.drawImage(raster.image, 0, 0);
        raster.image = null;
      }
      if (!raster.pixels) raster.pixels = raster.context.getImageData(0, 0, boardWidth, boardHeight);
      return raster;
    }
    function snapRect(source, rect) {
      const bytes = new Uint8ClampedArray(rect.w * rect.h * 4);
      for (let row = 0; row < rect.h; row++) {
        const start = ((rect.y + row) * boardWidth + rect.x) * 4;
        bytes.set(source.subarray(start, start + rect.w * 4), row * rect.w * 4);
      }
      return bytes;
    }
    function applyBytes(raster, bytes, rect) {
      for (let row = 0; row < rect.h; row++) {
        const start = ((rect.y + row) * boardWidth + rect.x) * 4;
        raster.pixels.data.set(bytes.subarray(row * rect.w * 4, (row + 1) * rect.w * 4), start);
      }
      raster.context.putImageData(raster.pixels, 0, 0, rect.x, rect.y, rect.w, rect.h);
    }

    function displayAssetOptions() {
      const assets = [project?.pixel?.mainImage, ...(project?.pixel?.refImages || [])].filter(Boolean);
      const oldChoice = $('pixelReferenceSelect').value;
      const oldPaletteSource = $('pixelPaletteSource').value;
      $('pixelMainThumb').replaceChildren();
      $('pixelRefThumbs').replaceChildren();
      $('pixelReferenceSelect').replaceChildren();
      $('pixelPaletteSource').replaceChildren(new Option('当前画布', 'composite'));
      for (const asset of assets) {
        const refName = asset.id === 'main' ? '主图' : '参考图 #' + asset.id.replace(/^ref_/, '');
        const name = asset.name || refName;
        const item = document.createElement('div');
        item.className = 'pixel-thumb';
        const image = document.createElement('img');
        image.src = asset.url;
        image.title = name;
        image.alt = name;
        const tag = document.createElement('button');
        tag.type = 'button';
        tag.textContent = asset.id === 'main' ? '主图' : '#' + asset.id.replace(/^ref_/, '');
        tag.title = asset.id === 'main' ? '分屏查看主图' : '把' + refName + '插入提示词';
        tag.addEventListener('click', () => {
          $('pixelReferenceSelect').value = asset.id;
          if (asset.id === 'main') { compare = true; renderReference(); }
          else {
            const prompt = $('prompt');
            prompt.setRangeText('[' + refName + ']', prompt.selectionStart, prompt.selectionEnd, 'end');
            prompt.dispatchEvent(new Event('input', { bubbles: true }));
            prompt.focus();
          }
        });
        const remove = document.createElement('button');
        remove.type = 'button';
        remove.textContent = '×';
        remove.setAttribute('aria-label', '删除' + refName);
        remove.addEventListener('click', () => mutate('/api/pixel/assets', { id: asset.id }, 'DELETE'));
        item.append(image, tag, remove);
        $(asset.id === 'main' ? 'pixelMainThumb' : 'pixelRefThumbs').append(item);
        $('pixelReferenceSelect').append(new Option(refName + (asset.name ? ' · ' + asset.name : ''), asset.id));
        $('pixelPaletteSource').append(new Option(refName, asset.id));
      }
      if (assets.some((asset) => asset.id === oldChoice)) $('pixelReferenceSelect').value = oldChoice;
      if (assets.some((asset) => asset.id === oldPaletteSource)) $('pixelPaletteSource').value = oldPaletteSource;
      renderReference();
    }

    function renderReference() {
      const assets = [project?.pixel?.mainImage, ...(project?.pixel?.refImages || [])].filter(Boolean);
      const asset = assets.find((entry) => entry.id === $('pixelReferenceSelect').value);
      $('pixelReference').classList.toggle('hidden', !compare || !asset);
      $('pixelCompare').setAttribute('aria-pressed', String(compare && Boolean(asset)));
      if (asset) $('pixelReferenceImage').src = asset.url;
      else $('pixelReferenceImage').removeAttribute('src');
      layout();
    }

    function renderPalette() {
      $('pixelPrimary').value = primary.slice(0, 7);
      $('pixelSecondary').value = secondary.slice(0, 7);
      $('pixelHex').value = primary;
      const swatches = $('pixelSwatches');
      swatches.replaceChildren();
      for (const color of project?.pixel?.palette || []) {
        const button = document.createElement('button');
        button.type = 'button';
        button.style.backgroundColor = color;
        button.title = color + ' · 左键设前景，右键设背景';
        button.setAttribute('aria-label', color);
        button.addEventListener('click', () => setColors(color, secondary));
        button.addEventListener('contextmenu', (event) => {
          event.preventDefault();
          setColors(primary, color);
        });
        swatches.append(button);
      }
    }

    function setColors(fg, bg) {
      if (primary === fg && secondary === bg) return;
      primary = fg;
      secondary = bg;
      renderPalette();
      if (hasBoard() && !running) {
        colorsDirty = true;
        debounceSync();
      }
    }

    function setProject(next, options = {}) {
      const old = project;
      if (!next?.open || next.engine !== 'pixel') next = null;
      const keyChanged = next?.projectId !== old?.projectId;
      const resized = next?.pixel?.width !== old?.pixel?.width || next?.pixel?.height !== old?.pixel?.height;
      const modeChanged = next?.pixel?.mode !== old?.pixel?.mode;
      if (!keyChanged && next && old && next.revision < old.revision) return;
      if (keyChanged || resized || modeChanged || options.reload) stopPlayback();
      if (keyChanged || resized || modeChanged) {
        cancelWheelZoom();
        generation++;
        selection = null;
        cursor = null;
        stroke = null;
        layers = new Map();
        if (!options.keepUndo) { undo = []; redo = []; }
        pending = [];
        previewVisibility.clear();
        remoteError = '';
        if (keyChanged) {
          fit = true;
          spaceDown = false;
          setTool('pan');
          compare = false;
          primary = next?.pixel?.primaryColor || '#ffffff';
          secondary = next?.pixel?.secondaryColor || '#000000';
          colorsDirty = false;
        }
      }
      project = next;
      if (!next) {
        activeID = '';
        loading = false;
        updateControls();
        return;
      }
      boardWidth = next.pixel.width;
      boardHeight = next.pixel.height;
      const previousActive = activeID;
      activeID = options.activeLayer || options.frameId || animation(next)?.activeFrameId || next.activeLayer || entries(next)[0]?.id || '';
      if (previousActive !== activeID && isAnimated()) playbackID = '';
      if (previousActive !== activeID) {
        const previousRaster = layers.get(previousActive);
        if (previousRaster) previousRaster.pixels = null;
      }
      if (animation(next)) {
        $('pixelAnimFPS').value = animation(next).fps;
        $('pixelAnimLoop').checked = animation(next).loop;
      }
      if (composite.width !== boardWidth || composite.height !== boardHeight) {
        composite.width = overlay.width = onion.width = boardWidth;
        composite.height = overlay.height = onion.height = boardHeight;
      }
      if (modeChanged) onion.getContext('2d').clearRect(0, 0, boardWidth, boardHeight);
      if (resized || keyChanged) {
        $('pixelWidth').value = boardWidth;
        $('pixelHeight').value = boardHeight;
        $('pixelCoordinates').textContent = boardWidth + '×' + boardHeight;
        $('pixelSampledColor').textContent = '—';
        const preset = boardWidth + '×' + boardHeight;
        $('pixelSizePreset').value = [...$('pixelSizePreset').options].some((option) => option.value === preset) ? preset : '';
      }
      if (next.pixel.draftMode && keyChanged) $('pixelDraft').value = next.pixel.draftMode;
      if (keyChanged || (next.pixel.fit && next.pixel.fit !== old?.pixel?.fit)) $('pixelSamplingFit').value = next.pixel.fit || 'fit';
      const newKey = JSON.stringify([next.pixel.mainImage, next.pixel.refImages]);
      if (assetKey !== newKey) {
        assetKey = newKey;
        overlayImage = null;
        const src = next.pixel.mainImage?.url;
        if (src) {
          loadImage(src).then((img) => {
            if (project?.pixel?.mainImage?.url === src) { overlayImage = img; drawOverlay(); }
          }).catch(() => {});
        } else drawOverlay();
        displayAssetOptions();
      }
      if (keyChanged || resized) drawOverlay();
      if (keyChanged || JSON.stringify(next.pixel.palette) !== JSON.stringify(old?.pixel?.palette)) renderPalette();
      const layerIDs = new Set(entries(next).map((layer) => layer.id));
      for (const id of layers.keys()) {
        if (!layerIDs.has(id)) layers.delete(id);
      }
      for (const layer of entries(next)) {
        const local = layers.get(layer.id);
        const previous = entries(old).find((item) => item.id === layer.id);
        const changedID = options.frameId || options.layerId;
        const changed = options.reload || keyChanged || resized || changedID === layer.id
          || (!options.local && (previous?.updated !== layer.updated || previous?.file !== layer.file));
        if (!local || (changed && !pending.some((entry) => entry.id === layer.id) && stroke?.id !== layer.id)) {
          if (local && !local.loading && !keyChanged && !resized && next.revision === old.revision + 1
              && changedID === layer.id && options.rgbaBase64 && options.region) {
            const bytes = Uint8ClampedArray.from(atob(options.rgbaBase64), (char) => char.charCodeAt(0));
            applyBytes(editableRaster(local), bytes, options.region);
          } else {
            const fallback = changedID === layer.id && options.pngBase64
              ? 'data:image/png;base64,' + options.pngBase64 : null;
            readLayer(layer.id, fallback);
          }
        }
      }
      loading = [...layers.values()].some((item) => item.loading);
      if (keyChanged || resized || modeChanged) layout();
      updateControls();
      schedulePaint();
    }

    function readLayer(id, dataURI) {
      const which = generation;
      const raster = layers.get(id) || { version: 0 };
      layers.set(id, raster);
      const version = ++raster.version;
      raster.loading = true;
      raster.failed = false;
      loading = true;
      const query = rasterQuery(id);
      const load = dataURI ? loadImage(dataURI) : loadRaster(query).catch((error) => {
        if (generation !== which || raster.version !== version || String(project.revision) === query.get('revision')) throw error;
        query.set('revision', String(project.revision));
        return loadRaster(query);
      });
      load.then((result) => {
        if (generation !== which || raster.version !== version || !layers.has(id)) return;
        if (result.naturalWidth !== boardWidth || result.naturalHeight !== boardHeight) throw new Error('图层像素尺寸不匹配');
        raster.canvas = raster.context = raster.pixels = null;
        raster.image = result;
        schedulePaint();
      }).catch((error) => {
        if (generation === which && raster.version === version) {
          raster.failed = true;
          displayError(error);
        }
      }).finally(() => {
        if (generation !== which || raster.version !== version) return;
        raster.loading = false;
        loading = [...layers.values()].some((item) => item.loading);
        updateControls();
      });
    }

    async function loadRaster(query) {
      const response = await fetch('/api/pixel/layer?' + query, { cache: 'no-store' });
      if (response.ok) {
        const url = URL.createObjectURL(await response.blob());
        try { return await loadImage(url); } finally { URL.revokeObjectURL(url); }
      }
      throw new Error('读取图层失败（' + response.status + '）');
    }

    function point(event, bounded = true) {
      const bounds = stage.getBoundingClientRect();
      const x = Math.floor((event.clientX - bounds.left) * boardWidth / bounds.width);
      const y = Math.floor((event.clientY - bounds.top) * boardHeight / bounds.height);
      if (bounded) return { x: clamp(x, 0, boardWidth - 1), y: clamp(y, 0, boardHeight - 1) };
      return { x, y };
    }
    function selected(x, y, current = selection) {
      if (!current) return true;
      const gx = x - current.x;
      const gy = y - current.y;
      return gx >= 0 && gy >= 0 && gx < current.w && gy < current.h && (!current.mask || current.mask[gy * current.w + gx]);
    }
    function writePoint(edit, x, y, rgba, mask = selection) {
      if (x < 0 || y < 0 || x >= boardWidth || y >= boardHeight || !selected(x, y, mask)) return;
      const k = (y * boardWidth + x) * 4;
      const pixels = edit.raster.pixels.data;
      if (rgba.every((value, offset) => pixels[k + offset] === value)) return;
      const tileX = Math.floor(x / tileSize) * tileSize;
      const tileY = Math.floor(y / tileSize) * tileSize;
      const tileID = tileY * boardWidth + tileX;
      if (!edit.before.has(tileID)) {
        const rect = { x: tileX, y: tileY, w: Math.min(tileSize, boardWidth - tileX), h: Math.min(tileSize, boardHeight - tileY) };
        edit.before.set(tileID, { rect, bytes: snapRect(pixels, rect) });
      }
      pixels.set(rgba, k);
      edit.left = Math.min(edit.left, x);
      edit.right = Math.max(edit.right, x);
      edit.top = Math.min(edit.top, y);
      edit.bottom = Math.max(edit.bottom, y);
    }
    function colorDot(edit, rgba, x, y) {
      writePoint(edit, x, y, rgba);
      if (edit.mirrorX) writePoint(edit, boardWidth - 1 - x, y, rgba);
      if (edit.mirrorY) writePoint(edit, x, boardHeight - 1 - y, rgba);
      if (edit.mirrorX && edit.mirrorY) writePoint(edit, boardWidth - 1 - x, boardHeight - 1 - y, rgba);
    }
    function stamp(edit, x, y) {
      const before = -Math.floor((edit.thickness - 1) / 2);
      for (let j = before; j < before + edit.thickness; j++) {
        for (let i = before; i < before + edit.thickness; i++) colorDot(edit, edit.rgba, x + i, y + j);
      }
    }
    function editRect(edit) {
      if (edit.left > edit.right) return null;
      return { x: edit.left, y: edit.top, w: edit.right - edit.left + 1, h: edit.bottom - edit.top + 1 };
    }
    function paintEdit(edit) {
      const rect = editRect(edit);
      if (rect) edit.raster.context.putImageData(edit.raster.pixels, 0, 0, rect.x, rect.y, rect.w, rect.h);
      schedulePaint(!!rect, rect);
    }
    function resetPreview(edit) {
      const rect = editRect(edit);
      if (!rect) return;
      for (const tile of edit.before.values()) applyBytes(edit.raster, tile.bytes, tile.rect);
      edit.before.clear();
      edit.left = edit.top = Infinity;
      edit.right = edit.bottom = -1;
      schedulePaint(true, rect);
    }

    function maskFor(start, pixels, clip) {
      if (!selected(start.x, start.y, clip)) return null;
      const count = boardWidth * boardHeight;
      const startAt = start.y * boardWidth + start.x;
      const target = pixels.subarray(startAt * 4, startAt * 4 + 4);
      const tolerance = clamp(Number($('pixelTolerance').value) || 0, 0, 255);
      const match = (index) => {
        const x = index % boardWidth;
        const y = Math.floor(index / boardWidth);
        if (!selected(x, y, clip)) return false;
        const offset = index * 4;
        return target.every((value, channel) => Math.abs(value - pixels[offset + channel]) <= tolerance);
      };
      const mask = new Uint8Array(count);
      if (!$('pixelContiguous').checked) {
        for (let i = 0; i < count; i++) mask[i] = Number(match(i));
        return mask;
      }
      const queue = new Uint32Array(count);
      const visited = new Uint8Array(count);
      let read = 0;
      let write = 1;
      queue[0] = startAt;
      visited[startAt] = 1;
      const visit = (index) => {
        if (visited[index]) return;
        visited[index] = 1;
        if (match(index)) queue[write++] = index;
      };
      while (read < write) {
        const index = queue[read++];
        mask[index] = 1;
        const col = index % boardWidth;
        if (col > 0) visit(index - 1);
        if (col + 1 < boardWidth) visit(index + 1);
        if (index >= boardWidth) visit(index - boardWidth);
        if (index + boardWidth < count) visit(index + boardWidth);
      }
      return mask;
    }

    function normalizeSelection(mask) {
      let left = boardWidth;
      let right = -1;
      let top = boardHeight;
      let bottom = -1;
      for (let k = 0; k < mask.length; k++) {
        if (!mask[k]) continue;
        const x = k % boardWidth;
        const y = Math.floor(k / boardWidth);
        left = Math.min(left, x);
        right = Math.max(right, x);
        top = Math.min(top, y);
        bottom = Math.max(bottom, y);
      }
      if (right < 0) return null;
      const box = { x: left, y: top, w: right - left + 1, h: bottom - top + 1 };
      const cropped = new Uint8Array(box.w * box.h);
      for (let row = 0; row < box.h; row++) {
        const start = (top + row) * boardWidth + left;
        cropped.set(mask.subarray(start, start + box.w), row * box.w);
      }
      if (cropped.includes(0)) box.mask = cropped;
      return box;
    }

    function select(next = null) {
      selection = next;
      updateControls();
      schedulePaint(false);
    }

    function selectPayload() {
      if (!selection) return undefined;
      const { mask, ...box } = selection;
      if (!mask) return box;
      const runs = [];
      let index = 0;
      while (index < mask.length) {
        if (!mask[index]) { index++; continue; }
        const start = index++;
        while (index < mask.length && mask[index]) index++;
        runs.push([start, index - start]);
      }
      return { ...box, runs };
    }

    function beginEdit(start, chosenColor, chosenTool) {
      const layer = layerMetadata();
      const raster = layers.get(activeID);
      if (!ownsEdits() || !layer || !raster || raster.loading || raster.failed || layer.locked || (!isAnimated() && !layer.visible)) return null;
      return {
        id: activeID, raster: editableRaster(raster), before: new Map(), start, last: start, tool: chosenTool,
        rgba: chosenTool === 'eraser' ? zero : toRGBA(chosenColor),
        thickness: clamp(Number($('pixelThickness').value) || 1, 1, 8),
        mirrorX: $('pixelMirrorX').checked, mirrorY: $('pixelMirrorY').checked,
        left: Infinity, right: -1, top: Infinity, bottom: -1,
      };
    }

    function moveSelection(edit, pointer) {
      resetPreview(edit);
      const deltaX = pointer.x - edit.start.x;
      const deltaY = pointer.y - edit.start.y;
      if (!deltaX && !deltaY) { select(edit.oldSelection); return; }
      const source = edit.oldSelection;
      for (let y = source.y; y < source.y + source.h; y++) {
        for (let x = source.x; x < source.x + source.w; x++) {
          if (selected(x, y, source)) writePoint(edit, x, y, zero, null);
        }
      }
      const left = Math.max(0, source.x + deltaX);
      const right = Math.min(boardWidth, source.x + source.w + deltaX);
      const top = Math.max(0, source.y + deltaY);
      const bottom = Math.min(boardHeight, source.y + source.h + deltaY);
      const next = right > left && bottom > top ? { x: left, y: top, w: right - left, h: bottom - top } : null;
      if (next && source.mask) next.mask = new Uint8Array(next.w * next.h);
      for (let y = source.y; y < source.y + source.h; y++) {
        for (let x = source.x; x < source.x + source.w; x++) {
          if (!selected(x, y, source)) continue;
          const nx = x + deltaX;
          const ny = y + deltaY;
          if (nx < 0 || nx >= boardWidth || ny < 0 || ny >= boardHeight) continue;
          const index = ((y - source.y) * source.w + x - source.x) * 4;
          const rgba = edit.movePixels.subarray(index, index + 4);
          if (rgba[3]) writePoint(edit, nx, ny, rgba, null);
          if (next?.mask) next.mask[(ny - next.y) * next.w + nx - next.x] = 1;
        }
      }
      paintEdit(edit);
      select(next?.mask && !next.mask.includes(1) ? null : next);
    }

    function retainHistory(entry) {
      undo.push(entry);
      redo = [];
      let bytes = undo.reduce((sum, change) => sum + (change.before?.length || 0) + (change.after?.length || 0), 0);
      while (undo.length > 60 || bytes > undoBudget) {
        const oldest = undo.shift();
        bytes -= (oldest.before?.length || 0) + (oldest.after?.length || 0);
      }
      updateControls();
    }

    function finishEdit(edit) {
      const rect = editRect(edit);
      if (!rect) return;
      const after = snapRect(edit.raster.pixels.data, rect);
      const before = after.slice();
      for (const tile of edit.before.values()) {
        const left = Math.max(rect.x, tile.rect.x);
        const right = Math.min(rect.x + rect.w, tile.rect.x + tile.rect.w);
        const top = Math.max(rect.y, tile.rect.y);
        const bottom = Math.min(rect.y + rect.h, tile.rect.y + tile.rect.h);
        for (let y = top; y < bottom; y++) {
          const source = ((y - tile.rect.y) * tile.rect.w + left - tile.rect.x) * 4;
          const target = ((y - rect.y) * rect.w + left - rect.x) * 4;
          before.set(tile.bytes.subarray(source, source + (right - left) * 4), target);
        }
      }
      if (before.every((byte, index) => byte === after[index])) return;
      const change = { id: edit.id, rect, before, after };
      retainHistory(change);
      queueEdit(change.id, rect, after);
    }

    function queueEdit(id, rect, bytes) {
      const last = pending[pending.length - 1];
      const combine = last && !last.sent && last.id === id;
      const dirtyRect = combine ? union(last.rect, rect) : rect;
      const raster = layers.get(id);
      const payload = { id, rect: dirtyRect, animated: isAnimated() };
      if (dirtyRect.w * dirtyRect.h * 4 > partialUploadBytes) {
        payload.png = encodePNG(raster.canvas);
        payload.png.catch(() => {});
      } else payload.bytes = combine ? snapRect(raster.pixels.data, dirtyRect) : bytes;
      if (combine) pending[pending.length - 1] = payload;
      else pending.push(payload);
      remoteError = '';
      debounceSync();
      updateControls();
    }

    function debounceSync() {
      clearTimeout(syncTimer);
      syncTimer = setTimeout(() => flush().catch(displayError), 350);
    }

    function rasterQuery(id, version = project) {
      const target = version.pixel?.mode === 'anim' ? { frameId: id } : { id };
      const query = new URLSearchParams({ ...target, projectId: version.projectId, revision: String(version.revision) });
      if (token) query.set('k', token);
      return query;
    }

    async function recoverPending(batch, version) {
      const validate = (snapshot) => snapshot?.projectId === version.projectId
        && snapshot.revision === version.revision + 1
        && snapshot.pixel.width === boardWidth && snapshot.pixel.height === boardHeight;
      const before = (await api('/api/project'))?.project;
      if (!validate(before)) return null;
      const image = await loadRaster(rasterQuery(batch.id, before));
      const { x, y, w, h } = batch.rect;
      const full = batch.png ? { x: 0, y: 0, w: boardWidth, h: boardHeight } : { x, y, w, h };
      const canvas = canvas2D(full.w, full.h);
      const ctx = canvas.getContext('2d', { willReadFrequently: true });
      ctx.drawImage(image, -full.x, -full.y);
      const remote = ctx.getImageData(0, 0, full.w, full.h).data;
      ctx.clearRect(0, 0, full.w, full.h);
      if (batch.png) {
        const url = URL.createObjectURL(await batch.png);
        try { ctx.drawImage(await loadImage(url), 0, 0); }
        finally { URL.revokeObjectURL(url); }
      } else ctx.putImageData(new ImageData(batch.bytes, w, h), 0, 0);
      const expected = ctx.getImageData(0, 0, full.w, full.h).data;
      if (!remote.every((byte, index) => byte === expected[index])) return null;
      const after = (await api('/api/project'))?.project;
      return validate(after) ? { project: after } : null;
    }

    async function sendPending() {
      while (pending.length) {
        const batch = pending[0];
        batch.sent = true;
        const version = requestWithRevision();
        const target = batch.animated ? { frameId: batch.id } : { layerId: batch.id };
        const bitmap = batch.png
          ? { pngBase64: byte64(new Uint8Array(await (await batch.png).arrayBuffer())) }
          : { region: batch.rect, rgbaBase64: byte64(batch.bytes) };
        let result;
        try {
          result = await api('/api/pixel/layer', {
            method: 'PUT',
            body: { ...version, ...target, ...bitmap },
          });
        } catch (error) {
          if (!error.status || error.status === 409) result = await recoverPending(batch, version).catch(() => null);
          if (!result) throw error;
        }
        pending.shift();
        loadResult(result, { local: true, activeLayer: activeID });
      }
    }

    function flush() {
      if (stroke?.raster) finishPointer(null, false);
      clearTimeout(syncTimer);
      return serialize(async () => {
        if (!pending.length && !colorsDirty) return;
        syncing = true;
        updateControls();
        try {
          await sendPending();
          if (colorsDirty) {
            const body = requestWithRevision({ action: 'save', primaryColor: primary, secondaryColor: secondary });
            const result = await api('/api/pixel/palette', { method: 'POST', body });
            if (body.primaryColor === primary && body.secondaryColor === secondary) colorsDirty = false;
            loadResult(result, { local: true, activeLayer: activeID });
          }
        }
        finally { syncing = false; updateControls(); }
      });
    }

    async function mutate(path, body, method = 'POST', options) {
      if (!hasBoard() || running || busy) {
        if (!hasBoard()) openProject();
        return null;
      }
      busy = true;
      if (!options?.keepPlayback) stopPlayback();
      remoteError = '';
      updateControls();
      try {
        const wasResizing = path === '/api/pixel/resize';
        await flush();
        const result = await serialize(() => api(path, { method, body: requestWithRevision(body) }));
        loadResult(result, options);
        const replacesCheckpoint = path === '/api/pixel/quantize'
          || (path === '/api/layers/ops' && body.op !== 'active')
          || (path === '/api/pixel/frames' && !['active', 'fps', 'loop'].includes(body.action))
          || (path === '/api/pixel/palette' && (body.action === 'extract' || body.colors !== undefined));
        if (result?.project && replacesCheckpoint) {
          const deleted = (body.op || body.action) === 'delete' ? (body.id || body.frameId) : '';
          const keep = (entry) => !entry.ai && entry.id !== deleted;
          undo = undo.filter(keep);
          redo = redo.filter(keep);
        }
        if (wasResizing && result?.project) toast('尺寸已调整，选区与撤销记录已清空');
        return result;
      } catch (error) {
        displayError(error);
        return null;
      } finally {
        busy = false;
        updateControls();
      }
    }

    async function stepHistory(forward) {
      const from = forward ? redo : undo;
      const to = forward ? undo : redo;
      const entry = from[from.length - 1];
      if (!ownsEdits() || !entry) return;
      if (entry.ai) {
        const result = await mutate('/api/pixel/checkpoint', { action: forward ? 'redo' : 'undo' }, 'POST', { reload: true, keepUndo: true });
        if (!result) return;
      } else {
        const raster = layers.get(entry.id);
        const layer = layerMetadata(entry.id);
        if (!raster || !layer || layer.locked) {
          toast('要撤销或重做的图层已删除或锁定', true);
          return;
        }
        const bytes = forward ? entry.after : entry.before;
        applyBytes(editableRaster(raster), bytes, entry.rect);
        queueEdit(entry.id, entry.rect, bytes);
        schedulePaint(true, entry.rect);
      }
      from.pop();
      to.push(entry);
      updateControls();
    }

    function startPointer(event) {
      if (!hasBoard() || stroke || event.button > 2) return;
      const pan = event.button === 1 || event.button === 0 && (spaceDown || tool === 'pan' || running || playing);
      if (!pan && (event.target !== stage || !ownsEdits())) return;
      if (tool === 'pan' && !pan) return;
      viewport.focus({ preventScroll: true });
      if (pan) {
        cancelWheelZoom();
        fit = false;
        stroke = { tool: 'pan', x: event.clientX, y: event.clientY, left: viewport.scrollLeft, top: viewport.scrollTop, pointer: event.pointerId };
        viewport.setPointerCapture(event.pointerId);
        updateControls();
        event.preventDefault();
        return;
      }
      const start = point(event);
      if (playbackID) stopPlayback();
      cursor = start;
      if (tool === 'picker') {
        const sample = compositeCtx.getImageData(start.x, start.y, 1, 1).data;
        if (event.button === 2) setColors(primary, toHex(sample));
        else setColors(toHex(sample), secondary);
        return;
      } else if (tool === 'select') {
        stroke = { tool, start, oldSelection: selection };
        select(area(start, start));
      } else if (tool === 'wand') {
        const raster = layers.get(activeID);
        if (raster && !raster.failed) select(normalizeSelection(maskFor(start, editableRaster(raster).pixels.data, null)));
        return;
      } else {
        if (tool === 'move' && (!selection || !selected(start.x, start.y))) return;
        const edit = beginEdit(start, event.button === 2 ? secondary : primary, tool);
        if (!edit) return;
        stroke = edit;
        if (tool === 'move') {
          edit.oldSelection = selection;
          edit.movePixels = snapRect(edit.raster.pixels.data, selection);
        }
        else if (tool === 'fill') {
          const mask = maskFor(start, edit.raster.pixels.data, selection);
          if (mask) {
            for (let i = 0; i < mask.length; i++) {
              if (mask[i]) colorDot(edit, edit.rgba, i % boardWidth, Math.floor(i / boardWidth));
            }
          }
          paintEdit(edit);
          finishEdit(edit);
          stroke = null;
          return;
        } else if (shapes.includes(tool)) previewShape(edit, start);
        else {
          stamp(edit, start.x, start.y);
          paintEdit(edit);
        }
      }
      stroke.pointer = event.pointerId;
      stage.setPointerCapture(event.pointerId);
      event.preventDefault();
    }

    function previewShape(edit, next) {
      resetPreview(edit);
      drawShape(edit.tool, edit.start, next, $('pixelFilled').checked, (x, y) => stamp(edit, x, y),
        (x, y) => colorDot(edit, edit.rgba, x, y));
      paintEdit(edit);
    }

    function dragPointer(event) {
      if (!hasBoard()) return;
      const next = point(event);
      cursor = next;
      $('pixelCoordinates').textContent = `${boardWidth}×${boardHeight} · ${next.x},${next.y}`;
      if (!stroke || stroke.pointer !== event.pointerId) { schedulePaint(false); return; }
      const edit = stroke;
      if (edit.tool === 'pan') {
        viewport.scrollLeft = edit.left + edit.x - event.clientX;
        viewport.scrollTop = edit.top + edit.y - event.clientY;
      } else if (edit.tool === 'select') select(area(edit.start, next));
      else if (edit.tool === 'move') moveSelection(edit, point(event, false));
      else if (shapes.includes(edit.tool)) previewShape(edit, next);
      else {
        drawLine(edit.last, next, (x, y) => stamp(edit, x, y));
        paintEdit(edit);
      }
      edit.last = next;
    }

    function finishPointer(event, cancel) {
      if (!stroke || (event && stroke.pointer !== event.pointerId)) return;
      const edit = stroke;
      stroke = null;
      const owner = edit.tool === 'pan' ? viewport : stage;
      if (owner.hasPointerCapture(edit.pointer)) owner.releasePointerCapture(edit.pointer);
      if (edit.tool === 'pan') updateControls();
      if (cancel) {
        if (edit.raster) resetPreview(edit);
        if (edit.oldSelection !== undefined) select(edit.oldSelection);
        schedulePaint();
      } else if (edit.raster) finishEdit(edit);
    }

    function eraseSelection() {
      if (!selection) return;
      const edit = beginEdit({ x: selection.x, y: selection.y }, primary, 'eraser');
      if (!edit) return;
      const { x, y, w, h } = selection;
      for (let j = y; j < y + h; j++) {
        for (let i = x; i < x + w; i++) writePoint(edit, i, j, zero);
      }
      paintEdit(edit);
      finishEdit(edit);
    }

    async function readAssetFile(file) {
      if (file.size > 12 * 1024 * 1024) throw new Error('每张图片最大 12 MiB');
      if (['image/png', 'image/jpeg', 'image/gif'].includes(file.type) || /\.(png|jpe?g|gif)$/i.test(file.name)) {
        return byte64(new Uint8Array(await file.arrayBuffer()));
      }
      const url = URL.createObjectURL(file);
      try {
        const image = await loadImage(url);
        if (image.width > 4096 || image.height > 4096) throw new Error('图片单边不能超过 4096 像素');
        const canvas = canvas2D(image.width, image.height);
        canvas.getContext('2d').drawImage(image, 0, 0);
        return canvas.toDataURL('image/png').split(',')[1];
      } finally { URL.revokeObjectURL(url); }
    }

    async function addAssets(files, role) {
      if (!hasBoard() || !ownsEdits()) return;
      const startingProject = projectKey();
      const selectedFiles = role === 'main' ? files.slice(0, 1) : files;
      for (const file of selectedFiles) {
        if (projectKey() !== startingProject || !isActive()) return;
        if (role === 'ref' && (project.pixel.refImages?.length || 0) >= 8) {
          toast('最多可添加 8 张参考图', true);
          return;
        }
        try {
          const data = await readAssetFile(file);
          if (projectKey() !== startingProject || !isActive()) return;
          const result = await mutate('/api/pixel/assets', { role, name: file.name, data });
          if (!result) return;
        } catch (error) {
          displayError(error);
          return;
        }
      }
    }

    async function exportFile(format) {
      if (!hasBoard() || !ownsEdits()) return;
      busy = true;
      updateControls();
      try {
        await flush();
        const params = new URLSearchParams(requestWithRevision({ format, scale: parseInt($('pixelExportScale').value, 10) }));
        if (isAnimated() && format === 'png') params.set('frameId', playbackID || activeID);
        if (token) params.set('k', token);
        const response = await fetch('/api/pixel/export?' + params);
        if (!response.ok) {
          const error = await response.json().catch(() => ({}));
          throw new Error(error.error || '导出失败');
        }
        downloadBlob(await response.blob(), (project.name || 'pixel-art') + '.' + format);
      } catch (error) { displayError(error); }
      finally { busy = false; updateControls(); }
    }

    function downloadBlob(blob, filename) {
      const url = URL.createObjectURL(blob);
      const link = document.createElement('a');
      link.href = url;
      link.download = filename;
      link.click();
      setTimeout(() => URL.revokeObjectURL(url), 1500);
    }

    async function exportGIF() {
      if (!ownsEdits() || !isAnimated()) return;
      if (typeof window.GIFEncoder !== 'function') { toast('GIF 编码器无法加载', true); return; }
      busy = exportingGIF = true;
      updateControls();
      try {
        await flush();
        const frames = visibleFrames();
        if (!frames.length) throw new Error('请至少显示一帧再导出 GIF');
        const scale = Math.min(parseInt($('pixelExportScale').value, 10), 640 / Math.max(boardWidth, boardHeight));
        const width = Math.max(1, Math.round(boardWidth * scale));
        const height = Math.max(1, Math.round(boardHeight * scale));
        const { fps, loop } = animation();
        const encoder = new window.GIFEncoder(width, height, { loop: loop ? 0 : -1 });
        const canvas = canvas2D(width, height);
        const context = canvas.getContext('2d', { willReadFrequently: true });
        context.imageSmoothingEnabled = false;
        for (const frame of frames) {
          const raster = layers.get(frame.id);
          if (!rasterImage(raster) || raster.failed) throw new Error('无法读取帧「' + frame.name + '」');
          context.clearRect(0, 0, width, height);
          context.drawImage(rasterImage(raster), 0, 0, width, height);
          const delay = Math.max(2, Math.round(100 / fps) * (frame.delayMult || 1));
          encoder.addFrame(context.getImageData(0, 0, width, height).data, delay);
        }
        await new Promise((resolve) => setTimeout(resolve, 0));
        downloadBlob(encoder.finish(), 'pixel-' + (project.name || 'animation') + '.gif');
      } catch (error) { displayError(error); }
      finally { busy = exportingGIF = false; updateControls(); }
    }

    function onKey(event) {
      if (!isActive() || event.defaultPrevented || event.target.closest?.('textarea,input,select,[contenteditable=true],.modal:not(.hidden)')) return false;
      const key = event.key.toLowerCase();
      let handled = true;
      if ((event.ctrlKey || event.metaKey) && (key === 'z' || key === 'y')) {
        stepHistory(key === 'y' || event.shiftKey);
      } else if (event.ctrlKey || event.metaKey || event.altKey) return false;
      else if (key === ' ' && hasBoard() && !event.target.closest?.('button')) { spaceDown = true; updateControls(); }
      else if (shortcuts[key]) setTool(shortcuts[key]);
      else if (key === 'x') setColors(secondary, primary);
      else if (key === 'delete') eraseSelection();
      else if (key === 'escape' && selection && !running) select();
      else handled = false;
      if (handled) event.preventDefault();
      return handled;
    }

    function paste(event) {
      const files = [...(event.clipboardData?.items || [])].filter((item) => item.kind === 'file')
        .map((item) => item.getAsFile()).filter(Boolean);
      if (!files.length) return;
      const role = event.target.closest?.('#pixelRefDrop') ? 'ref' : focusSlot;
      event.preventDefault();
      addAssets(files, role);
    }

    for (const button of buttons) button.addEventListener('click', () => setTool(button.dataset.pixelTool));
    for (const [slot, buttonID, inputID] of [['main', 'pixelMainDrop', 'pixelMainFile'], ['ref', 'pixelRefDrop', 'pixelRefFile']]) {
      click(buttonID, () => { focusSlot = slot; $(inputID).click(); });
      listen(buttonID, 'focus', () => { focusSlot = slot; });
      listen(inputID, 'change', () => {
        addAssets([...$(inputID).files], slot);
        $(inputID).value = '';
      });
      listen(buttonID, 'dragover', (event) => { event.preventDefault(); $(buttonID).classList.add('is-over'); });
      listen(buttonID, 'dragleave', () => $(buttonID).classList.remove('is-over'));
      listen(buttonID, 'drop', (event) => {
        event.preventDefault();
        $(buttonID).classList.remove('is-over');
        addAssets([...event.dataTransfer.files], slot);
      });
    }
    viewport.addEventListener('pointerdown', startPointer);
    viewport.addEventListener('pointermove', (event) => {
      if (stroke || event.target === stage) dragPointer(event);
    });
    viewport.addEventListener('pointerup', (event) => {
      if (stroke?.pointer !== event.pointerId) return;
      dragPointer(event);
      finishPointer(event, false);
    });
    viewport.addEventListener('pointercancel', (event) => finishPointer(event, true));
    viewport.addEventListener('lostpointercapture', (event) => finishPointer(event, true));
    stage.addEventListener('pointerleave', () => {
      cursor = null;
      if (!stroke) {
        $('pixelCoordinates').textContent = `${boardWidth}×${boardHeight}`;
        $('pixelSampledColor').textContent = '—';
        schedulePaint();
      }
    });
    stage.addEventListener('contextmenu', (event) => event.preventDefault());
    viewport.addEventListener('wheel', (event) => {
      if (!hasBoard() || !event.deltaY) return;
      event.preventDefault();
      if (stroke) return;
      const unit = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? viewport.clientHeight : 1;
      wheelDelta += event.deltaY * unit;
      wheelAnchor = { x: event.clientX, y: event.clientY };
      if (!wheelFrame) wheelFrame = requestAnimationFrame(() => {
        const delta = wheelDelta;
        wheelFrame = 0;
        wheelDelta = 0;
        changeZoom(zoom * Math.exp(-delta * 0.00125), wheelAnchor);
      });
    }, { passive: false });
    viewport.addEventListener('scroll', refreshViewport, { passive: true });
    if (window.ResizeObserver) new ResizeObserver(layout).observe(viewport);
    window.addEventListener('keyup', (event) => {
      if (event.code === 'Space' && spaceDown) { spaceDown = false; updateControls(); }
    });
    window.addEventListener('blur', () => { spaceDown = false; finishPointer(null, true); });
    click('pixelClearSelection', () => select());
    click('pixelUndo', () => stepHistory(false));
    click('pixelRedo', () => stepHistory(true));
    listen('pixelZoom', 'focus', () => zoomInput.select());
    listen('pixelZoom', 'input', () => { editingZoom = true; });
    listen('pixelZoom', 'blur', finishZoomInput);
    listen('pixelZoom', 'keydown', (event) => {
      if (event.key !== 'Enter' && event.key !== 'Escape') return;
      event.preventDefault();
      event.stopPropagation();
      if (event.key === 'Escape') editingZoom = false;
      zoomInput.blur();
    });
    click('pixelZoomIn', () => changeZoom(zoom * 1.15));
    click('pixelZoomOut', () => changeZoom(zoom / 1.15));
    click('pixelActual', () => changeZoom(1));
    click('pixelFit', () => { cancelWheelZoom(); fit = true; layout(); });
    listen('pixelGrid', 'change', () => schedulePaint(false));
    listen('pixelOverlayOpacity', 'input', drawOverlay);
    listen('pixelSamplingFit', 'change', drawOverlay);
    click('pixelCompare', () => { compare = !compare; renderReference(); });
    listen('pixelReferenceSelect', 'change', renderReference);
    listen('pixelPrimary', 'input', () => setColors(encodeColor('pixelPrimary'), secondary));
    listen('pixelSecondary', 'input', () => setColors(primary, encodeColor('pixelSecondary')));
    listen('pixelHex', 'change', (event) => {
      const color = event.target.value.trim().toLowerCase();
      if (validColor(color)) setColors(color, secondary);
      else {
        toast('请输入 #RRGGBB 或 #RRGGBBAA', true);
        renderPalette();
      }
    });
    click('pixelSwap', () => setColors(secondary, primary));
    listen('pixelSizePreset', 'change', (event) => {
      if (!event.target.value) return;
      const [width, height] = event.target.value.split('×');
      $('pixelWidth').value = width;
      $('pixelHeight').value = height;
    });
    click('pixelResize', async () => {
      const [width, height] = dimensions();
      if (!checkDimensions(width, height)) return;
      if (width === boardWidth && height === boardHeight) return;
      if (!isAnimated() && Math.max(width, height) > 1024 && entries().length > 14) {
        toast('画布宽或高超过 1024 时最多支持 14 个图层，请先精简图层。', true);
        return;
      }
      await mutate('/api/pixel/resize', { width, height, mode: $('pixelResizeMode').value }, 'POST', { reload: true });
    });
    listen('pixelMode', 'change', async (event) => {
      const pixelMode = event.target.value;
      event.target.value = isAnimated() ? 'anim' : 'static';
      if (event.target.value === pixelMode) return;
      if (pixelMode === 'anim' && Math.max(boardWidth, boardHeight) > maxAnimationSize) {
        toast('请先将画布宽、高调整至 512 像素以内。', true);
        return;
      }
      if (pixelMode === 'static' && entries().length > 1) {
        toast('请将帧序列精简为一帧，再切换到静态多层。', true);
        return;
      }
      const flatten = pixelMode === 'anim' && entries().length > 1;
      if (flatten && !await confirm('切换到动图', '当前合成画面将成为首帧，原图层将合并。', '合并并切换')) return;
      await mutate('/api/pixel/frames', { action: 'modeSwitch', pixelMode, flatten }, 'POST', { reload: true });
    });
    click('pixelAnimPlay', togglePlayback);
    listen('pixelAnimOnion', 'change', () => schedulePaint());
    listen('pixelAnimFPS', 'change', (event) => {
      const fps = Number(event.target.value);
      if (!Number.isInteger(fps) || fps < 1 || fps > 30) { event.target.value = animation().fps; return; }
      mutate('/api/pixel/frames', { action: 'fps', fps });
    });
    listen('pixelAnimLoop', 'change', (event) => mutate('/api/pixel/frames', { action: 'loop', loop: event.target.checked }));
    listen('pixelFrameDelay', 'change', (event) => {
      const frame = layerMetadata();
      const delayMult = Number(event.target.value);
      if (!frame || !isAnimated()) return;
      if (!Number.isInteger(delayMult) || delayMult < 1 || delayMult > 8) {
        event.target.value = frame.delayMult || 1;
        return;
      }
      mutate('/api/pixel/frames', { action: 'delayMult', frameId: frame.id, delayMult });
    });
    const timelineClick = (id, handler) => { const node = $(id); if (node) node.addEventListener('click', handler); };
    timelineClick('pixelAnimPrev', () => stepFrame(-1));
    timelineClick('pixelAnimNext', () => stepFrame(1));
    timelineClick('pixelFrameAdd', () => { if (ownsEdits()) mutate('/api/pixel/frames', { action: 'add' }); });
    timelineClick('pixelFrameDup', () => {
      if (ownsEdits() && activeID) mutate('/api/pixel/frames', { action: 'duplicate', frameId: activeID, sourceFrameId: activeID });
    });
    timelineClick('pixelFrameDel', async () => {
      if (!ownsEdits()) return;
      const frame = layerMetadata();
      if (!frame) return;
      if (!await confirm('删除帧', '确定删除「' + (frame.name || '帧 ' + frame.index) + '」？', '删除')) return;
      await mutate('/api/pixel/frames', { action: 'delete', frameId: frame.id });
    });
    timelineClick('pixelFrameLeft', () => moveFrame(-1));
    timelineClick('pixelFrameRight', () => moveFrame(1));
    listen('pixelDraft', 'change', () => {
      if ($('pixelDraft').value === 'quantize') $('pixelSampling').open = true;
    });
    const quantize = () => {
      const maxColors = Number($('pixelColors').value);
      if (!Number.isInteger(maxColors) || maxColors < 4 || maxColors > 64) {
        toast('颜色数量须为 4–64 的整数', true);
        return null;
      }
      return mutate('/api/pixel/quantize', {
        sampleMode: $('pixelSamplingMode').value, fit: $('pixelSamplingFit').value,
        dither: $('pixelDither').value, maxColors,
        palette: $('pixelUsePalette').checked ? project?.pixel?.palette : undefined,
      }, 'POST', { reload: true });
    };
    click('pixelQuantize', quantize);
    listen('pixelPalettePreset', 'change', () => {
      const value = $('pixelPalettePreset').value;
      $('pixelPalettePreset').value = '';
      if (value) mutate('/api/pixel/palette', { action: 'save', colors: palettes[value].split(' ').map((color) => '#' + color) });
    });
    click('pixelPaletteAdd', () => {
      const colors = [...new Set([...(project?.pixel?.palette || []), primary])];
      mutate('/api/pixel/palette', { action: 'save', colors });
    });
    click('pixelPaletteExtract', () => mutate('/api/pixel/palette', {
      action: 'extract', source: $('pixelPaletteSource').value, maxColors: clamp(Number($('pixelColors').value) || 16, 4, 64),
    }));
    click('pixelExportPNG', () => exportFile('png'));
    click('pixelExportSVG', () => exportFile('svg'));
    click('pixelExportGIF', exportGIF);

    function checkDimensions(width, height, mode = isAnimated() ? 'anim' : 'static') {
      const limit = mode === 'anim' ? maxAnimationSize : maxCanvasSize;
      if ([width, height].every((size) => Number.isInteger(size) && size >= 8 && size <= limit)) return true;
      toast(`像素画布的宽和高都须为 8–${limit} 的整数`, true);
      return false;
    }

    function turnContents() {
      return JSON.stringify([entries(), activeID, project?.pixel?.palette]);
    }

    setTool(tool);
    return {
      setProject, layout, onKey, paste, flush, requestWithRevision, mutate, quantize, checkDimensions,
      stopPlayback,
      get isAnimatedNow() { return isAnimated(); },
      get hasEditableBoard() { return hasBoard() && ownsEdits(); },
      // GIF 开关联动：保证工程处于动画形态（无工程返回 false，由调用方决定建板）。
      async ensureAnimatedBoard() {
        if (!hasBoard()) return false;
        if (isAnimated()) return true;
        if (!ownsEdits() || project?.pixel?.modeLocked) return false;
        if (Math.max(boardWidth, boardHeight) > maxAnimationSize) {
          toast('画布宽或高超过 512 像素，无法开启动画时间轴，请先调整尺寸。', true);
          return false;
        }
        await mutate('/api/pixel/frames', { action: 'modeSwitch', pixelMode: 'anim', flatten: entries().length > 1 }, 'POST', { reload: true });
        return isAnimated();
      },
      get layerLimit() {
        if (isAnimated()) return maxFrameCount;
        return Math.max(boardWidth, boardHeight) > 1024 ? 14 : 32;
      },
      requestFields() {
        const areaSelection = selectPayload();
        return requestWithRevision({
          engine: 'pixel', pixelMode: isAnimated() ? 'anim' : 'static', pixelDraftMode: $('pixelDraft').value,
          pixelGif: Boolean(document.getElementById('gifEnabled')?.checked),
          selection: areaSelection,
          restrictToSelection: Boolean(areaSelection && $('pixelRestrict').checked),
        });
      },
      get canDraft() { return Boolean(project?.pixel?.mainImage); },
      get hasDraft() {
        const id = project?.pixel?.draftFrameId || project?.pixel?.draftLayerId;
        return Boolean(id && layerMetadata(id));
      },
      get hasAssets() { return Boolean(project?.pixel?.mainImage || project?.pixel?.refImages?.length); },
      get editable() { return ownsEdits(); },
      get actionBusy() { return busy; },
      get hasPending() { return Boolean(stroke?.raster || pending.length || syncing || colorsDirty); },
      visibleForPreview,
      previewLayer(id, visible) {
        previewVisibility.set(id, visible);
        schedulePaint();
      },
      runState(on) {
        if (on) stopPlayback();
        running = on;
        if (!running && previewVisibility.size) {
          previewVisibility.clear();
          schedulePaint();
        }
        updateControls();
      },
      beginTurn() {
        turnBefore = turnContents();
      },
      finishTurn() {
        if (project && turnContents() !== turnBefore) {
          undo = undo.filter((entry) => !entry.ai);
          retainHistory({ ai: true });
        }
      },
      handleUpdate(update) {
        if (update?.projectId !== projectKey() || !update.project) return;
        applyProject(update.project, {
          layerId: update.layerId, frameId: update.frameId,
          pngBase64: update.pngBase64, rgbaBase64: update.rgbaBase64, region: update.region,
        });
        if (update.region) {
          const marker = update.region;
          lastHighlight = marker;
          setTimeout(() => {
            if (lastHighlight === marker) { lastHighlight = null; schedulePaint(); }
          }, 650);
          schedulePaint();
        }
      },
      reset() {
        stopPlayback();
        undo = [];
        redo = [];
        select();
        focusSlot = 'main';
      },
    };
  };
})();
