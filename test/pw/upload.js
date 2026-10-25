// 用法：playwright 侧执行本脚本前，先准备一张 PNG 到 SMOKE_IMG 指定的路径。
// 默认取系统临时目录下的 testimg.png，可用环境变量覆盖，避免写死本机路径。
const os = require('os');
const path = require('path');
const img = process.env.SMOKE_IMG || path.join(os.tmpdir(), 'testimg.png');

async (page) => {
  await page.setInputFiles('#fileInput', img);
  await page.waitForTimeout(800);
  const thumbs = await page.evaluate(() => document.querySelectorAll('#thumbs .thumb').length);
  return 'thumbs=' + thumbs;
}
