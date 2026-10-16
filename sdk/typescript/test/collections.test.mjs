import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { createHash } from 'node:crypto';
import { spawn } from 'node:child_process';
import { gzipSync } from 'node:zlib';
import { mkdtemp, readFile, writeFile, readdir, rm, stat, unlink, symlink, appendFile, link } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath, pathToFileURL } from 'node:url';
import test from 'node:test';

const mainEntry = process.env.CUTMY_SDK_JS_ENTRY ? pathToFileURL(process.env.CUTMY_SDK_JS_ENTRY).href : new URL('../dist/index.js', import.meta.url).href;
const collectionsEntry = process.env.CUTMY_SDK_COLLECTION_JS_ENTRY ? pathToFileURL(process.env.CUTMY_SDK_COLLECTION_JS_ENTRY).href : new URL('../dist/collections.js', import.meta.url).href;
const localEntry = process.env.CUTMY_SDK_LOCAL_JS_ENTRY ? pathToFileURL(process.env.CUTMY_SDK_LOCAL_JS_ENTRY).href : new URL('../dist/local.js', import.meta.url).href;
const { CutmyClient, ApiError, CollectionError, exportPortableProject, importPortableProject } = await import(mainEntry);
const { runCollection, resumeCollection } = await import(collectionsEntry);
const { LocalCutmy, LocalError } = await import(localEntry);
const cookie = 'a'.repeat(43);
const limits = { max_source_bytes: 1000000, max_output_bytes: 1000000, max_fetch_bytes: 1000000, max_ranges: 128, max_range_ms: 100000, max_job_ms: 100000 };
const request = (count = 1) => ({ source_id: 'source-1', plan: { version: 1, options: { format: 'mp4', quality: 'best', cut_mode: 'accurate' }, clips: Array.from({ length: count }, (_, i) => ({ id: 'clip' + i, ranges: [{ start_ms: i * 1000, end_ms: i * 1000 + 500 }] })) } });
const chosen = req => req.plan.clips.filter(clip => !req.clip_ids?.length || req.clip_ids.includes(clip.id));
const effective = (req, clip) => ({ source_id: req.source_id, ranges: clip.ranges, format: 'mp4', quality: req.rendition === 'draft' ? '720p' : 'best', cut_mode: 'accurate', output_mode: 'joined', ...(req.rendition === 'draft' ? { rendition: 'draft' } : {}) });
const hash = data => createHash('sha256').update(data).digest('hex');
function json(res, data, status = 200) { res.writeHead(status, { 'content-type': 'application/json' }); res.end(JSON.stringify(data)); }
async function readBody(req) { const chunks = []; for await (const part of req) chunks.push(part); return JSON.parse(Buffer.concat(chunks).toString()); }
async function directory(t) { const root = await mkdtemp(join(tmpdir(), 'cutmy-collections-')); t.after(() => rm(root, { recursive: true, force: true })); return root; }
async function fixture(t, config = {}) {
  const calls = [], jobs = new Map(), identities = new Map(), bytes = new Map(), polls = new Map(); let lost = false, interrupted = false, capacity = false;
  const server = createServer(async (req, res) => {
    calls.push({ url: req.url, method: req.method, range: req.headers.range, ifRange: req.headers['if-range'], encoding: req.headers['accept-encoding'], key: req.headers['idempotency-key'], at: Date.now() });
    try {
      if (req.url === '/api/v1/session') { res.setHeader('set-cookie', 'cutmy_session=' + cookie + '; Path=/; HttpOnly'); return json(res, { ok: true, limits }); }
      assert.equal(req.headers.cookie, 'cutmy_session=' + cookie);
      if (req.method === 'GET' && req.url === '/api/v1/projects/project-1' && config.project) return json(res, config.project);
      if (req.url === '/api/v1/collections/validate') {
        const data = await readBody(req);
        if (config.validationError) return json(res, { error: { code: 'invalid_collection', message: 'Fix this clip range', clip_id: 'clip0', field: 'ranges', session_cookie: 'private-unknown-cookie', owner: { private: 'private-unknown-owner' } } }, 400);
        return json(res, { version: 1, valid: true, rendition: data.rendition ?? 'final', requested_duration_ms: chosen(data).length * 500,
          clips: chosen(data).map(clip => ({ clip_id: clip.id, label: '', preflight: { version: 1, valid: true, source: { duration_ms: 100000 }, request: effective(data, clip), requested_duration_ms: 500, output_count: 1, checks: [], warnings: [], track_manifest: [] } })) });
      }
      if (req.url === '/api/v1/collections') {
        const data = await readBody(req); calls.at(-1).body = data;
        const clips = chosen(data).map((clip, i) => {
          const normalized = effective(data, clip), identity = calls.at(-1).key + JSON.stringify({ id: clip.id, request: normalized });
          if (config.capacity && !capacity && i === 2) return { clip_id: clip.id, label: '', requested_duration_ms: 500, request: normalized, retryable: true, error: { code: 'too_many_jobs', message: 'Wait' } };
          let job = identities.get(identity);
          if (!job) {
            const n = jobs.size + 1, id = 'job-' + n, artifactID = 'artifact-' + n, payload = Buffer.alloc(config.large ? 180000 : 100, n);
            const artifact = { id: artifactID, filename: 'untrusted/path.mp4', size_bytes: payload.length, download_url: 'https://foreign.invalid/private', actual_start_ms: 0, actual_end_ms: 500, expires_at: null, segments: [], track_manifest: [] };
            job = { id, status: config.failedClip === clip.id ? 'failed' : 'succeeded', stage: 'done', message: '', storage_wait_until: null, items: [{ id: 'item-' + n, label: '', start_ms: 0, end_ms: 500, status: config.failedClip === clip.id ? 'failed' : 'succeeded', artifact: config.failedClip === clip.id ? null : artifact }] };
            if (config.workerDiagnostic && config.failedClip === clip.id) {
              job.stage = 'processing';
              Object.assign(job.items[0], { error_code: 'job_timeout', message: 'Processing deadline exceeded', progress_ms: 250, unknown_secret: 'private-unknown-worker' });
            }
            jobs.set(id, job); identities.set(identity, job); bytes.set(artifactID, payload);
          }
          return { clip_id: clip.id, label: '', requested_duration_ms: 500, request: normalized, retryable: false, job };
        });
        const partial = clips.some(clip => clip.error);
        if (config.capacity && !capacity) { capacity = true; res.setHeader('Retry-After', '1'); }
        if (config.loseReply && !lost) { lost = true; return req.socket.destroy(); }
        return json(res, { version: 1, complete: !partial, rendition: data.rendition ?? 'final', clips, future: 'retained' }, config.submitStatus ?? (partial ? 429 : 202));
      }
      if (req.url.startsWith('/api/v1/jobs/')) {
        const id = req.url.split('/').at(-1), job = jobs.get(id), count = (polls.get(id) ?? 0) + 1; polls.set(id, count);
        if (config.pollRetryStatus && count === 1) { res.setHeader('Retry-After', '2'); return json(res, { error: { code: 'temporarily_unavailable', message: 'Wait before polling again' } }, config.pollRetryStatus); }
        if (config.queuedPolls && (!config.queuedJob || config.queuedJob === id) && count <= config.queuedPolls) return json(res, { ...job, status: 'running', items: job.items.map(item => ({ ...item, status: 'pending', artifact: null })) });
        return json(res, job);
      }
      if (req.url.startsWith('/api/v1/artifacts/')) {
        const payload = bytes.get(req.url.split('/')[4]);
        const offset = req.headers.range ? Number(/^bytes=(\d+)-$/.exec(req.headers.range)[1]) : 0;
        const headers = { 'content-type': 'video/mp4', 'content-length': String(payload.length - offset), 'last-modified': 'Wed, 14 Oct 2026 21:23:39 GMT' };
        if (offset && !config.ignoreRange) headers['content-range'] = 'bytes ' + (config.badRange ? offset + 1 : offset) + '-' + (payload.length - 1) + '/' + payload.length;
        if (offset && config.ignoreRange) headers['content-length'] = String(payload.length);
        if (config.encoded) { const encoded = gzipSync(payload.subarray(offset)); headers['content-encoding'] = 'gzip'; headers['content-length'] = String(encoded.length); res.writeHead(offset ? 206 : 200, headers); return res.end(encoded); }
        res.writeHead(offset && !config.ignoreRange ? 206 : 200, headers);
        if (config.interrupt && !interrupted && !offset) { interrupted = true; res.write(payload.subarray(0, 60000)); setTimeout(() => res.destroy(), 25); return; }
        return res.end(offset && !config.ignoreRange ? payload.subarray(offset) : payload);
      }
      json(res, { error: { code: 'not_found', message: 'Missing' } }, 404);
    } catch { if (!res.headersSent) json(res, { error: { code: 'fixture_error', message: 'Fixture failed' } }, 500); else res.destroy(); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => { server.closeAllConnections(); return new Promise(resolve => server.close(resolve)); });
  const baseUrl = 'http://127.0.0.1:' + server.address().port;
  return { baseUrl, client: new CutmyClient({ baseUrl }), calls, jobs, bytes, config };
}

test('low-level collection validation is read-only and partial admission retains status and Retry-After without retry', async t => {
  const { client, calls, jobs } = await fixture(t, { capacity: true });
  assert.equal((await client.validateCollection(request(3))).valid, true); assert.equal(jobs.size, 0);
  const submitted = await client.submitCollection(request(3), { idempotencyKey: 'saved-key' });
  assert.equal(submitted.http_status, 429); assert.equal(submitted.retry_after_seconds, 1); assert.equal(submitted.future, 'retained');
  assert.equal(submitted.clips[2].error.code, 'too_many_jobs'); assert.equal(jobs.size, 2);
  assert.equal(calls.filter(call => call.method === 'POST' && call.url === '/api/v1/collections').length, 1);
  assert.throws(() => client.submitCollection(request(), { idempotencyKey: 'bad\nkey' }), /control/);
});

test('collection repair diagnostics whitelist named clip/field and discard unknown secret-bearing error fields', async t => {
  const root = join(await directory(t), 'project'), f = await fixture(t, { validationError: true });
  await assert.rejects(f.client.validateCollection(request()), error => {
    assert(error instanceof ApiError); assert.deepEqual(error.diagnostic, { code: 'invalid_collection', message: 'Fix this clip range', clip_id: 'clip0', field: 'ranges' });
    assert(!JSON.stringify(error).includes('private-unknown')); return error.status === 400;
  });
  await assert.rejects(runCollection(f.client, request(), root), error => {
    assert(error instanceof CollectionError); assert.equal(error.report.pending[0].clip_id, 'clip0'); assert.equal(error.report.pending[0].field, 'ranges'); assert.equal(error.report.pending[0].retryable, false);
    assert(!JSON.stringify(error).includes('private-unknown')); return error.code === 'invalid_collection';
  });
  assert.equal(f.jobs.size, 0); f.config.validationError = false;
  const repaired = request(); repaired.plan.clips[0].ranges[0].end_ms = 600;
  assert((await resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, { request: repaired })).complete);
});

test('numeric and nonlexical clip IDs follow plan order across run, selection and reordered revision', async t => {
  const root = join(await directory(t), 'project'), f = await fixture(t), value = request(4);
  const ids = ['2', '1', 'z', 'a']; value.plan.clips.forEach((clip, i) => { clip.id = ids[i]; });
  const done = await runCollection(f.client, value, root, { timeoutMs: 5000 }); assert.deepEqual(done.outputs.map(output => output.clip_id), ids);
  const reordered = structuredClone(value); reordered.plan.clips.reverse(); reordered.clip_ids = ['1', 'a'];
  const again = await resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, { request: reordered }); assert.deepEqual(again.outputs.map(output => output.clip_id), ['a', '1']); assert.equal(f.jobs.size, 4);
  for (const output of again.outputs) assert.equal(done.outputs.find(prior => prior.clip_id === output.clip_id).job_id, output.job_id);
});

test('portable edit documents exclude authority and require an explicit matching source', () => {
  const fingerprint = { kind: 'content', sha256: hash('source'), size_bytes: 6 };
  const source = { id: 'source-1', title: 'Interview', duration_ms: 120000, fingerprint, source_url: 'private-url', session_cookie: 'private-cookie' };
  const document = exportPortableProject(request(2), source);
  assert(!JSON.stringify(document).includes('private-')); assert(!Object.hasOwn(document, 'source_id'));
  assert.deepEqual(importPortableProject(document, source), request(2));
  assert.throws(() => importPortableProject(document, { ...source, fingerprint: { ...fingerprint, sha256: '0'.repeat(64) } }), /fingerprint/);
  assert.throws(() => importPortableProject({ ...document, session_cookie: 'private-cookie' }, source), /portable/i);
  for (const mutate of [body => { body.plan.session_cookie = 'private-cookie'; }, body => { body.plan.clips[0].label = { session_cookie: 'private-cookie' }; }]) {
    const unsafe = request(2); mutate(unsafe); assert.throws(() => exportPortableProject(unsafe, source), /edit fields/);
  }
  assert.throws(() => exportPortableProject(request(2), { ...source, title: { session_cookie: 'private-cookie' } }), error => error instanceof ApiError && error.code === 'invalid_response');
});

test('retained receipt limit refuses a new transfer without making the project unreadable', async t => {
  const root = join(await directory(t), 'project'), f = await fixture(t);
  const done = await runCollection(f.client, request(), root), original = await readFile(done.outputs[0].path);
  const statePath = join(root, 'state.json'), state = JSON.parse(await readFile(statePath, 'utf8'));
  // Intent receipts reserve bytes but have no prefix file. A bounded fixture
  // reaches the real restore/append boundary without thousands of media jobs.
  for (let i = 0; i < 4095; i++) {
    const id = 'retained-' + i;
    state.receipts[id] = { clip_id: 'clip0', job_id: 'retained-job-' + i, artifact_id: id, rendition: 'final',
      path: 'files/clip0.final.' + id + '.mp4', part: 'parts/' + id + '.00000000-0000-0000-0000-000000000000.part',
      size_bytes: 1, offset: 0, prefix_sha256: hash(Buffer.alloc(0)), phase: 'intent', segments: [], track_manifest: [] };
  }
  await writeFile(statePath, JSON.stringify(state));
  const edited = request(); edited.plan.clips[0].ranges[0].end_ms += 50;
  for (const options of [{ request: edited }, {}]) {
    await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, options), error => error.code === 'receipt_limit');
    const saved = JSON.parse(await readFile(statePath, 'utf8'));
    assert.equal(Object.keys(saved.receipts).length, 4096);
    assert.deepEqual(await readFile(done.outputs[0].path), original);
  }
  assert.equal(f.jobs.size, 2);
});

test('accepted job polling observes the minimum interval while batches are submitted', async t => {
  const root = join(await directory(t), 'project'), f = await fixture(t, { queuedPolls: 1 });
  assert((await runCollection(f.client, request(4), root, { timeoutMs: 15000 })).complete);
  for (const id of f.jobs.keys()) {
    const polls = f.calls.filter(call => call.url === '/api/v1/jobs/' + id); assert(polls.length >= 2);
    for (let i = 1; i < polls.length; i++) assert(polls[i].at - polls[i - 1].at >= 950);
  }
});

test('per-job Retry-After survives cancellation and fresh-client resume for 429 and 503', async t => {
  for (const status of [429, 503]) {
    const root = join(await directory(t), 'project'), f = await fixture(t, { pollRetryStatus: status }), controller = new AbortController();
    await assert.rejects(runCollection(f.client, request(), root, { signal: controller.signal, onProgress: report => { if (report.phase === 'poll_retry') controller.abort(); } }), error => error.code === 'cancelled');
    const done = await resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, { timeoutMs: 5000 }); assert(done.complete);
    const polls = f.calls.filter(call => call.url === '/api/v1/jobs/job-1'); assert.equal(polls.length, 2);
    const wait = polls[1].at - polls[0].at; assert(wait >= 1900, 'status ' + status + ' replayed after ' + wait + 'ms instead of waiting two seconds');
  }
});

test('a successful running-job poll keeps its minimum interval across a fresh client', async t => {
  const root = join(await directory(t), 'project'), f = await fixture(t, { queuedPolls: 1, queuedJob: 'job-1' }), controller = new AbortController();
  t.after(() => controller.abort());
  let saved;
  await assert.rejects(runCollection(f.client, request(2), root, { signal: controller.signal, timeoutMs: 10000, pollIntervalMs: 3000, onProgress: async report => {
    // The second job's durable output provides a barrier after the first job's
    // running response was saved, before another poll can replace that state.
    if (report.phase !== 'output_ready' || !report.pending.some(clip => clip.code === 'job_running')) return;
    const state = JSON.parse(await readFile(join(root, 'state.json')));
    saved = state.clips.clip0;
    assert.equal(saved.job.status, 'running'); assert(Number.isSafeInteger(saved.next_poll_at));
    assert(saved.next_poll_at > Date.now(), 'The saved poll deadline must still be in the future at interruption');
    controller.abort();
  } }), error => { assert.equal(error.code, 'cancelled'); return true; });
  assert(saved, 'The running-job checkpoint must be observed through the durable output callback');
  assert(saved.next_poll_at > Date.now(), 'Fresh-client resume must begin before the saved poll deadline');
  assert.equal(f.calls.filter(call => call.url === '/api/v1/jobs/job-1').length, 1);
  assert((await resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, { timeoutMs: 10000, pollIntervalMs: 3000 })).complete);
  const polls = f.calls.filter(call => call.url === '/api/v1/jobs/job-1'); assert.equal(polls.length, 2); assert(polls[1].at - polls[0].at >= 2950);
});

test('malformed saved per-job poll deadlines are refused before restoring the owner or networking', async t => {
  const root = join(await directory(t), 'project'), f = await fixture(t); await runCollection(f.client, request(), root);
  const original = JSON.parse(await readFile(join(root, 'state.json'))), before = f.calls.length;
  for (const malformed of [null, -1, 'soon', 1.5, {}]) {
    const state = structuredClone(original); state.clips.clip0.next_poll_at = malformed;
    await writeFile(join(root, 'state.json'), JSON.stringify(state), { mode: 0o600 });
    await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root), error => error.code === 'invalid_state'); assert.equal(f.calls.length, before);
  }
});

test('admission batch bounds accepted unfinished work while queued clips drain', async t => {
  const root = join(await directory(t), 'project'), f = await fixture(t, { queuedPolls: 2 }); let outstanding = 0;
  const done = await runCollection(f.client, request(5), root, { admissionBatchSize: 2, maxSubmitBatch: 3, timeoutMs: 15000, onProgress: report => {
    outstanding = Math.max(outstanding, report.pending.filter(clip => clip.code.startsWith('job_')).length);
  } });
  assert(done.complete); assert.equal(done.outputs.length, 5); assert.equal(outstanding, 2);
  assert(f.calls.filter(call => call.url === '/api/v1/collections').every(call => call.body.clip_ids.length <= 2));
});

test('project output budget includes prior revisions and reserved unfinished transfers', async t => {
  const root = join(await directory(t), 'project'), f = await fixture(t), first = request();
  const initial = await runCollection(f.client, first, root, { maxOutputBytes: 200 });
  const revised = structuredClone(first); revised.plan.clips[0].ranges[0].end_ms = 600;
  const second = await resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, { request: revised, maxOutputBytes: 200 });
  const changed = structuredClone(revised); changed.plan.clips[0].ranges[0].end_ms = 700;
  await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, { request: changed, maxOutputBytes: 200 }), error => error.code === 'output_limit');
  assert.equal((await readdir(join(root, 'files'))).length, 2);
  for (const output of [...initial.outputs, ...second.outputs]) assert.equal(hash(await readFile(output.path)), output.sha256);
  const other = join(await directory(t), 'project'), large = await fixture(t, { large: true, interrupt: true }), controller = new AbortController();
  await assert.rejects(runCollection(large.client, request(), other, { signal: controller.signal, onProgress: report => { if (report.phase === 'download_interrupted') controller.abort(); } }), error => error.code === 'cancelled');
  const state = JSON.parse(await readFile(join(other, 'state.json'))), part = Object.values(state.receipts)[0], prefix = await readFile(join(other, part.part));
  await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: large.baseUrl }), other, { maxOutputBytes: 170000 }), error => error.code === 'output_limit');
  assert.deepEqual(await readFile(join(other, part.part)), prefix); assert.equal(large.jobs.size, 1);
});

test('shape-valid unauthorized and unexpected-status collection responses stay ordinary API errors', async t => {
  for (const status of [401, 403, 418]) {
    const f = await fixture(t, { submitStatus: status });
    await assert.rejects(f.client.submitCollection(request(), { idempotencyKey: 'saved-key' }), error => error instanceof ApiError && error.status === status && error.code === 'http_error');
    assert.equal(f.calls.filter(call => call.url === '/api/v1/collections').length, 1);
  }
});

test('durable batches drain accepted clips during capacity wait and resume revisions and draft selections', async t => {
  const parent = await directory(t), root = join(parent, 'project');
  const f = await fixture(t, { capacity: true }), phases = [];
  const completed = await runCollection(f.client, request(5), root, { timeoutMs: 12000, onProgress: report => { phases.push(report.phase); report.outputs.length = 0; } });
  assert.equal(completed.outputs.length, 5); assert.equal(f.jobs.size, 5); assert(phases.indexOf('output_ready') < phases.lastIndexOf('submitted'));
  const posts = f.calls.filter(call => call.url === '/api/v1/collections'); assert(posts.every(call => call.body.clip_ids.length <= 3));
  assert(posts[1].at - posts[0].at >= 950); assert.equal(new Set(posts.map(call => call.key)).size, 1);
  if (process.platform !== 'win32') { assert.equal((await stat(root)).mode & 0o077, 0); assert.equal((await stat(join(root, 'owner.json'))).mode & 0o077, 0); }
  assert(!JSON.stringify(completed).includes(cookie)); assert(!(await readFile(join(root, 'state.json'), 'utf8')).includes(cookie));
  const unchanged = await resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, { timeoutMs: 3000 }); assert.deepEqual(unchanged.outputs, completed.outputs);
  const revised = request(5); revised.plan.clips[0].ranges[0].end_ms = 600; revised.plan.clips[3].ranges[0].end_ms = 3800;
  const updated = await resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, { request: revised, timeoutMs: 5000 }); assert.equal(f.jobs.size, 7);
  assert.equal(updated.outputs[1].job_id, completed.outputs[1].job_id); assert.notEqual(updated.outputs[0].job_id, completed.outputs[0].job_id);
  const drafts = await resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, { request: { ...revised, clip_ids: ['clip0', 'clip3'], rendition: 'draft' }, timeoutMs: 5000 });
  assert.equal(f.jobs.size, 9); assert.deepEqual(drafts.outputs.map(output => output.clip_id), ['clip0', 'clip3']); assert.equal((await readdir(join(root, 'files'))).length, 9);
  for (const output of [...completed.outputs, ...updated.outputs, ...drafts.outputs]) assert.equal(hash(await readFile(output.path)), output.sha256);
});

test('lost committed submission reply persists its exact intent and fresh owner resume reuses accepted jobs', async t => {
  const root = join(await directory(t), 'project'), f = await fixture(t, { loseReply: true });
  const controller = new AbortController();
  await assert.rejects(runCollection(f.client, request(3), root, { signal: controller.signal, onProgress: report => { if (report.phase === 'submission_uncertain') controller.abort(); } }), error => error instanceof CollectionError && error.code === 'cancelled');
  const saved = JSON.parse(await readFile(join(root, 'state.json'))); assert(saved.intent); assert.equal(f.jobs.size, 3);
  const resumed = await resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, { timeoutMs: 5000 }); assert.equal(resumed.outputs.length, 3); assert.equal(f.jobs.size, 3);
  const posts = f.calls.filter(call => call.url === '/api/v1/collections'); assert.deepEqual(posts[0].body, posts[1].body); assert.equal(posts[0].key, posts[1].key);
});

test('interrupted body checkpoints before callback and a fresh process resumes a verified Range prefix after crash extras', async t => {
  const root = join(await directory(t), 'project'), f = await fixture(t, { large: true, interrupt: true });
  const controller = new AbortController(); let offset;
  await assert.rejects(runCollection(f.client, request(), root, { signal: controller.signal, onProgress: async report => {
    if (report.phase === 'download_interrupted') { offset = report.download.offset; const state = JSON.parse(await readFile(join(root, 'state.json'))); assert.equal(state.receipts[report.download.artifact_id].offset, offset); controller.abort(); }
  } }), error => error.code === 'cancelled');
  assert(offset > 0 && offset < 180000);
  const state = JSON.parse(await readFile(join(root, 'state.json'))), receipt = Object.values(state.receipts)[0]; await appendFile(join(root, receipt.part), 'uncheckpointed bytes');
  const done = await resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, { timeoutMs: 5000 }); assert.equal(done.outputs.length, 1);
  const range = f.calls.find(call => call.range); assert.equal(range.range, 'bytes=' + offset + '-'); assert.equal(range.ifRange, 'Wed, 14 Oct 2026 21:23:39 GMT');
  assert.equal(hash(await readFile(done.outputs[0].path)), hash(f.bytes.get(done.outputs[0].artifact_id))); assert.equal((await readdir(join(root, 'parts'))).length, 0);
});

test('ignored Range restarts owned bytes and malformed 206 refuses without publishing', async t => {
  for (const badRange of [false, true]) {
    const root = join(await directory(t), 'project'), f = await fixture(t, { large: true, interrupt: true, ignoreRange: !badRange, badRange });
    const controller = new AbortController();
    await assert.rejects(runCollection(f.client, request(), root, { signal: controller.signal, onProgress: report => { if (report.phase === 'download_interrupted') controller.abort(); } }), error => error.code === 'cancelled');
    if (badRange) { await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root), error => error.code === 'invalid_range' && error.report.outputs.length === 0); assert.equal((await readdir(join(root, 'files'))).length, 0); }
    else { const done = await resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, { timeoutMs: 5000 }); assert.equal(hash(await readFile(done.outputs[0].path)), done.outputs[0].sha256); }
  }
});

test('Range requests require identity encoding and reject a proxy gzip response before appending saved bytes', async t => {
  const root = join(await directory(t), 'project'), f = await fixture(t, { large: true, interrupt: true });
  const controller = new AbortController();
  await assert.rejects(runCollection(f.client, request(), root, { signal: controller.signal, onProgress: report => { if (report.phase === 'download_interrupted') controller.abort(); } }), error => error.code === 'cancelled');
  const state = JSON.parse(await readFile(join(root, 'state.json'))), receipt = Object.values(state.receipts)[0], before = await readFile(join(root, receipt.part));
  f.config.encoded = true;
  await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root), error => error.code === 'content_encoding_unsupported');
  const encodings = f.calls.filter(call => call.url.startsWith('/api/v1/artifacts')).at(-1).encoding.split(',').map(value => value.trim());
  // Node Fetch adds its own identity token for Range; repeated identity is valid.
  assert(encodings.length > 0 && encodings.every(value => value === 'identity'));
  assert.deepEqual(await readFile(join(root, receipt.part)), before); assert.equal((await readdir(join(root, 'files'))).length, 0);
});

test('owner/base conflicts, modified files and symlink projects refuse before collection network or overwrite', async t => {
  const parent = await directory(t), root = join(parent, 'project'), f = await fixture(t);
  const done = await runCollection(f.client, request(), root); const before = f.calls.length;
  await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: 'http://127.0.0.1:1' }), root), error => error.code === 'server_conflict');
  await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl, sessionCookie: 'b'.repeat(43) }), root), error => error.code === 'owner_conflict');
  assert.equal(f.calls.length, before);
  await writeFile(done.outputs[0].path, Buffer.alloc(100, 42), { mode: 0o600 });
  await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root), error => error.code === 'file_tampered');
  assert.deepEqual(await readFile(done.outputs[0].path), Buffer.alloc(100, 42));
  if (process.platform !== 'win32') { const alias = join(parent, 'alias'); await symlink(root, alias); await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), alias), error => error.code === 'unsafe_directory'); }
});

test('initialization journal completes both missing publications with the original owner/key and rejects absent proof', async t => {
  for (const missingOwner of [false, true]) {
    const root = join(await directory(t), 'project'), f = await fixture(t);
    await assert.rejects(runCollection(f.client, request(), root, { onProgress: report => { if (report.phase === 'resumed') throw new Error('stop before admission'); } }), error => error.code === 'callback_error');
    const state = JSON.parse(await readFile(join(root, 'state.json'))), owner = JSON.parse(await readFile(join(root, 'owner.json')));
    assert.equal(f.jobs.size, 0); assert.equal((await readdir(root)).includes('initialize.json'), false);
    await writeFile(join(root, 'initialize.json'), JSON.stringify({ schema_version: 1, sdk: 'typescript', state, owner }), { mode: 0o600 }); await unlink(join(root, 'state.json')); if (missingOwner) await unlink(join(root, 'owner.json'));
    const done = await resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, { timeoutMs: 5000 }); assert(done.complete);
    assert.equal(f.calls.find(call => call.url === '/api/v1/collections').key, state.idempotency_key); assert.equal((await readdir(root)).includes('initialize.json'), false);
    await unlink(join(root, 'state.json')); const count = f.calls.length;
    await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root), error => error instanceof CollectionError); assert.equal(f.calls.length, count);
  }
});

test('bootstrap verifies both existing primary files and active owner before repairing either missing file', async t => {
  for (const missing of ['owner.json', 'state.json']) {
    const root = join(await directory(t), 'project'), f = await fixture(t);
    await assert.rejects(runCollection(f.client, request(), root, { onProgress: report => { if (report.phase === 'resumed') throw new Error('stop'); } }), error => error.code === 'callback_error');
    const state = JSON.parse(await readFile(join(root, 'state.json'))), owner = JSON.parse(await readFile(join(root, 'owner.json')));
    await writeFile(join(root, 'initialize.json'), JSON.stringify({ schema_version: 1, sdk: 'typescript', state, owner }), { mode: 0o600 }); await unlink(join(root, missing));
    const existing = missing === 'owner.json' ? 'state.json' : 'owner.json', altered = existing === 'state.json' ? { ...state, idempotency_key: 'changed-key' } : { ...owner, session_cookie: 'b'.repeat(43) };
    await writeFile(join(root, existing), JSON.stringify(altered), { mode: 0o600 }); const before = f.calls.length;
    await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root), error => error.code === 'initialization_conflict');
    await assert.rejects(stat(join(root, missing)), error => error.code === 'ENOENT'); assert.equal(f.calls.length, before); assert.equal((await readdir(root)).includes('initialize.json'), true);
    await writeFile(join(root, existing), JSON.stringify(existing === 'state.json' ? state : owner), { mode: 0o600 });
    await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl, sessionCookie: 'b'.repeat(43) }), root), error => error.code === 'owner_conflict');
    await assert.rejects(stat(join(root, missing)), error => error.code === 'ENOENT'); assert.equal(f.calls.length, before);
  }
});

test('hardlinked mutable part is refused before truncation or writes and its external inode is preserved', async t => {
  const parent = await directory(t), root = join(parent, 'project'), f = await fixture(t, { large: true, interrupt: true }), controller = new AbortController();
  await assert.rejects(runCollection(f.client, request(), root, { signal: controller.signal, onProgress: report => { if (report.phase === 'download_interrupted') controller.abort(); } }), error => error.code === 'cancelled');
  const state = JSON.parse(await readFile(join(root, 'state.json'))), receipt = Object.values(state.receipts)[0], part = join(root, receipt.part), original = await readFile(part), external = join(parent, 'unowned-file');
  const bytes = Buffer.concat([original, Buffer.from('unowned suffix must survive')]); await writeFile(external, bytes, { mode: 0o600 }); await unlink(part); await link(external, part);
  await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root), error => error.code === 'unsafe_file');
  assert.deepEqual(await readFile(external), bytes); assert.equal((await readdir(join(root, 'files'))).length, 0); assert.equal(f.jobs.size, 1);
});

test('writer fence refuses simultaneous access and SIGKILL releases its kernel lock for fresh resume', async t => {
  const root = join(await directory(t), 'project'), f = await fixture(t);
  const child = spawn(process.execPath, ['--input-type=module', '-e', `const { CutmyClient } = await import(process.argv[1]); const { runCollection } = await import(process.argv[2]); await runCollection(new CutmyClient({baseUrl:process.argv[3]}), JSON.parse(process.argv[4]), process.argv[5], {onProgress: async r => { if (r.phase === 'output_ready' && r.outputs.length === 2) { console.log('checkpoint'); await new Promise(() => setInterval(() => {},1000)); } }});`, mainEntry, collectionsEntry, f.baseUrl, JSON.stringify(request(3)), root], { stdio: ['ignore', 'pipe', 'pipe'] });
  t.after(() => { if (child.exitCode === null) child.kill('SIGKILL'); });
  await new Promise((yes, no) => { const timer = setTimeout(() => no(new Error('Child checkpoint timed out')), 7000); child.stdout.on('data', data => { if (data.toString().includes('checkpoint')) { clearTimeout(timer); yes(); } }); child.once('exit', code => { clearTimeout(timer); no(new Error('Child exited before checkpoint: ' + code)); }); });
  await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root), error => error.code === 'project_busy');
  await new Promise(resolve => { child.once('exit', resolve); child.kill('SIGKILL'); });
  const done = await resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, { timeoutMs: 5000 }); assert.equal(done.outputs.length, 3); assert.equal(f.jobs.size, 3);
});

test('terminal and deadline errors retain verified earlier outputs without cancelling remote work', async t => {
  const root = join(await directory(t), 'project'), f = await fixture(t, { failedClip: 'clip1' });
  await assert.rejects(runCollection(f.client, request(2), root), error => error.code === 'failed' && error.report.outputs.length === 1 && error.report.pending[0].retryable === false);
  const other = join(await directory(t), 'project'), next = await fixture(t);
  await assert.rejects(runCollection(next.client, request(), other, { timeoutMs: 150, onProgress: report => report.phase === 'resumed' ? new Promise(() => {}) : undefined }), error => error.code === 'timeout' && !error.report.complete);
  assert(![...f.calls, ...next.calls].some(call => call.url.includes('/cancel')));
});

test('prepared exclusive publication recovers its exact hash receipt and refuses unrelated existing output', async t => {
  const root = join(await directory(t), 'project'), f = await fixture(t);
  const done = await runCollection(f.client, request(), root), output = done.outputs[0];
  const state = JSON.parse(await readFile(join(root, 'state.json'))), receipt = state.receipts[output.artifact_id];
  receipt.phase = 'prepared'; await link(output.path, join(root, receipt.part));
  await writeFile(join(root, 'state.json'), JSON.stringify(state), { mode: 0o600 });
  const count = f.calls.filter(call => call.url.startsWith('/api/v1/artifacts')).length;
  const resumed = await resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root); assert.deepEqual(resumed.outputs, done.outputs);
  assert.equal((await stat(output.path)).nlink, 1); assert.equal((await readdir(join(root, 'parts'))).length, 0);
  assert.equal(f.calls.filter(call => call.url.startsWith('/api/v1/artifacts')).length, count);
  receipt.phase = 'intent'; receipt.offset = 0; receipt.prefix_sha256 = hash(Buffer.alloc(0)); delete receipt.sha256;
  await writeFile(join(root, 'state.json'), JSON.stringify(state), { mode: 0o600 });
  await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root), error => error.code === 'output_exists');
  assert.equal(hash(await readFile(output.path)), output.sha256);
});

test('saved receipt path, partial-byte tampering, oversized state and symlink owner fail safely', async t => {
  const root = join(await directory(t), 'project'), f = await fixture(t, { large: true, interrupt: true });
  const controller = new AbortController();
  await assert.rejects(runCollection(f.client, request(), root, { signal: controller.signal, onProgress: report => { if (report.phase === 'download_interrupted') controller.abort(); } }), error => error.code === 'cancelled');
  const saved = JSON.parse(await readFile(join(root, 'state.json'))), receipt = Object.values(saved.receipts)[0], originalPart = await readFile(join(root, receipt.part));
  const tampered = Buffer.from(originalPart); tampered[0] ^= 255; await writeFile(join(root, receipt.part), tampered, { mode: 0o600 });
  await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root), error => error.code === 'part_tampered');
  assert.equal((await readdir(join(root, 'files'))).length, 0); await writeFile(join(root, receipt.part), originalPart, { mode: 0o600 });
  const before = f.calls.length, bad = structuredClone(saved); Object.values(bad.receipts)[0].path = 'files/../../outside.mp4';
  await writeFile(join(root, 'state.json'), JSON.stringify(bad), { mode: 0o600 });
  await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root), error => error.code === 'invalid_state'); assert.equal(f.calls.length, before);
  await writeFile(join(root, 'state.json'), 'x'.repeat((4 << 20) + 1), { mode: 0o600 });
  await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root), error => error.code === 'state_too_large'); assert.equal(f.calls.length, before);
  await writeFile(join(root, 'state.json'), JSON.stringify(saved), { mode: 0o600 });
  if (process.platform !== 'win32') {
    const outside = join(await directory(t), 'secret.json'); await writeFile(outside, '{}', { mode: 0o600 }); await unlink(join(root, 'owner.json')); await symlink(outside, join(root, 'owner.json'));
    await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root), error => error instanceof CollectionError); assert.equal(await readFile(outside, 'utf8'), '{}'); assert.equal(f.calls.length, before);
  }
});

test('main entry import closure contains no Node built-ins and low-level malformed collection reports are refused', async t => {
  const visited = new Set();
  async function visit(href) {
    if (visited.has(href)) return; visited.add(href); const source = await readFile(new URL(href), 'utf8'); assert(!/from\s+["']node:|import\s*\(["']node:/.test(source));
    for (const match of source.matchAll(/(?:from|import)\s+["'](\.\/[^"']+)["']/g)) await visit(new URL(match[1], href).href);
  }
  await visit(mainEntry);
  const client = new CutmyClient({ baseUrl: 'http://127.0.0.1:1', sessionCookie: cookie, fetch: async url => {
    if (new URL(url).pathname.endsWith('/session')) return new Response(JSON.stringify({ ok: true, limits }), { headers: { 'content-type': 'application/json' } });
    return new Response(JSON.stringify({ version: 1, complete: true, rendition: 'final', clips: [{ clip_id: 'clip0', request: {}, job: { id: 'job-1' }, retryable: false }] }), { status: 202 });
  } });
  await assert.rejects(client.submitCollection(request(), { idempotencyKey: 'saved-key' }), error => error instanceof ApiError && error.code === 'invalid_response');
});

test('project adoption uses exact current requests, preserves thirteen files and respects a two-clip selection with historical jobs', async t => {
  const root = join(await directory(t), 'project'), f = await fixture(t), body = request(15), key = 'private-workflow-key';
  const first = await runCollection(f.client, body, root, { idempotencyKey: key, timeoutMs: 30000 });
  const jobs = new Map(first.outputs.map(item => [item.clip_id, f.jobs.get(item.job_id)]));
  const changed = request(15); for (const clip of changed.plan.clips.slice(0, 2)) clip.ranges[0].end_ms += 20;
  const submission = await f.client.submitCollection({ ...changed, clip_ids: ['clip0','clip1'] }, { idempotencyKey: key });
  for (const item of submission.clips) jobs.set(item.clip_id, item.job);
  function dto(value) {
    const selected = new Set(chosen(value).map(clip => clip.id));
    return { version: 1, id: 'project-1', revision: 2, ...value, rendition: 'final', source: { id: value.source_id, duration_ms: 100000, title: 'Interview' },
      clips: chosen(value).map(clip => ({ clip_id: clip.id, request: effective(value, clip) })),
      jobs: value.plan.clips.map(clip => ({ clip_id: clip.id, request: effective(value, clip), job: jobs.get(clip.id), current: selected.has(clip.id) })),
    };
  }
  f.config.project = dto(changed);
  const before = f.calls.filter(call => call.method === 'POST').length;
  const result = await resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, { projectId: 'project-1', timeoutMs: 30000 });
  assert.equal(result.outputs.length, 15); assert.equal(f.jobs.size, 17); assert.equal(f.calls.filter(call => call.method === 'POST').length, before);
  assert.deepEqual(result.outputs.filter(item => !['clip0','clip1'].includes(item.clip_id)), first.outputs.filter(item => !['clip0','clip1'].includes(item.clip_id)));
  const selected = { ...changed, clip_ids: ['clip0','clip1'] }; f.config.project = dto(selected);
  const subset = await resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, { projectId: 'project-1', timeoutMs: 10000 });
  assert.deepEqual(subset.outputs.map(item => item.clip_id), ['clip0','clip1']);
  f.config.project.jobs[0].request = { ...f.config.project.jobs[0].request, ranges: [{ start_ms: 7000, end_ms: 8000 }] };
  await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, { projectId: 'project-1' }), error => error.code === 'project_conflict');
  assert.equal(f.calls.filter(call => call.method === 'POST').length, before);
});

test('retryFailed retains fourteen verified outputs and reconciles an uncertain new attempt key', async t => {
  const root = join(await directory(t), 'project'), f = await fixture(t, { failedClip: 'clip14', workerDiagnostic: true });
  let initial;
  await assert.rejects(runCollection(f.client, request(15), root, { timeoutMs: 30000 }), error => {
    initial = error.report;
    const pending = error.report.pending.find(item => item.clip_id === 'clip14');
    assert.equal(pending.error_code, 'job_timeout'); assert.equal(pending.message, 'Processing deadline exceeded');
    assert.equal(pending.progress_ms, 250); assert.equal(pending.stage, 'processing'); assert(f.jobs.has(pending.job_id));
    assert(!JSON.stringify(error.report).includes('private-unknown-worker'));
    return error instanceof CollectionError && error.code === 'failed';
  });
  assert.equal(initial.outputs.length, 14);
  f.config.failedClip = undefined; f.config.loseReply = true;
  await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, {
    retryFailed: ['clip14'], timeoutMs: 30000,
    onProgress: report => { if (report.phase === 'submission_uncertain') throw new Error('Stop after lost retry reply'); },
  }), error => error instanceof CollectionError && error.code === 'callback_error');
  const state = JSON.parse(await readFile(join(root, 'state.json')));
  assert.equal(state.intent_key, state.attempt_keys.clip14);
  const result = await resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, { timeoutMs: 30000 });
  assert.equal(result.complete, true); assert.equal(f.jobs.size, 16);
  assert.deepEqual(result.outputs.filter(item => item.clip_id !== 'clip14'), initial.outputs);
  const retryPosts = f.calls.filter(call => call.method === 'POST' && call.key === state.attempt_keys.clip14);
  assert.equal(retryPosts.length, 2); assert.deepEqual(retryPosts[0].body, retryPosts[1].body);
  assert.deepEqual(retryPosts[0].body.clip_ids, ['clip14']);
  const changed = request(15); changed.plan.clips[0].ranges[0].end_ms += 5;
  const revised = await resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, { request: changed, timeoutMs: 30000 });
  assert.equal(f.jobs.size, 17);
  assert.deepEqual(revised.outputs.find(item => item.clip_id === 'clip14'), result.outputs.find(item => item.clip_id === 'clip14'));
  const before = f.calls.filter(call => call.method === 'POST' && call.url === '/api/v1/collections').length;
  await assert.rejects(resumeCollection(new CutmyClient({ baseUrl: f.baseUrl }), root, { retryFailed: ['clip14'] }), error => error.code === 'retry_not_failed');
  assert.equal(f.calls.filter(call => call.method === 'POST' && call.url === '/api/v1/collections').length, before);
});

test('local collection wrappers pass literal selection/rendition and retain partial collection outputs', async t => {
  const cwd = await directory(t), cli = fileURLToPath(new URL('./fake-cli.mjs', import.meta.url));
  const engine = new LocalCutmy({ binary: process.execPath, binaryArgs: [cli], cwd });
  const plan = { ...request().plan, input: 'interview $(literal).mp4', clips: [{ id: 'answer', ranges: [{ start_ms: 0, end_ms: 1000 }] }] };
  const validated = await engine.validateCollection(plan, 'out', { clipIds: ['answer'], rendition: 'draft', maxOutputBytes: 10000 });
  assert.deepEqual(validated.selected_clip_ids, ['answer']); assert.equal(validated.rendition, 'draft'); assert.equal(validated.input, join(cwd, plan.input));
  assert.equal((await engine.executeCollection(plan, 'out', { rendition: 'draft' })).outputs[0].id, 'answer');
  const failing = new LocalCutmy({ binary: process.execPath, binaryArgs: [cli], env: { FAKE_MODE: 'fail' } });
  await assert.rejects(failing.executeCollection(plan, 'out'), error => error instanceof LocalError && error.code === 'future_local_error' && error.result.outputs[0].id === 'answer' && error.result.incomplete);
  await assert.rejects(engine.executeCollection(plan, 'out', { clipIds: ['answer', 'answer'] }), /unique/);
  await assert.rejects(engine.executeCollection(plan, 'out', { maxOutputBytes: 0 }), /maxOutputBytes/);
});
