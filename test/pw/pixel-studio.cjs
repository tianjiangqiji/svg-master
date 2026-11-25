// Run with test/fake_llm and test/ui_smoke. PIXEL_STUDIO_URL is the isolated UI_SMOKE_URL.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const root = path.resolve(__dirname, '../..');
const { chromium } = require(require.resolve('playwright', {
  paths: [__dirname, path.join(root, '.tools/ui-test')],
}));

async function main() {
  assert.ok(process.env.PIXEL_STUDIO_URL, 'Set PIXEL_STUDIO_URL to UI_SMOKE_URL from go run ./test/ui_smoke');
  const workspace = new URL(process.env.PIXEL_STUDIO_URL);
  const token = workspace.searchParams.get('k') || '';
  assert.ok(token && ['127.0.0.1', 'localhost', '[::1]'].includes(workspace.hostname), 'Use the isolated loopback UI_SMOKE_URL');
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'svg-master-pixel-pw-'));
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      ...(process.env.GIF_BROWSER_PATH ? { executablePath: process.env.GIF_BROWSER_PATH } : { channel: 'msedge' }),
    });
    const page = await browser.newPage({ viewport: { width: 1440, height: 1024 } });
    const errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.route('**/api/project/browse', (route) => route.fulfill({ json: { path: directory } }));
    const button = (id) => page.locator('#' + id);
    const get = async (route) => {
      const response = await page.request.get(new URL(route, workspace).href, { headers: { 'X-App-Token': token } });
      assert.ok(response.ok(), route + ' returned HTTP ' + response.status());
      return response;
    };
    const snapshot = async () => (await (await get('/api/project')).json()).project;
    const raster = async (project, id) => {
      const query = new URLSearchParams({ id, projectId: project.projectId, format: 'rgba' });
      return (await get('/api/pixel/layer?' + query)).body();
    };
    const untilControlEnabled = (id) => page.waitForFunction((key) => !document.getElementById(key).disabled, id);
    const waitForLayer = (name) => page.locator('#layerList .nm').filter({ hasText: name }).waitFor();
    const tillSaved = () => page.waitForFunction(() => {
      const status = document.getElementById('pixelStatus').textContent;
      return !status.includes('同步') && !status.includes('正在加载');
    });
    const toolButton = (tool) => page.locator('[data-pixel-tool="' + tool + '"]');
    const pressTool = (tool) => toolButton(tool).click();
    const viewportPosition = () => button('pixelViewport').evaluate((viewport) => ({
      x: viewport.scrollLeft,
      y: viewport.scrollTop,
      maxX: viewport.scrollWidth - viewport.clientWidth,
      maxY: viewport.scrollHeight - viewport.clientHeight,
    }));
    const boardPoint = async (anchor) => {
      const stage = await button('pixelStage').boundingBox();
      return {
        x: 256 * (anchor.x - stage.x) / stage.width,
        y: 256 * (anchor.y - stage.y) / stage.height,
        width: stage.width,
      };
    };
    async function checkNavigation(project) {
      assert.equal(await toolButton('pan').getAttribute('aria-pressed'), 'true', 'A newly opened board defaults to pan');
      const initialPixels = await raster(project, project.activeLayer);
      const zoom = button('pixelZoom');
      await zoom.fill('975.5');
      await zoom.press('Enter');
      await page.waitForFunction(() => Math.abs(document.getElementById('pixelStage').getBoundingClientRect().width - 256 * 9.755) <= 1);
      assert.equal(Number(await zoom.inputValue()), 975.5, 'Enter commits the exact custom percentage');

      const viewport = await button('pixelViewport').boundingBox();
      const stage = await button('pixelStage').boundingBox();
      const anchor = {
        x: (Math.max(viewport.x, stage.x) + Math.min(viewport.x + viewport.width, stage.x + stage.width)) / 2,
        y: (Math.max(viewport.y, stage.y) + Math.min(viewport.y + viewport.height, stage.y + stage.height)) / 2,
      };
      const initialScroll = await viewportPosition();
      const dx = initialScroll.x < initialScroll.maxX / 2 ? 55 : -55;
      const dy = initialScroll.y < initialScroll.maxY / 2 ? 40 : -40;
      await page.mouse.move(anchor.x, anchor.y);
      await page.mouse.down();
      await page.mouse.move(anchor.x - dx, anchor.y - dy, { steps: 4 });
      await page.mouse.up();
      const panned = await viewportPosition();
      assert.ok((panned.x - initialScroll.x) * Math.sign(dx) >= 40, 'Pan scrolls the enlarged board horizontally');
      assert.ok((panned.y - initialScroll.y) * Math.sign(dy) >= 30, 'Pan scrolls the enlarged board vertically');
      await tillSaved();
      assert.ok(await button('pixelUndo').isDisabled(), 'Pan leaves the drawing history empty');
      assert.deepEqual(await raster(await snapshot(), project.activeLayer), initialPixels, 'Pan leaves the layer pixels intact');

      await page.mouse.move(anchor.x, anchor.y);
      const beforeWheel = await boardPoint(anchor);
      await page.mouse.wheel(0, -40);
      await page.waitForFunction((width) => document.getElementById('pixelStage').getBoundingClientRect().width > width, beforeWheel.width);
      const larger = await boardPoint(anchor);
      assert.ok(larger.width < beforeWheel.width * 1.25, 'A short wheel gesture zooms in by a small increment');
      assert.ok(Math.abs(larger.x - beforeWheel.x) <= 1 && Math.abs(larger.y - beforeWheel.y) <= 1,
        'Zoom in preserves the canvas point below the mouse');
      await page.mouse.wheel(0, 40);
      await page.waitForFunction((width) => document.getElementById('pixelStage').getBoundingClientRect().width < width, larger.width);
      const smaller = await boardPoint(anchor);
      assert.ok(smaller.width > larger.width * 0.8, 'A short wheel gesture zooms out by a small increment');
      assert.ok(Math.abs(smaller.x - larger.x) <= 1 && Math.abs(smaller.y - larger.y) <= 1,
        'Zoom out preserves the canvas point below the mouse');
      await button('pixelFit').click();
    }
    const point = async (x, y) => {
      const stage = await button('pixelStage').boundingBox();
      const width = await button('pixelCanvas').evaluate((canvas) => canvas.width);
      const height = await button('pixelCanvas').evaluate((canvas) => canvas.height);
      return { x: stage.x + (x + 0.5) * stage.width / width, y: stage.y + (y + 0.5) * stage.height / height };
    };
    const hit = async (x, y) => {
      const target = await point(x, y);
      await page.mouse.click(target.x, target.y);
    };
    const drag = async (a, b) => {
      const start = await point(a[0], a[1]);
      const end = await point(b[0], b[1]);
      await page.mouse.move(start.x, start.y);
      await page.mouse.down();
      await page.mouse.move(end.x, end.y, { steps: 4 });
      await page.mouse.up();
    };
    const run = async (prompt) => {
      await button('prompt').fill(prompt);
      const completedCount = ((await button('bigLog').textContent()).match(/任务完成/g) || []).length;
      const sent = page.waitForRequest((request) => new URL(request.url()).pathname === '/api/chat');
      const completed = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/chat' && response.request().method() === 'POST');
      await button('btnRun').click();
      const request = (await sent).postDataJSON();
      const response = await completed;
      assert.ok(response.ok(), 'Chat returned HTTP ' + response.status());
      await page.waitForFunction(() => !document.getElementById('btnRun').classList.contains('running'));
      await tillSaved();
      assert.equal(((await button('bigLog').textContent()).match(/任务完成/g) || []).length, completedCount + 1);
      return request;
    };
    const pngFixture = async (name, color) => {
      const bytes = await page.evaluate((fillStyle) => {
        const canvas = document.createElement('canvas');
        canvas.width = 16;
        canvas.height = 12;
        const ctx = canvas.getContext('2d');
        ctx.fillStyle = fillStyle;
        ctx.fillRect(1, 1, 14, 10);
        return canvas.toDataURL().split(',')[1];
      }, color);
      return { name, mimeType: 'image/png', buffer: Buffer.from(bytes, 'base64') };
    };
    const download = async (id) => {
      const scheduled = page.waitForEvent('download');
      await button(id).click();
      const stream = await (await scheduled).createReadStream();
      const chunks = [];
      for await (const chunk of stream) chunks.push(chunk);
      return Buffer.concat(chunks);
    };
    const createProject = async (name, width, height, mode = 'static') => {
      await button('btnProjNew').click();
      await button('projNameInput').fill(name);
      await button('pixelProjectMode').selectOption(mode);
      await button('pixelProjectWidth').fill(String(width));
      await button('pixelProjectHeight').fill(String(height));
      const response = page.waitForResponse((result) => new URL(result.url()).pathname === '/api/project/create');
      await button('btnProjCreate').click();
      assert.ok((await response).ok(), 'Create the ' + mode + ' pixel project');
      await button('modalProject').waitFor({ state: 'hidden' });
      await button('pixelStage').waitFor({ state: 'visible' });
      await tillSaved();
      return snapshot();
    };
    const assertPNGSize = (png, width, height) => {
      assert.equal(png.subarray(1, 4).toString(), 'PNG');
      assert.deepEqual([png.readUInt32BE(16), png.readUInt32BE(20)], [width, height]);
    };

    await page.goto(workspace.href);
    await button('projectWorkspacePixel').click();
    const created = await createProject('像素协同验收', 256, 256);
    assert.equal(created.engine, 'pixel');
    assert.deepEqual([created.pixel.width, created.pixel.height], [256, 256]);
    assert.equal(await button('btnSave').isVisible(), false);
    await checkNavigation(created);

    await button('pixelMainFile').setInputFiles(await pngFixture('构图.png', '#303080'));
    await page.locator('#pixelMainThumb img[alt="构图.png"]').waitFor();
    await button('pixelRefFile').setInputFiles([
      await pngFixture('角色甲.png', '#b03a26'),
      await pngFixture('角色乙.png', '#30ac80'),
    ]);
    await page.locator('#pixelRefThumbs img[alt="角色乙.png"]').waitFor();
    await page.getByRole('button', { name: '删除参考图 #1' }).click();
    await page.locator('#pixelRefThumbs img[alt="角色甲.png"]').waitFor({ state: 'detached' });
    await button('pixelRefFile').setInputFiles(await pngFixture('角色丙.png', '#bc9032'));
    await page.locator('#pixelRefThumbs img[alt="角色丙.png"]').waitFor();
    assert.deepEqual((await snapshot()).pixel.refImages.map((asset) => asset.id), ['ref_2', 'ref_3']);
    await button('pixelCompare').click();
    await button('pixelReferenceSelect').selectOption('ref_2');
    assert.ok(await button('pixelReferenceImage').isVisible());
    await button('pixelCompare').click();
    await button('pixelDraft').selectOption('quantize');
    await button('pixelQuantize').click();
    await waitForLayer('采样底稿');
    await tillSaved();

    let board = await snapshot();
    const draftId = board.pixel.draftLayerId;
    const lockedDraft = await raster(board, draftId);
    assert.ok(lockedDraft.some((value) => value !== 0), 'The sampled draft should contain pixels');
    const draftRow = page.locator('#layerList .layer-row').filter({ hasText: '采样底稿' });
    await draftRow.locator('.lock').click();
    await draftRow.locator('.lock.on').waitFor();
    await button('btnAddLayer').click();
    await waitForLayer('新图层');
    await tillSaved();
    await button('pixelActual').click();
    await pressTool('pencil');
    await button('pixelHex').fill('#e23d66');
    await button('pixelHex').press('Tab');
    await hit(10, 10);
    await hit(11, 10);
    await hit(10, 11);
    await tillSaved();
    board = await snapshot();
    const humanLayer = board.activeLayer;
    const humanPixels = await raster(board, humanLayer);
    const offset = (10 * 256 + 10) * 4;
    assert.deepEqual([...humanPixels.subarray(offset, offset + 4)], [226, 61, 102, 255]);
    await button('pixelUndo').click();
    await tillSaved();
    const undoPixels = await raster(await snapshot(), humanLayer);
    assert.deepEqual([...undoPixels.subarray(offset + 256 * 4, offset + 256 * 4 + 4)], [0, 0, 0, 0]);
    await button('pixelRedo').click();
    await tillSaved();
    assert.deepEqual(await raster(await snapshot(), humanLayer), humanPixels);

    await pressTool('wand');
    await hit(10, 10);
    assert.match(await button('pixelSelectionInfo').textContent(), /10,10.*2×2/);
    await pressTool('select');
    await drag([0, 0], [39, 39]);
    const beforeAI = await snapshot();
    const request = await run('沿用采样底稿构图，在选区内参考参考图 #2 添加细节。');
    assert.equal(request.engine, 'pixel');
    assert.equal(request.pixelDraftMode, 'quantize');
    assert.deepEqual(request.selection, { x: 0, y: 0, w: 40, h: 40 });
    assert.equal(request.restrictToSelection, true);
    const afterAI = await snapshot();
    assert.ok(afterAI.revision > beforeAI.revision, 'The fake model should write at least one change');
    assert.deepEqual(await raster(afterAI, draftId), lockedDraft);

    await button('pixelUndo').click();
    await tillSaved();
    const undone = await snapshot();
    assert.deepEqual(undone.layers.map((layer) => layer.id), beforeAI.layers.map((layer) => layer.id));
    assert.deepEqual(await raster(undone, humanLayer), humanPixels);
    await button('pixelRedo').click();
    await tillSaved();
    await untilControlEnabled('pixelExportPNG');
    assertPNGSize(await download('pixelExportPNG'), 256, 256);
    const svg = await download('pixelExportSVG');
    assert.match(svg.toString(), /shape-rendering="crispEdges"/);
    await button('pixelSave').click();
    await tillSaved();

    await button('btnProject').click();
    await page.unroute('**/api/project/browse');
    const liveDirectory = (await snapshot()).dir;
    await page.route('**/api/project/browse', (route) => route.fulfill({ json: { path: liveDirectory } }));
    await button('btnProjOpen').click();
    await button('modalProject').waitFor({ state: 'hidden' });
    assert.deepEqual((await snapshot()).pixel.refImages.map((asset) => asset.id), ['ref_2', 'ref_3']);
    await tillSaved();

    if (await button('pixelClearSelection').isEnabled()) await button('pixelClearSelection').click();
    await button('pixelDraft').selectOption('vision');
    assert.equal((await run('')).pixelDraftMode, 'vision');
    await page.getByRole('button', { name: '删除主图' }).click();
    await page.locator('#pixelMainThumb img').waitFor({ state: 'detached' });
    for (const id of ['#2', '#3']) {
      await page.getByRole('button', { name: '删除参考图 ' + id }).click();
      await page.getByRole('button', { name: '删除参考图 ' + id }).waitFor({ state: 'detached' });
    }
    await button('pixelDraft').selectOption('prompt');
    assert.equal((await run('画一枚 16 像素大小的红色徽章。')).pixelDraftMode, 'prompt');

    await page.unroute('**/api/project/browse');
    await page.route('**/api/project/browse', (route) => route.fulfill({ json: { path: directory } }));
    await button('btnProject').click();
    const large = await createProject('超大画布验收', 2048, 2048);
    assert.deepEqual([large.pixel.width, large.pixel.height], [2048, 2048]);
    const presets = await button('pixelSizePreset').locator('option').allTextContents();
    assert.equal(presets.at(-1), '1024×1024', 'The preset list caps at 1024 while custom dimensions reach 2048');
    assert.match(await button('layerMeta').textContent(), /1 \/ 14/);
    await button('pixelActual').click();
    await pressTool('pencil');
    const viewport = await button('pixelViewport').boundingBox();
    const stage = await button('pixelStage').boundingBox();
    const middle = { x: viewport.x + viewport.width / 2, y: viewport.y + viewport.height / 2 };
    const cx = Math.floor((middle.x - stage.x) * 2048 / stage.width);
    const cy = Math.floor((middle.y - stage.y) * 2048 / stage.height);
    const nextBrush = page.waitForRequest((request) => request.method() === 'PUT' && new URL(request.url()).pathname === '/api/pixel/layer');
    await hit(cx, cy);
    const brush = (await nextBrush).postDataJSON();
    await tillSaved();
    assert.deepEqual(brush.region, { x: cx, y: cy, w: 1, h: 1 });
    assert.ok(brush.rgbaBase64 && !brush.pngBase64);
    await pressTool('fill');
    const nextFill = page.waitForRequest((request) => request.method() === 'PUT' && new URL(request.url()).pathname === '/api/pixel/layer');
    await hit(cx + 12, cy + 12);
    const fill = (await nextFill).postDataJSON();
    await tillSaved();
    assert.ok(fill.pngBase64 && !fill.rgbaBase64, 'A large fill uploads a compressed PNG');
    async function largePixel(x, y) {
      const state = await snapshot();
      const data = await raster(state, state.activeLayer);
      return data[(y * 2048 + x) * 4 + 3];
    }
    assert.equal(await largePixel(cx + 12, cy + 12), 255);
    await button('pixelUndo').click();
    await tillSaved();
    assert.equal(await largePixel(cx + 12, cy + 12), 0);
    assert.equal(await largePixel(cx, cy), 255, 'Undo keeps pixels from the previous brush stroke');
    await button('pixelRedo').click();
    await tillSaved();
    assert.equal(await largePixel(cx + 12, cy + 12), 255);
    assert.ok(await button('pixelExportScale').locator('option').filter({ hasText: /^8×$/ }).isDisabled());
    assertPNGSize(await download('pixelExportPNG'), 2048, 2048);
    await button('pixelExportScale').selectOption('4×');
    assertPNGSize(await download('pixelExportPNG'), 8192, 8192);
    const ruler = await button('pixelRulerX').evaluate((canvas) => ({
      width: canvas.width, viewport: document.getElementById('pixelViewport').clientWidth,
    }));
    assert.ok(ruler.width > 0 && ruler.width <= ruler.viewport + 2);
    for (let count = 2; count <= 14; count++) {
      await button('btnAddLayer').click();
      await page.waitForFunction((target) => document.getElementById('layerMeta').textContent.startsWith(target + ' / 14'), count);
      await tillSaved();
    }
    assert.ok(await button('btnAddLayer').isDisabled());
    assert.match(await button('btnAddLayer').getAttribute('title'), /14/);
    assert.deepEqual(errors, []);
    console.log('PASS Pixel Studio navigation, assets, drawing, AI, undo/redo, reopen and 2048 × 2048 editing/export');
  } finally {
    if (browser) await browser.close();
    await fs.rm(directory, { recursive: true, force: true });
  }
}

main().catch((error) => { console.error(error); process.exitCode = 1; });
