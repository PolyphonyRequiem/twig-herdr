import { execFile } from 'node:child_process';
import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createServer, connect } from 'node:net';
import { promisify } from 'node:util';
import { emitKeypressEvents } from 'node:readline';
import { createHash } from 'node:crypto';
import pty from 'node-pty';
import xterm from '@xterm/headless';
import serializer from '@xterm/addon-serialize';

const execute = promisify(execFile);
const herdr = process.env.HERDR_BIN_PATH || 'herdr';
const pluginId = 'twig-herdr';
const stateRoot = path.join(os.tmpdir(), pluginId);
const scrollback = 10000;

function safe(text) {
  return String(text ?? '').replace(/[\x00-\x1f\x7f]/g, ' ');
}

function clip(text, width) {
  const value = safe(text);
  if (value.length <= width) return value;
  return `${value.slice(0, Math.max(0, width - 1))}…`;
}

function encodeId(id) {
  return Buffer.from(String(id)).toString('hex');
}

function socketPathFor(workspaceId, tabId) {
  const socketIdentity = process.env.HERDR_SOCKET_PATH || 'herdr-default-socket';
  const key = createHash('sha256')
    .update(`${socketIdentity}\0${workspaceId}\0${tabId}`)
    .digest('hex')
    .slice(0, 32);
  if (process.platform === 'win32') {
    return `\\\\.\\pipe\\${pluginId}-${key}`;
  }
  return path.join(stateRoot, encodeId(workspaceId), `${key}.sock`);
}

async function run(binary, args, cwd = process.cwd()) {
  try {
    const { stdout, stderr } = await execute(binary, args, {
      cwd,
      timeout: 15000,
      maxBuffer: 4 * 1024 * 1024,
    });
    return stdout + (stderr ? `\n${stderr}` : '');
  } catch (error) {
    const output = `${error.stdout || ''}${error.stderr ? `\n${error.stderr}` : ''}`.trim();
    throw new Error(output || error.message || `${binary} failed`);
  }
}

async function api(args, cwd = process.cwd()) {
  const raw = await run(herdr, args, cwd);
  const parsed = JSON.parse(raw);
  if (parsed.error) {
    throw new Error(parsed.error.message || parsed.error || 'Herdr command failed');
  }
  return parsed.result;
}

function parseArgs(argv) {
  const positionals = [];
  const options = { pane: null, file: null, help: false };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === '--pane') {
      options.pane = argv[++i] || null;
    } else if (arg === '--file') {
      options.file = argv[++i] || null;
    } else if (arg === '--help' || arg === '-h') {
      options.help = true;
    } else if (arg.startsWith('-')) {
      throw new Error(`Unknown option: ${arg}`);
    } else {
      positionals.push(arg);
    }
  }
  return { command: positionals[0] || 'panel', options };
}

function printHelp() {
  console.log(`Twig bench panel\n\n` +
    `Usage:\n` +
    `  node panel.mjs open [--pane SOURCE_OR_PANEL_ID]\n` +
    `  node panel.mjs table [--pane SOURCE_OR_PANEL_ID]\n` +
    `  node panel.mjs tree [--pane SOURCE_OR_PANEL_ID]\n` +
    `  node panel.mjs review --file PATH [--pane SOURCE_OR_PANEL_ID]\n` +
    `  node panel.mjs exit-review [--pane SOURCE_OR_PANEL_ID]\n` +
    `  node panel.mjs --help\n\n` +
    `Requirements:\n` +
    `  - Run inside a Herdr pane (HERDR_ENV=1 and HERDR_PANE_ID).\n` +
    `  - Twig and Herdr must be on PATH.\n` +
    `  - Requires Twig 0.93.0+ (workspace --view and proposal preview --interactive).\n` +
    `  - Install dependencies with npm ci.\n` +
    `  - Standalone fixtures may set TWIG_PANEL_CWD, HERDR_WORKSPACE_ID, HERDR_TAB_ID, and HERDR_SOCKET_PATH.\n\n` +
    `Keys in the panel:\n` +
    `  1  Table view\n` +
    `  2  Tree view\n` +
    `  j/k or arrows  Scroll\n` +
    `  r  Refresh bench view; redraw review only\n` +
    `  d  Details in review\n` +
    `  b  Back in review\n` +
    `  Esc/c  Leave review and return to the previous bench view\n` +
    `  q  Close the panel\n\n` +
    `Notes:\n` +
    `  - table/tree switches consume native twig workspace --view table|tree.\n` +
    `  - review uses twig proposal preview --file PATH --interactive.\n` +
    `  - review files are resolved from the caller/source pane cwd.\n`);
}

async function paneContext(paneId) {
  const id = paneId || process.env.HERDR_PANE_ID;
  if (!id) {
    throw new Error('Open this command from a Herdr pane or pass --pane.');
  }
  const ownPane = !paneId || paneId === process.env.HERDR_PANE_ID;
  if (ownPane && process.env.HERDR_WORKSPACE_ID && process.env.HERDR_TAB_ID && process.env.TWIG_PANEL_CWD) {
    return {
      paneId: id,
      workspaceId: process.env.HERDR_WORKSPACE_ID,
      tabId: process.env.HERDR_TAB_ID,
      cwd: process.env.TWIG_PANEL_CWD,
      terminalTitle: process.env.TERM || '',
    };
  }
  const { pane } = await api(['pane', 'get', id]);
  const cwd = pane.foreground_cwd || pane.cwd;
  if (!cwd) {
    throw new Error('The source pane has no working directory.');
  }
  if (!pane.workspace_id || !pane.tab_id) {
    throw new Error('The source pane has no workspace/tab identity.');
  }
  return {
    paneId: id,
    workspaceId: pane.workspace_id,
    tabId: pane.tab_id,
    cwd,
    terminalTitle: pane.terminal_title || '',
  };
}

async function paneLayout(paneId) {
  const { layout } = await api(['pane', 'layout', '--pane', paneId]);
  return layout.panes.find(item => item.pane_id === paneId)?.rect || null;
}

async function launchPanelPane(context, envPairs) {
  const rect = await paneLayout(context.paneId);
  const direction = rect && rect.width >= 166 ? 'right' : 'down';
  const args = [
    'plugin', 'pane', 'open',
    '--plugin', pluginId,
    '--entrypoint', 'bench',
    '--target-pane', context.paneId,
    '--direction', direction,
    '--cwd', context.cwd,
  ];
  const launchEnv = [
    ...envPairs,
    ['HERDR_WORKSPACE_ID', context.workspaceId],
    ['HERDR_TAB_ID', context.tabId],
  ];
  if (process.env.HERDR_SOCKET_PATH) launchEnv.push(['HERDR_SOCKET_PATH', process.env.HERDR_SOCKET_PATH]);
  for (const [key, value] of launchEnv) {
    args.push('--env', `${key}=${value}`);
  }
  args.push('--no-focus');
  await api(args, context.cwd);
}

async function requestControl(context, payload, timeoutMs = 20000) {
  const address = socketPathFor(context.workspaceId, context.tabId);
  return await new Promise((resolve, reject) => {
    const socket = connect(address);
    const failTimer = setTimeout(() => {
      socket.destroy(new Error('Timed out waiting for the Twig panel control socket.'));
    }, timeoutMs);
    let buffer = '';
    let settled = false;
    const finish = (error, result) => {
      if (settled) return;
      settled = true;
      clearTimeout(failTimer);
      socket.end();
      if (error) reject(error);
      else resolve(result);
    };
    socket.setEncoding('utf8');
    socket.on('connect', () => {
      socket.write(`${JSON.stringify(payload)}\n`);
    });
    socket.on('data', chunk => {
      buffer += chunk;
      const newline = buffer.indexOf('\n');
      if (newline < 0) return;
      const line = buffer.slice(0, newline).trim();
      if (!line) {
        finish(new Error('Empty response from the Twig panel control socket.'));
        return;
      }
      try {
        const message = JSON.parse(line);
        if (message.ok) finish(null, message.result ?? null);
        else finish(new Error(message.error || 'Twig panel rejected the request.'));
      } catch (error) {
        finish(new Error(`Invalid response from the Twig panel control socket: ${error.message}`));
      }
    });
    socket.on('error', error => finish(error));
    socket.on('close', () => {
      if (!settled) finish(new Error('Panel closed before acknowledging the request.'));
    });
  });
}

async function pingPanel(context) {
  try {
    await requestControl(context, { token: '', command: 'ping' }, 250);
    return true;
  } catch {
    return false;
  }
}

async function waitForPanel(context) {
  for (let attempt = 0; attempt < 20; attempt += 1) {
    if (await pingPanel(context)) return true;
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  return false;
}

function makeRuntime(cols, rows) {
  const terminal = new xterm.Terminal({
    cols,
    rows,
    scrollback,
    allowProposedApi: true,
  });
  const addon = new serializer.SerializeAddon();
  terminal.loadAddon(addon);
  return { terminal, addon, marker: terminal.registerMarker(0) };
}

function currentRows() {
  return Math.max(1, (process.stdout.rows || 24) - 2);
}

function currentCols() {
  return Math.max(20, process.stdout.columns || 80);
}

function shiftFrameRows(frame) {
  return frame
    .replace(/^\x1b\[[0-9;]*H/, '')
    .replace(/\x1b\[H/g, '\x1b[2;1H')
    .replace(/\x1b\[(\d+);(\d+)([Hf])/g, (_match, row, column, suffix) => {
      return `\x1b[${Number(row) + 1};${column}${suffix}`;
    });
}

const state = {
  cwd: '',
  paneId: '',
  workspaceId: '',
  tabId: '',
  mode: 'bench',
  benchView: 'table',
  benchSummary: null,
  reviewFile: null,
  notice: '',
  noticeTimer: null,
  generation: 0,
  benchGeneration: 0,
  benchTimer: null,
  closing: false,
  sockets: null,
  bench: {
    terminal: null,
    addon: null,
    child: null,
    busy: false,
  },
  review: {
    terminal: null,
    addon: null,
    child: null,
    density: 'b',
  },
  offsets: {
    table: 0,
    tree: 0,
    review: 0,
  },
};

function activeKey() {
  return state.mode === 'review' ? 'review' : state.benchView;
}

function activeRuntime() {
  return state.mode === 'review' ? state.review : state.bench;
}

function clearNoticeTimer() {
  if (state.noticeTimer) {
    clearTimeout(state.noticeTimer);
    state.noticeTimer = null;
  }
}

function setNotice(text, ttlMs = 0) {
  clearNoticeTimer();
  state.notice = text;
  if (ttlMs > 0) {
    state.noticeTimer = setTimeout(() => {
      if (state.notice === text) {
        state.notice = '';
        draw();
      }
    }, ttlMs);
  }
}

function setError(text) {
  clearNoticeTimer();
  state.notice = text;
}

function footerKeys() {
  if (state.mode === 'review') {
    return 'd details · b back · Esc/c exit review · r redraw · q close';
  }
  return '1 table · 2 tree · j/k scroll · r refresh · q close';
}

function headerText() {
  const bench = safe(state.benchSummary?.current || 'loading');
  const view = state.mode === 'review' ? 'Review (snapshot)' : state.benchView === 'tree' ? 'Tree' : 'Table';
  return `${view} · Bench: ${bench} · ${state.reviewFile ? safe(path.basename(state.reviewFile)) : safe(state.cwd)}`;
}

function footerText(truncated = 0) {
  const modeLabel = state.mode === 'review'
    ? 'Mode: review'
    : `Mode: ${state.benchView}`;
  const bits = [];
  if (truncated) bits.push('OUTPUT TRUNCATED — earliest lines discarded');
  if (state.notice) bits.push(state.notice);
  bits.push(modeLabel, footerKeys());
  return bits.join(' · ');
}

function draw() {
  if (state.closing) return;
  const cols = currentCols();
  const rows = Math.max(3, process.stdout.rows || 24);
  const contentRows = Math.max(1, rows - 2);
  const runtime = activeRuntime();
  const terminal = runtime.terminal;
  let frame = '';
  let truncated = 0;
  if (terminal) {
    const buffer = terminal.buffer.active;
    const end = buffer.baseY + buffer.cursorY;
    const maxStart = Math.max(0, end - contentRows + 1);
    const key = activeKey();
    const offset = Math.min(Math.max(0, state.offsets[key] || 0), maxStart);
    state.offsets[key] = offset;
    truncated = runtime.marker?.isDisposed ? 1 : 0;
    if (end >= offset) {
      frame = runtime.addon.serialize({
        range: { start: offset, end: Math.min(end, offset + contentRows - 1) },
        excludeModes: true,
        excludeAltBuffer: true,
      });
      frame = shiftFrameRows(frame);
    }
  }
  const header = clip(headerText(), cols);
  const footer = clip(footerText(truncated), cols);
  process.stdout.write('\x1b[0m\x1b[H\x1b[2J');
  process.stdout.write(`\x1b[1;1H\x1b[2K${header}`);
  if (frame) {
    process.stdout.write(`\x1b[2;1H${frame}`);
  } else {
    process.stdout.write(`\x1b[2;1H\x1b[2K${clip(state.mode === 'review' ? 'Waiting for review output…' : 'Loading bench output…', cols)}`);
  }
  process.stdout.write(`\x1b[0m\x1b[${rows};1H\x1b[2K${footer}\x1b[?25l`);
}

function killRuntime(runtime) {
  if (!runtime) return;
  if (runtime.child) {
    try {
      runtime.child.kill();
    } catch {
      // Ignore process teardown noise.
    }
  }
  if (runtime.terminal) {
    try {
      runtime.terminal.dispose();
    } catch {
      // Ignore disposal noise.
    }
  }
  runtime.child = null;
  runtime.terminal = null;
  runtime.addon = null;
}

async function loadBenchSummary() {
  const raw = await run('twig', ['bench', 'list', '-o', 'json'], state.cwd);
  const summary = JSON.parse(raw);
  state.benchSummary = summary;
  return summary;
}

async function refreshBench({ force = false } = {}) {
  if (state.closing || state.mode === 'review' || (state.bench.busy && !force)) return;
  if (force) state.bench.child?.kill();
  state.bench.busy = true;
  const generation = ++state.generation;
  state.benchGeneration = generation;
  let next;
  try {
    await loadBenchSummary();
    if (state.closing || state.mode === 'review' || generation !== state.generation) return;
    next = makeRuntime(currentCols(), currentRows());
    const child = pty.spawn('twig', ['workspace', '--view', state.benchView], {
      name: 'xterm-256color', cols: currentCols(), rows: currentRows(), cwd: state.cwd,
      env: { ...process.env, TERM: 'xterm-256color', COLORTERM: 'truecolor' },
    });
    state.bench.child = child;
    next.terminal.onData(data => child.write(data));
    let timedOut = false;
    const deadline = setTimeout(() => { timedOut = true; child.kill(); }, 15000);
    const exitCode = await new Promise(resolve => {
      child.onData(data => next?.terminal.write(data));
      child.onExit(({ exitCode }) => { clearTimeout(deadline); resolve(exitCode); });
    });
    await new Promise(resolve => next.terminal.write('', resolve));
    if (state.closing || state.mode === 'review' || generation !== state.generation) return;
    state.bench.terminal?.dispose();
    Object.assign(state.bench, next, { child: null });
    next = null;
    if (timedOut || exitCode) setError(timedOut ? 'Twig workspace timed out' : `Twig workspace exited with code ${exitCode}`);
    else if (/^Twig (bench refresh failed|workspace exited|workspace timed out)/.test(state.notice)) state.notice = '';
  } catch (error) {
    if (!state.closing && state.generation === generation) setError(`Twig bench refresh failed: ${safe(error.message)}`);
  } finally {
    next?.terminal.dispose();
    if (state.benchGeneration === generation) state.bench.busy = false;
    if (!state.closing && state.mode !== 'review' && state.generation === generation) draw();
  }
}

function stopBenchTimer() {
  if (state.benchTimer) {
    clearInterval(state.benchTimer);
    state.benchTimer = null;
  }
}

function startBenchTimer() {
  stopBenchTimer();
  state.benchTimer = setInterval(() => void refreshBench(), 3000);
}

async function startReview(file, { replacing = false } = {}) {
  const absolute = path.resolve(state.cwd, file);
  stopBenchTimer();
  const generation = ++state.generation;
  killRuntime(state.bench);
  state.bench.busy = false;
  killRuntime(state.review);
  state.reviewFile = absolute;
  state.review.density = 'b';
  state.offsets.review = 0;
  if (!replacing) {
    setNotice(`Reviewing ${safe(path.basename(absolute))}`, 1200);
  } else {
    setNotice(`Replacing review with ${safe(path.basename(absolute))}`, 1200);
  }
  state.mode = 'review';
  const cols = currentCols();
  const rows = currentRows();
  const runtime = state.review;
  runtime.ready = false;
  runtime.pendingDensity = null;
  let promptTail = '';
  Object.assign(runtime, makeRuntime(cols, rows));
  let child;
  try {
    child = pty.spawn('twig', ['proposal', 'preview', '--file', absolute, '--interactive'], {
      name: 'xterm-256color',
      cols,
      rows,
      cwd: state.cwd,
      env: {
        ...process.env,
        TERM: 'xterm-256color',
        COLORTERM: 'truecolor',
      },
    });
  } catch (error) {
    setError(`Review failed to start: ${safe(error.message)}`);
    draw();
    throw error;
  }
  runtime.child = child;
  runtime.terminal.onData(data => {
    if (state.generation === generation && runtime.child === child) child.write(data);
  });
  child.onData(data => {
    if (state.closing || state.generation !== generation || state.mode !== 'review' || runtime.child !== child) return;
    // This is the native input-readiness prompt, not proposal data or approval state.
    promptTail = (promptTail + data).slice(-256);
    const ready = promptTail.includes('Review only — Details / Back / Cancel: ');
    if (ready) promptTail = '';
    runtime.terminal.write(data, () => {
      if (state.closing || state.generation !== generation || state.mode !== 'review') return;
      if (ready) {
        runtime.ready = true;
        if (runtime.pendingDensity) reviewDensity(runtime.pendingDensity);
      }
      draw();
    });
  });
  child.onExit(({ exitCode }) => {
    if (state.generation !== generation || runtime.child !== child) return;
    runtime.child = null;
    if (state.mode === 'review') {
      setError(exitCode ? `Review exited with code ${exitCode}` : 'Review exited unexpectedly');
      runtime.terminal.write('', draw);
    }
  });
  draw();
}

async function exitReview() {
  if (state.mode !== 'review') return;
  ++state.generation;
  killRuntime(state.review);
  state.reviewFile = null;
  state.mode = 'bench';
  setNotice('Returned to bench view', 1000);
  await refreshBench({ force: true });
  if (!state.closing) startBenchTimer();
  if (!state.closing) draw();
}

function reviewDensity(density) {
  if (!state.review.child) return;
  state.review.density = density;
  state.review.pendingDensity = density;
  if (!state.review.ready) return;
  state.review.ready = false;
  state.review.pendingDensity = null;
  state.review.terminal.reset();
  state.review.marker = state.review.terminal.registerMarker(0);
  state.offsets.review = 0;
  state.review.child.write(`${density}\r`);
}


function resizeActiveRuntime() {
  const runtime = activeRuntime();
  const cols = currentCols();
  const rows = currentRows();
  if (state.mode === 'review') {
    if (runtime.terminal) {
      try {
        runtime.terminal.resize(cols, rows);
      } catch {
        // Ignore resize noise.
      }
    }
    if (runtime.child) {
      try {
        runtime.child.resize(cols, rows);
        reviewDensity(state.review.density);
      } catch {
        // Ignore resize noise.
      }
    }
    draw();
    return;
  }
  void refreshBench({ force: true });
}

async function control(command, options) {
  const context = await paneContext(options.pane);
  const address = socketPathFor(context.workspaceId, context.tabId);
  const absoluteReviewFile = options.file ? path.resolve(context.cwd, options.file) : null;
  const initialView = command === 'tree' ? 'tree' : 'table';

  const send = async payload => requestControl(context, payload);
  const panelAlive = await pingPanel(context);

  if (command === 'open') {
    if (panelAlive) return;
    await launchPanelPane(context, [['TWIG_PANEL_CWD', context.cwd], ['TWIG_PANEL_INITIAL_VIEW', initialView]]);
    if (!await waitForPanel(context)) throw new Error('Twig panel did not become ready in this tab.');
    return;
  }

  if (command === 'table' || command === 'tree') {
    if (panelAlive) {
      await send({ token: address, command: 'view', view: initialView });
      return;
    }
    await launchPanelPane(context, [['TWIG_PANEL_CWD', context.cwd], ['TWIG_PANEL_INITIAL_VIEW', initialView]]);
    if (!await waitForPanel(context)) throw new Error('Twig panel did not become ready in this tab.');
    return;
  }

  if (command === 'review') {
    if (!absoluteReviewFile) {
      throw new Error('Review requires --file PATH.');
    }
    if (panelAlive) {
      await send({ token: address, command: 'review', file: absoluteReviewFile });
      return;
    }
    await launchPanelPane(context, [
      ['TWIG_PANEL_CWD', context.cwd],
      ['TWIG_PANEL_INITIAL_VIEW', initialView],
      ['TWIG_PANEL_INITIAL_REVIEW_FILE', absoluteReviewFile],
    ]);
    if (!await waitForPanel(context)) throw new Error('Twig panel did not become ready in this tab.');
    return;
  }

  if (command === 'exit-review') {
    if (!panelAlive) {
      throw new Error('No Twig review panel is open in this tab.');
    }
    await send({ token: address, command: 'exit-review' });
    return;
  }

  throw new Error(`Unknown command: ${command}`);
}

async function panel() {
  if (!process.stdout.isTTY || !process.stdin.isTTY) {
    throw new Error('The Twig bench panel requires a terminal.');
  }
  if (process.env.HERDR_ENV !== '1' || !process.env.HERDR_PANE_ID) {
    throw new Error('Open this plugin from a Herdr pane.');
  }

  const context = await paneContext(process.env.HERDR_PANE_ID);
  state.cwd = process.env.TWIG_PANEL_CWD || context.cwd;
  state.paneId = context.paneId;
  state.workspaceId = context.workspaceId;
  state.tabId = context.tabId;
  state.benchView = process.env.TWIG_PANEL_INITIAL_VIEW === 'tree' ? 'tree' : 'table';
  const initialReviewFile = process.env.TWIG_PANEL_INITIAL_REVIEW_FILE
    ? path.resolve(state.cwd, process.env.TWIG_PANEL_INITIAL_REVIEW_FILE)
    : null;

  const socketPath = socketPathFor(state.workspaceId, state.tabId);
  if (await pingPanel(context)) throw new Error('A Twig panel already owns this tab.');
  if (process.platform !== 'win32') {
    await fs.mkdir(path.dirname(socketPath), { recursive: true, mode: 0o700 });
    await fs.rm(socketPath, { force: true });
  }

  const server = createServer(socket => {
    socket.setEncoding('utf8');
    socket.on('error', () => socket.destroy());
    socket.setTimeout(20000, () => socket.destroy());
    let handled = false;
    let buffer = '';
    socket.on('data', chunk => {
      if (handled) return;
      buffer += chunk;
      if (buffer.length > 65536) { socket.destroy(); return; }
      const newline = buffer.indexOf('\n');
      if (newline < 0) return;
      handled = true;
      const raw = buffer.slice(0, newline).trim();
      if (!raw) return;
      let payload;
      try {
        payload = JSON.parse(raw);
      } catch (error) {
        socket.end(`${JSON.stringify({ ok: false, error: `Invalid control request: ${error.message}` })}\n`);
        return;
      }
      if (payload.token !== socketPath && payload.command !== 'ping') {
        socket.end(`${JSON.stringify({ ok: false, error: 'Control request rejected.' })}\n`);
        return;
      }
      (async () => {
        try {
          if (payload.command === 'ping') {
            socket.end(`${JSON.stringify({ ok: true, result: { mode: state.mode, view: state.benchView } })}\n`);
            return;
          }
          if (payload.command === 'view') {
            state.benchView = payload.view === 'tree' ? 'tree' : 'table';
            setNotice(`Switched bench view to ${state.benchView}`, 1200);
            if (state.mode !== 'review') {
              await refreshBench({ force: true });
            } else {
              await exitReview();
            }
            socket.end(`${JSON.stringify({ ok: true, result: { mode: state.mode, view: state.benchView } })}\n`);
            return;
          }
          if (payload.command === 'review') {
            if (!payload.file) {
              throw new Error('Missing review file.');
            }
            const replacing = state.mode === 'review' && !!state.reviewFile;
            await startReview(payload.file, { replacing });
            socket.end(`${JSON.stringify({ ok: true, result: { mode: 'review', file: state.reviewFile, replaced: replacing } })}\n`);
            return;
          }
          if (payload.command === 'exit-review') {
            await exitReview();
            socket.end(`${JSON.stringify({ ok: true, result: { mode: 'bench', view: state.benchView } })}\n`);
            return;
          }
          throw new Error(`Unknown control command: ${payload.command}`);
        } catch (error) {
          socket.end(`${JSON.stringify({ ok: false, error: safe(error.message) })}\n`);
        }
      })();
    });
  });

  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(socketPath, resolve);
  });
  if (process.platform !== 'win32') await fs.chmod(socketPath, 0o600);
  state.sockets = server;
  server.unref();

  process.stdout.write('\x1b[?1049h\x1b[?25l');
  process.stdin.setRawMode(true);
  emitKeypressEvents(process.stdin);

  process.stdin.on('keypress', (text, key) => {
    if (state.closing) return;
    if (text === 'q' || (key && key.ctrl && key.name === 'c')) {
      shutdown();
      return;
    }
    if (state.mode === 'review') {
      if (text === 'd') {
        reviewDensity('d');
        return;
      }
      if (text === 'b') {
        reviewDensity('b');
        return;
      }
      if (text === 'r') {
        setNotice('Review redraw only; no new preview was fetched.', 1200);
        draw();
        return;
      }
      if (text === 'c' || key?.name === 'escape') {
        void exitReview();
        return;
      }
      if (text === '1') {
        state.benchView = 'table';
        void exitReview();
        return;
      }
      if (text === '2') {
        state.benchView = 'tree';
        void exitReview();
        return;
      }
      if (text === 'j' || key?.name === 'down') {
        state.offsets.review += 1;
        draw();
        return;
      }
      if (text === 'k' || key?.name === 'up') {
        state.offsets.review = Math.max(0, state.offsets.review - 1);
        draw();
        return;
      }
      if (key?.name === 'pagedown') {
        state.offsets.review += currentRows() - 1;
        draw();
        return;
      }
      if (key?.name === 'pageup') {
        state.offsets.review = Math.max(0, state.offsets.review - (currentRows() - 1));
        draw();
        return;
      }
      return;
    }

    if (text === '1') {
      state.benchView = 'table';
      void refreshBench({ force: true });
      return;
    }
    if (text === '2') {
      state.benchView = 'tree';
      void refreshBench({ force: true });
      return;
    }
    if (text === 'r') {
      void refreshBench({ force: true });
      return;
    }
    if (text === 'j' || key?.name === 'down') {
      state.offsets[state.benchView] += 1;
      draw();
      return;
    }
    if (text === 'k' || key?.name === 'up') {
      state.offsets[state.benchView] = Math.max(0, state.offsets[state.benchView] - 1);
      draw();
      return;
    }
    if (key?.name === 'pagedown') {
      state.offsets[state.benchView] += currentRows() - 1;
      draw();
      return;
    }
    if (key?.name === 'pageup') {
      state.offsets[state.benchView] = Math.max(0, state.offsets[state.benchView] - (currentRows() - 1));
      draw();
    }
  });

  process.stdout.on('resize', () => {
    resizeActiveRuntime();
  });
  process.on('SIGTERM', shutdown);
  process.on('SIGINT', shutdown);

  draw();
  if (initialReviewFile) {
    state.mode = 'review';
    state.reviewFile = initialReviewFile;
    try {
      await loadBenchSummary();
      await startReview(initialReviewFile);
    } catch (error) {
      setError(`Review failed to start: ${safe(error.message)}`);
      draw();
    }
  } else {
    await refreshBench();
    if (state.mode !== 'review') startBenchTimer();
  }

  async function shutdown() {
    if (state.closing) return;
    state.closing = true;
    clearNoticeTimer();
    stopBenchTimer();
    killRuntime(state.review);
    killRuntime(state.bench);
    try {
      state.sockets?.close();
    } catch {
      // Ignore shutdown noise.
    }
    if (process.platform !== 'win32') {
      try {
        await fs.rm(socketPath, { force: true });
      } catch {
        // Ignore stale socket cleanup noise.
      }
    }
    try {
      process.stdin.setRawMode(false);
    } catch {
      // Ignore terminal teardown noise.
    }
    process.stdout.write('\x1b[?25h\x1b[?1049l');
    process.exit(0);
  }
}

async function main() {
  const { command, options } = parseArgs(process.argv.slice(2));
  if (options.help || command === 'help') {
    printHelp();
    return;
  }
  if (command === 'panel') {
    await panel();
    return;
  }
  await control(command, options);
}

try {
  await main();
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
