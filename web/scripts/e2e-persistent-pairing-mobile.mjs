import { pathToFileURL } from 'node:url';

const origin = process.env.SHOTFORWARD_E2E_ORIGIN;
const pairingCode = process.env.SHOTFORWARD_E2E_PAIRING_CODE;
const playwrightModule = process.env.SHOTFORWARD_PLAYWRIGHT_MODULE;
if (!origin || !pairingCode || !playwrightModule) throw new Error('missing E2E environment');

const { chromium } = await import(pathToFileURL(playwrightModule).href);
const browser = await chromium.launch({ headless: true, args: ['--no-sandbox'] });

async function pair(context, url = origin) {
  const page = await context.newPage();
  await page.goto(url, { waitUntil: 'domcontentloaded' });
  await page.getByLabel(/语言|Language/).selectOption('zh-CN');
  await page.getByLabel('配对码').fill(pairingCode);
  await page.getByRole('button', { name: '连接工作室' }).click();
  await page.getByRole('heading', { name: '从一个想法，到一部作品' }).waitFor({ timeout: 30_000 });
  return page;
}

try {
  for (let index = 0; index < 2; index++) {
    const context = await browser.newContext({ viewport: { width: 1280, height: 800 }, locale: 'zh-CN' });
    await pair(context);
    await context.close();
  }

  const mobile = await browser.newContext({
    viewport: { width: 390, height: 844 },
    screen: { width: 390, height: 844 },
    deviceScaleFactor: 3,
    hasTouch: true,
    isMobile: true,
    locale: 'zh-CN',
  });
  const page = await pair(mobile, `${origin}/?desktop=1`);
  const desktop = await page.evaluate(() => ({
    innerWidth: window.innerWidth,
    viewport: document.querySelector('meta[name="viewport"]')?.getAttribute('content'),
    stored: window.localStorage.getItem('shotforward.desktop-layout'),
  }));
  if (desktop.innerWidth < 1400 || desktop.stored !== '1' || !desktop.viewport?.includes('width=1440')) {
    throw new Error(`desktop viewport was not applied: ${JSON.stringify(desktop)}`);
  }
  await page.screenshot({ path: '/tmp/shotforward-mobile-desktop.png', fullPage: false });

  await page.goto(`${origin}/?desktop=0`, { waitUntil: 'domcontentloaded' });
  const responsive = await page.evaluate(() => ({
    innerWidth: window.innerWidth,
    viewport: document.querySelector('meta[name="viewport"]')?.getAttribute('content'),
    stored: window.localStorage.getItem('shotforward.desktop-layout'),
  }));
  if (
    responsive.innerWidth > 700 ||
    responsive.stored !== null ||
    !responsive.viewport?.includes('device-width')
  ) {
    throw new Error(`responsive viewport was not restored: ${JSON.stringify(responsive)}`);
  }
  await mobile.close();

  process.stdout.write(`${JSON.stringify({ passed: true, consecutivePairings: 3, desktop, responsive })}\n`);
} finally {
  await browser.close();
}
