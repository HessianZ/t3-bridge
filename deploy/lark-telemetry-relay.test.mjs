import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { copyFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { hostname, tmpdir } from 'node:os';
import { join } from 'node:path';
import { createInterface } from 'node:readline';
import { test } from 'node:test';
import { fileURLToPath, pathToFileURL } from 'node:url';

const relay = new URL('./lark-telemetry-relay.mjs', import.meta.url);
const events = new URL('./lark-telemetry-events.mjs', import.meta.url);
const versionArgs = ['--bridge-version', 'v1.2.3'];
const moduleEnv = 'LARK_CHANNEL_TELEMETRY_MODULE';

function failure(tenant = 'feishu', appId = 'cli_synthetic') {
  return JSON.stringify({
    type: 'receiver-failure', tenant, appId, at: '2026-11-19T02:03:04Z', kind: 'inbound-failed',
  });
}

function tempDir(t) {
  const root = mkdtempSync(join(tmpdir(), 't3-telemetry-relay-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  return root;
}

function run(input, args = versionArgs, { cwd, module, script = fileURLToPath(relay) } = {}) {
  const env = { ...process.env };
  delete env[moduleEnv];
  if (module !== undefined) env[moduleEnv] = module;
  const result = spawnSync(process.execPath, [script, ...args], {
    cwd, env, input, encoding: 'utf8', timeout: 4000,
  });
  assert.ifError(result.error);
  assert.equal(result.signal, null);
  return result;
}

function output(result, status = 0) {
  assert.equal(result.status, status, result.stderr);
  return result.stdout.trim().split('\n').filter(Boolean).map((line) => JSON.parse(line));
}

test('relay is off without a module and explicit configuration wins', () => {
  for (const result of [
    run(failure()),
    run(failure(), [...versionArgs, '--module='], { module: 'missing-synthetic-package' }),
  ]) {
    assert.deepEqual(output(result), []);
    assert.equal(result.stderr, '');
  }
});

test('the default factory and async methods handle two accounts through EOF', (t) => {
  const root = tempDir(t);
  const plugin = join(root, 'default #%.mjs');
  writeFileSync(plugin, `
    const log = (...values) => console.log(JSON.stringify(values));
    export default async function (meta) {
      log('create', meta);
      return {
        tenant: meta.tenant,
        calls: 0,
        async emit() {
          const call = ++this.calls;
          await new Promise((resolve) => setImmediate(resolve));
          log('emit', this.tenant, call);
        },
        async flush(timeoutMs) {
          await new Promise((resolve) => setImmediate(resolve));
          log('flush', this.tenant, timeoutMs);
        },
        async close() {
          await new Promise((resolve) => setImmediate(resolve));
          log('close', this.tenant);
        },
      };
    }
  `);
  const result = run(['ordinary log', failure(), failure('lark'), failure()].join('\r\n'),
    [...versionArgs, '--module', plugin], { module: 'missing-synthetic-package' });
  const rows = output(result);
  assert.equal(result.stderr, '');
  assert.deepEqual(rows.slice(0, 5), [
    ['create', { version: '1.2.3', tenant: 'feishu', appId: 'cli_synthetic', hostname: hostname() }],
    ['emit', 'feishu', 1],
    ['create', { version: '1.2.3', tenant: 'lark', appId: 'cli_synthetic', hostname: hostname() }],
    ['emit', 'lark', 1],
    ['emit', 'feishu', 2],
  ]);
  assert.equal(rows.length, 9);
  for (const tenant of ['feishu', 'lark']) {
    const [flush, close] = rows.slice(5).filter((row) => row[1] === tenant);
    assert.deepEqual(flush.slice(0, 2), ['flush', tenant]);
    assert.ok(flush[2] > 0 && flush[2] <= 2000);
    assert.deepEqual(close, ['close', tenant]);
  }
  assert.deepEqual(output(run('ordinary log\n', [...versionArgs, '--module', plugin])), []);
});

test('the named factory loads from the legacy env, file URL and relative paths', (t) => {
  const root = tempDir(t);
  const plugin = join(root, '~named.mjs');
  const subdir = join(root, 'subdir');
  mkdirSync(subdir);
  writeFileSync(plugin, `
    export function createAdapter(meta) {
      return { emit() { console.log(JSON.stringify(meta.version)); } };
    }
  `);
  const url = pathToFileURL(plugin).href.replace(/~named\.mjs$/, '%7enamed.mjs');
  for (const [module, cwd] of [[url, root], ['./~named.mjs', root], ['../~named.mjs', subdir]]) {
    const result = run(failure(), ['--bridge-version', '1.2.3'], { module, cwd });
    assert.deepEqual(output(result), ['1.2.3']);
    assert.equal(result.stderr, '');
  }
});

test('a bare package resolves from the relay location with Node package exports', (t) => {
  const root = tempDir(t);
  const deployment = join(root, 'deploy');
  const packageDir = join(deployment, 'node_modules', '@synthetic', 'adapter');
  mkdirSync(packageDir, { recursive: true });
  const script = join(deployment, 'lark-telemetry-relay.mjs');
  copyFileSync(relay, script);
  copyFileSync(events, join(deployment, 'lark-telemetry-events.mjs'));
  writeFileSync(join(packageDir, 'package.json'), JSON.stringify({
    exports: { import: './plugin.cjs' },
  }));
  writeFileSync(join(packageDir, 'plugin.cjs'), `
    module.exports = () => ({ emit() { console.log(JSON.stringify('bare-package')); } });
  `);
  const result = run(failure(), [...versionArgs, '--module', '@synthetic/adapter'], { script, cwd: root });
  assert.deepEqual(output(result), ['bare-package']);
  assert.equal(result.stderr, '');
});

test('invalid arguments and invalid modules have fixed diagnostics', (t) => {
  const root = tempDir(t);
  const throwingModule = join(root, 'synthetic-private.mjs');
  const noFactory = join(root, 'no-factory.mjs');
  writeFileSync(throwingModule, `
    setInterval(() => {}, 1000);
    throw new Error('synthetic-private-import-error');
  `);
  writeFileSync(noFactory, 'export default {}');
  const usage = run(failure(), ['--synthetic-private-arg']);
  assert.equal(usage.status, 1);
  assert.equal(usage.stdout, '');
  assert.equal(usage.stderr, 'Usage: node deploy/lark-telemetry-relay.mjs --bridge-version x [--module location]\n');

  for (const location of [throwingModule, noFactory, join(root, 'missing.mjs'),
    'https://example.invalid/synthetic-private.mjs', 'data:text/javascript,console.log(123)']) {
    const result = run(failure(), [...versionArgs, '--module', location]);
    assert.equal(result.status, 1);
    assert.equal(result.stdout, '');
    assert.equal(result.stderr, 'lark-telemetry-relay: adapter module load failed\n');
  }
});

test('factory and method failures leave other records and cleanup working', (t) => {
  const plugin = join(tempDir(t), 'flaky.mjs');
  writeFileSync(plugin, `
    const log = (...values) => console.log(JSON.stringify(values));
    export async function createAdapter({ appId }) {
      if (appId === 'cli_throw') throw new Error('synthetic-private-factory-error');
      if (appId === 'cli_bad') return { close() { throw new Error('invalid adapter used'); } };
      return {
        appId,
        calls: 0,
        async emit() {
          log('emit', this.appId, ++this.calls);
          if (this.calls === 1) throw new Error('synthetic-private-emit-error');
        },
        async recordError() {
          await new Promise((resolve) => setImmediate(resolve));
          log('error', this.appId, this.calls);
        },
        get flush() { throw new Error('synthetic-private-flush-error'); },
        close() { log('close', this.appId); },
      };
    }
  `);
  const result = run([
    failure('feishu', 'cli_throw'), failure(), failure('feishu', 'cli_bad'),
    failure('feishu', 'cli_throw'), failure(),
  ].join('\n'), [...versionArgs, '--module', plugin]);
  assert.deepEqual(output(result), [
    ['emit', 'cli_synthetic', 1], ['error', 'cli_synthetic', 1],
    ['emit', 'cli_synthetic', 2], ['error', 'cli_synthetic', 2], ['close', 'cli_synthetic'],
  ]);
  assert.equal(result.stderr, [
    'lark-telemetry-relay: adapter factory failed',
    'lark-telemetry-relay: adapter method failed',
    'lark-telemetry-relay: adapter must provide emit',
    'lark-telemetry-relay: adapter method failed',
    '',
  ].join('\n'));
});

test('the cleanup deadline covers both accounts and both methods', (t) => {
  const plugin = join(tempDir(t), 'slow.mjs');
  writeFileSync(plugin, `
    import { writeSync } from 'node:fs';
    const log = (...values) => writeSync(1, JSON.stringify(values) + '\\n');
    function hang() {
      setInterval(() => {}, 1000);
      return new Promise(() => {});
    }
    export function createAdapter({ tenant }) {
      return {
        emit() {},
        flush() {
          log('flush', tenant);
          if (tenant === 'feishu') return hang();
        },
        close() {
          log('close', tenant);
          return hang();
        },
      };
    }
  `);
  const result = run([failure(), failure('lark')].join('\n'), [...versionArgs, '--module', plugin]);
  assert.deepEqual(output(result, 1), [
    ['flush', 'feishu'], ['flush', 'lark'], ['close', 'lark'], ['close', 'feishu'],
  ]);
  assert.equal(result.stderr, 'lark-telemetry-relay: adapter cleanup timed out\n');
});

test('SIGTERM bounds the wait for a pending adapter callback', {
  skip: process.platform === 'win32', timeout: 5000,
}, async (t) => {
  const plugin = join(tempDir(t), 'pending.mjs');
  writeFileSync(plugin, `
    export default () => ({
      emit() { console.log('started'); return new Promise(() => {}); },
    });
  `);
  const child = spawn(process.execPath, [fileURLToPath(relay), ...versionArgs, '--module', plugin]);
  t.after(() => {
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
  });
  const closed = once(child, 'close');
  const lines = createInterface({ input: child.stdout });
  lines.once('line', () => child.kill('SIGTERM'));
  let stderr = '';
  child.stderr.setEncoding('utf8').on('data', (chunk) => { stderr += chunk; });
  child.stdin.write(`${failure()}\n`);
  assert.deepEqual(await closed, [1, null]);
  assert.equal(stderr, 'lark-telemetry-relay: active adapter call timed out during shutdown\n');
});

for (const signal of ['SIGINT', 'SIGTERM']) {
  test(`${signal} stops an open reader, finishes the current record and cleans up`, {
    skip: process.platform === 'win32', timeout: 5000,
  }, async (t) => {
    const plugin = join(tempDir(t), 'signal.mjs');
    writeFileSync(plugin, `
      import { once } from 'node:events';
      const log = (value) => console.log(JSON.stringify(value));
      export default () => ({
        async emit() {
          const signaled = once(process, ${JSON.stringify(signal)});
          log('started');
          await signaled;
          log('finished');
        },
        async flush() { log('flush'); },
        async close() { log('close'); },
      });
    `);
    const child = spawn(process.execPath, [fileURLToPath(relay), ...versionArgs, '--module', plugin]);
    t.after(() => {
      if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
    });
    const closed = once(child, 'close');
    const rows = [];
    const lines = createInterface({ input: child.stdout });
    lines.on('line', (line) => {
      rows.push(line);
      if (rows.length === 1) child.kill(signal);
    });
    let stderr = '';
    child.stderr.setEncoding('utf8').on('data', (chunk) => { stderr += chunk; });
    child.stdin.write(`${failure()}\n${failure()}\n`);
    assert.deepEqual(await closed, [0, null], stderr);
    assert.deepEqual(rows.map((line) => JSON.parse(line)), ['started', 'finished', 'flush', 'close']);
    assert.equal(stderr, '');
  });
}
