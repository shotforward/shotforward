import { pathToFileURL } from 'node:url';

const origin = process.env.SHOTFORWARD_E2E_ORIGIN;
const pairingCode = process.env.SHOTFORWARD_E2E_PAIRING_CODE;
const projectID = process.env.SHOTFORWARD_E2E_PROJECT_ID;
const episodeID = process.env.SHOTFORWARD_E2E_EPISODE_ID;
const playwrightModule = process.env.SHOTFORWARD_PLAYWRIGHT_MODULE;
if (!origin || !pairingCode || !projectID || !episodeID || !playwrightModule) {
  throw new Error('missing E2E environment');
}

const { chromium } = await import(pathToFileURL(playwrightModule).href);
const browser = await chromium.launch({ headless: true, args: ['--no-sandbox'] });
const page = await browser.newPage({ viewport: { width: 1536, height: 920 }, locale: 'zh-CN' });

try {
  await page.goto(origin, { waitUntil: 'domcontentloaded' });
  await page.getByLabel('语言').selectOption('zh-CN');
  await page.getByLabel('配对码').fill(pairingCode);
  await page.getByRole('button', { name: '连接工作室' }).click();
  await page.getByRole('heading', { name: '从一个想法，到一部作品' }).waitFor();
  await page.goto(`${origin}/projects/${projectID}/episodes/${episodeID}/script`);
  const history = page.locator('.agent-history');
  await history.waitFor({ timeout: 30_000 });
  await page.locator('.conversation-turn').last().waitFor({ timeout: 30_000 });
  const metrics = await page.evaluate(() => {
    const read = (selector) => {
      const element = document.querySelector(selector);
      if (!element) return null;
      const box = element.getBoundingClientRect();
      return {
        height: Math.round(box.height),
        scrollHeight: element.scrollHeight,
        clientHeight: element.clientHeight,
        overflowY: getComputedStyle(element).overflowY,
      };
    };
    return {
      viewportHeight: window.innerHeight,
      documentHeight: document.documentElement.scrollHeight,
      page: read('.creative-page'),
      layout: read('.creative-layout'),
      dock: read('.agent-dock'),
      history: read('.agent-history'),
    };
  });
  if (metrics.page?.height !== metrics.viewportHeight || metrics.dock?.height > metrics.viewportHeight) {
    throw new Error(`Agent dock is not viewport-bounded: ${JSON.stringify(metrics)}`);
  }
  if (metrics.history?.overflowY !== 'auto' || metrics.history.scrollHeight <= metrics.history.clientHeight) {
    throw new Error(`Agent history is not independently scrollable: ${JSON.stringify(metrics)}`);
  }
  await history.evaluate((element) => {
    element.scrollTop = 0;
    element.dispatchEvent(new Event('scroll', { bubbles: true }));
  });
  await page.getByRole('button', { name: '回到最新' }).click();
  await page.waitForFunction(() => {
    const element = document.querySelector('.agent-history');
    return element && element.scrollHeight - element.scrollTop - element.clientHeight < 64;
  });
  await page.screenshot({ path: '/tmp/shotforward-agent-scroll-check.png' });
  process.stdout.write(`${JSON.stringify({ passed: true, metrics })}\n`);
} finally {
  await browser.close();
}
