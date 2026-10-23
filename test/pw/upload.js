async (page) => {
  await page.setInputFiles('#fileInput', 'C:/Users/tianj/AppData/Local/Temp/testimg.png');
  await page.waitForTimeout(800);
  const thumbs = await page.evaluate(() => document.querySelectorAll('#thumbs .thumb').length);
  return 'thumbs=' + thumbs;
}
