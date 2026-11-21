import { Buffer } from 'node:buffer';

const maxLineBytes = 4 * 1024;
const maxInt64 = 9_223_372_036_854_775_807n;
const appIdPattern = /^cli_[A-Za-z0-9_-]{1,124}$/;
const commandSteps = new Set([
  'handler', 'reply', 'reply-fallback', 'reply-expired', 'reconnect', 'doctor-run',
  'workspace', 'group-read', 'chat-create', 'chat-welcome', 'meeting', 'receiver-stop',
]);
const readinessLevels = new Map([
  ['mcp-verified', 'info'], ['mcp-unverified', 'warn'],
  ['feishu-ready', 'info'], ['feishu-not-ready', 'warn'],
]);
const countDiagnostics = new Set(['reconcile', 'delivery-pending']);
const failureKinds = new Set([
  'store-open-failed', 'account-bind-failed', 'initial-connect-failed',
  'inbound-failed', 'channel-stopped', 'worker-tick-failed',
]);
const receiverEvents = new Map([
  ['run-started', ['info', 'run', 'started']],
  ['run-completed', ['info', 'run', 'completed']],
  ['run-failed', ['warn', 'run', 'failed']],
  ['outbound-sent', ['info', 'outbound', 'sent']],
  ['ws-reconnected', ['info', 'ws', 'reconnected']],
]);

export function parseTelemetryLine(line) {
  if (typeof line !== 'string' || Buffer.byteLength(line) > maxLineBytes) return undefined;
  const newline = line.indexOf('\n');
  if (newline !== -1 && newline !== line.length - 1) return undefined;

  let fields;
  try {
    fields = JSON.parse(line);
  } catch {
    return undefined;
  }
  if (fields === null || typeof fields !== 'object' || Array.isArray(fields)) return undefined;

  const { type, appId, tenant, at } = fields;
  if (typeof appId !== 'string' || !appIdPattern.test(appId) ||
      (tenant !== 'feishu' && tenant !== 'lark') || !validTimestamp(at)) return undefined;

  const record = { type, appId, tenant, at };
  switch (type) {
    case 'receiver-metric':
      return parseMetric(record, fields, line);
    case 'receiver-diagnostic': {
      const { kind } = fields;
      const count = readIntegerField(fields, line, 'count');
      if ((readinessLevels.has(kind) && count === 0) ||
          (countDiagnostics.has(kind) && count !== undefined && count <= 9_999)) {
        return { ...record, kind, count };
      }
      break;
    }
    case 'receiver-failure':
      if (failureKinds.has(fields.kind)) return { ...record, kind: fields.kind };
      break;
    case 'receiver-event':
      if (receiverEvents.has(fields.kind)) return { ...record, kind: fields.kind };
      break;
  }
  return undefined;
}

function parseMetric(record, fields, line) {
  const { name, step, terminal } = fields;
  const value = readIntegerField(fields, line, 'value');
  if (value === undefined) return undefined;

  switch (name) {
    case 'run_e2e_ms':
      if (terminal === 'completed' || terminal === 'failed') return { ...record, name, value, terminal };
      break;
    case 'command_fail':
      if (value === 1 && commandSteps.has(step)) return { ...record, name, value, step };
      break;
    case 'ws_reconnect':
      if (value === 1) return { ...record, name, value };
      break;
    case 'pool_active':
    case 'pool_waiting':
      return { ...record, name, value };
  }
  return undefined;
}

function readIntegerField(fields, line, key) {
  const value = fields[key];
  if (!Number.isInteger(value) || value < 0) return undefined;

  // JSON.parse rounds large numbers and erases the distinction between 1 and 1.0.
  // Quote numeric tokens in the already validated JSON to recover the exact integer spelling.
  const source = JSON.parse(line.replace(/"(?:[^"\\]|\\.)*"|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/g,
    token => token.startsWith('"') ? token : `"${token}"`))[key];
  if (!/^-?\d{1,19}$/.test(source) || BigInt(source) > maxInt64) return undefined;
  return value === 0 ? 0 : value;
}

function validTimestamp(at) {
  if (typeof at !== 'string') return false;
  const match = /^(\d{4}-\d{2}-\d{2})T((?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d)(?:[.,](\d+))?(Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/.exec(at);
  if (!match) return false;

  const [, date, time, fraction = '', offset] = match;
  const day = new Date(`${date}T00:00:00Z`);
  if (!Number.isFinite(day.getTime()) || day.toISOString().slice(0, 10) !== date) return false;

  const utc = new Date(`${date}T${time}${offset}`);
  const year = utc.getUTCFullYear();
  if (year < 0 || year > 9999) return false;
  return utc.toISOString() !== '0001-01-01T00:00:00.000Z' || /[1-9]/.test(fraction.slice(0, 9));
}

// The relay supplies an invoke implementation that contains adapter errors.
export async function dispatchTelemetryRecord(record, invoke) {
  if (record.type === 'receiver-metric') {
    let tags;
    switch (record.name) {
      case 'run_e2e_ms':
        tags = { terminal: record.terminal === 'completed' ? 'done' : 'error' };
        break;
      case 'ws_reconnect':
        tags = { kind: 'ws' };
        break;
      case 'command_fail':
        tags = { step: record.step };
        break;
    }
    await invoke('recordMetric', record.name, record.value, tags);
    return;
  }

  if (record.type === 'receiver-event') {
    const [level, phase, event] = receiverEvents.get(record.kind);
    await invoke('emit', { level, phase, event, fields: {}, ctx: {}, ts: record.at });
    return;
  }

  const { kind: event, at: ts, count } = record;
  const phase = 'receiver';
  const failure = record.type === 'receiver-failure';
  const level = failure ? 'error' : readinessLevels.get(event) ?? (count > 0 ? 'warn' : 'info');
  await invoke('emit', { level, phase, event, fields: failure ? {} : { count }, ctx: {}, ts });
  if (failure) await invoke('recordError', `receiver.${event}`, { phase, event });
}
