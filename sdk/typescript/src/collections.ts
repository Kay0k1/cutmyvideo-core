/** Node-only durable collection cycle. The browser entry never imports this module. */
import { createHash, randomUUID } from "node:crypto";
import { renderReferences } from "./media.js";
import { link, lstat, unlink } from "node:fs/promises";
import { join } from "node:path";
import type { CutmyClient } from "./client.js";
import { ApiError, CollectionError, readyArtifacts } from "./errors.js";
import { abortable, abortedError, budget, decode, delay, JSON_LIMIT, object, positive } from "./protocol.js";
import { atomicJSON, canonical, childPath, clipID, EMPTY_HASH, existsRegular, fail, hashFile, openPart, privateDirectory, projectDirectory, readJSON, requireValue, safeID, sha256, syncDirectory, writerLock } from "./collection_store.js";
import type { Artifact, CollectionFile, CollectionReport, CollectionRequest, CollectionSubmission, Job, NormalizedRequest, RequestOptions, Segment, TrackSelection } from "./types.js";

export interface CollectionRunOptions extends RequestOptions {
  idempotencyKey?: string;
  pollIntervalMs?: number;
  maxOutputBytes?: number;
  maxSubmitBatch?: number;
  /** Maximum accepted clips still waiting for local publication. */
  admissionBatchSize?: number;
  onProgress?: (report: CollectionReport) => void | Promise<void>;
}
export interface CollectionResumeOptions extends Omit<CollectionRunOptions, "idempotencyKey"> { request?: CollectionRequest; retryFailed?: string[]; projectId?: string }
interface Entry { signature: string; job?: Job; effective?: NormalizedRequest; next_poll_at?: number; error?: { code: string; retryable: boolean; field?: string; message?: string; diagnostic_clip_id?: string } }
interface Receipt {
  clip_id: string; job_id: string; artifact_id: string; rendition: "final" | "draft";
  path: string; part: string; size_bytes: number; offset: number; prefix_sha256: string;
  phase: "intent" | "part" | "prepared" | "complete"; sha256?: string; last_modified?: string;
  segments: Segment[]; track_manifest: TrackSelection[];
}
interface State {
  schema_version: 1; sdk: "typescript"; mode: "http"; server_url: string; source_id: string;
  owner_sha256: string; idempotency_key: string; request: CollectionRequest;
  clips: Record<string, Entry>; receipts: Record<string, Receipt>;
  validated?: string; intent?: CollectionRequest; next_submit_at?: number;
  attempt_keys?: Record<string, string>; intent_key?: string;
  retry_requested?: string[]; retry_active?: string[];
  project_id?: string; project_revision?: number; project_retry_keys?: Record<string, string>;
  intent_project?: { id: string; revision: number; retry: boolean };
}
const digestPattern = /^[a-f0-9]{64}$/;
const ownMap = <T>(): Record<string, T> => Object.create(null) as Record<string, T>;
const CHECKPOINT = 64 << 10;
const validKey = (key: unknown): key is string => typeof key === "string" && !!key.trim() && Buffer.byteLength(key) <= 128 && !/[\x00-\x1f\x7f]/.test(key);
const attemptKey = (state: State, id: string): string => state.attempt_keys && Object.hasOwn(state.attempt_keys, id) ? state.attempt_keys[id]! : state.idempotency_key;
function retryIDs(state: State, values: unknown): string[] {
  requireValue(Array.isArray(values) && values.length > 0 && values.every(id => typeof id === "string" && selected(state.request).includes(id)) && new Set(values).size === values.length, "invalid_retry_selection");
  return values;
}

function copiedRequest(value: CollectionRequest): CollectionRequest {
  let data: string;
  try { renderReferences(value); data = JSON.stringify(value); } catch { fail("invalid_request"); }
  requireValue(Buffer.byteLength(data) <= JSON_LIMIT, "request_too_large");
  const request = JSON.parse(data) as CollectionRequest;
  requireValue(object(request) && safeID(request.source_id) && object(request.plan) && request.plan.version === 1 && (!request.plan.input || request.plan.input === "") && Array.isArray(request.plan.clips) && request.plan.clips.length > 0 && request.plan.clips.length <= 128, "invalid_request");
  const ids = request.plan.clips.map(clip => { requireValue(object(clip) && clipID(clip.id) && Array.isArray(clip.ranges), "invalid_request"); return clip.id; });
  requireValue(new Set(ids).size === ids.length, "invalid_request");
  requireValue(request.rendition === undefined || request.rendition === "draft" || request.rendition === "final", "invalid_request");
  if (request.clip_ids !== undefined) requireValue(Array.isArray(request.clip_ids) && new Set(request.clip_ids).size === request.clip_ids.length && request.clip_ids.every(id => ids.includes(id)), "invalid_request");
  return request;
}
function selected(request: CollectionRequest): string[] {
  const ids = new Set(request.clip_ids ?? []);
  return request.plan.clips.filter(clip => ids.size === 0 || ids.has(clip.id)).map(clip => clip.id);
}
function signature(request: CollectionRequest, id: string): string {
  return sha256(canonical({ source_id: request.source_id, rendition: request.rendition ?? "final", options: request.plan.options ?? {}, clip: request.plan.clips.find(clip => clip.id === id) }));
}
function applyRequest(state: State, request: CollectionRequest): void {
  requireValue(request.source_id === state.source_id, "source_conflict");
  state.request = request;
  const entries = ownMap<Entry>();
  for (const id of selected(request)) {
    const sig = signature(request, id), old = state.clips[id];
    entries[id] = old?.signature === sig ? old : { signature: sig };
  }
  state.clips = entries; delete state.validated;
}
function report(state: State, root: string): CollectionReport {
  const outputs: CollectionFile[] = [], pending: CollectionReport["pending"] = [];
  for (const id of selected(state.request)) {
    const entry = state.clips[id]!;
    const artifacts = entry.job ? readyArtifacts(entry.job) : [];
    const receipt = artifacts.map(artifact => state.receipts[artifact.id]).find(value => value?.phase === "complete");
    if (receipt?.sha256) outputs.push({ clip_id: id, job_id: receipt.job_id, artifact_id: receipt.artifact_id, path: childPath(root, receipt.path), size_bytes: receipt.size_bytes, sha256: receipt.sha256, segments: receipt.segments, track_manifest: receipt.track_manifest });
    else {
      const job = entry.job, item = job?.items.find(item => item.status === "failed" || item.status === "cancelled") ?? job?.items[0];
      const stage = job && /^[A-Za-z0-9_-]{1,128}$/.test(job.stage) ? job.stage : job?.status ?? "admission";
      const errorCode = item?.error_code && /^[a-z0-9_]{1,128}$/.test(item.error_code) ? item.error_code : undefined;
      const message = item?.message && Buffer.byteLength(item.message) <= 4096 ? item.message : entry.error?.message;
      pending.push({ clip_id: id, code: entry.error?.code ?? (job ? "job_" + job.status : "admission_pending"), retryable: entry.error?.retryable ?? true,
        stage, ...(job ? { job_id: job.id, job_status: job.status } : {}),
        ...(item && Number.isSafeInteger(item.progress_ms) && item.progress_ms! >= 0 ? { progress_ms: item.progress_ms } : {}),
        ...(errorCode ? { error_code: errorCode } : {}), ...(message ? { message } : {}),
        ...(entry.error?.field ? { field: entry.error.field } : {}), ...(entry.error?.diagnostic_clip_id ? { diagnostic_clip_id: entry.error.diagnostic_clip_id } : {}) });
    }
  }
  return { complete: pending.length === 0, source_id: state.source_id, rendition: state.request.rendition ?? "final", outputs, pending };
}
function readState(value: unknown): State {
  requireValue(object(value) && value.schema_version === 1 && value.sdk === "typescript" && value.mode === "http" && typeof value.server_url === "string" && safeID(value.source_id) && typeof value.owner_sha256 === "string" && digestPattern.test(value.owner_sha256) && typeof value.idempotency_key === "string" && value.idempotency_key.length > 0 && object(value.clips) && object(value.receipts), "invalid_state");
  const state = value as unknown as State;
  state.request = copiedRequest(state.request);
  requireValue(state.request.source_id === state.source_id && new TextEncoder().encode(state.idempotency_key).length <= 128 && !/[\x00-\x1f\x7f]/.test(state.idempotency_key), "invalid_state");
  state.clips = Object.assign(ownMap<Entry>(), state.clips);
  state.receipts = Object.assign(ownMap<Receipt>(), state.receipts);
  requireValue(Object.keys(state.clips).length === selected(state.request).length && Object.keys(state.receipts).length <= 4096, "invalid_state");
  for (const id of selected(state.request)) {
    const entry = state.clips[id];
    requireValue(object(entry) && entry.signature === signature(state.request, id), "invalid_state");
    if (entry.job) { decode(entry.job, "job"); requireValue(safeID(entry.job.id) && object(entry.effective), "invalid_state"); }
    if (entry.next_poll_at !== undefined) requireValue(Number.isSafeInteger(entry.next_poll_at) && entry.next_poll_at >= 0, "invalid_state");
    if (entry.error) requireValue(object(entry.error) && typeof entry.error.code === "string" && typeof entry.error.retryable === "boolean" && ["field","message","diagnostic_clip_id"].every(key => entry.error![key as "field"] === undefined || typeof entry.error![key as "field"] === "string"), "invalid_state");
  }
  for (const [id, receipt] of Object.entries(state.receipts)) {
    requireValue(object(receipt) && id === receipt.artifact_id && safeID(id) && clipID(receipt.clip_id) && safeID(receipt.job_id) && ["final","draft"].includes(receipt.rendition) && typeof receipt.path === "string" && typeof receipt.part === "string" && Number.isSafeInteger(receipt.size_bytes) && receipt.size_bytes > 0 && Number.isSafeInteger(receipt.offset) && receipt.offset >= 0 && receipt.offset <= receipt.size_bytes && digestPattern.test(receipt.prefix_sha256) && ["intent","part","prepared","complete"].includes(receipt.phase) && Array.isArray(receipt.segments) && Array.isArray(receipt.track_manifest), "invalid_state");
    const prefix = "files/" + receipt.clip_id + "." + receipt.rendition + "." + id + ".";
    requireValue(receipt.path.startsWith(prefix) && ["mp4","mp3","mkv"].includes(receipt.path.slice(prefix.length)) && /^parts\/[A-Za-z0-9_-]+\.[a-f0-9-]{36}\.part$/.test(receipt.part), "invalid_state");
    if (receipt.phase === "prepared" || receipt.phase === "complete") requireValue(receipt.offset === receipt.size_bytes && typeof receipt.sha256 === "string" && digestPattern.test(receipt.sha256), "invalid_state");
    if (receipt.last_modified !== undefined) requireValue(typeof receipt.last_modified === "string" && receipt.last_modified.length <= 128 && !/[\x00-\x1f\x7f]/.test(receipt.last_modified), "invalid_state");
  }
  if (state.intent) {
    state.intent = copiedRequest(state.intent);
    requireValue(state.intent.source_id === state.source_id && selected(state.intent).every(id => state.clips[id]?.signature === signature(state.intent!, id)), "invalid_state");
    requireValue(selected(state.intent).every(id => attemptKey(state, id) === (state.intent_key ?? state.idempotency_key)), "invalid_state");
  }
  if (state.attempt_keys !== undefined) {
    requireValue(object(state.attempt_keys) && Object.keys(state.attempt_keys).length <= 1024 && Object.entries(state.attempt_keys).every(([id, key]) => clipID(id) && validKey(key)), "invalid_state");
    state.attempt_keys = Object.assign(ownMap<string>(), state.attempt_keys);
  }
  if (state.intent_key !== undefined) requireValue(validKey(state.intent_key), "invalid_state");
  if (state.project_id !== undefined) requireValue(safeID(state.project_id) && Number.isSafeInteger(state.project_revision) && state.project_revision! > 0, "invalid_state");
  if (state.project_retry_keys !== undefined) {
    requireValue(object(state.project_retry_keys) && Object.entries(state.project_retry_keys).every(([id, key]) => Object.hasOwn(state.clips, id) && validKey(key)), "invalid_state");
    state.project_retry_keys = Object.assign(ownMap<string>(), state.project_retry_keys);
  }
  if (state.intent_project !== undefined) requireValue(object(state.intent_project) && state.intent && state.intent_project.id === state.project_id && Number.isSafeInteger(state.intent_project.revision) && state.intent_project.revision === state.project_revision && typeof state.intent_project.retry === "boolean", "invalid_state");
  if (state.retry_requested) retryIDs(state, state.retry_requested);
  if (state.retry_active) retryIDs(state, state.retry_active);
  if (state.next_submit_at !== undefined) requireValue(Number.isSafeInteger(state.next_submit_at) && state.next_submit_at >= 0, "invalid_state");
  return state;
}

async function initialize(client: CutmyClient, root: string, request: CollectionRequest, options: CollectionRunOptions, signal: AbortSignal): Promise<State> {
  requireValue(!(await existsRegular(join(root, "state.json"))) && !(await existsRegular(join(root, "owner.json"))) && !(await existsRegular(join(root, "initialize.json"))), "project_exists");
  const key = options.idempotencyKey ?? "collection-" + randomUUID();
  requireValue(typeof key === "string" && key.trim() && Buffer.byteLength(key) <= 128 && !/[\x00-\x1f\x7f]/.test(key), "invalid_key");
  await client.initializeSession({ signal });
  const cookie = client.exportSessionCookie();
  requireValue(/^[A-Za-z0-9_-]{43}$/.test(cookie), "invalid_owner");
  const state: State = { schema_version: 1, sdk: "typescript", mode: "http", server_url: client.serverUrl, source_id: request.source_id, owner_sha256: sha256(cookie), idempotency_key: key, request, clips: ownMap<Entry>(), receipts: ownMap<Receipt>() };
  applyRequest(state, request);
  const owner = { schema_version: 1, server_url: client.serverUrl, session_cookie: cookie };
  await atomicJSON(root, "initialize.json", { schema_version: 1, sdk: "typescript", owner, state }, true);
  await finishInitialization(client, root);
  return state;
}
async function finishInitialization(client: CutmyClient, root: string): Promise<void> {
  let journal: unknown;
  try { journal = await readJSON(join(root, "initialize.json")); }
  catch (error) { if (object(error) && error.code === "ENOENT") return; throw error; }
  requireValue(object(journal) && journal.schema_version === 1 && journal.sdk === "typescript", "invalid_initialization");
  const state = readState(journal.state), owner = journal.owner;
  requireValue(state.server_url === client.serverUrl, "server_conflict");
  requireValue(object(owner) && owner.schema_version === 1 && owner.server_url === state.server_url && typeof owner.session_cookie === "string" && /^[A-Za-z0-9_-]{43}$/.test(owner.session_cookie) && sha256(owner.session_cookie) === state.owner_sha256 && !state.intent && Object.keys(state.receipts).length === 0 && Object.values(state.clips).every(entry => !entry.job), "invalid_initialization");
  let currentOwner: string | undefined;
  try { currentOwner = client.exportSessionCookie(); }
  catch (error) { if (!(error instanceof ApiError) || error.code !== "session_unavailable") throw error; }
  requireValue(currentOwner === undefined || currentOwner === owner.session_cookie, "owner_conflict");
  const publications = [["owner.json", owner], ["state.json", state]] as const, missing = new Set<string>();
  for (const [name, value] of publications) {
    let existing: unknown;
    try { existing = await readJSON(join(root, name)); }
    catch (error) { if (!object(error) || error.code !== "ENOENT") throw error; }
    if (existing === undefined) missing.add(name);
    else requireValue(canonical(existing) === canonical(value), "initialization_conflict");
  }
  for (const [name, value] of publications) if (missing.has(name)) await atomicJSON(root, name, value, true);
  await unlink(join(root, "initialize.json")); await syncDirectory(root);
}
async function restore(client: CutmyClient, root: string, signal: AbortSignal): Promise<State> {
  await finishInitialization(client, root);
  const state = readState(await readJSON(join(root, "state.json")));
  requireValue(state.server_url === client.serverUrl, "server_conflict");
  const owner = await readJSON(join(root, "owner.json"), 4096);
  requireValue(object(owner) && owner.schema_version === 1 && owner.server_url === state.server_url && typeof owner.session_cookie === "string" && /^[A-Za-z0-9_-]{43}$/.test(owner.session_cookie) && sha256(owner.session_cookie) === state.owner_sha256, "owner_conflict");
  client.restoreSessionCookie(owner.session_cookie);
  await client.initializeSession({ signal });
  requireValue(client.exportSessionCookie() === owner.session_cookie, "owner_conflict");
  return state;
}

async function cycle(client: CutmyClient, directory: string, options: CollectionRunOptions, initial?: CollectionRequest, revision?: CollectionRequest, retryFailed?: string[], projectId?: string): Promise<CollectionReport> {
  const interval = positive(options.pollIntervalMs ?? 1000, "pollIntervalMs");
  requireValue(interval >= 1000, "invalid_poll_interval");
  const maximum = options.maxOutputBytes ?? 10 * 1024 ** 3;
  requireValue(Number.isSafeInteger(maximum) && maximum > 0, "invalid_output_budget");
  const networkMaximum = Math.min(Number.MAX_SAFE_INTEGER, maximum * 9);
  const batch = options.maxSubmitBatch ?? 3;
  requireValue(Number.isInteger(batch) && batch >= 1 && batch <= 128, "invalid_batch_size");
  const admission = options.admissionBatchSize ?? batch;
  requireValue(Number.isInteger(admission) && admission >= 1 && admission <= 128, "invalid_admission_batch_size");
  const scope = budget(options.timeoutMs ?? 30 * 60_000, options.signal);
  let root = "", state: State | undefined, unlock: (() => Promise<void>) | undefined;
  let networkBytes = 0;
  const checkpoint = async () => { await atomicJSON(root, "state.json", state!); };
  const progress = async (phase?: string, download?: CollectionReport["download"]) => {
    const value = structuredClone({ ...report(state!, root), ...(phase ? { phase } : {}), ...(download ? { download } : {}) });
    if (options.onProgress) {
      try { await abortable(Promise.resolve(options.onProgress(value)), scope.controller.signal); }
      catch { if (scope.controller.signal.aborted) throw abortedError(scope); fail("callback_error"); }
    }
  };
  const ensureLive = () => { if (scope.controller.signal.aborted) throw abortedError(scope); };
  try {
    root = await projectDirectory(directory, initial !== undefined);
    unlock = await writerLock(root);
    state = initial ? await initialize(client, root, copiedRequest(initial), options, scope.controller.signal) : await restore(client, root, scope.controller.signal);
    if (retryFailed && revision) fail("retry_revision_conflict");
    if (revision && state.intent) fail("submission_uncertain");
    if (projectId) {
      requireValue(safeID(projectId), "invalid_project");
      if (state.intent) fail("submission_uncertain");
      const project = await client.getProject(projectId, { signal: scope.controller.signal });
      const body = copiedRequest({ source_id: project.source_id, plan: project.plan, rendition: project.rendition, ...(project.clip_ids ? { clip_ids: project.clip_ids } : {}) });
      requireValue(body.source_id === state.source_id, "source_conflict");
      if (revision) requireValue(canonical(copiedRequest(revision)) === canonical(body), "project_conflict");
      const expected = new Map(project.clips.map(clip => [clip.clip_id, clip.request]));
      applyRequest(state, body);
      for (const id of selected(body)) state.clips[id] = { signature: signature(body, id) };
      for (const item of project.jobs) {
        if (!item.current) continue;
        requireValue(canonical(item.request) === canonical(expected.get(item.clip_id)), "project_conflict");
        state.clips[item.clip_id] = { signature: signature(body, item.clip_id), job: item.job, effective: item.request };
      }
      state.project_id = projectId; state.project_revision = project.revision;
      delete state.project_retry_keys; delete state.retry_requested; delete state.retry_active;
      if (selected(body).every(id => state!.clips[id]!.job)) state.validated = sha256(canonical(body));
      await checkpoint();
    } else if (revision) {
      const body = copiedRequest(revision);
      if (state.project_id && canonical(body) !== canonical(state.request)) fail("project_conflict");
      applyRequest(state, body); await checkpoint();
    }
    // Rehash prior completed files before requests, including earlier revisions.
    for (const receipt of Object.values(state.receipts)) {
      childPath(root, receipt.part); const final = childPath(root, receipt.path);
      if (receipt.phase === "complete") requireValue((await hashFile(final, receipt.size_bytes, scope.controller.signal)).hash === receipt.sha256, "file_tampered");
      if (receipt.phase === "prepared" && await existsPrepared(final, childPath(root, receipt.part))) {
        requireValue((await hashFile(final, receipt.size_bytes, scope.controller.signal)).hash === receipt.sha256, "file_tampered");
        receipt.phase = "complete"; await checkpoint();
      }
    }
    requireValue(Object.values(state.receipts).reduce((sum, receipt) => sum + receipt.size_bytes, 0) <= maximum, "output_limit");
    if (retryFailed !== undefined) {
      const ids = retryIDs(state, retryFailed);
      requireValue(!ids.some(id => report(state!, root).outputs.some(output => output.clip_id === id)), "retry_not_failed");
      requireValue(!state.retry_requested || canonical([...state.retry_requested].sort()) === canonical([...ids].sort()), "retry_pending");
      state.retry_requested = ids; await checkpoint();
    }
    await progress("resumed");

    const transfer = async (id: string, job: Job, artifact: Artifact, effective: NormalizedRequest) => {
      ensureLive();
      requireValue(safeID(artifact.id) && safeID(job.id) && Number.isSafeInteger(artifact.size_bytes) && artifact.size_bytes > 0 && artifact.size_bytes <= maximum, "invalid_artifact");
      let receipt = state!.receipts[artifact.id];
      if (receipt?.phase === "complete") return;
      const reservedSize = Object.values(state!.receipts).filter(x => x.artifact_id !== artifact.id).reduce((sum, x) => sum + x.size_bytes, 0);
      requireValue(artifact.size_bytes <= maximum - reservedSize, "output_limit");
      if (!receipt) {
        requireValue(Object.keys(state!.receipts).length < 4096, "receipt_limit");
        const format = effective.format;
        requireValue(["mp4","mp3","mkv"].includes(format), "invalid_response");
        const rendition = state!.request.rendition ?? "final";
        receipt = { clip_id: id, job_id: job.id, artifact_id: artifact.id, rendition, path: "files/" + id + "." + rendition + "." + artifact.id + "." + format, part: "parts/" + artifact.id + "." + randomUUID() + ".part", size_bytes: artifact.size_bytes, offset: 0, prefix_sha256: EMPTY_HASH, phase: "intent", segments: artifact.segments ?? [], track_manifest: artifact.track_manifest ?? [] };
        requireValue(!(await existsRegular(childPath(root, receipt.path))), "output_exists");
        state!.receipts[artifact.id] = receipt; await checkpoint();
      }
      requireValue(receipt.job_id === job.id && receipt.size_bytes === artifact.size_bytes, "artifact_changed");
      const part = childPath(root, receipt.part), final = childPath(root, receipt.path);
      if (receipt.phase === "prepared") {
        if (await existsPrepared(final, part)) {
          requireValue((await hashFile(final, receipt.size_bytes, scope.controller.signal)).hash === receipt.sha256, "file_tampered");
          receipt.phase = "complete"; await checkpoint(); await progress("output_ready"); return;
        }
      }
      const present = await existsRegular(part);
      requireValue(present || receipt.offset === 0, "part_missing");
      const file = await openPart(part, !present);
      let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
      let hash = createHash("sha256"), position = receipt.offset, resumed = receipt.offset > 0;
      try {
        const info = await file.stat();
        requireValue(info.size >= receipt.offset, "part_tampered");
        await file.truncate(receipt.offset);
        const buffer = Buffer.alloc(CHECKPOINT);
        let read = 0;
        while (read < receipt.offset) { ensureLive(); const next = await file.read(buffer, 0, Math.min(buffer.length, receipt.offset - read), read); requireValue(next.bytesRead > 0, "part_tampered"); hash.update(buffer.subarray(0, next.bytesRead)); read += next.bytesRead; }
        requireValue(hash.copy().digest("hex") === receipt.prefix_sha256, "part_tampered");
        const persistPart = async (phase: string) => {
          await file.sync(); receipt!.offset = position; receipt!.prefix_sha256 = hash.copy().digest("hex"); receipt!.phase = "part"; await checkpoint();
          await progress(phase, { clip_id: id, artifact_id: artifact.id, offset: position, total_size: artifact.size_bytes, resumed });
        };
        if (position < receipt.size_bytes) {
          const download = await client.downloadArtifactRange(artifact.id, { offset: position, ...(receipt.last_modified ? { ifRange: receipt.last_modified } : {}), expectedSize: receipt.size_bytes, maxBytes: maximum, signal: scope.controller.signal });
          if (position > 0 && download.status === 200) { await file.truncate(0); position = 0; hash = createHash("sha256"); receipt.offset = 0; receipt.prefix_sha256 = EMPTY_HASH; resumed = false; await checkpoint(); }
          if (download.lastModified) receipt.last_modified = download.lastModified;
          reader = download.stream.getReader();
          let checked = position;
          try {
            while (true) {
              ensureLive(); const next = await reader.read(); if (next.done) break;
              requireValue(next.value.length <= receipt.size_bytes - position && next.value.length <= networkMaximum - networkBytes, "output_limit");
              let written = 0;
              while (written < next.value.length) { const count = await file.write(next.value, written, next.value.length - written, position + written); requireValue(count.bytesWritten > 0, "filesystem_error"); written += count.bytesWritten; }
              hash.update(next.value); position += next.value.length; networkBytes += next.value.length;
              if (position - checked >= CHECKPOINT && position < receipt.size_bytes) { await persistPart("download_checkpoint"); checked = position; }
            }
            requireValue(position === receipt.size_bytes, "download_interrupted");
          } catch (error) {
            await persistPart("download_interrupted");
            throw error;
          }
        }
        await file.sync(); receipt.offset = position; receipt.prefix_sha256 = hash.copy().digest("hex"); receipt.sha256 = receipt.prefix_sha256; receipt.phase = "prepared"; await checkpoint();
      } finally { try { await reader?.cancel().catch(() => {}); } finally { reader?.releaseLock(); await file.close(); } }
      requireValue(receipt.offset === receipt.size_bytes && receipt.sha256 === (await hashFile(part, receipt.size_bytes, scope.controller.signal)).hash, "part_tampered");
      try { await link(part, final); } catch (error) { if (object(error) && error.code === "EEXIST") fail("output_exists"); throw error; }
      await unlink(part); await syncDirectory(join(root, "files")); await syncDirectory(join(root, "parts"));
      receipt.phase = "complete"; await checkpoint(); await progress("output_ready");
    };

    let failures = 0;
    const transferFailures = new Map<string, number>();
    const pollFailures = new Map<string, number>();
    while (true) {
      ensureLive();
      if (!state.intent && state.validated !== sha256(canonical(state.request))) {
        const checked = await client.validateCollection(state.request, { signal: scope.controller.signal });
        const ids = selected(state.request);
        requireValue(checked.valid && checked.rendition === (state.request.rendition ?? "final") && checked.clips.length === ids.length && checked.clips.every((clip, index) => clip.clip_id === ids[index]), "invalid_response");
        state.validated = sha256(canonical(state.request)); await checkpoint(); await progress("validated");
      }
      let changed = false;
      for (const id of selected(state.request)) {
        const entry = state.clips[id]!;
        if (!entry.job) continue;
        const prior = readyArtifacts(entry.job);
        if (entry.job.status === "succeeded" && prior.length > 0 && prior.every(artifact => state!.receipts[artifact.id]?.phase === "complete")) continue;
        const priorID = entry.job.id;
        if (Date.now() < (entry.next_poll_at ?? 0)) continue;
        try {
          entry.job = await client.getJob(priorID, { signal: scope.controller.signal });
          entry.next_poll_at = Date.now() + interval; pollFailures.delete(priorID);
        } catch (error) {
          if (error instanceof ApiError && (["network_error","timeout"].includes(error.code) || error.status === 429 || (error.status ?? 0) >= 500) && !scope.controller.signal.aborted) {
            const count = (pollFailures.get(priorID) ?? 0) + 1; pollFailures.set(priorID, count); requireValue(count <= 8, "poll_failed");
            entry.next_poll_at = Date.now() + Math.max(interval, (error.retryAfterSeconds ?? Math.min(5, count)) * 1000);
            await checkpoint(); await progress("poll_retry"); continue;
          }
          throw error;
        }
        requireValue(entry.job.id === priorID, "invalid_response");
        requireValue(entry.job.items.length > 0, "invalid_response");
        await checkpoint();
        for (const artifact of readyArtifacts(entry.job)) {
          requireValue(entry.effective, "invalid_state");
          try { await transfer(id, entry.job, artifact, entry.effective); }
          catch (error) {
            if (error instanceof ApiError && (["network_error", "timeout", "download_interrupted"].includes(error.code) || (error.code === "invalid_response" && error.status === undefined)) && !scope.controller.signal.aborted) { const count = (transferFailures.get(artifact.id) ?? 0) + 1; transferFailures.set(artifact.id, count); requireValue(count <= 8, "download_interrupted"); await delay(Math.min(5000, interval * count), scope.controller.signal); changed = true; break; }
            throw error;
          }
        }
        if (entry.job.status === "failed" || entry.job.status === "cancelled") { entry.error = { code: entry.job.status, retryable: false }; await checkpoint(); }
        if (entry.job.status === "succeeded" && readyArtifacts(entry.job).length === 0) { entry.error = { code: "artifact_expired", retryable: false }; await checkpoint(); }
      }
      if (state.retry_requested && !state.intent) {
        const ids = state.retry_requested;
        if (!ids.every(id => ["failed","cancelled"].includes(state!.clips[id]!.job?.status ?? "") || state!.clips[id]!.error?.code === "artifact_expired")) {
          delete state.retry_requested; await checkpoint(); fail("retry_not_failed");
        }
        const attempts = Object.assign(ownMap<string>(), state.attempt_keys ?? {});
        if (new Set([...Object.keys(attempts), ...ids]).size > 1024) { delete state.retry_requested; await checkpoint(); fail("attempt_limit"); }
        for (const id of ids) {
          attempts[id] = "retry-" + randomUUID(); state.clips[id] = { signature: signature(state.request, id) };
          if (state.project_id) { state.project_retry_keys ??= ownMap<string>(); state.project_retry_keys[id] = attempts[id]!; }
        }
        state.attempt_keys = attempts; state.retry_active = ids; delete state.retry_requested; delete state.validated;
        await checkpoint(); await progress("retry_prepared"); continue;
      }
      const value = report(state, root);
      requireValue(Object.values(state.receipts).reduce((sum, receipt) => sum + receipt.size_bytes, 0) <= maximum, "output_limit");
      if (value.complete) { await progress("complete"); return value; }
      const activePending = state.retry_active?.some(id => value.pending.some(clip => clip.clip_id === id && clip.retryable));
      if (state.retry_active && !activePending) { delete state.retry_active; await checkpoint(); }
      const terminal = value.pending.find(clip => !clip.retryable);
      if (terminal && !state.retry_requested && !activePending) fail(terminal.code);
      const pending = selected(state.request).filter(id => !state!.clips[id]!.job && state!.clips[id]!.error?.retryable !== false);
      const outstanding = selected(state.request).filter(id => state!.clips[id]!.job && value.pending.some(clip => clip.clip_id === id && clip.retryable)).length;
      const available = Math.max(0, admission - outstanding);
      if ((state.intent || (pending.length && available > 0)) && Date.now() >= (state.next_submit_at ?? 0)) {
        const wanted = pending.slice(0, Math.min(batch, available));
        const key = state.intent_key ?? (state.intent ? state.idempotency_key : attemptKey(state, wanted[0]!));
        const submission = state.intent ?? { ...state.request, clip_ids: wanted.filter(id => attemptKey(state!, id) === key) };
        if (!state.intent) {
          state.intent = copiedRequest(submission); state.intent_key = key;
          if (state.project_id) state.intent_project = { id: state.project_id, revision: state.project_revision!, retry: selected(submission).every(id => state!.project_retry_keys && Object.hasOwn(state!.project_retry_keys, id) && state!.project_retry_keys[id] === key) };
          await checkpoint();
        }
        try {
          const route = state.intent_project;
          const response = route ? await client.submitProjectCollection(route.id, { revision: route.revision, clip_ids: selected(submission) }, { retryFailed: route.retry, idempotencyKey: key, signal: scope.controller.signal }) : await client.submitCollection(submission, { idempotencyKey: key, signal: scope.controller.signal });
          const expected = selected(submission), seen = new Set<string>();
          requireValue(response.rendition === (submission.rendition ?? "final") && response.clips.length === expected.length && response.clips.every((clip, index) => clip.clip_id === expected[index]), "invalid_response");
          for (const clip of response.clips) {
            requireValue(expected.includes(clip.clip_id) && !seen.has(clip.clip_id) && object(clip.request) && clip.request.source_id === state.source_id, "invalid_response"); seen.add(clip.clip_id);
            const entry = state.clips[clip.clip_id]!;
            if (clip.job) { requireValue(safeID(clip.job.id), "invalid_response"); entry.job = clip.job; entry.effective = clip.request; delete entry.error; if (state.project_retry_keys) delete state.project_retry_keys[clip.clip_id]; }
            else { requireValue(clip.error, "invalid_response"); entry.error = { code: clip.error.code, retryable: clip.retryable, message: clip.error.message, ...(clip.error.field ? { field: clip.error.field } : {}), ...(clip.error.clip_id ? { diagnostic_clip_id: clip.error.clip_id } : {}) }; }
          }
          delete state.intent; delete state.intent_key; delete state.intent_project; failures = 0;
          state.next_submit_at = Date.now() + Math.max(interval, (response.retry_after_seconds ?? 0) * 1000);
          await checkpoint(); await progress("submitted"); changed = true;
        } catch (error) {
          if (error instanceof ApiError && (["network_error","timeout","invalid_response"].includes(error.code) || error.status === 429 || (error.status ?? 0) >= 500)) {
            failures++; requireValue(failures <= 8, "submission_uncertain");
            state.next_submit_at = Date.now() + Math.max(interval, (error.retryAfterSeconds ?? Math.min(5, failures)) * 1000);
            await checkpoint(); await progress("submission_uncertain");
          } else throw error;
        }
      }
      if (!changed) await delay(interval, scope.controller.signal);
    }
  } catch (error) {
    if (state && error instanceof ApiError && error.diagnostic) {
      const diagnostic = error.diagnostic;
      const ids = diagnostic.clip_id && state.clips[diagnostic.clip_id] ? [diagnostic.clip_id] : selected(state.request).filter(id => !state!.clips[id]!.job);
      for (const id of ids) state.clips[id]!.error = { code: diagnostic.code, retryable: error.status === 429 || (error.status ?? 0) >= 500, message: diagnostic.message, ...(diagnostic.field ? { field: diagnostic.field } : {}), ...(diagnostic.clip_id ? { diagnostic_clip_id: diagnostic.clip_id } : {}) };
      try { await checkpoint(); } catch { error = new ApiError("Collection checkpoint could not be saved", { code: "filesystem_error" }); }
    }
    const current = state ? report(state, root) : { complete: false, source_id: initial?.source_id ?? "", rendition: initial?.rendition ?? "final", outputs: [], pending: [] } satisfies CollectionReport;
    const code = scope.controller.signal.aborted ? (scope.timeout() ? "timeout" : "cancelled") : error instanceof ApiError ? error.code : "filesystem_error";
    throw new CollectionError(code, current);
  } finally {
    scope.dispose();
    if (unlock) {
      try { await unlock(); }
      catch { throw new CollectionError("writer_lock_changed", state ? report(state, root) : { complete: false, source_id: initial?.source_id ?? "", rendition: initial?.rendition ?? "final", outputs: [], pending: [] }); }
    }
  }
}

/** Recover only a prepared publication with its verified runner-owned receipt. */
async function existsPrepared(final: string, part: string): Promise<boolean> {
  try {
    const info = await lstat(final);
    requireValue(info.isFile() && !info.isSymbolicLink(), "unsafe_file");
    if (info.nlink === 2) {
      const staged = await lstat(part);
      requireValue(staged.isFile() && !staged.isSymbolicLink() && staged.nlink === 2 && staged.ino === info.ino && staged.dev === info.dev, "unsafe_file");
      await unlink(part);
    } else requireValue(info.nlink === 1, "unsafe_file");
    return true;
  } catch (error) { if (object(error) && error.code === "ENOENT") return false; throw error; }
}

export function runCollection(client: CutmyClient, request: CollectionRequest, directory: string, options: CollectionRunOptions = {}): Promise<CollectionReport> {
  return cycle(client, directory, options, request);
}
export function resumeCollection(client: CutmyClient, directory: string, options: CollectionResumeOptions = {}): Promise<CollectionReport> {
  return cycle(client, directory, options, undefined, options.request, options.retryFailed, options.projectId);
}
export { CollectionError } from "./errors.js";
export type { CollectionReport, CollectionRequest } from "./types.js";
