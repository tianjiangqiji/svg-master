const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const root = path.resolve(__dirname, '../..');
const { chromium } = require(require.resolve('playwright', {
  paths: [__dirname, path.join(root, '.tools/ui-test')],
}));

const web = path.join(root, 'internal/api/webdist');
const CANVAS = { name: '1:1 方形', width: 1024, height: 1024, viewBox: '0 0 1024 1024' };
const layer = (id, index) => ({
  id, name: '图层 ' + index, file: 'layer-' + id + '.svg', visible: true, locked: false,
  opacity: 1, blend: 'normal', index, bytes: 20, elements: 2, content: '<rect width="10" height="10"/>',
});
const QUICK = {
  open: true, mode: 'quick', engine: 'svg', name: '临时工程', dir: 'data/quick/session', kind: 'quick',
  canvas: CANVAS, activeLayer: 'q1', layers: [layer('q1', 1)], svg: '', dirty: false, canSaveAs: true, empty: true,
};
const MULTI = {
  open: true, mode: 'project', engine: 'svg', name: '多图层工程', dir: 'D:/projects/multi', kind: 'user',
  canvas: CANVAS, activeLayer: 'm3', layers: [layer('m1', 1), layer('m2', 2), layer('m3', 3)], svg: '', dirty: false, canSaveAs: false, empty: false,
};
const NEW_PROJECT = {
  ...MULTI, name: '新项目', dir: 'D:/projects/new', layers: [layer('n1', 1)], activeLayer: 'n1', kind: 'user',
};

function settings(over) {
  return {
    workspace: 'svg', version: 5, providers: [], activeProviderId: '', activeModel: '', thinking: 'off',
    fastMode: false, extendedThinking: false,
    agent: { maxSteps: 80, compressOn: true, compressAt: 75, timeoutSec: 600, genMode: 'quick', gif: false, multiStep: false },
    layers: { enabled: true, panelHeight: 220 },
    project: { mode: 'quick', dir: '', autoSave: true, recent: [] },
    canvas: CANVAS, export: { scale: 2 }, ...over,
  };
}

async function main() {
  const browser = await chromium.launch({ headless: true, ...(process.env.GIF_BROWSER_PATH ? { executablePath: process.env.GIF_BROWSER_PATH } : { channel: 'msedge' }) });
  try {
    const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
    let state = { defaultPrompt: 'A pelican riding a bicycle.' };
    let savedSettings = settings();
    let currentProject = { open: false, mode: 'quick' };
    let chatRequests = 0;
    const files = {
      '/style.css': ['text/css', 'style.css'],
      '/app.js': ['text/javascript', 'app.js'],
      '/pixel.js': ['text/javascript', 'pixel.js'],
      '/gif.js': ['text/javascript', 'gif.js'],
    };
    await page.route('http://localhost/**', async (route) => {
      const url = new URL(route.request().url());
      const json = (body) => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
      if (url.pathname === '/api/state') {
        return json({
          settings: savedSettings, protocols: [], canvases: [CANVAS], prompts: [], categories: [],
          defaultPrompt: state.defaultPrompt, version: 'test', dataDir: '', project: { open: false, mode: 'quick' }, warn: '',
        });
      }
      if (url.pathname === '/api/settings') {
        try { savedSettings = Object.assign({}, savedSettings, route.request().postDataJSON()); } catch { /* ignore */ }
        return json({ ok: true, settings: savedSettings });
      }
      if (url.pathname === '/api/project/open') {
        let body = {};
        try { body = route.request().postDataJSON(); } catch { /* ignore */ }
        currentProject = body.mode === 'project' ? MULTI : QUICK;
        return json({ ok: true, project: currentProject });
      }
      if (url.pathname === '/api/project/create') {
        currentProject = NEW_PROJECT;
        return json({ ok: true, project: currentProject });
      }
      if (url.pathname === '/api/project/browse') return json({ path: 'D:/projects' });
      if (url.pathname === '/api/project') return json({ project: currentProject, recent: [] });
      if (url.pathname === '/api/session/drop') {
        currentProject = { ...QUICK, layers: [layer('q2', 1)], empty: true };
        return json({ ok: true, project: currentProject });
      }
      if (url.pathname.startsWith('/api/chat') && !url.pathname.includes('abort')) chatRequests++;
      if (url.pathname.startsWith('/api/')) return json({ ok: true });
      const file = files[url.pathname];
      if (file) return route.fulfill({ contentType: file[0], body: await fs.readFile(path.join(web, file[1]), 'utf8') });
      return route.fulfill({ contentType: 'text/html', body: await fs.readFile(path.join(web, 'index.html'), 'utf8') });
    });

    await page.goto('http://localhost/');
    await page.evaluate(() => localStorage.clear());
    await page.reload();
    await page.locator('#modalProject').waitFor({ state: 'visible' });

    // 1) 首次运行才填默认提示词。
    assert.equal(await page.locator('#prompt').inputValue(), 'A pelican riding a bicycle.', 'first run should seed the demo prompt');
    await page.locator('#btnQuickNew').click();
    await page.locator('#modalProject').waitFor({ state: 'hidden' });

    // 2) 输入内容按工作区持久化，重启后恢复。
    await page.locator('#prompt').fill('restore me');
    await page.waitForTimeout(700); // 等 settings 防抖落盘
    await page.reload();
    await page.locator('#modalProject').waitFor({ state: 'visible' });
    assert.equal(await page.locator('#prompt').inputValue(), 'restore me', 'saved prompt should be restored after restart');
    await page.locator('#btnQuickNew').click();
    await page.locator('#modalProject').waitFor({ state: 'hidden' });

    // 3) 清空全部后回到默认提示词，重启后仍然是默认提示词。
    await page.locator('#btnClear').click();
    await page.locator('#confirmOk').click();
    await page.waitForFunction(() => document.getElementById('prompt').value === 'A pelican riding a bicycle.');
    await page.waitForTimeout(700); // 等清空后的设置落盘
    await page.reload();
    await page.locator('#modalProject').waitFor({ state: 'visible' });
    assert.equal(await page.locator('#prompt').inputValue(), 'A pelican riding a bicycle.', 'clear should restore the default prompt');
    await page.locator('#btnQuickNew').click();
    await page.locator('#modalProject').waitFor({ state: 'hidden' });

    // 4) 新建 SVG 项目默认快速模式。
    await page.locator('#btnProject').click();
    await page.locator('#btnProjNew').click();
    await page.locator('#projNameInput').fill('新项目');
    await page.locator('#btnProjCreate').click();
    await page.waitForFunction(() => window.__SM__.getState().genMode === 'quick');
    assert.equal(await page.evaluate(() => window.__SM__.getState().genMode), 'quick', 'new SVG project should start in quick mode');
    assert.equal(await page.locator('#genTrack [data-mode="quick"]').getAttribute('aria-checked'), 'true');

    // 5) 快速模式下打开多图层工程仍保持快速；直接生成会被单层交付守卫拦下。
    await page.locator('#btnProject').click();
    await page.locator('#btnProjOpen').click();
    await page.waitForFunction(() => window.__SM__.getState().genMode === 'quick');
    assert.equal(await page.evaluate(() => window.__SM__.getState().genMode), 'quick', 'opening a project should keep the current quick mode');
    chatRequests = 0;
    await page.locator('#btnRun').click();
    await page.waitForFunction(() => document.getElementById('toast').classList.contains('show'));
    assert.match(await page.locator('#toast').innerText(), /快速模式只能交付单层画面/);
    assert.equal(chatRequests, 0, 'quick mode with multiple layers must not start a request');

    // 6) GIF 开关开着但用户没写主题时不能直接生成。
    await page.reload();
    await page.locator('#modalProject').waitFor({ state: 'visible' });
    await page.locator('#btnQuickNew').click();
    if (await page.locator('#modalConfirm').isVisible()) await page.locator('#confirmOk').click();
    await page.locator('#modalProject').waitFor({ state: 'hidden' });
    await page.locator('#prompt').fill('');
    await page.locator('.switch-control').click();
    assert.match(await page.locator('#prompt').inputValue(), /GIF 动图/, 'GIF suffix should be visible in the prompt box');
    chatRequests = 0;
    await page.locator('#btnRun').click();
    await page.waitForFunction(() => document.getElementById('toast').classList.contains('show'));
    assert.match(await page.locator('#toast').innerText(), /请先填写提示词/);
    assert.equal(chatRequests, 0, 'empty prompt with only the GIF suffix must not start a request');

    console.log('PASS clear restores default prompt, new project quick mode, quick multi-layer guard, and GIF empty-prompt guard');
  } finally {
    await browser.close();
  }
}

main().catch((error) => { console.error(error); process.exitCode = 1; });
