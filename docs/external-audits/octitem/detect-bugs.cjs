#!/usr/bin/env node
'use strict';

// Offline regression probes against the JavaScript shipped inside the VSIX.
// No npm packages, backend credentials, VS Code install, or remote calls needed.
const fs = require('node:fs');
const fsp = fs.promises;
const path = require('node:path');
const os = require('node:os');
const vm = require('node:vm');
const zlib = require('node:zlib');
const { createHash } = require('node:crypto');
const { isDeepStrictEqual } = require('node:util');

function zipEntry(zip, wanted) {
  let end = -1;
  for (let i = zip.length - 22; i >= Math.max(0, zip.length - 65557); i--) {
    if (zip.readUInt32LE(i) === 0x06054b50 && i + 22 + zip.readUInt16LE(i + 20) === zip.length) {
      end = i; break;
    }
  }
  if (end < 0) throw new Error('VSIX ZIP end record missing');
  if (zip.readUInt16LE(end + 4) || zip.readUInt16LE(end + 6)) throw new Error('Multi-disk ZIP unsupported');
  let at = zip.readUInt32LE(end + 16);
  for (let i = 0; i < zip.readUInt16LE(end + 10); i++) {
    if (zip.readUInt32LE(at) !== 0x02014b50) throw new Error('Invalid ZIP directory');
    const flags = zip.readUInt16LE(at + 8);
    const method = zip.readUInt16LE(at + 10);
    const compressed = zip.readUInt32LE(at + 20);
    const size = zip.readUInt32LE(at + 24);
    const nameLen = zip.readUInt16LE(at + 28);
    const name = zip.subarray(at + 46, at + 46 + nameLen).toString('utf8');
    if (name === wanted) {
      if (flags & 1) throw new Error('Encrypted ZIP unsupported');
      if (size > 32 * 1024 * 1024) throw new Error('Audit input entry too large');
      const local = zip.readUInt32LE(at + 42);
      if (zip.readUInt32LE(local) !== 0x04034b50) throw new Error('Invalid local ZIP entry');
      const start = local + 30 + zip.readUInt16LE(local + 26) + zip.readUInt16LE(local + 28);
      const data = zip.subarray(start, start + compressed);
      const result = method === 0 ? Buffer.from(data) : method === 8
        ? zlib.inflateRawSync(data, { maxOutputLength: 32 * 1024 * 1024 })
        : (() => { throw new Error(`Unsupported ZIP method ${method}`); })();
      if (result.length !== size) throw new Error('ZIP length mismatch');
      return result;
    }
    at += 46 + nameLen + zip.readUInt16LE(at + 30) + zip.readUInt16LE(at + 32);
  }
  throw new Error(`Missing VSIX entry: ${wanted}`);
}

class ManualClock {
  constructor() { this.jobs = new Map(); this.seq = 0; }
  setTimeout(fn) { const id = ++this.seq; this.jobs.set(id, fn); return id; }
  clearTimeout(id) { this.jobs.delete(id); }
  async next() {
    const first = this.jobs.entries().next().value;
    if (!first) throw new Error('No timer queued');
    this.jobs.delete(first[0]); first[1]();
    await settle();
  }
}
const settle = () => new Promise(resolve => setImmediate(resolve));
function deferred() {
  let resolve;
  const promise = new Promise(r => { resolve = r; });
  return { promise, resolve };
}
const plain = value => value === undefined ? null : JSON.parse(JSON.stringify(value));
const digest = data => createHash('sha256').update(data).digest('hex');

// This small VS Code adapter reads fixture files and applies edits only under
// the freshly created audit directory. It does not connect to a running editor.
function editorStub(auditRoot) {
  const state = { workspaceRoot: null, applied: [], shown: [], untitled: 0 };
  function checked(file) {
    const abs = path.resolve(file);
    const rel = path.relative(auditRoot, abs);
    if (rel === '..' || rel.startsWith(`..${path.sep}`) || path.isAbsolute(rel)) {
      throw new Error('Fixture adapter refused a path outside auditRoot');
    }
    return abs;
  }
  const uri = file => ({ scheme: 'file', fsPath: path.resolve(file), toString() { return this.fsPath; } });
  function document(target, text) {
    return {
      uri: target, getText: () => text, lineCount: text.split('\n').length,
      positionAt(offset) {
        const before = text.slice(0, offset).split('\n');
        return { line: before.length - 1, character: before.at(-1).length };
      },
      lineAt(index) {
        const line = text.split('\n')[index];
        if (line === undefined) throw new Error('Invalid line index');
        return { range: { start: { line: index, character: 0 }, end: { line: index, character: line.replace(/\r$/, '').length } } };
      },
    };
  }
  function offset(text, pos) {
    const lines = text.split('\n');
    return lines.slice(0, pos.line).reduce((sum, line) => sum + line.length + 1, 0) + pos.character;
  }
  class WorkspaceEdit {
    constructor() { this.items = []; }
    replace(target, range, text) { this.items.push({ target, range, text }); }
  }
  class Range { constructor(start, end) { this.start = start; this.end = end; } }
  const vscode = {
    env: { language: 'en' },
    Uri: { file: uri, joinPath: (base, ...parts) => uri(path.join(base.fsPath, ...parts)) },
    WorkspaceEdit, Range,
    workspace: {
      get workspaceFolders() { return state.workspaceRoot ? [{ uri: uri(state.workspaceRoot) }] : []; },
      async openTextDocument(target) {
        if (target && 'content' in target) return document(uri(path.join(auditRoot, `untitled-${++state.untitled}`)), target.content);
        return document(target, await fsp.readFile(checked(target.fsPath), 'utf8'));
      },
      async applyEdit(edit) {
        const groups = new Map();
        for (const item of edit.items) {
          const file = checked(item.target.fsPath);
          if (!groups.has(file)) groups.set(file, []);
          groups.get(file).push(item);
        }
        for (const [file, items] of groups) {
          let text = await fsp.readFile(file, 'utf8');
          const ranges = items.map(item => ({ ...item, from: offset(text, item.range.start), to: offset(text, item.range.end) }));
          for (const item of ranges.sort((a, b) => b.from - a.from)) text = text.slice(0, item.from) + item.text + text.slice(item.to);
          await fsp.writeFile(file, text);
        }
        state.applied.push(edit); return true;
      },
      fs: {
        writeFile: (target, bytes) => fsp.writeFile(checked(target.fsPath), bytes),
        stat: target => fsp.stat(checked(target.fsPath)),
      },
    },
    window: {
      showTextDocument: async doc => { state.shown.push(doc.uri.fsPath); },
      showWarningMessage: async () => undefined,
    },
  };
  return { state, vscode };
}

function loadBundle(bundle, vscode, clock) {
  const allowed = new Set(['events', 'fs', 'node:fs', 'node:crypto', 'node:os', 'node:path', 'node:stream', 'node:stream/promises', 'stream', 'util', 'zlib']);
  const sandbox = {
    module: { exports: {} }, exports: {}, Buffer, URL, AbortSignal, TextDecoder, TextEncoder, Error,
    console: { log() {}, warn() {}, error() {} },
    process: { env: {}, platform: process.platform, versions: process.versions, nextTick: process.nextTick.bind(process) },
    setTimeout: clock.setTimeout.bind(clock), clearTimeout: clock.clearTimeout.bind(clock),
    fetch: async () => { throw new Error('Remote network is disabled in this audit'); },
    require(name) {
      if (name === 'vscode') return vscode;
      if (!allowed.has(name)) throw new Error(`Unexpected dependency: ${name}`);
      return require(name);
    },
  };
  vm.createContext(sandbox);
  // Instrument exports in memory; the shipped function bodies remain unchanged.
  const expose = '\n;globalThis.__audit = { ApiClient, ApiError, TaskPoller, patchSides, spliceLines, linesMatch, textMatch, insideRoot, editFile, batchEditFile, materializeWorkspace, materializeFullHead, parseGitHubRemote, SettingsStore, normalize };';
  vm.runInContext(bundle + expose, sandbox, { filename: 'vsix-original-extension.js', timeout: 5000 });
  return sandbox.__audit;
}

async function main() {
  if (process.argv.includes('--help')) {
    console.log('Usage: node detect-bugs.cjs [path/to/extension.vsix] [report-directory]\nExit: 0 no failed probes, 1 bugs reproduced, 2 audit error. Requires Node.js 20+.');
    return;
  }
  if (!process.argv[2]) throw new Error('Pass the OctItem VSIX path as the first argument');
  const vsixPath = path.resolve(process.argv[2]);
  const reportDir = path.resolve(process.argv[3] || __dirname);
  const bytes = await fsp.readFile(vsixPath);
  const manifest = JSON.parse(zipEntry(bytes, 'extension/package.json').toString('utf8'));
  const bundle = zipEntry(bytes, 'extension/dist/extension.js').toString('utf8');
  const auditRoot = await fsp.mkdtemp(path.join(os.tmpdir(), 'octitem-bug-audit-'));
  const { state, vscode } = editorStub(auditRoot);
  const clock = new ManualClock();
  const results = [];
  function location(needle) {
    const index = bundle.indexOf(needle);
    return index < 0 ? null : `extension/dist/extension.js:${bundle.slice(0, index).split('\n').length}`;
  }
  async function check(id, title, severity, source, why, fix, probe) {
    try {
      const { expected, actual } = await probe();
      const status = isDeepStrictEqual(plain(expected), plain(actual)) ? 'PASS' : severity ? 'BUG' : 'ERROR';
      results.push({ id, title, severity, status, source: location(source), why, fix, expected: plain(expected), actual: plain(actual),
        ...(status === 'ERROR' ? { error: 'Control probe mismatch: inspect the fixture adapter before attributing this to the project.' } : {}) });
      console.log(`[${status}] ${id} ${title}`);
    } catch (error) {
      results.push({ id, title, status: 'ERROR', error: error.stack || String(error) });
      console.log(`[ERROR] ${id} ${title}: ${error.message}`);
    }
  }
  const control = (id, title, probe) => check(id, title, null, '', '', '', probe);
  async function fixture(name, text = 'before\n') {
    const root = path.join(auditRoot, name);
    await fsp.mkdir(root, { recursive: true });
    await fsp.writeFile(path.join(root, 'sample.js'), text);
    state.workspaceRoot = root;
    return root;
  }
  const filePayload = filePath => {
    const data = Buffer.from('export const ok = true;\n');
    return { filePath, contentBase64: data.toString('base64'), sha256: digest(data), size: data.length };
  };
  const workspaceManifest = taskId => ({ taskId, repoOwner: 'audit-owner', repoName: 'audit-repo', prNo: 7, headSha: 'a'.repeat(40), prTitle: 'Offline fixture', fileCount: 1, downloadableCount: 1, unavailableCount: 0, files: [{ filePath: 'sample.js', changeType: 'modified', downloadable: true }] });
  try {
    const api = loadBundle(bundle, vscode, clock);
    await control('C01', '正常的行替换', async () => ({ expected: 'a\nB\nc\n', actual: api.spliceLines('a\nb\nc\n', 2, 2, 'B') }));
    await control('C02', '正确识别代码已发生变化', async () => ({ expected: false, actual: api.linesMatch('other\n', 1, 1, 'before') }));
    await control('C03', '普通子路径和父级越界检查', async () => ({ expected: [true, false], actual: [api.insideRoot(auditRoot, path.join(auditRoot, 'file.js')), api.insideRoot(auditRoot, path.dirname(auditRoot))] }));
    await control('C04', 'GitHub 远程地址白名单', async () => ({ expected: [{ owner: 'acme', repo: 'demo' }, null], actual: [api.parseGitHubRemote('git@github.com:acme/demo.git'), api.parseGitHubRemote('https://example.com/acme/demo.git')] }));
    await check('B01', '行替换函数未保留 CRLF，输出混合换行符', 'P2', 'function spliceLines(',
      'base 按 LF 拆分但保留原行末 CR，replacement 则去除 CR；重新拼接后，替换行只有 LF。本项只证明 helper 输出，未验证真实 VS Code 是否会统一换行，不能据此断言最终保存文件含混合换行。',
      '识别原文档的换行约定，并让新旧各行使用一致的 EOL。',
      async () => ({ expected: 'a\r\nB\r\nc\r\n', actual: api.spliceLines('a\r\nb\r\nc\r\n', 2, 2, 'B') }));
    await check('B02', '合法的 ..cache 子目录被误判为路径越界', 'P3', 'function insideRoot(',
      'rel.startsWith("..") 会把 ..cache 等合法名称也当成父目录；编辑器与下载工作区均使用此模式。',
      '只拒绝 rel 等于 .. 或以 .. 加路径分隔符开头的路径。',
      async () => ({ expected: true, actual: api.insideRoot(auditRoot, path.join(auditRoot, '..cache', 'file.js')) }));
    await control('C05', '正常单文件编辑调用真实处理函数', async () => {
      const root = await fixture('normal-edit');
      const res = await api.editFile('sample.js', 'after', false, 1, 1, 'before');
      return { expected: { replaced: true, content: 'after\n' }, actual: { replaced: res.replaced, content: await fsp.readFile(path.join(root, 'sample.js'), 'utf8') } };
    });
    await control('C06', '代码不匹配时单文件编辑应保持原文件', async () => {
      const root = await fixture('changed-edit');
      const res = await api.editFile('sample.js', 'after', false, 1, 1, 'DIFFERENT');
      return { expected: { replaced: false, content: 'before\n' }, actual: { replaced: res.replaced, content: await fsp.readFile(path.join(root, 'sample.js'), 'utf8') } };
    });
    await check('B03', '含 ../ 的输入被静默改写并修改了工作区内同名文件', 'P2', 'async function editFile(',
      '过滤掉 .. 段并没有拒绝非法路径；../sample.js 被改成 sample.js，内容匹配时仍会修改错误目标。此处测试的是异常路径输入。',
      '先拒绝绝对路径和任何 .. 段；不要通过删除路径段来修正输入。', async () => {
        const root = await fixture('traversal-edit');
        const res = await api.editFile('../sample.js', 'WRONG TARGET', false, 1, 1, 'before');
        return { expected: { replaced: false, content: 'before\n' }, actual: { replaced: res.replaced, content: await fsp.readFile(path.join(root, 'sample.js'), 'utf8') } };
      });
    await check('B04', '链接目录下新建文件可写到当前工作区之外', 'P1', 'async function editFile(',
      'realpath(目标文件) 在目标尚不存在时失败，catch 直接放行；随后 full=true 的新建流程沿已有目录链接写入外部位置。测试中的外部位置仍位于临时审计目录。',
      '新文件须校验最近存在的父目录的 realpath；路径检查异常不能一律当作安全的新文件。', async () => {
        const root = await fixture('link-workspace');
        const outside = path.join(auditRoot, 'outside-workspace');
        await fsp.mkdir(outside);
        await fsp.symlink(outside, path.join(root, 'linked'), process.platform === 'win32' ? 'junction' : 'dir');
        const res = await api.editFile('linked/new.js', 'OUTSIDE_WRITE', true, undefined, undefined, undefined);
        const wrote = await fsp.access(path.join(outside, 'new.js')).then(() => true, () => false);
        return { expected: { replaced: false, wroteOutsideWorkspace: false }, actual: { replaced: res.replaced, wroteOutsideWorkspace: wrote } };
      });
    await control('C07', '批量编辑拒绝重复 issue ID', async () => {
      const edit = { issueId: 'duplicate', filePath: 'sample.js', content: 'after', lineStart: 1, lineEnd: 1, expected: 'before' };
      const res = await api.batchEditFile([edit, edit]);
      return { expected: { applied: false, codes: ['DUPLICATE_ISSUE', 'DUPLICATE_ISSUE'] }, actual: { applied: res.applied, codes: Array.from(res.items, item => item.code) } };
    });
    await control('C08', '正常任务到达终态后停止轮询', async () => {
      const seen = [];
      const poller = new api.TaskPoller({ fetchTask: async id => ({ id, status: 'COMPLETED' }), onUpdate: task => seen.push(task.status), onError: () => {}, visible: () => true });
      poller.watch('normal'); await clock.next();
      const actual = { updates: seen, watching: poller.watching('normal') }; poller.dispose();
      return { expected: { updates: ['COMPLETED'], watching: false }, actual };
    });
    await control('C09', '取消监听后忽略尚未返回的旧响应', async () => {
      const pending = deferred(); const seen = [];
      const poller = new api.TaskPoller({ fetchTask: () => pending.promise, onUpdate: task => seen.push(task.status), onError: () => {}, visible: () => true });
      poller.watch('cancel'); await clock.next(); poller.unwatch('cancel');
      pending.resolve({ id: 'cancel', status: 'COMPLETED' }); await settle();
      return { expected: { updates: [], watching: false }, actual: { updates: seen, watching: poller.watching('cancel') } };
    });
    await check('B05', '重新监听同一任务产生重复定时器，取消后仍会发送请求', 'P2', 'var TaskPoller = class',
      '在途请求只通过 timers.has(id) 判断是否仍有效，缺少监听代次；旧请求返回后额外调度一个定时器并覆盖新句柄，导致同时存在两个定时器，unwatch 只能取消其中一个。',
      '为每次 watch 保存 generation/token，tick 更新状态和重新调度前核对代次。', async () => {
        const pending = deferred(); const seen = []; let requests = 0;
        const poller = new api.TaskPoller({ fetchTask: () => { requests++; return pending.promise; }, onUpdate: task => seen.push(task.status), onError: () => {}, visible: () => true });
        poller.watch('rewatch'); await clock.next(); poller.unwatch('rewatch'); poller.watch('rewatch');
        pending.resolve({ id: 'rewatch', status: 'ANALYZING' }); await settle();
        const timersAfterRewatch = clock.jobs.size;
        poller.unwatch('rewatch');
        const timersAfterUnwatch = clock.jobs.size;
        if (clock.jobs.size) await clock.next();
        const actual = { staleUpdates: seen, timersAfterRewatch, timersAfterUnwatch, requestsAfterUnwatch: requests };
        poller.dispose(); clock.jobs.clear();
        return { expected: { staleUpdates: [], timersAfterRewatch: 1, timersAfterUnwatch: 0, requestsAfterUnwatch: 1 }, actual };
      });
    const client = (fetchImpl, timeoutMs = 25) => new api.ApiClient({ baseUrl: () => 'https://offline.invalid', token: async () => undefined, onAuthExpired() {}, fetchImpl, timeoutMs });
    await control('C10', '正常 API 成功响应', async () => ({ expected: { ok: true }, actual: await client(async () => ({ ok: true, status: 200, json: async () => ({ code: 0, data: { ok: true } }) })).get('/fixture') }));
    await control('C11', '响应头之前的超时映射为 50401', async () => {
      let code = null;
      try { await client(async () => { const err = new Error('timeout'); err.name = 'TimeoutError'; throw err; }).get('/fixture'); } catch (err) { code = err.code ?? null; }
      return { expected: 50401, actual: code };
    });
    await check('B06', 'HTTP 500 配合 code=0 会被当成 API 成功', 'P2', 'var ApiClient = class',
      'JSON 请求没有检查 res.ok，只判断业务 code。异常网关或服务端返回矛盾状态时会向上层报告成功；这是异常响应处理测试。',
      '同时验证 HTTP 状态与业务状态，非 2xx 必须报错。', async () => {
        let actual = 'rejected';
        try { await client(async () => ({ ok: false, status: 500, json: async () => ({ code: 0, data: { accepted: true } }) })).get('/fixture'); actual = 'accepted'; } catch { /* expected */ }
        return { expected: 'rejected', actual };
      });
    await check('B07', '响应头之后读取 JSON 超时被误报为普通坏响应', 'P2', 'var ApiClient = class',
      'fetch 的 catch 能识别超时，但 res.json() 在另一个 catch 中；相同的 TimeoutError 会被映射为 50001，而不是 50401。',
      '将响应体读取纳入相同超时分类，下载 arrayBuffer 也应使用同样的处理。', async () => {
        let code = null;
        const apiClient = client(async (_url, options) => ({ ok: true, status: 200, json: () => new Promise((_resolve, reject) => {
          if (options.signal.aborted) reject(options.signal.reason);
          else options.signal.addEventListener('abort', () => reject(options.signal.reason), { once: true });
        }) }));
        // AbortSignal.timeout uses an unref'ed timer; keep the probe alive briefly.
        const keepAlive = setTimeout(() => {}, 2000);
        try { await apiClient.get('/fixture'); } catch (err) { code = err.code ?? null; } finally { clearTimeout(keepAlive); }
        return { expected: 50401, actual: code };
      });
    await control('C12', '下载工作区校验文件哈希并成功写入', async () => {
      const parent = path.join(auditRoot, 'download-ok'); await fsp.mkdir(parent);
      const res = await api.materializeWorkspace(parent, workspaceManifest('first'), async file => filePayload(file), '# fixture');
      return { expected: { downloaded: 1, exists: true }, actual: { downloaded: res.downloaded, exists: fs.existsSync(path.join(res.directory, 'checkout', 'sample.js')) } };
    });
    await control('C13', '下载工作区拒绝哈希不一致的文件', async () => {
      const parent = path.join(auditRoot, 'download-bad'); await fsp.mkdir(parent); let code = null;
      try { await api.materializeWorkspace(parent, workspaceManifest('bad'), async file => ({ ...filePayload(file), sha256: '0'.repeat(64) }), '# fixture'); } catch (err) { code = err.code; }
      return { expected: 'HASH_MISMATCH', actual: code };
    });
    await check('B08', '同一提交重新分析后，第二个任务无法在同一父目录准备工作区', 'P2', 'var targetFor =',
      '目录名只含 PR 编号与 head SHA，复用校验却要求 taskId 也一致；重跑生成新 taskId 后撞上已有目录并抛 TARGET_EXISTS。',
      '目录名加入 taskId，或在显式保留各次报告的前提下复用同一提交的代码。', async () => {
        const parent = path.join(auditRoot, 'rerun-download'); await fsp.mkdir(parent);
        await api.materializeWorkspace(parent, workspaceManifest('task-1'), async file => filePayload(file), '# first report');
        let actual = 'prepared';
        try { await api.materializeWorkspace(parent, workspaceManifest('task-2'), async file => filePayload(file), '# second report'); } catch (err) { actual = err.code || err.message; }
        return { expected: 'prepared', actual };
      });
  } finally {
    // Validate the exact cleanup target before recursive removal on Windows.
    const absolute = path.resolve(auditRoot);
    if (path.dirname(absolute) !== path.resolve(os.tmpdir()) || !path.basename(absolute).startsWith('octitem-bug-audit-')) {
      throw new Error('Refused unsafe fixture cleanup target');
    }
    await fsp.rm(absolute, { recursive: true, force: true });
  }
  const counts = Object.fromEntries(['PASS', 'BUG', 'ERROR'].map(status => [status, results.filter(item => item.status === status).length]));
  const report = { generatedAt: new Date().toISOString(), target: { vsixPath, name: manifest.name, version: manifest.version, sha256: digest(bytes) }, scope: 'Offline shipped host code; mocked VS Code adapter and HTTP responses; real filesystem fixtures and deterministic timer scheduling. No backend/AI analysis or live VS Code UI was exercised.', counts, results };
  await fsp.mkdir(reportDir, { recursive: true });
  await fsp.writeFile(path.join(reportDir, 'audit-results.json'), JSON.stringify(report, null, 2) + '\n');
  const md = [
    '# OctItem VSIX 离线缺陷检测报告', '',
    `目标：${manifest.name} ${manifest.version}。`, '', `SHA-256：\`${report.target.sha256}\`。`, '',
    `运行结果：${counts.PASS} 项通过，${counts.BUG} 项复现缺陷，${counts.ERROR} 项检测器错误。`, '',
    '直接读取 VSIX 中的 extension/dist/extension.js，在内存中开放函数供测试调用；不重写被测函数。文件写入限定为临时样本。VS Code 接口与 HTTP 响应使用适配器，结果不等同于完整编辑器端到端测试。', '',
    '本次没有连接默认服务器、没有登录账户、没有提交 PR，也没有验证后端 AI 找 bug 的能力。P1/P2/P3 为本报告的影响分级。异常路径和矛盾 HTTP 状态等用例已单独注明触发条件。', '',
    '## 已复现问题', '',
  ];
  for (const item of results.filter(item => item.status === 'BUG')) {
    md.push(`### ${item.id} [${item.severity}] ${item.title}`, '',
      `位置：\`${item.source}\`（VSIX 内原始打包文件行号）。`, '', item.why, '',
      `预期：\`${JSON.stringify(item.expected)}\`。`, '', `实际：\`${JSON.stringify(item.actual)}\`。`, '',
      `修复方向：${item.fix}`, '');
  }
  md.push('## 运行方法', '', '需要 Node.js 20+。在本目录执行：', '', '```powershell', 'node ./detect-bugs.cjs', '```', '',
    '也可指定其他安装包和报告目录：', '', '```powershell', 'node ./detect-bugs.cjs "C:/path/extension.vsix" "C:/path/reports"', '```', '',
    '退出码：0 表示所有已定义检查通过；1 表示复现缺陷；2 表示检测器错误。ERROR 不计作项目缺陷。脚本针对当前包中的符号编写，其他版本可能需要适配。VM 用于加载和隔离测试状态，不是用于执行不可信安装包的安全边界。', '',
    '## 全部检查', '', '| ID | 状态 | 检查 |', '| --- | --- | --- |');
  for (const item of results) md.push(`| ${item.id} | ${item.status} | ${item.title} |`);
  const errors = results.filter(item => item.status === 'ERROR');
  if (errors.length) md.push('', '## 检测器错误', '', ...errors.map(item => `- ${item.id}: ${item.error}`));
  await fsp.writeFile(path.join(reportDir, '检测报告.md'), md.join('\n') + '\n');
  console.log(`\nPASS=${counts.PASS} BUG=${counts.BUG} ERROR=${counts.ERROR}`);
  console.log(`Reports: ${reportDir}`);
  process.exitCode = counts.ERROR ? 2 : counts.BUG ? 1 : 0;
}

main().catch(error => { console.error(error.stack || error); process.exitCode = 2; });
