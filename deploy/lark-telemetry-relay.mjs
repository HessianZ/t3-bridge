import { hostname } from 'node:os';
import { isAbsolute } from 'node:path';
import { createInterface } from 'node:readline';
import { pathToFileURL } from 'node:url';
import { parseArgs } from 'node:util';
import { dispatchTelemetryRecord, parseTelemetryLine } from './lark-telemetry-events.mjs';

const cleanupTimeoutMs = 2000;

async function main() {
  let options;
  try {
    ({ values: options } = parseArgs({ options: {
      'bridge-version': { type: 'string' },
      module: { type: 'string' },
    } }));
    if (!options['bridge-version']) throw new Error();
  } catch {
    console.error('Usage: node deploy/lark-telemetry-relay.mjs --bridge-version x [--module location]');
    process.exitCode = 1;
    return;
  }

  const location = options.module ?? process.env.LARK_CHANNEL_TELEMETRY_MODULE;
  if (!location) return;

  let factory;
  try {
    const plugin = await import(moduleURL(location));
    factory = typeof plugin.default === 'function' ? plugin.default : plugin.createAdapter;
    if (typeof factory !== 'function') throw new Error();
  } catch {
    process.stderr.write('lark-telemetry-relay: adapter module load failed\n', () => process.exit(1));
    return;
  }

  const version = options['bridge-version'].replace(/^v/, '');
  const host = hostname();
  const adapters = new Map();
  const reader = createInterface({ input: process.stdin, terminal: false, crlfDelay: Infinity });
  let stopping = false;
  let stopTimer;
  const stop = () => {
    if (stopping) return;
    stopping = true;
    stopTimer = setTimeout(() => {
      console.error('lark-telemetry-relay: active adapter call timed out during shutdown');
      process.exit(1);
    }, cleanupTimeoutMs);
    reader.close();
    process.stdin.destroy();
  };
  process.on('SIGINT', stop);
  process.on('SIGTERM', stop);
  reader.on('error', () => {
    console.error('lark-telemetry-relay: stdin read failed');
    process.exitCode = 1;
    stop();
  });

  try {
    for await (const line of reader) {
      if (stopping) break;
      const record = parseTelemetryLine(line);
      if (!record) continue;
      const { tenant, appId } = record;
      const key = JSON.stringify([tenant, appId]);
      if (!adapters.has(key)) {
        adapters.set(key, await createAdapter(factory, { version, appId, tenant, hostname: host }));
      }
      const adapter = adapters.get(key);
      if (adapter) {
        await dispatchTelemetryRecord(record, (method, ...args) => invoke(adapter, method, ...args));
      }
    }
  } finally {
    clearTimeout(stopTimer);
    reader.close();
    await cleanup(adapters.values());
    process.off('SIGINT', stop);
    process.off('SIGTERM', stop);
  }
}

function moduleURL(location) {
  let url;
  if (isAbsolute(location) || /^\.\.?[/\\]/.test(location)) {
    url = pathToFileURL(location);
  } else if (/^file:/i.test(location)) {
    url = new URL(location.replace(/%7E/gi, '~'));
  } else {
    url = new URL(import.meta.resolve(location));
  }
  if (url.protocol !== 'file:' || url.hostname) throw new Error();
  return url;
}

async function createAdapter(factory, meta) {
  try {
    const adapter = await factory(meta);
    if (typeof adapter?.emit === 'function') return adapter;
  } catch {
    console.error('lark-telemetry-relay: adapter factory failed');
    return;
  }
  console.error('lark-telemetry-relay: adapter must provide emit');
}

async function invoke(adapter, method, ...args) {
  try {
    const callback = adapter[method];
    if (typeof callback === 'function') await Reflect.apply(callback, adapter, args);
  } catch {
    console.error('lark-telemetry-relay: adapter method failed');
  }
}

async function cleanup(adapters) {
  let timer;
  let complete = false;
  const deadline = performance.now() + cleanupTimeoutMs;
  const timeout = new Promise((resolve) => {
    timer = setTimeout(() => {
      if (!complete) {
        console.error('lark-telemetry-relay: adapter cleanup timed out');
        process.exitCode = 1;
      }
      resolve();
      // Give the races a turn to invoke close even if flush consumed the budget.
      setImmediate(() => process.exit(process.exitCode ?? 0));
    }, cleanupTimeoutMs);
  });
  await Promise.all(Array.from(adapters, async (adapter) => {
    if (!adapter) return;
    const remainingMs = Math.max(0, Math.floor(deadline - performance.now()));
    await Promise.race([invoke(adapter, 'flush', remainingMs), timeout]);
    await Promise.race([invoke(adapter, 'close'), timeout]);
  }));
  complete = true;
  // Let normal output drain and enforce the deadline if a plugin leaves handles open.
  timer.unref();
}

await main();
