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

async function json(path) {
  const response = await page.request.get(`${origin}${path}`);
  if (!response.ok()) throw new Error(`GET ${path}: ${response.status()}`);
  return response.json();
}

try {
  await page.goto(origin, { waitUntil: 'domcontentloaded' });
  await page.getByLabel('语言').selectOption('zh-CN');
  await page.getByLabel('配对码').fill(pairingCode);
  await page.getByRole('button', { name: '连接工作室' }).click();
  await page.getByRole('heading', { name: '从一个想法，到一部作品' }).waitFor();
  await page.goto(`${origin}/projects/${projectID}/episodes/${episodeID}/script`);

  const dock = page.locator('.agent-dock');
  await dock.getByRole('button', { name: '切换 Agent 会话' }).click();
  await dock
    .getByRole('button', { name: /新建会话/ })
    .last()
    .click();
  const composer = dock.getByPlaceholder('描述你想分析或修改的内容…');
  await composer.waitFor({ timeout: 30_000 });
  const sessions = await json(`/api/v1/projects/${projectID}/episodes/${episodeID}/agent-sessions`);
  const session = sessions.sessions.find((item) => item.status === 'active');
  if (!session) throw new Error('new active Agent session was not created');

  await composer.fill(
    '不要修改项目，也不要使用工具。请用 Markdown 回复并包含：二级标题“Markdown 验证”；一个粗体列表项“重点”；一个两列表格；一个 JSON 围栏代码块；一个指向 https://openai.com 的链接。',
  );
  await dock.getByRole('button', { name: '发送' }).click();

  const deadline = Date.now() + 180_000;
  let snapshot;
  while (Date.now() < deadline) {
    snapshot = await json(`/api/v1/agent-sessions/${session.id}/snapshot`);
    const turn = snapshot.turns.at(-1);
    if (turn && ['completed', 'succeeded', 'failed', 'interrupted'].includes(turn.status)) break;
    await page.waitForTimeout(500);
  }
  const turn = snapshot?.turns.at(-1);
  if (!turn || !['completed', 'succeeded'].includes(turn.status)) {
    throw new Error(`Markdown turn failed: ${JSON.stringify(turn)}`);
  }
  if (snapshot.change_sets.some((item) => item.agent_turn_id === turn.id)) {
    throw new Error('Markdown discussion unexpectedly created a ChangeSet');
  }

  const message = dock.locator('.conversation-message--assistant .agent-markdown').last();
  await message.waitFor({ timeout: 30_000 });
  await message.locator('h2').waitFor();
  const structure = {
    heading: await message.locator('h2').count(),
    strong: await message.locator('strong').count(),
    list: await message.locator('ul, ol').count(),
    table: await message.locator('table').count(),
    code: await message.locator('pre code').count(),
    externalLink: await message.locator('a[href="https://openai.com"][target="_blank"]').count(),
  };
  if (Object.values(structure).some((count) => count < 1)) {
    throw new Error(`Markdown structure missing: ${JSON.stringify(structure)}`);
  }
  await page.screenshot({ path: '/tmp/shotforward-agent-markdown.png' });
  process.stdout.write(
    `${JSON.stringify({ passed: true, sessionID: session.id, turnID: turn.id, structure })}\n`,
  );
} finally {
  await browser.close();
}
