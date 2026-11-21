import assert from 'node:assert/strict';
import { Buffer } from 'node:buffer';
import { test } from 'node:test';
import { dispatchTelemetryRecord, parseTelemetryLine } from './lark-telemetry-events.mjs';

const account = { appId: 'cli_synthetic', tenant: 'feishu', at: '2026-11-19T02:03:04.123456789Z' };
const metric = { ...account, type: 'receiver-metric', name: 'pool_active', value: 1 };
const diagnostic = { ...account, type: 'receiver-diagnostic', kind: 'reconcile', count: 0 };
const failure = { ...account, type: 'receiver-failure', kind: 'inbound-failed' };

function parse(record) {
  return parseTelemetryLine(JSON.stringify(record));
}

test('copies only the fields allowed for each record', () => {
  const privateFields = { message: 'private', path: '/private', fields: { cost: 4 }, ctx: { tokens: 9 } };
  assert.deepEqual(parse({ ...metric, ...privateFields, step: 'reply', terminal: 'failed', count: 2 }), metric);
  assert.deepEqual(parse({ ...diagnostic, ...privateFields, name: 'pool_active', value: 3 }), diagnostic);
  assert.deepEqual(parse({ ...failure, ...privateFields, count: 2, error: 'private' }), failure);

  const larkMetric = { ...metric, tenant: 'lark', appId: 'cli_Z_-0' };
  assert.deepEqual(parse(larkMetric), larkMetric);
  const safe = JSON.stringify(metric).replace(/}$/, ',"__proto__":{"step":"reply"}}');
  assert.deepEqual(parseTelemetryLine(safe), metric);
});

test('checks the UTF-8 byte budget and JSON line framing', () => {
  const prefix = JSON.stringify({ ...metric, message: '' }).slice(0, -2);
  const payloadBytes = 4096 - Buffer.byteLength(prefix) - Buffer.byteLength('"}');
  const payload = '界'.repeat(Math.floor(payloadBytes / 3)) + 'x'.repeat(payloadBytes % 3);
  const line = `${prefix}${payload}"}`;
  assert.equal(Buffer.byteLength(line), 4096);
  assert.deepEqual(parseTelemetryLine(line), metric);
  assert.equal(parseTelemetryLine(`${line} `), undefined);
  assert.deepEqual(parseTelemetryLine(`${JSON.stringify(metric)}\r\n`), metric);

  for (const input of [null, 1, '', 'null', '[]', '{', `log ${JSON.stringify(metric)}`, JSON.stringify(metric, null, 2)]) {
    assert.equal(parseTelemetryLine(input), undefined);
  }
});

test('checks identity, timestamp, and numeric value types and bounds', () => {
  for (const overrides of [
    { type: 'other' }, { tenant: 'other' }, { appId: 'cli_' }, { appId: 'cli_a\n' },
    { appId: `cli_${'a'.repeat(125)}` }, { appId: undefined, AppID: account.appId },
    { at: undefined }, { at: null }, { at: 'yesterday' }, { at: '2026-11-19' },
    { at: '2026-02-29T02:03:04Z' }, { at: '0001-01-01T00:00:00Z' },
    { at: '9999-12-31T23:59:59-01:00' }, { at: '0000-01-01T00:00:00+01:00' },
    { name: 'pool_unknown' }, { value: undefined }, { value: null }, { value: '1' },
    { value: -1 }, { value: 1.5 }, { name: 'run_e2e_ms', terminal: 'interrupted' },
    { name: 'run_e2e_ms' }, { name: 'command_fail' }, { name: 'command_fail', step: 'private' },
    { name: 'command_fail', step: 'reply', value: 0 }, { name: 'ws_reconnect', value: 2 },
  ]) {
    assert.equal(parse({ ...metric, ...overrides }), undefined, JSON.stringify(overrides));
  }

  const leapDay = { ...metric, at: '2024-02-29T03:03:04.123456789+01:00' };
  assert.deepEqual(parse(leapDay), leapDay);
  const command = JSON.stringify({ ...metric, name: 'command_fail', step: 'reply' });
  for (const token of ['1.0', '1e0', '1.00000000000000001', '9223372036854775808']) {
    assert.equal(parseTelemetryLine(command.replace('"value":1', `"value":${token}`)), undefined, token);
  }
  const pool = JSON.stringify({ ...metric, comment: '"value":3.5', nested: { value: 9 } });
  const largest = parseTelemetryLine(pool.replace('"value":1', '"value":9223372036854775807'));
  assert.deepEqual(largest, { ...metric, value: Number(9_223_372_036_854_775_807n) });
  assert.equal(parseTelemetryLine(pool.replace('"value":1', '"value":9223372036854775808')), undefined);
  assert.deepEqual(parseTelemetryLine(pool.replace('"value":1', '"value":-0')), { ...metric, value: 0 });
});

test('dispatches only legacy metric calls using fixed tags', async () => {
  const cases = [
    [{ name: 'pool_active', value: 5 }, undefined],
    [{ name: 'pool_waiting', value: 0 }, undefined],
    [{ name: 'ws_reconnect', value: 1 }, { kind: 'ws' }],
    [{ name: 'run_e2e_ms', value: 700, terminal: 'completed' }, { terminal: 'done' }],
    [{ name: 'run_e2e_ms', value: 0, terminal: 'failed' }, { terminal: 'error' }],
    ...[
      'handler', 'reply', 'reply-fallback', 'reply-expired', 'reconnect', 'doctor-run',
      'workspace', 'group-read', 'chat-create', 'chat-welcome', 'meeting', 'receiver-stop',
    ].map(step => [{ name: 'command_fail', value: 1, step }, { step }]),
  ];
  const calls = [];
  const invoke = async (...args) => { calls.push(args); };
  for (const [fields] of cases) {
    await dispatchTelemetryRecord(parse({ ...metric, ...fields, tags: { private: 'context' } }), invoke);
  }
  assert.deepEqual(calls, cases.map(([{ name, value }, tags]) => ['recordMetric', name, value, tags]));
});

test('dispatches diagnostic counts and levels', async () => {
  const cases = [
    ['mcp-verified', 0, 'info'], ['mcp-unverified', 0, 'warn'],
    ['feishu-ready', 0, 'info'], ['feishu-not-ready', 0, 'warn'],
    ['reconcile', 0, 'info'], ['reconcile', 9_999, 'warn'],
    ['delivery-pending', 0, 'info'], ['delivery-pending', 1, 'warn'],
  ];
  const calls = [];
  for (const [kind, count] of cases) {
    const record = parse({ ...diagnostic, kind, count, ctx: { raw: 'private' } });
    await dispatchTelemetryRecord(record, (...args) => calls.push(args));
  }
  assert.deepEqual(calls, cases.map(([event, count, level]) => [
    'emit', { level, phase: 'receiver', event, fields: { count }, ctx: {}, ts: account.at },
  ]));

  for (const [kind, count] of [['unknown', 0], ['mcp-verified', 1], ['feishu-not-ready', -1],
    ['reconcile', 10_000], ['delivery-pending', null], ['reconcile', undefined], ['reconcile', '1']]) {
    assert.equal(parse({ ...diagnostic, kind, count }), undefined);
  }
  assert.equal(parseTelemetryLine(JSON.stringify(diagnostic).replace('"count":0', '"count":0e0')), undefined);
});

test('dispatches an error event before recording the fixed failure string', async () => {
  const kinds = [
    'store-open-failed', 'account-bind-failed', 'initial-connect-failed',
    'inbound-failed', 'channel-stopped', 'worker-tick-failed',
  ];
  for (const event of kinds) {
    const calls = [];
    const record = parse({ ...failure, kind: event, error: 'private', stack: 'private', count: 1 });
    await dispatchTelemetryRecord(record, async (...args) => {
      if (args[0] === 'recordError') assert.equal(calls.length, 1);
      await Promise.resolve();
      calls.push(args);
    });
    assert.deepEqual(calls, [
      ['emit', { level: 'error', phase: 'receiver', event, fields: {}, ctx: {}, ts: account.at }],
      ['recordError', `receiver.${event}`, { phase: 'receiver', event }],
    ]);
  }
  assert.equal(parse({ ...failure, kind: 'private' }), undefined);
});

test('dispatches only allowlisted receiver events with empty fields and context', async () => {
  const cases = [
    ['run-started', 'info', 'run', 'started'],
    ['run-completed', 'info', 'run', 'completed'],
    ['run-failed', 'warn', 'run', 'failed'],
    ['outbound-sent', 'info', 'outbound', 'sent'],
    ['ws-reconnected', 'info', 'ws', 'reconnected'],
  ];
  const wire = { ...account, type: 'receiver-event' };
  const extras = {
    level: 'error', phase: 'private', event: 'private', ts: 'private',
    count: 1, error: 'private', fields: { message: 'private' }, ctx: { path: '/private' },
  };
  const calls = [];
  for (const [kind] of cases) {
    const record = parse({ ...wire, kind, ...extras });
    assert.deepEqual(record, { ...wire, kind });
    await dispatchTelemetryRecord(record, (...args) => calls.push(args));
  }
  assert.deepEqual(calls, cases.map(([, level, phase, event]) => [
    'emit', { level, phase, event, fields: {}, ctx: {}, ts: account.at },
  ]));

  for (const kind of [undefined, null, ['run-started'], 'Run-Started', 'run.started', 'inbound-failed', '__proto__']) {
    assert.equal(parse({ ...wire, kind }), undefined);
  }
});
