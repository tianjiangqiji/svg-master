// Run against the isolated UI smoke server and the local fake model.
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
  const origin = new URL(process.env.PIXEL_STUDIO_URL);
  const token = origin.searchParams.get('k') || '';
  assert.ok(token && ['127.0.0.1', 'localhost', '[::1]'].includes(origin.hostname), 'Use the isolated loopback UI_SMOKE_URL');
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'svg-master-anim-pw-'));
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      ...(process.env.GIF_BROWSER_PATH ? { executablePath: process.env.GIF_BROWSER_PATH } : { channel: 'msedge' }),
    });
    const page = await browser.newPage({ viewport: { width: 1440, height: 1024 } });
    const errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    let chooseDirectory = directory;
    await page.route('**/api/project/browse', (route) => route.fulfill({ json: { path: chooseDirectory } }));
    const control = (id) => page.locator('#' + id);
    const row = (id) => page.locator('.layer-row[data-id="' + id + '"]');
    const get = async (route) => {
      const response = await page.request.get(new URL(route, origin).href, { headers: { 'X-App-Token': token } });
      assert.ok(response.ok(), route + ' returned HTTP ' + response.status());
      return response;
    };
    const snapshot = async () => (await (await get('/api/project')).json()).project;
    const rawFrame = async (project, id) => {
      const query = new URLSearchParams({ frameId: id, projectId: project.projectId, format: 'rgba' });
      return (await get('/api/pixel/layer?' + query)).body();
    };
    const saved = () => page.waitForFunction(() => {
      const text = document.getElementById('pixelStatus').textContent;
      return !text.includes('同步') && !text.includes('正在加载');
    });
    const response = (endpoint, method = 'POST') => page.waitForResponse((result) =>
      new URL(result.url()).pathname === endpoint && result.request().method() === method);
    const projectAfter = async (endpoint, interact) => {
      const scheduled = response(endpoint);
      await interact();
      const result = await scheduled;
      const body = await result.json();
      assert.ok(result.ok(), endpoint + ' failed with ' + body.error);
      await saved();
      return body.project;
    };
    const framesAfter = (interact) => projectAfter('/api/pixel/frames', interact);
    const pixelAt = (rgba, x, y, width = 16) => Array.from(rgba.subarray((y * width + x) * 4, (y * width + x) * 4 + 4));
    const selectTool = (tool) => page.locator('[data-pixel-tool="' + tool + '"]').click();
    const hit = async (x, y) => {
      const stage = await control('pixelStage').boundingBox();
      await page.mouse.click(stage.x + (x + 0.5) * stage.width / 16, stage.y + (y + 0.5) * stage.height / 16);
    };
    const drawPixel = async (x, y) => {
      const scheduled = response('/api/pixel/layer', 'PUT');
      await hit(x, y);
      const result = await scheduled;
      assert.ok(result.ok(), 'Persist animation frame stroke');
      assert.ok(result.request().postDataJSON().frameId);
      await saved();
    };
    const readDownload = async (id) => {
      const scheduled = page.waitForEvent('download');
      await control(id).click();
      const stream = await (await scheduled).createReadStream();
      const chunks = [];
      for await (const chunk of stream) chunks.push(chunk);
      return Buffer.concat(chunks);
    };
    const decodeGIF = (buffer) => page.evaluate(async (bytes) => {
      const decoder = new ImageDecoder({ data: new Uint8Array(bytes), type: 'image/gif' });
      await decoder.tracks.ready;
      const count = decoder.tracks.selectedTrack.frameCount;
      const frames = [];
      for (let i = 0; i < count; i++) {
        const { image } = await decoder.decode({ frameIndex: i });
        const canvas = document.createElement('canvas');
        canvas.width = image.displayWidth;
        canvas.height = image.displayHeight;
        const ctx = canvas.getContext('2d');
        ctx.drawImage(image, 0, 0);
        const { data } = ctx.getImageData(0, 0, canvas.width, canvas.height);
        let count = 0;
        let first = -1;
        for (let p = 3; p < data.length; p += 4) {
          if (data[p] > 127) {
            if (first < 0) first = (p - 3) / 4;
            count++;
          }
        }
        frames.push({ count, x: first % canvas.width, y: Math.floor(first / canvas.width), ms: image.duration / 1000 });
        image.close();
      }
      decoder.close();
      return frames;
    }, Array.from(buffer));

    await page.goto(origin.href);
    await control('projectWorkspacePixel').click();
    await control('btnProjNew').click();
    await control('projNameInput').fill('像素动画验收');
    await control('pixelProjectMode').selectOption('anim');
    assert.equal(await control('pixelProjectWidth').getAttribute('max'), '512');
    await control('pixelProjectMode').selectOption('static');
    await control('pixelProjectWidth').fill('16');
    await control('pixelProjectHeight').fill('16');
    await projectAfter('/api/project/create', () => control('btnProjCreate').click());
    await control('modalProject').waitFor({ state: 'hidden' });
    await projectAfter('/api/layers/ops', () => control('btnAddLayer').click());
    await control('pixelMode').selectOption('anim');
    await control('modalConfirm').waitFor({ state: 'visible' });
    let board = await framesAfter(() => control('confirmOk').click());
    assert.equal(board.pixel.mode, 'anim');
    assert.equal(board.pixel.anim.frames.length, 1);
    assert.equal(await control('layerBlockTitle').textContent(), '帧序列');
    assert.ok(await control('pixelAnimBar').isVisible());
    assert.ok(await control('pixelExportGIF').isVisible());
    assert.ok(!await control('pixelExportSVG').isVisible());
    assert.ok(await control('pixelSizePreset').locator('option').filter({ hasText: /^1024×1024$/ }).isDisabled());

    const first = board.pixel.anim.activeFrameId;
    await selectTool('pencil');
    await drawPixel(2, 2);
    const firstPixels = await rawFrame(await snapshot(), first);
    board = await framesAfter(() => control('pixelDuplicate').click());
    const second = board.pixel.anim.activeFrameId;
    assert.equal(board.pixel.anim.frames.length, 2);
    assert.notEqual(second, first);
    assert.deepEqual(await rawFrame(board, second), firstPixels, 'The duplicate preserves each pixel');
    await selectTool('eraser');
    await drawPixel(2, 2);
    await control('pixelAnimOnion').check();
    await page.waitForFunction(() => document.getElementById('pixelOnion').getContext('2d').getImageData(2, 2, 1, 1).data[3] === 255);
    assert.equal(await control('pixelOnion').evaluate((canvas) => getComputedStyle(canvas).opacity), '0.25');
    assert.deepEqual(pixelAt(await rawFrame(await snapshot(), second), 2, 2), [0, 0, 0, 0]);
    assert.deepEqual(await rawFrame(await snapshot(), first), firstPixels);
    await selectTool('pencil');
    await drawPixel(4, 2);
    board = await framesAfter(() => row(first).locator('.lock').click());
    assert.ok(board.pixel.anim.frames.find((frame) => frame.id === first).locked);
    board = await framesAfter(() => control('btnLayerUp').click());
    assert.equal(board.pixel.anim.frames[0].id, second);
    await framesAfter(() => control('btnLayerDown').click());
    await framesAfter(() => row(second).locator('.eye').click());
    await framesAfter(() => row(second).locator('.eye').click());
    await framesAfter(async () => {
      await control('pixelAnimFPS').fill('1');
      await control('pixelAnimFPS').press('Tab');
    });
    await framesAfter(async () => {
      await control('pixelFrameDelay').fill('2');
      await control('pixelFrameDelay').press('Tab');
    });
    await framesAfter(() => control('pixelAnimLoop').uncheck());
    await control('pixelAnimPlay').click();
    await page.waitForFunction(() => document.getElementById('pixelAnimPosition').textContent === '1 / 2');
    await page.waitForFunction(() => document.getElementById('pixelAnimPosition').textContent === '2 / 2');
    await page.waitForFunction(() => document.getElementById('pixelAnimPlay').getAttribute('aria-label') === '播放');
    const handGIF = await readDownload('pixelExportGIF');
    assert.ok(!handGIF.includes('NETSCAPE2.0'), 'A non-looping GIF omits the loop extension');
    const handFrames = await decodeGIF(handGIF);
    assert.deepEqual(handFrames, [{ count: 1, x: 2, y: 2, ms: 1000 }, { count: 1, x: 4, y: 2, ms: 2000 }]);
    await control('btnLayerDel').click();
    await control('modalConfirm').waitFor({ state: 'visible' });
    board = await framesAfter(() => control('confirmOk').click());
    assert.equal(board.pixel.anim.frames.length, 1);
    await framesAfter(() => row(first).locator('.lock').click());
    await framesAfter(async () => {
      await control('pixelAnimFPS').fill('8');
      await control('pixelAnimFPS').press('Tab');
    });
    await framesAfter(() => control('pixelAnimLoop').check());

    await control('prompt').fill('生成循环动画，粉色方块每帧向右移动三像素。');
    const sent = page.waitForRequest((request) => new URL(request.url()).pathname === '/api/chat');
    const turn = response('/api/chat');
    await control('btnRun').click();
    assert.equal((await sent).postDataJSON().pixelMode, 'anim');
    assert.ok((await turn).ok());
    await page.waitForFunction(() => !document.getElementById('btnRun').classList.contains('running'));
    await saved();
    assert.match(await control('bigLog').textContent(), /任务完成/);
    board = await snapshot();
    assert.equal(board.pixel.anim.frames.length, 3, 'Fake model draws a base frame and two shifted copies');
    assert.ok(await control('pixelMode').isDisabled(), 'AI generation locks the animation mode');
    const animated = await readDownload('pixelExportGIF');
    assert.ok(animated.includes('NETSCAPE2.0'), 'Looping GIF includes an infinite loop extension');
    assert.deepEqual((await decodeGIF(animated)).map(({ count, x, y }) => ({ count, x, y })), [
      { count: 9, x: 1, y: 1 }, { count: 9, x: 4, y: 1 }, { count: 9, x: 7, y: 1 },
    ]);
    const scheduledPNG = page.waitForRequest((request) => new URL(request.url()).pathname === '/api/pixel/export');
    const png = await readDownload('pixelExportPNG');
    assert.equal(new URL((await scheduledPNG).url()).searchParams.get('frameId'), board.pixel.anim.activeFrameId);
    assert.deepEqual([png.readUInt32BE(16), png.readUInt32BE(20)], [16, 16]);
    assert.equal((await projectAfter('/api/pixel/checkpoint', () => control('pixelUndo').click())).pixel.anim.frames.length, 1);
    assert.equal((await projectAfter('/api/pixel/checkpoint', () => control('pixelRedo').click())).pixel.anim.frames.length, 3);

    chooseDirectory = board.dir;
    await control('btnProject').click();
    await projectAfter('/api/project/open', () => control('btnProjOpen').click());
    board = await snapshot();
    assert.equal(board.pixel.mode, 'anim');
    assert.equal(board.pixel.anim.frames.length, 3);
    assert.deepEqual(errors, []);
    console.log('PASS Pixel Studio animation conversion, frame editing, playback, onion skin, AI and decoded GIF export');
  } finally {
    if (browser) await browser.close();
    await fs.rm(directory, { recursive: true, force: true });
  }
}

main().catch((error) => { console.error(error); process.exitCode = 1; });
