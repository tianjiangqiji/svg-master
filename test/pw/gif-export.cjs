// Run with an installed Playwright; this checkout also resolves .tools/ui-test.
// GIF_BROWSER_PATH selects an existing Chromium/Edge executable.
// Optional first argument: a real SVG to verify and export to GIF_OUTPUT_PATH.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const root = path.resolve(__dirname, '../..');
const { chromium } = require(require.resolve('playwright', {
  paths: [__dirname, path.join(root, '.tools/ui-test')],
}));

const svg = (body) => `<svg xmlns="http://www.w3.org/2000/svg" width="64" height="32" viewBox="0 0 64 32" data-fps="8">${body}</svg>`;
const rect = (x) => `<rect x="${x}" width="16" height="32" fill="red"/>`;
const mixed = svg(Array.from({ length: 12 }, (_, i) => {
  const values = Array.from({ length: 13 }, (_, j) => j % 12 === i ? 1 : 0).join(';');
  return `<g data-frame="${i}" opacity="${i === 0 ? 1 : 0}"><animate attributeName="opacity" values="${values}" dur="1.5s" calcMode="discrete" repeatCount="indefinite"/>${rect(i * 4)}</g>`;
}).join(''));

async function main() {
  const browser = await chromium.launch({ headless: true,
    ...(process.env.GIF_BROWSER_PATH ? { executablePath: process.env.GIF_BROWSER_PATH } : { channel: 'msedge' }),
  });
  try {
    const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
    const errors = [];
    page.on('pageerror', (e) => errors.push(e.message));
    const web = path.join(root, 'internal/api/webdist');
    const html = (await fs.readFile(path.join(web, 'index.html'), 'utf8'))
      .replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi, '').replace(/<link\b[^>]*>/gi, '');
    // Serve the actual UI without a backend, credentials, or writes to user projects.
    await page.route('http://localhost/**', (route) => route.fulfill(
      route.request().url().includes('/api/') ? { json: { issues: [] } } : { contentType: 'text/html', body: html }));
    await page.goto('http://localhost/');
    await page.addStyleTag({ path: path.join(web, 'style.css') });
    await page.addScriptTag({ path: path.join(web, 'gif.js') });
    const app = await fs.readFile(path.join(web, 'app.js'), 'utf8');
    const boot = /  boot\(\);\s*\}\)\(\);\s*$/;
    assert.match(app, boot);
    // Expose the real functions only in this isolated test page.
    await page.addScriptTag({ content: app.replace(boot,
      'window.testAPI = { S, setSVG, animRoot, animStop, animPlay, animShowFrame, animShowAll, svgTextForFrame, svgTextAtTime, rasterizeSVG, exportGIF };\n})();') });

    async function mount(text, paused = false) {
      await page.evaluate(({ text, paused }) => {
        const a = testAPI;
        a.animStop();
        a.S.genMode = 'anim';
        a.setSVG(text, 'silent');
        if (paused) a.animStop();
        window.mountedRoot = a.animRoot();
      }, { text, paused });
    }

    async function exportAndDecode() {
      const pending = page.waitForEvent('download');
      await page.locator('#btnGif').click();
      const download = await pending;
      const chunks = [];
      for await (const chunk of await download.createReadStream()) chunks.push(chunk);
      const bytes = Buffer.concat(chunks);
      await page.waitForFunction(() => !testAPI.S.anim.busy);
      const decoded = await page.evaluate(async (bytes) => {
        const decoder = new ImageDecoder({ data: new Uint8Array(bytes), type: 'image/gif' });
        await decoder.tracks.ready;
        const count = decoder.tracks.selectedTrack.frameCount;
        const frames = [];
        let first;
        let different = 0;
        for (let i = 0; i < count; i++) {
          const { image } = await decoder.decode({ frameIndex: i });
          const cv = document.createElement('canvas');
          cv.width = image.displayWidth;
          cv.height = image.displayHeight;
          const ctx = cv.getContext('2d');
          ctx.drawImage(image, 0, 0);
          const rgba = ctx.getImageData(0, 0, cv.width, cv.height).data;
          let opaque = 0, sumX = 0;
          for (let p = 3; p < rgba.length; p += 4) {
            if (rgba[p] > 128) { opaque++; sumX += ((p - 3) / 4) % cv.width; }
          }
          if (!first) first = rgba;
          else if (rgba.some((v, p) => v !== first[p])) different++;
          frames.push({ opaque, x: opaque ? sumX / opaque : null, duration: image.duration });
          image.close();
        }
        decoder.close();
        return { count, different, frames };
      }, Array.from(bytes));
      return { bytes, decoded };
    }

    // Same frame/opacity timing pattern as the reported twelve-frame cat SVG.
    await mount(mixed);
    const mixedResult = await exportAndDecode();
    assert.equal(mixedResult.decoded.count, 12);
    assert.equal(mixedResult.decoded.different, 11);
    assert.ok(mixedResult.decoded.frames.every((f) => f.opaque === 512));
    const visible = await page.evaluate(() => {
      const a = testAPI;
      // Deliberately put SMIL and the frame player out of phase, as export used to do.
      a.animStop();
      a.animRoot().setCurrentTime(0.8);
      return Array.from({ length: 12 }, (_, i) => {
        a.animShowFrame(a.animRoot(), i);
        return Array.from(a.animRoot().querySelectorAll('[data-frame]')).filter((g) => {
          const s = getComputedStyle(g);
          return s.display !== 'none' && s.visibility === 'visible' && Number(s.opacity) > 0;
        }).map((g) => Number(g.dataset.frame));
      });
    });
    assert.deepEqual(visible, Array.from({ length: 12 }, (_, i) => [i]));
    console.log('PASS mixed data-frame + SMIL: 12 distinct nonempty decoded frames; preview stays visible when clocks differ');

    // Numeric frame order, presentation attributes, inline styles and stylesheet !important.
    const styled = svg(`<style>.off {display:none!important;visibility:hidden!important}</style>
      <g data-frame="1" class="off" style="display:none;opacity:0">${rect(48)}</g>
      <g data-frame="0" display="none" style="display:inline">${rect(0)}</g>`);
    await mount(styled, true);
    const styledResult = await exportAndDecode();
    assert.deepEqual(styledResult.decoded.frames.map((f) => f.x), [7.5, 55.5]);
    assert.deepEqual(styledResult.decoded.frames.map((f) => f.opaque), [512, 512]);
    assert.equal(await page.evaluate(() => testAPI.S.anim.playing), false);
    assert.equal(await page.evaluate(() => {
      testAPI.animShowAll(testAPI.animRoot());
      return testAPI.animRoot().querySelector('[data-frame="1"]').getAttribute('style');
    }), 'display:none;opacity:0');
    console.log('PASS frame ordering/visibility, paused export, original styles restored, transparent frames leave no trails');

    const smil = svg('<rect width="16" height="32" fill="red"><animate attributeName="x" from="0" to="48" dur="2s" begin="0s" repeatCount="indefinite"/></rect>');
    await mount(smil);
    const smilResult = await exportAndDecode();
    assert.equal(smilResult.decoded.count, 16);
    assert.equal(smilResult.decoded.different, 15);
    assert.ok(smilResult.decoded.frames[8].x > smilResult.decoded.frames[0].x + 20);
    assert.equal(await page.evaluate(() => testAPI.S.anim.playing && !testAPI.animRoot().animationsPaused()), true);
    const delayed = await page.evaluate(async (text) => {
      const a = testAPI;
      const input = text.replace('begin="0s"', 'begin="500ms;3s" end="2s"');
      const data = await a.rasterizeSVG(a.svgTextAtTime(input, 1, 64, 32), 64, 32);
      let count = 0, x = 0;
      for (let p = 3; p < data.data.length; p += 4) if (data.data[p] > 128) { count++; x += ((p - 3) / 4) % 64; }
      return x / count;
    }, smil);
    assert.equal(delayed, 19.5);
    console.log('PASS SMIL begin=0s, delayed/list begin and end offsets produce changing pixels');

    // A real encoding error must release busy without resetting the preview or source.
    const failure = await page.evaluate(async () => {
      const a = testAPI;
      const source = a.S.svg;
      const finish = GIFEncoder.prototype.finish;
      GIFEncoder.prototype.finish = () => { throw new Error('injected encoding failure'); };
      try { await a.exportGIF(); } finally { GIFEncoder.prototype.finish = finish; }
      return { busy: a.S.anim.busy, playing: a.S.anim.playing, sameRoot: mountedRoot === a.animRoot(), sameSource: source === a.S.svg };
    });
    assert.deepEqual(failure, { busy: false, playing: true, sameRoot: true, sameSource: true });
    await mount(svg(rect(0)), true);
    assert.equal((await exportAndDecode()).decoded.count, 1);
    console.log('PASS export failure recovery and static single-frame GIF');

    if (process.argv[2]) {
      const source = await fs.readFile(path.resolve(process.argv[2]), 'utf8');
      await mount(source);
      const result = await exportAndDecode();
      assert.equal(result.decoded.count, 12);
      assert.equal(result.decoded.different, 11);
      assert.ok(result.decoded.frames.every((f) => f.opaque > 0));
      const start = await page.evaluate(() => testAPI.S.anim.index);
      await page.waitForFunction((start) => testAPI.S.anim.index !== start, start);
      const state = await page.evaluate(() => ({
        source: testAPI.S.svg,
        connected: mountedRoot.isConnected && mountedRoot === testAPI.animRoot(),
        visible: Array.from(mountedRoot.querySelectorAll('[data-frame]')).filter((g) => {
          const s = getComputedStyle(g);
          return s.display !== 'none' && s.visibility === 'visible' && Number(s.opacity) > 0;
        }).length,
      }));
      assert.equal(state.source, source);
      assert.equal(state.connected, true);
      assert.equal(state.visible, 1);
      if (process.env.GIF_OUTPUT_PATH) await fs.writeFile(process.env.GIF_OUTPUT_PATH, result.bytes);
      console.log('PASS supplied cat SVG: 12 distinct visible GIF frames; original source and live preview preserved');
      console.log(JSON.stringify(result.decoded));
    }
    assert.deepEqual(errors, []);
  } finally {
    await browser.close();
  }
}

main().catch((e) => { console.error(e); process.exitCode = 1; });
