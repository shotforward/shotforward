import { pathToFileURL } from 'node:url';

const origin = process.env.SHOTFORWARD_E2E_ORIGIN;
const pairingCode = process.env.SHOTFORWARD_E2E_PAIRING_CODE;
const projectRoot = process.env.SHOTFORWARD_E2E_PROJECT_ROOT;
const playwrightModule = process.env.SHOTFORWARD_PLAYWRIGHT_MODULE;
if (!origin || !pairingCode || !projectRoot || !playwrightModule) throw new Error('missing E2E environment');

const { chromium } = await import(pathToFileURL(playwrightModule).href);
const browser = await chromium.launch({ headless: true, args: ['--no-sandbox'] });
const context = await browser.newContext({ viewport: { width: 1536, height: 920 }, locale: 'zh-CN' });
const page = await context.newPage();
const results = [];
const browserErrors = [];
let paired = false;
let projectID = '';
let episodeID = '';
let sessionID = '';

page.on('pageerror', (error) => browserErrors.push(`page: ${error.message}`));
page.on('console', (message) => {
  if (message.type() === 'error' && (paired || !message.text().includes('401')))
    browserErrors.push(`console: ${message.text()}`);
});
page.on('requestfailed', (request) => {
  if (request.failure()?.errorText !== 'net::ERR_ABORTED')
    browserErrors.push(`request: ${request.method()} ${request.url()} ${request.failure()?.errorText}`);
});
page.on('response', (response) => {
  if (response.status() >= 500)
    browserErrors.push(`response: ${response.status()} ${response.request().method()} ${response.url()}`);
});

async function step(name, operation) {
  const started = Date.now();
  const detail = await operation();
  const result = { name, milliseconds: Date.now() - started, ...detail };
  results.push(result);
  process.stdout.write(`${JSON.stringify({ step: result })}\n`);
}

async function api(path) {
  const response = await page.request.get(`${origin}${path}`);
  if (!response.ok()) throw new Error(`GET ${path}: ${response.status()}`);
  return response.json();
}

async function snapshot() {
  return api(`/api/v1/agent-sessions/${sessionID}/snapshot`);
}

async function waitUntil(check, timeout = 300_000) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    last = await check();
    if (last) return last;
    await new Promise((resolve) => setTimeout(resolve, 800));
  }
  throw new Error(`condition timed out; last=${JSON.stringify(last)}`);
}

async function sendAndWait(text, timeout = 300_000) {
  const before = await snapshot();
  const prior = new Set(before.turns.map((turn) => turn.id));
  const dock = page.locator('.agent-dock');
  const composer = dock.getByPlaceholder('描述你想分析或修改的内容…');
  await composer.fill(text);
  await dock.getByRole('button', { name: '发送' }).click();
  let sawLiveOutput = false;
  const completed = await waitUntil(async () => {
    sawLiveOutput ||= (await dock.locator('.is-streaming, .agent-activity').count()) > 0;
    const current = await snapshot();
    const turn = current.turns.find((item) => !prior.has(item.id));
    if (!turn || !['completed', 'succeeded', 'failed', 'interrupted'].includes(turn.status)) return false;
    return { current, turn };
  }, timeout);
  if (completed.turn.status !== 'completed' && completed.turn.status !== 'succeeded') {
    throw new Error(`Agent turn ${completed.turn.id} failed: ${JSON.stringify(completed.turn.failure)}`);
  }
  await page.waitForTimeout(500);
  const messages = await api(`/api/v1/agent-sessions/${sessionID}/messages?limit=50`);
  const conversation = messages.turns.find((item) => item.turn.id === completed.turn.id);
  const turnMessages = conversation?.messages ?? [];
  sawLiveOutput ||= turnMessages.some(
    (message) => message.role === 'assistant' && message.phase === 'commentary' && message.text.trim(),
  );
  return {
    turn: completed.turn,
    changeSets: completed.current.change_sets.filter((item) => item.agent_turn_id === completed.turn.id),
    messages: turnMessages,
    sawLiveOutput,
  };
}

async function undoFromCard(changeSetID) {
  const card = page
    .locator('.changeset-card')
    .filter({ has: page.locator(`a[href="/change-sets/${changeSetID}"]`) });
  await card.getByRole('button', { name: '撤销' }).click();
  return waitUntil(async () => {
    const current = await snapshot();
    const original = current.change_sets.find((item) => item.id === changeSetID);
    return original?.reverted_by_change_set_id ? { current, original } : false;
  }, 30_000);
}

try {
  await step('01 deploy page and pair', async () => {
    await page.goto(origin, { waitUntil: 'domcontentloaded' });
    await page.getByLabel('语言').selectOption('zh-CN');
    await page.getByLabel('配对码').fill(pairingCode);
    await page.getByRole('button', { name: '连接工作室' }).click();
    await page.getByRole('heading', { name: '从一个想法，到一部作品' }).waitFor();
    paired = true;
    return { passed: true };
  });

  await step('02 create isolated project', async () => {
    await page
      .getByRole('button', { name: /创建第一个项目|新建项目/ })
      .first()
      .click();
    await page.getByRole('textbox', { name: '项目名称' }).fill(`Agent需求逐项验收-${Date.now()}`);
    await page.getByLabel('项目描述').fill('Agent 历史、Changeset、Undo 与 Storyboard 粒度自动验收');
    await page.getByLabel('保存位置').fill(projectRoot);
    await page.getByRole('button', { name: '下一步' }).click();
    await page.getByText('电脑 / 电视横屏', { exact: true }).click();
    await page.getByText('720p', { exact: true }).click();
    await Promise.all([
      page.waitForURL(/\/projects\/[^/]+$/),
      page.getByRole('button', { name: '创建项目', exact: true }).click(),
    ]);
    projectID = page.url().match(/\/projects\/([^/?]+)/)?.[1] ?? '';
    const project = await api(`/api/v1/projects/${projectID}`);
    episodeID = project.episodes[0].episode_id;
    await page.goto(`${origin}/projects/${projectID}/episodes/${episodeID}/script`);
    const editor = page.locator('.script-editor textarea');
    await editor.fill(
      '## 第一场 荣国府门前\n黛玉乘轿来到荣国府。\n\n## 第二场 正房\n黛玉拜见贾母，众人相见。',
    );
    await page.getByText('等待保存', { exact: true }).waitFor({ timeout: 15_000 });
    await page.getByText('已保存', { exact: true }).waitFor({ timeout: 15_000 });
    return { passed: true, projectID, episodeID };
  });

  await step('03 create Agent session', async () => {
    const dock = page.locator('.agent-dock');
    const create = dock.getByRole('button', { name: '新建会话' });
    await create.waitFor({ timeout: 30_000 });
    await create.click();
    await dock.getByPlaceholder('描述你想分析或修改的内容…').waitFor({ timeout: 30_000 });
    const sessions = await api(`/api/v1/projects/${projectID}/episodes/${episodeID}/agent-sessions`);
    sessionID = sessions.sessions.find((item) => item.status === 'active').id;
    return { passed: true, sessionID };
  });

  await step('04 Agent dock horizontal resize', async () => {
    const dock = page.locator('.agent-dock');
    const handle = dock.locator('.agent-resize-handle');
    const before = await dock.boundingBox();
    const box = await handle.boundingBox();
    if (!before || !box) throw new Error('resize geometry unavailable');
    await page.mouse.move(box.x + box.width / 2, box.y + 100);
    await page.mouse.down();
    await page.mouse.move(box.x - 84, box.y + 100, { steps: 5 });
    await page.mouse.up();
    const after = await dock.boundingBox();
    if (!after || after.width < before.width + 60)
      throw new Error(`dock did not resize: ${before.width} -> ${after?.width}`);
    return { passed: true, before: Math.round(before.width), after: Math.round(after.width) };
  });

  let discussion;
  await step('05 discussion creates no ChangeSet', async () => {
    discussion = await sendAndWait('只讨论，不修改项目：请用两句话分析“黛玉进府”开场的情绪基调。');
    if (discussion.changeSets.length !== 0) throw new Error('discussion unexpectedly created a ChangeSet');
    if (!discussion.messages.some((message) => message.role === 'assistant' && message.text.trim()))
      throw new Error('discussion has no assistant output');
    return { passed: true, turnID: discussion.turn.id, messages: discussion.messages.length };
  });

  await step('06 conversation history survives reload', async () => {
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page
      .locator('.agent-dock')
      .getByPlaceholder('描述你想分析或修改的内容…')
      .waitFor({ timeout: 30_000 });
    await page.getByText('只讨论，不修改项目：请用两句话分析“黛玉进府”开场的情绪基调。').waitFor();
    const assistant = discussion.messages.find(
      (message) => message.role === 'assistant' && message.text.trim(),
    );
    await page.getByText(assistant.text, { exact: true }).waitFor();
    return { passed: true };
  });

  let scriptChange;
  let scriptBefore;
  await step('07 script request auto-applies script ChangeSet', async () => {
    const episode = await api(`/api/v1/projects/${projectID}/episodes/${episodeID}`);
    scriptBefore = episode.episode?.script?.markdown ?? episode.script?.markdown ?? '';
    scriptChange = await sendAndWait(
      '请把本集剧本改写并写入项目。保留两场，完整剧本中必须包含独特句子“黛玉轻轻掀起轿帘，秋光落在眉间。”，总长度控制在600字以内。不要只在对话中给剧本。',
      360_000,
    );
    const applied = scriptChange.changeSets.filter((item) => item.status === 'applied');
    if (applied.length !== 1) throw new Error(`script applied ChangeSets=${applied.length}`);
    const operations = await api(`/api/v1/change-sets/${applied[0].id}/operations?limit=100`);
    if (!operations.operations.some((item) => item.type === 'replace_episode_script'))
      throw new Error('script operation missing');
    const episodeAfter = await api(`/api/v1/projects/${projectID}/episodes/${episodeID}`);
    const markdown = episodeAfter.episode?.script?.markdown ?? episodeAfter.script?.markdown ?? '';
    if (!markdown.includes('黛玉轻轻掀起轿帘，秋光落在眉间。'))
      throw new Error('script marker not persisted');
    if (!scriptChange.sawLiveOutput) throw new Error('no streaming progress/activity was observed');
    return { passed: true, changeSetID: applied[0].id, streamed: true };
  });

  await step('08 deterministic script Undo', async () => {
    const original = scriptChange.changeSets.find((item) => item.status === 'applied');
    const undone = await undoFromCard(original.id);
    const inverse = undone.current.change_sets.find(
      (item) => item.id === undone.original.reverted_by_change_set_id,
    );
    if (!inverse || inverse.reverts_change_set_id !== original.id || inverse.status !== 'applied')
      throw new Error('inverse ChangeSet link missing');
    const episode = await api(`/api/v1/projects/${projectID}/episodes/${episodeID}`);
    const markdown = episode.episode?.script?.markdown ?? episode.script?.markdown ?? '';
    if (markdown !== scriptBefore) throw new Error('Undo did not restore exact script');
    return { passed: true, inverseChangeSetID: inverse.id };
  });

  let assetChange;
  await step('09 asset definitions auto-apply as bounded ChangeSet', async () => {
    assetChange = await sendAndWait(
      '请读取剧本和当前资产，并写入两个资产定义：角色“林黛玉”，提示词“清秀纤弱的古典少女，含愁眉眼”；场景“荣国府正门”，提示词“朱门石狮，古典贵族府邸”。请使用Changeset写入项目。',
      360_000,
    );
    const applied = assetChange.changeSets.filter((item) => item.status === 'applied');
    if (!applied.length) throw new Error('asset ChangeSet was not applied');
    const operationCounts = [];
    for (const item of applied) {
      const operations = await api(`/api/v1/change-sets/${item.id}/operations?limit=100`);
      const creations = operations.operations.filter(
        (operation) => operation.type === 'create_episode_asset',
      );
      operationCounts.push(creations.length);
      if (!creations.length || creations.length > 20)
        throw new Error(`invalid Asset ChangeSet size ${creations.length}`);
    }
    const assets = await api(`/api/v1/projects/${projectID}/episodes/${episodeID}/assets`);
    const names = assets.assets.map((item) => item.identity.name);
    if (!names.includes('林黛玉') || !names.includes('荣国府正门'))
      throw new Error(`assets not persisted: ${names.join(',')}`);
    return { passed: true, changeSets: applied.length, operationCounts };
  });

  await step('10 asset Undo removes created definitions', async () => {
    const applied = assetChange.changeSets.filter((item) => item.status === 'applied').reverse();
    for (const item of applied) await undoFromCard(item.id);
    const assets = await api(`/api/v1/projects/${projectID}/episodes/${episodeID}/assets`);
    const names = assets.assets.map((item) => item.identity.name);
    if (names.includes('林黛玉') || names.includes('荣国府正门'))
      throw new Error('created Assets survived Undo');
    return { passed: true };
  });

  let storyboardChange;
  await step('11 large Storyboard splits one Scene per ChangeSet', async () => {
    storyboardChange = await sendAndWait(
      '请根据当前两场剧本创建Storyboard并写入项目：第一场标题“荣府门前”，包含一个视频单元；第二场标题“拜见贾母”，包含一个视频单元。每个视频单元5秒，不引用资产。必须逐场使用Changeset完成。',
      480_000,
    );
    const applied = storyboardChange.changeSets.filter((item) => item.status === 'applied');
    if (applied.length !== 2) throw new Error(`Storyboard applied ChangeSets=${applied.length}, want 2`);
    for (const item of applied) {
      const operations = await api(`/api/v1/change-sets/${item.id}/operations?limit=100`);
      const scenes = operations.operations.filter((operation) => operation.type === 'create_scene');
      if (scenes.length !== 1) throw new Error(`ChangeSet ${item.id} Scene count=${scenes.length}`);
    }
    const storyboard = await api(`/api/v1/projects/${projectID}/episodes/${episodeID}/storyboard`);
    const scenes = storyboard.storyboard?.scenes ?? storyboard.scenes ?? [];
    if (scenes.length !== 2) throw new Error(`persisted Scene count=${scenes.length}`);
    return { passed: true, changeSets: applied.map((item) => item.id) };
  });

  await step('12 per-Scene Storyboard Undo', async () => {
    const applied = storyboardChange.changeSets.filter((item) => item.status === 'applied');
    await undoFromCard(applied.at(-1).id);
    const storyboard = await api(`/api/v1/projects/${projectID}/episodes/${episodeID}/storyboard`);
    const scenes = storyboard.storyboard?.scenes ?? storyboard.scenes ?? [];
    if (scenes.length !== 1) throw new Error(`Scene count after Undo=${scenes.length}`);
    return { passed: true, remainingScenes: scenes.length };
  });

  await step('13 message-only scroll and latest-answer follow', async () => {
    const history = page.locator('.agent-history');
    const metrics = await history.evaluate((element) => ({
      overflowY: getComputedStyle(element).overflowY,
      scrollHeight: element.scrollHeight,
      clientHeight: element.clientHeight,
      bottom: element.scrollHeight - element.scrollTop - element.clientHeight,
      bodyOverflow: getComputedStyle(document.body).overflowY,
    }));
    if (metrics.overflowY !== 'auto' || metrics.scrollHeight <= metrics.clientHeight)
      throw new Error(`history is not independently scrollable: ${JSON.stringify(metrics)}`);
    if (metrics.bottom > 64)
      throw new Error(`history did not follow latest answer: bottom=${metrics.bottom}`);
    await history.evaluate((element) => {
      element.scrollTop = 0;
      element.dispatchEvent(new Event('scroll', { bubbles: true }));
    });
    const latest = page.getByRole('button', { name: '回到最新' });
    await latest.waitFor();
    await latest.click();
    await page.waitForFunction(() => {
      const element = document.querySelector('.agent-history');
      return element && element.scrollHeight - element.scrollTop - element.clientHeight < 64;
    });
    return { passed: true, scrollHeight: metrics.scrollHeight, clientHeight: metrics.clientHeight };
  });

  await step('14 MCP boundary excludes external-effect actions', async () => {
    const forbidden = ['generation', 'provider', 'preview', 'export'];
    const response = await sendAndWait(
      '不要修改项目。请确认你是否拥有生成图片、配置Provider、预览或导出的ShotForward工具；只简短说明能力边界。',
    );
    if (response.changeSets.length) throw new Error('capability discussion created ChangeSet');
    const text = response.messages
      .filter((item) => item.role === 'assistant')
      .map((item) => item.text)
      .join('\n')
      .toLowerCase();
    if (!forbidden.some((word) => text.includes(word)))
      throw new Error('capability boundary response missing');
    return { passed: true };
  });

  if (browserErrors.length) throw new Error(browserErrors.join(' | '));
  await page.screenshot({ path: '/tmp/shotforward-agent-requirements-final.png', fullPage: true });
  process.stdout.write(
    `${JSON.stringify({ passed: results.length, projectID, episodeID, sessionID, results })}\n`,
  );
} catch (error) {
  await page.screenshot({ path: '/tmp/shotforward-agent-requirements-failure.png', fullPage: true });
  process.stderr.write(`${error.stack || error}\n`);
  if (browserErrors.length) process.stderr.write(`${browserErrors.join('\n')}\n`);
  process.stderr.write(
    `${JSON.stringify({ passed: results.length, projectID, episodeID, sessionID, results })}\n`,
  );
  process.exitCode = 1;
} finally {
  await browser.close();
}
