import assert from 'node:assert/strict';
import test from 'node:test';
import { buildCompletionCheckRequest, buildContentRequest, extractUsageOnComplete, listArtifacts, parseTaskResult } from './plugins/tasks/aicost-branch/plugin.js';

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

test('completed without URL is a candidate for the host media check; missing status stays unknown', () => {
  assert.deepEqual(parseTaskResult({}, { status: 'succeeded', data: { seconds: 5 } }), { status: 'SUCCESS', progress: '100%' });
  assert.equal(parseTaskResult({}, null).status, 'UNKNOWN');
  assert.equal(parseTaskResult({}, { video_url: 'https://example.invalid/video.mp4' }).status, 'UNKNOWN');
});

test('completion probe uses the private polling task id and upstream credentials', () => {
  assert.deepEqual(buildCompletionCheckRequest({ baseUrl: 'https://upstream.example.invalid', taskId: 'upstream/id', publicTaskId: 'branch-id', apiKey: 'test-key' }), {
    url: 'https://upstream.example.invalid/v1/videos/upstream%2Fid/content', method: 'GET', headers: { Authorization: 'Bearer test-key' },
  });
});

test('actual completion usage can live in nested request metadata', () => {
  assert.deepEqual(extractUsageOnComplete({}, {}, { status: 'succeeded', data: { seconds: '5' } }), { seconds: 5 });
  assert.deepEqual(extractUsageOnComplete({}, {}, { data: { status: 'completed', duration: 6 } }), { seconds: 6 });
  assert.deepEqual(extractUsageOnComplete({}, {}, { status: 'completed', seconds: 7, data: { seconds: 5 } }), { seconds: 7 });
  assert.deepEqual(extractUsageOnComplete({}, {}, { status: 'completed' }), {});
});

for (const method of ['GET', 'HEAD']) {
  for (const data of [
    { status: 'completed' },
    { status: 'completed', result_url: 'https://cdn.example.invalid/expired.mp4' },
    { data: { id: 'ignored-id', status: 'completed', video_url: 'https://untrusted.example.invalid/content' } },
  ]) {
    test(`content ${method} uses channel credentials and upstream id (${JSON.stringify(data)})`, () => {
      assert.deepEqual(buildContentRequest({ baseUrl: 'https://upstream.example.invalid/gateway/', apiKey: 'test-upstream-key', taskId: 'public-branch-id', upstreamTaskId: 'upstream/id?#', data, artifactKey: 'video', clientRequest: { method } }), {
        url: 'https://upstream.example.invalid/gateway/v1/videos/upstream%2Fid%3F%23/content', method, headers: { Authorization: 'Bearer test-upstream-key' },
      });
    });
  }
}

test('host-approved completed tasks expose video artifacts without a result URL', () => {
  const result = parseTaskResult({}, { data: { status: 'completed' } });
  assert.deepEqual(listArtifacts({ status: result.status }), [{ key: 'video', type: 'video' }]);
  assert.deepEqual(listArtifacts({ status: 'IN_PROGRESS' }), []);
  assert.deepEqual(listArtifacts({ status: 'FAILURE' }), []);
});

test('content requests reject unknown artifacts and missing private context', () => {
  const ctx = { baseUrl: 'https://upstream.example.invalid', upstreamTaskId: 'upstream-id', apiKey: 'test-key', artifactKey: 'video', clientRequest: { method: 'GET' } };
  assert.throws(() => buildContentRequest({ ...ctx, artifactKey: 'image' }), /artifact_not_found/);
  assert.throws(() => buildContentRequest({ ...ctx, upstreamTaskId: undefined, taskId: 'public-id' }), /upstream content context/);
  assert.throws(() => buildContentRequest({ ...ctx, baseUrl: undefined }), /upstream content context/);
});
