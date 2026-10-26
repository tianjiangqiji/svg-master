const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const root = path.resolve(__dirname, '../..');
const { chromium } = require(require.resolve('playwright', {
  paths: [__dirname, path.join(root, '.tools/ui-test')],
}));

async function main() {
  const browser = await chromium.launch({ headless: true,
    ...(process.env.GIF_BROWSER_PATH ? { executablePath: process.env.GIF_BROWSER_PATH } : { channel: 'msedge' }),
  });
  try {
    const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
    const web = path.join(root, 'internal/api/webdist');
    const html = (await fs.readFile(path.join(web, 'index.html'), 'utf8'))
      .replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi, '').replace(/<link\b[^>]*>/gi, '');
    await page.route('http://localhost/**', (route) => route.fulfill(
      route.request().url().includes('/style.css')
        ? { contentType: 'text/css', body: fs.readFile(path.join(web, 'style.css'), 'utf8') }
        : route.request().url().includes('/app.js')
          ? { contentType: 'text/javascript', body: fs.readFile(path.join(web, 'app.js'), 'utf8') }
          : { contentType: 'text/html', body: html },
    ));
    await page.goto('http://localhost/');
    await page.waitForSelector('#btnSave');

    const gifButton = page.locator('#btnGif');
    const saveButton = page.locator('#btnSave');
    const gifAtTop = await gifButton.evaluate((el) => el.parentElement.id === 'topActions');
    assert.equal(gifAtTop, true, 'GIF export button should be in the top actions beside Save');
    assert.equal(await gifButton.isVisible(), false, 'GIF export button should be hidden outside animation mode');

    await page.locator('[data-mode="anim"]').click();
    assert.equal(await gifButton.isVisible(), true, 'GIF export button should be visible in animation mode');
    assert.match(await saveButton.innerText(), /代码/, 'Save should identify code export in animation mode');
    assert.match(await saveButton.getAttribute('title'), /SVG/, 'Save title should identify SVG source export');

    await page.locator('[data-mode="quick"]').click();
    assert.equal(await gifButton.isVisible(), false, 'GIF export button should hide after leaving animation mode');
    console.log('PASS GIF button placement/visibility and animation-mode source-save affordance');
  } finally {
    await browser.close();
  }
}

main().catch((error) => { console.error(error); process.exitCode = 1; });
