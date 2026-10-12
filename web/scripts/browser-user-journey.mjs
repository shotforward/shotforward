import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');

const origin = process.env.SHOTFORWARD_E2E_ORIGIN;
const pairingCode = process.env.SHOTFORWARD_E2E_PAIRING_CODE;
const projectRoot = process.env.SHOTFORWARD_E2E_PROJECT_ROOT;
const playwrightModule = process.env.SHOTFORWARD_PLAYWRIGHT_MODULE;
if (!origin || !pairingCode || !projectRoot || !playwrightModule) {
  throw new Error('SHOTFORWARD_E2E_ORIGIN, PAIRING_CODE, PROJECT_ROOT, and PLAYWRIGHT_MODULE are required');
}

const { chromium } = await import(pathToFileURL(playwrightModule).href);
const browser = await chromium.launch({ headless: true, args: ['--no-sandbox'] });
const context = await browser.newContext({ viewport: { width: 1536, height: 1024 }, locale: 'zh-CN' });
const page = await context.newPage();
const results = [];
const browserErrors = [];
let paired = false;
page.on('pageerror', (error) => browserErrors.push(`page: ${error.message}`));
page.on('console', (message) => {
  if (message.type() !== 'error') return;
  // The unpaired bootstrap probe is expected to receive one 401 before the
  // pairing form is shown. All console errors after pairing remain failures.
  if (!paired && message.text().includes('401')) return;
  browserErrors.push(`console: ${message.text()}`);
});
page.on('requestfailed', (request) => {
  // Navigating between screens intentionally aborts the current SSE stream.
  if (request.failure()?.errorText === 'net::ERR_ABORTED') return;
  browserErrors.push(`request: ${request.method()} ${request.url()} ${request.failure()?.errorText ?? ''}`);
});
page.on('response', (response) => {
  if (response.status() === 401 && response.url().endsWith('/api/v1/session')) return;
  if (response.url().includes('/api/') && !response.ok()) {
    browserErrors.push(`response: ${response.status()} ${response.url()}`);
  }
});

async function step(name, operation) {
  const started = Date.now();
  await operation();
  results.push({ name, milliseconds: Date.now() - started });
}

try {
  await step('pair local daemon', async () => {
    await page.goto(origin, { waitUntil: 'domcontentloaded' });
    await page.getByLabel('语言').selectOption('zh-CN');
    await page.getByLabel('配对码').fill(pairingCode);
    await page.getByRole('button', { name: '连接工作室' }).click();
    await page.getByRole('heading', { name: '从一个想法，到一部作品' }).waitFor();
    await page.locator('.project-card, .empty-state').first().waitFor();
    paired = true;
  });

  await step('create project', async () => {
    const existing = page.getByRole('button', { name: /浏览器验收项目/ });
    if (await existing.count()) {
      await Promise.all([page.waitForURL(/\/projects\/[^/]+$/), existing.click()]);
      await page.getByRole('heading', { name: '浏览器验收项目' }).waitFor();
      return;
    }
    await page.getByRole('button', { name: '创建第一个项目' }).click();
    await page.getByLabel('项目名称').fill('浏览器验收项目');
    await page.getByLabel('项目描述').fill('逐页模拟用户操作的隔离验收项目');
    await page.getByLabel('保存位置').fill(projectRoot);
    await page.getByRole('button', { name: '下一步' }).click();
    await page.getByText('电脑 / 电视横屏', { exact: true }).click();
    await page.getByText('720p', { exact: true }).click();
    await Promise.all([
      page.waitForURL(/\/projects\/[^/]+$/),
      page.getByRole('button', { name: '创建项目', exact: true }).click(),
    ]);
    await page.getByRole('heading', { name: '浏览器验收项目' }).waitFor();
  });

  const projectMatch = page.url().match(/\/projects\/([^/?]+)/);
  if (!projectMatch) throw new Error(`project id missing from ${page.url()}`);
  const projectID = projectMatch[1];

  await step('edit and import script', async () => {
    await page.getByRole('link', { name: '剧本', exact: true }).click();
    const editor = page.locator('.script-editor textarea');
    await editor.fill('## 第一幕\n### 初始场景\n这是一段自动保存测试。');
    await page.getByText('等待保存', { exact: true }).waitFor();
    await page.getByText('已保存', { exact: true }).waitFor({ timeout: 15_000 });
    await page.getByRole('button', { name: '导入剧本' }).click();
    await page.getByLabel('源文件路径').fill('/tmp/shotforward-browser-e2e/import-script.md');
    await page.getByRole('button', { name: '导入并替换' }).click();
    await page.waitForFunction(
      () => document.querySelector('.script-editor textarea')?.value.includes('雨夜车站'),
      undefined,
      { timeout: 15_000 },
    );
  });

  await step('create Agent session and apply ChangeSet', async () => {
    const episode = await episodeID(page, projectID);
    const existingAssets = await page.request.get(
      `${origin}/api/v1/projects/${projectID}/episodes/${episode}/assets`,
    );
    if (!existingAssets.ok()) throw new Error(`read assets before Agent step: ${existingAssets.status()}`);
    const existingAssetBody = await existingAssets.json();
    if (existingAssetBody.assets?.some((asset) => asset.identity?.name === '黄铜钥匙')) return;

    const dock = page.locator('.agent-dock');
    const composer = dock.getByPlaceholder('描述你想分析或修改的内容…');
    const createSession = dock.getByRole('button', { name: '新建会话' });
    await Promise.race([createSession.waitFor({ timeout: 30_000 }), composer.waitFor({ timeout: 30_000 })]);
    if (await createSession.isVisible()) await createSession.click();
    await composer.waitFor();
    await composer.fill(
      "Use the ShotForward tools now. Read the complete script and current assets. Create and finalize one ChangeSet that creates a prop named '黄铜钥匙' with prompt exactly '一把磨损的黄铜钥匙，电影质感'. Do not merely explain.",
    );
    await dock.getByRole('button', { name: '发送' }).click();
    const proposal = dock.locator('.changeset-card').last();
    await proposal.waitFor({ timeout: 180_000 });
    await proposal.click();
    await page.getByRole('heading', { name: /修改|黄铜|资产/ }).waitFor();
    await page.getByRole('button', { name: '应用全部修改' }).click();
    await page.getByText('已应用', { exact: true }).waitFor({ timeout: 30_000 });
    await page.getByRole('link', { name: /返回工作区/ }).click();
  });

  await step('create, edit, and import Episode assets', async () => {
    await page.getByRole('link', { name: '资产', exact: true }).click();
    await page.locator('.asset-list button').filter({ hasText: '黄铜钥匙' }).waitFor();
    const linWanAsset = page.getByRole('button', { name: /林晚.*角色/ });
    if (await linWanAsset.count()) {
      await linWanAsset.click();
    } else {
      await page.getByRole('button', { name: '新建资产' }).first().click();
      const createAssetDialog = page.getByRole('dialog', { name: '新建 Episode 资产' });
      await createAssetDialog.getByLabel('名称').fill('林晚');
      await createAssetDialog.getByLabel('视觉描述与提示词').fill('短发青年女性，深蓝风衣，冷静克制');
      await createAssetDialog.getByRole('button', { name: '创建资产' }).click();
    }
    await page.getByRole('heading', { name: '林晚' }).waitFor();
    if (!(await page.locator('.asset-preview img').count())) {
      await page.getByRole('button', { name: '导入图片' }).click();
      const importDialog = page.getByRole('dialog', { name: '导入图片' });
      await importDialog
        .getByLabel('图片文件路径')
        .fill(path.join(repositoryRoot, 'docs/images/studio-overview.png'));
      await importDialog.getByRole('button', { name: '导入并设为当前版本' }).click();
    }
    await page.locator('.asset-preview img').waitFor({ timeout: 30_000 });
    const editor = page.locator('.asset-editor');
    const prompt = editor.getByLabel('视觉描述与提示词');
    const finalPrompt = '短发青年女性，深蓝风衣，冷静克制，雨夜电影光线';
    if ((await prompt.inputValue()) !== finalPrompt) {
      await prompt.fill(finalPrompt);
      await editor.getByRole('button', { name: '保存修改' }).click();
    }
    await page
      .locator('.asset-detail .revision')
      .getByText(/v[2-9]/)
      .waitFor({ timeout: 15_000 });
  });

  await step('inspect cross-Episode asset index', async () => {
    await page.goto(`${origin}/projects/${projectID}/assets`);
    await page.getByRole('heading', { name: '林晚' }).waitFor();
    await page.getByText('主图已准备').waitFor();
    await page.getByRole('link', { name: '进入本集资产' }).first().click();
    await page.getByRole('heading', { name: /林晚|黄铜钥匙/ }).waitFor();
  });

  await step('create and edit storyboard', async () => {
    await page.getByRole('link', { name: '分镜' }).click();
    const existingScene = page.locator('.scene-rail > button').filter({ hasText: '雨夜车站' });
    if (await existingScene.count()) {
      await existingScene.click();
    } else {
      await page.getByRole('button', { name: '新建场景' }).first().click();
      await page.getByLabel('场景名称').fill('雨夜车站');
      await page.getByRole('button', { name: '创建场景' }).click();
    }
    await page.getByRole('heading', { name: '雨夜车站' }).waitFor();
    const existingClip = page.locator('.clip-grid > button').filter({ hasText: '林晚进入车站' });
    if (await existingClip.count()) {
      await existingClip.click();
    } else {
      await page.getByRole('button', { name: '添加视频单元' }).click();
      await page
        .getByText(/视频单元 1/)
        .first()
        .waitFor();
      await page.getByLabel('视频单元名称').fill('林晚进入车站');
      await page.getByLabel('画面描述').fill('林晚从雨幕中进入空旷车站');
      await page.getByLabel('对白').fill('终于到了。');
      await page.getByLabel('计划时长（秒）').fill('6');
      await page.getByRole('button', { name: '保存修改' }).click();
    }
    await page.getByRole('button', { name: '视频单元', exact: true }).click();
    await page.getByRole('button', { name: '资产', exact: true }).click();
    const assetCheckbox = page
      .locator('.clip-assets label')
      .filter({ hasText: '林晚' })
      .getByRole('checkbox');
    if (!(await assetCheckbox.isChecked())) {
      await assetCheckbox.click();
      await page.waitForFunction(() => {
        const labels = [...document.querySelectorAll('.clip-assets label')];
        return labels.some(
          (label) => label.textContent?.includes('林晚') && label.querySelector('input')?.checked,
        );
      });
    }
    await page.getByRole('button', { name: '生成', exact: true }).click();
    const firstPrompt = page.getByLabel('首帧图片提示词');
    const videoPrompt = page.getByLabel('视频提示词');
    const targetFirstPrompt = '雨夜车站入口，林晚撑伞走入，电影感宽银幕';
    const targetVideoPrompt = '镜头缓慢推进，雨滴反射车站灯光';
    if (
      (await firstPrompt.inputValue()) !== targetFirstPrompt ||
      (await videoPrompt.inputValue()) !== targetVideoPrompt
    ) {
      await firstPrompt.fill(targetFirstPrompt);
      await videoPrompt.fill(targetVideoPrompt);
      await page.getByRole('button', { name: '保存修改' }).click();
    }
    if (!(await page.locator('.clip-frame img').count())) {
      await page.getByRole('button', { name: '生成首帧' }).click();
      await page.getByText(/1 个任务已提交/).waitFor({ timeout: 30_000 });
    }
  });

  await step('observe real generation in task center', async () => {
    await page.goto(`${origin}/tasks`);
    await page.getByRole('heading', { name: '任务中心' }).waitFor();
    // The Task Center is ordered by updated_at descending, so the just-submitted
    // image task is the first matching row even when older retries are present.
    const imageTask = page.locator('.task-table > button').filter({ hasText: '生成图片' }).first();
    await imageTask.waitFor();
    await imageTask.locator('.task-state--succeeded').waitFor({ timeout: 180_000 });
    await imageTask.click();
    const taskDetail = page.locator('.task-detail');
    await taskDetail.getByRole('link', { name: '定位结果' }).waitFor();
    await page.locator('.task-metrics button').filter({ hasText: '已完成' }).click();
    await page.locator('.task-metrics button').filter({ hasText: '全部任务' }).click();
    await taskDetail.getByRole('link', { name: '定位结果' }).click();
    await page.locator('.clip-frame img').waitFor({ timeout: 30_000 });
  });

  await step('verify preview without configured video provider', async () => {
    await page.goto(`${origin}/projects/${projectID}/episodes/${await episodeID(page, projectID)}/preview`);
    await page.getByRole('heading', { name: '还没有可连续播放的视频' }).waitFor();
    const exportButton = page.getByRole('button', { name: '导出 MP4' });
    if (!(await exportButton.isDisabled())) throw new Error('export must be disabled without current videos');
    await page.getByText('没有当前视频版本').waitFor();
  });

  await step('delete and restore storyboard scene', async () => {
    const episode = await episodeID(page, projectID);
    await page.goto(`${origin}/projects/${projectID}/episodes/${episode}/storyboard`);
    await page.getByRole('button', { name: '删除场景' }).click();
    await page.getByRole('button', { name: '移到回收站' }).click();
    await page.goto(`${origin}/projects/${projectID}/trash`);
    await page.getByText('雨夜车站', { exact: true }).first().click();
    await page.getByRole('button', { name: '恢复场景' }).click();
    await page.getByText('回收站是空的').waitFor({ timeout: 30_000 });
  });

  await step('delete and restore referenced asset', async () => {
    const episode = await episodeID(page, projectID);
    await page.goto(`${origin}/projects/${projectID}/episodes/${episode}/assets`);
    await page.getByRole('button', { name: /黄铜钥匙/ }).click();
    await page.getByRole('button', { name: '移到回收站' }).click();
    await page.getByRole('button', { name: '移到回收站' }).last().click();
    await page.goto(`${origin}/projects/${projectID}/trash`);
    await page.getByText('黄铜钥匙', { exact: true }).first().click();
    await page.getByRole('button', { name: '恢复资产' }).click();
    await page.getByText('回收站是空的').waitFor({ timeout: 30_000 });
  });

  await step('exercise project and Studio settings', async () => {
    await page.goto(`${origin}/projects/${projectID}/settings`);
    await page.getByRole('heading', { name: '项目概览' }).waitFor();
    await page.getByText('1280 × 720').waitFor();
    await page.getByRole('link', { name: '设置', exact: true }).click();
    await page.getByRole('heading', { name: '通用' }).waitFor();
    await page.getByLabel('界面语言').selectOption('en');
    await page.getByRole('heading', { name: 'General' }).waitFor();
    await page.getByLabel('Interface language').selectOption('zh-CN');
    await page.getByRole('link', { name: 'Codex Runtime' }).click();
    await page.getByText('本机 Codex 已连接').waitFor({ timeout: 30_000 });
    await page.getByRole('button', { name: '重新检查' }).click();
    await page.getByRole('link', { name: '生成服务' }).click();
    await page.getByPlaceholder('搜索生成模型').fill('Codex');
    await page.getByRole('button', { name: /Codex Image/ }).click();
    await page.getByRole('button', { name: '检查连接' }).click();
    await page.getByText('可用', { exact: true }).first().waitFor({ timeout: 30_000 });
    await page.getByPlaceholder('搜索生成模型').fill('Seedance');
    await page
      .getByRole('button', { name: /Seedance 2.5/ })
      .first()
      .click();
    await page.getByLabel('API Key').waitFor();
    await page.getByRole('link', { name: '隐私与安全' }).click();
    await page.getByText('本地项目数据').waitFor();
  });

  await step('search and reopen project from library', async () => {
    await page.getByRole('link', { name: '项目库' }).click();
    await page.getByPlaceholder('搜索项目').fill('浏览器验收');
    await page.getByRole('heading', { name: '浏览器验收项目' }).waitFor();
    await page.getByRole('button', { name: /浏览器验收项目/ }).click();
    await page.getByRole('heading', { name: '浏览器验收项目' }).waitFor();
  });

  if (browserErrors.length) throw new Error(`browser errors: ${browserErrors.join(' | ')}`);
  process.stdout.write(`${JSON.stringify({ passed: results.length, results })}\n`);
} catch (error) {
  await page.screenshot({ path: '/tmp/shotforward-browser-e2e/failure.png', fullPage: true });
  if (browserErrors.length) process.stderr.write(`${browserErrors.join('\n')}\n`);
  process.stderr.write(`${error.stack || error}\n`);
  process.exitCode = 1;
} finally {
  await browser.close();
}

async function episodeID(page, projectID) {
  const response = await page.request.get(`${origin}/api/v1/projects/${projectID}`);
  if (!response.ok()) throw new Error(`read project for episode id: ${response.status()}`);
  const body = await response.json();
  const id = body.episodes?.[0]?.episode_id;
  if (!id) throw new Error('created project has no Episode');
  return id;
}
