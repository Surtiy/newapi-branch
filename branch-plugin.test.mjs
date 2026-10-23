import assert from 'node:assert/strict';
import test from 'node:test';
import { buildContentRequest, extractUsageOnComplete, parseTaskResult } from './plugins/tasks/aicost-branch/plugin.js';

for (const status of ['failed', 'failure', 'cancelled', 'canceled', 'expired']) {
  test(`outer ${status} overrides request metadata and stale nested status`, () => {
    const result = parseTaskResult({}, { status, data: { status: 'queued', seconds: '5' } });
    assert.equal(result.status, 'FAILURE');
    assert.equal(result.progress, '100%');
  });
}

test('actual failed common response is terminal, not queued', () => {
  const raw = { status: 'failed', progress: '100%', fail_reason: 'upstream model_not_found', data: { id: 'provider-task', model: 'wan3.0-720p', seconds: '5', request_params: { seconds: '5' } } };
  assert.equal(parseTaskResult({}, raw).status, 'FAILURE');
});

for (const status of ['completed', 'succeeded', 'success']) {
  test(`outer ${status} uses outer result_url before nested data`, () => {
    assert.deepEqual(parseTaskResult({}, { status, result_url: 'https://example.invalid/final.mp4', data: { url: 'https://example.invalid/stale.mp4' } }), { status: 'SUCCESS', progress: '100%', url: 'https://example.invalid/final.mp4' });
  });
}

test('success can find media inside data while keeping outer status', () => {
  assert.equal(parseTaskResult({}, { status: 'succeeded', data: { video_url: 'https://example.invalid/video.mp4' } }).status, 'SUCCESS');
});

test('nested response envelope still works', () => {
  assert.equal(parseTaskResult({}, { success: true, data: { status: 'failed' } }).status, 'FAILURE');
  assert.equal(parseTaskResult({}, { data: { status: 'completed', video_url: 'https://example.invalid/video.mp4' } }).status, 'SUCCESS');
});

test('outer running status and progress have precedence', () => {
  assert.deepEqual(parseTaskResult({}, { status: 'processing', progress: '80%', data: { status: 'queued' } }), { status: 'IN_PROGRESS', progress: '80%' });
});

test('unknown and missing result URL remain unknown', () => {
  assert.equal(parseTaskResult({}, { status: 'succeeded', data: { seconds: 5 } }).status, 'UNKNOWN');
  assert.equal(parseTaskResult({}, null).status, 'UNKNOWN');
});

test('actual completion usage can live in nested request metadata', () => {
  assert.deepEqual(extractUsageOnComplete({}, {}, { status: 'succeeded', data: { seconds: '5' } }), { seconds: 5 });
  assert.deepEqual(extractUsageOnComplete({}, {}, { data: { status: 'completed', duration: 6 } }), { seconds: 6 });
  assert.deepEqual(extractUsageOnComplete({}, {}, { status: 'completed', seconds: 7, data: { seconds: 5 } }), { seconds: 7 });
  assert.deepEqual(extractUsageOnComplete({}, {}, { status: 'completed' }), {});
});

test('content proxy resolves the outer common result URL', () => {
  assert.deepEqual(buildContentRequest({ data: { status: 'succeeded', result_url: 'https://example.invalid/final.mp4', data: { seconds: 5 } }, artifactKey: 'video', clientRequest: { method: 'HEAD' } }), { url: 'https://example.invalid/final.mp4', method: 'HEAD', credentialless: true });
});
