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
const page = await browser.newPage({ viewport: { width: 1536, height: 420 }, locale: 'zh-CN' });

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

  const prompt =
    '不要修改项目。请确认你是否拥有生成图片或视频、配置Provider、预览或导出的ShotForward工具；只简短说明能力边界。';
  await composer.fill(prompt);
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
    throw new Error(`boundary turn failed: ${JSON.stringify(turn)}`);
  }
  const changes = snapshot.change_sets.filter((item) => item.agent_turn_id === turn.id);
  if (changes.length) throw new Error(`boundary discussion created ${changes.length} ChangeSets`);
  const messages = await json(`/api/v1/agent-sessions/${session.id}/messages?limit=20`);
  const conversation = messages.turns.find((item) => item.turn.id === turn.id);
  const answer = (conversation?.messages ?? [])
    .filter((item) => item.role === 'assistant')
    .map((item) => item.text)
    .join('\n');
  if (!answer.trim()) throw new Error('boundary discussion has no answer');

  await page.waitForTimeout(500);
  const history = dock.locator('.agent-history');
  const metrics = await history.evaluate((element) => ({
    clientHeight: element.clientHeight,
    scrollHeight: element.scrollHeight,
    bottom: element.scrollHeight - element.scrollTop - element.clientHeight,
  }));
  if (metrics.scrollHeight <= metrics.clientHeight || metrics.bottom >= 64) {
    throw new Error(`current Agent answer was not auto-followed: ${JSON.stringify(metrics)}`);
  }
  await page.screenshot({ path: '/tmp/shotforward-agent-boundary-check.png' });
  process.stdout.write(
    `${JSON.stringify({ passed: true, sessionID: session.id, turnID: turn.id, changeSets: 0, autoFollow: metrics })}\n`,
  );
} finally {
  await browser.close();
}
