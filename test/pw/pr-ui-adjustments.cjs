const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const root = path.resolve(__dirname, '../..');
const { chromium } = require(require.resolve('playwright', {
  paths: [__dirname, path.join(root, '.tools/ui-test')],
}));

const web = path.join(root, 'internal/api/webdist');
const CANVAS = { name: '1:1 方形', width: 1024, height: 1024, viewBox: '0 0 1024 1024' };
const SETTINGS = {
  workspace: 'svg',
  version: 5,
  providers: [],
  activeProviderId: '',
  activeModel: '',
  thinking: 'off',
  fastMode: false,
  extendedThinking: false,
  agent: { maxSteps: 80, compressOn: true, compressAt: 75, timeoutSec: 600, genMode: 'quick', gif: false, multiStep: false },
  layers: { enabled: true, panelHeight: 220 },
  project: { mode: 'quick', dir: '', autoSave: true, recent: [] },
  canvas: CANVAS,
  export: { scale: 2 },
};
const PROJECT = {
  open: true,
  mode: 'quick',
  engine: 'svg',
  name: '临时工程',
  dir: 'data/quick/session',
  kind: 'quick',
  canvas: CANVAS,
  activeLayer: 'l1',
  layers: [{ id: 'l1', name: '图层 1', file: 'layer-l1.svg', visible: true, locked: false, opacity: 1, blend: 'normal', index: 1, bytes: 0, elements: 0, content: '' }],
  svg: '',
  dirty: false,
  canSaveAs: true,
  empty: true,
};

async function main() {
  const browser = await chromium.launch({ headless: true,
    ...(process.env.GIF_BROWSER_PATH ? { executablePath: process.env.GIF_BROWSER_PATH } : { channel: 'msedge' }),
  });
  try {
    const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
    const files = {
      '/style.css': ['text/css', 'style.css'],
      '/app.js': ['text/javascript', 'app.js'],
      '/pixel.js': ['text/javascript', 'pixel.js'],
      '/gif.js': ['text/javascript', 'gif.js'],
    };
    await page.route('http://localhost/**', async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === '/api/state') {
        return route.fulfill({ contentType: 'application/json', body: JSON.stringify({
          settings: SETTINGS, protocols: [], canvases: [CANVAS], prompts: [], categories: [],
          defaultPrompt: '', version: 'test', dataDir: '', project: PROJECT, warn: '',
        }) });
      }
      if (url.pathname === '/api/project/open') {
        return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ ok: true, project: PROJECT }) });
      }
      if (url.pathname === '/api/project') {
        return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ project: PROJECT, recent: [] }) });
      }
      if (url.pathname.startsWith('/api/')) {
        return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ ok: true, project: PROJECT }) });
      }
      const file = files[url.pathname];
      if (file) {
        return route.fulfill({ contentType: file[0], body: await fs.readFile(path.join(web, file[1]), 'utf8') });
      }
      return route.fulfill({ contentType: 'text/html', body: await fs.readFile(path.join(web, 'index.html'), 'utf8') });
    });

    await page.goto('http://localhost/');
    await page.waitForSelector('#btnSave');
    // 启动必答题：选「快速生成」，让工程弹窗关闭后再测顶栏控件。
    await page.locator('#btnQuickNew').click();
    await page.locator('#modalProject').waitFor({ state: 'hidden' });

    const gifButton = page.locator('#btnGif');
    const saveButton = page.locator('#btnSave');
    const gifAtTop = await gifButton.evaluate((el) => el.parentElement.id === 'topActions');
    assert.equal(gifAtTop, true, 'GIF export button should be in the top actions beside Save');
    assert.equal(await gifButton.isVisible(), false, 'GIF export button should be hidden while the GIF toggle is off');

    // GIF 现在是快速模式里的开关（像素模式走像素动图），不再是独立创作模式。
    await page.locator('.switch-control').click();
    assert.equal(await gifButton.isVisible(), true, 'GIF export button should be visible when the GIF toggle is on');
    assert.match(await saveButton.innerText(), /代码/, 'Save should identify code export in GIF mode');
    assert.match(await saveButton.getAttribute('title'), /SVG/, 'Save title should identify SVG source export');

    await page.locator('.switch-control').click();
    assert.equal(await gifButton.isVisible(), false, 'GIF export button should hide after turning the GIF toggle off');
    console.log('PASS GIF button placement/visibility and GIF-mode source-save affordance');
  } finally {
    await browser.close();
  }
}

main().catch((error) => { console.error(error); process.exitCode = 1; });
