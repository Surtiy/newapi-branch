export const meta = {
  apiVersion: 1, key: 'aicost-branch', name: 'AICost 主站视频', version: '1.0.0',
  author: { name: 'AICost' }, models: ['aicost-branch-video'], fetchMode: 'per_task',
  usageSchema: { seconds: { type: 'number', unit: 'second' } },
  protocols: ['openai_video'],
};

export function buildSubmitRequest(ctx) {
  const req = Object.assign({}, ctx.requestBody);
  // The channel mapping only selects this plugin. The upstream must receive
  // the public model name that the client requested, never the plugin alias.
  if (!ctx.model || ctx.model === 'aicost-branch-video') throw new Error('模型名称缺失');
  req.model = ctx.model;
  const headers = { Authorization: 'Bearer ' + ctx.apiKey };
  if ((ctx.files || []).length) {
    const parts = Object.keys(req).map(key => ({ name: key, value: typeof req[key] === 'object' ? JSON.stringify(req[key]) : String(req[key]) }));
    for (const file of ctx.files) parts.push({ name: file.field, fileRef: file.ref, filename: file.filename });
    return { url: ctx.baseUrl + '/v1/videos', method: 'POST', headers, bodyType: 'multipart', parts };
  }
  headers['Content-Type'] = 'application/json';
  return { url: ctx.baseUrl + '/v1/videos', method: 'POST', headers, body: req };
}

export function parseSubmitResponse(_ctx, response) {
  const body = response.body || {};
  const data = body.data || body;
  const taskId = data.id || data.task_id;
  if (!taskId || data.error) throw new Error('主站未接受视频任务');
  return { taskId, taskData: data };
}

export function buildQueryRequest(ctx) {
  return { url: ctx.baseUrl + '/v1/videos/' + encodeURIComponent(ctx.taskId), method: 'GET', headers: { Authorization: 'Bearer ' + ctx.apiKey } };
}

function videoURL(body) {
  if (!body) return '';
  if (body.data && typeof body.data === 'object' && !Array.isArray(body.data)) return videoURL(body.data);
  return (body.video && body.video.url) || body.video_url || body.url || (body.urls || [])[0] || '';
}

export function parseTaskResult(_ctx, raw) {
  const body = raw.data || raw;
  const status = String(body.status || '').toLowerCase();
  if (['completed', 'succeeded', 'success'].includes(status)) {
    const url = videoURL(body);
    if (!url) return { status: 'UNKNOWN', reason: '主站任务完成但成片地址尚未返回' };
    return { status: 'SUCCESS', progress: '100%', url };
  }
  if (['failed', 'failure', 'cancelled', 'canceled', 'expired'].includes(status)) return { status: 'FAILURE', progress: '100%', reason: '主站视频生成失败' };
  if (['queued', 'pending', 'not_start', 'submitted', 'submitting'].includes(status)) return { status: 'QUEUED', progress: '0%' };
  if (['processing', 'in_progress', 'running'].includes(status)) {
    const progress = Number(String(body.progress || 30).replace('%', ''));
    return { status: 'IN_PROGRESS', progress: Math.max(0, Math.min(99, Number.isFinite(progress) ? progress : 30)) + '%' };
  }
  return { status: 'UNKNOWN', reason: '等待主站任务状态' };
}

export function extractUsage(ctx) {
  const req = ctx.requestBody || {};
  const seconds = Number(req.seconds === undefined ? req.duration : req.seconds);
  if (!Number.isFinite(seconds) || seconds <= 0) throw new Error('请明确提供 seconds 视频时长');
  return { seconds };
}

export function extractUsageOnComplete(_task, _result, raw) {
  const body = raw.data || raw;
  const seconds = Number(body.seconds || body.duration);
  return Number.isFinite(seconds) && seconds > 0 ? { seconds } : {};
}

export function listArtifacts(task) {
  return task.status === 'SUCCESS' ? [{ key: 'video', type: 'video' }] : [];
}

export function buildContentRequest(ctx) {
  let data = ctx.data || {};
  if (data.data && data.data.task_id && data.data.data) data = data.data.data;
  const url = videoURL(data);
  if (ctx.artifactKey !== 'video' || !url) throw new Error('artifact_not_found');
  return { url, method: ctx.clientRequest.method, credentialless: true };
}

export const protocols = {
  openai_video: {
    decodeRequest(ctx) {
      const body = ctx.body || {};
      let req;
      if (body.kind === 'json') {
        if (!body.value || Array.isArray(body.value)) throw new Error('JSON object required');
        req = Object.assign({}, body.value);
      } else if (body.kind === 'multipart') {
        req = {};
        for (const key of Object.keys(body.fields || {})) {
          const values = body.fields[key];
          req[key] = values.length === 1 ? values[0] : values;
        }
      } else throw new Error('JSON or multipart body required');
      req.model = ctx.model;
      if (!String(req.prompt || '').trim()) throw new Error('prompt is required');
      const seconds = Number(req.seconds === undefined ? req.duration : req.seconds);
      if (!Number.isFinite(seconds) || seconds <= 0 || seconds > 3600) throw new Error('请提供有效的 seconds 视频时长');
      req.seconds = seconds;
      return { kind: 'submit', model: ctx.model, action: 'generate', requestBody: req };
    },
    render(_ctx, task) {
      const states = { NOT_START: 'queued', SUBMITTED: 'queued', QUEUED: 'queued', IN_PROGRESS: 'in_progress', SUCCESS: 'completed', FAILURE: 'failed' };
      const response = { id: task.task_id, object: 'video', model: (task.properties || {}).origin_model_name || '', status: states[task.status] || 'in_progress', progress: task.status === 'SUCCESS' ? 100 : Number(String(task.progress || 0).replace('%','')), created_at: task.created_at };
      if (task.status === 'FAILURE') response.error = { code: 'video_generation_failed', message: '视频生成失败' };
      return response;
    },
  },
};
