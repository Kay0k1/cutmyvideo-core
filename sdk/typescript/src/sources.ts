/** Node-only durable source preparation. Collection state remains separate. */
import { createHash, randomUUID } from "node:crypto";
import { openAsBlob } from "node:fs";
import { open, unlink } from "node:fs/promises";
import { basename, isAbsolute, join, resolve } from "node:path";
import type { CutmyClient } from "./client.js";
import { ApiError, SourcePreparationError } from "./errors.js";
import { abortable, abortedError, budget, delay, object } from "./protocol.js";
import { atomicJSON, canonical, existsRegular, projectDirectory, readJSON, requireValue, sha256, syncDirectory, writerLock } from "./collection_store.js";
import { preparationKey, publicPreparedSource, sourceFingerprint } from "./source_protocol.js";
import type { ContentFingerprint, RequestOptions, Source, SourceFingerprint, SourcePreparationInput, SourcePreparationReport } from "./types.js";

export interface SourceRunOptions extends RequestOptions { idempotencyKey?: string; onProgress?: (report: SourcePreparationReport) => void | Promise<void> }
type Intent = { kind: "upload"; path: string; fingerprint: ContentFingerprint } | { kind: "import"; url: string };
interface State {
  schema_version: 1; sdk: "typescript"; mode: "source"; server_url: string; owner_sha256: string;
  idempotency_key: string; intent: Intent; phase: string; uncertain: boolean;
  source?: Source; fingerprint?: SourceFingerprint; error?: { code: string; message?: string; retryable: boolean };
}
const phases = ["created","submitting","uncertain","waiting","processing","ready","failed"];
function report(state?: State): SourcePreparationReport {
  return structuredClone({ complete: state?.phase === "ready", phase: state?.phase ?? "initializing", ...(state?.source ? { source_id: state.source.id, source: state.source } : {}), ...(state?.fingerprint ? { fingerprint: state.fingerprint } : {}), ...(state?.error ? { error: state.error } : {}) });
}
function savedState(value: unknown, client: CutmyClient): State {
  requireValue(object(value) && value.schema_version === 1 && value.sdk === "typescript" && value.mode === "source" && value.server_url === client.serverUrl && typeof value.owner_sha256 === "string" && /^[a-f0-9]{64}$/.test(value.owner_sha256) && typeof value.uncertain === "boolean" && phases.includes(String(value.phase)) && object(value.intent), "source_state_invalid");
  preparationKey(value.idempotency_key);
  const intent = value.intent;
  if (intent.kind === "upload") requireValue(Object.keys(intent).length === 3 && typeof intent.path === "string" && isAbsolute(intent.path) && sourceFingerprint(intent.fingerprint).kind === "content", "source_state_invalid");
  else requireValue(intent.kind === "import" && Object.keys(intent).length === 2 && typeof intent.url === "string" && Buffer.byteLength(intent.url) > 0 && Buffer.byteLength(intent.url) <= 16384, "source_state_invalid");
  const state = value as unknown as State;
  if (state.source) state.source = publicPreparedSource(state.source);
  if (state.fingerprint) state.fingerprint = sourceFingerprint(state.fingerprint);
  if (state.error) {
    requireValue(object(state.error) && typeof state.error.code === "string" && /^[a-z0-9_]{1,128}$/.test(state.error.code) && typeof state.error.retryable === "boolean" && (state.error.message === undefined || typeof state.error.message === "string" && Buffer.byteLength(state.error.message) <= 4096), "source_state_invalid");
    state.error = { code: state.error.code, retryable: state.error.retryable, ...(state.error.message ? { message: state.error.message } : {}) };
  }
  return state;
}
function ownerFile(value: unknown, state: State, client: CutmyClient): string {
  requireValue(object(value) && value.schema_version === 1 && value.server_url === client.serverUrl && typeof value.session_cookie === "string" && /^[A-Za-z0-9_-]{43}$/.test(value.session_cookie) && sha256(value.session_cookie) === state.owner_sha256, "owner_conflict");
  try { requireValue(client.exportSessionCookie() === value.session_cookie, "owner_conflict"); }
  catch (error) { if (!(error instanceof ApiError) || error.code !== "session_unavailable") throw error; }
  return value.session_cookie;
}
async function hashInput(path: string, signal: AbortSignal): Promise<ContentFingerprint> {
  const source = await open(path, "r");
  try {
    const before = await source.stat(); requireValue(before.isFile() && Number.isSafeInteger(before.size) && before.size > 0, "source_invalid");
    const hash = createHash("sha256"), buffer = Buffer.alloc(1 << 20); let position = 0;
    while (true) {
      if (signal.aborted) throw new Error("cancelled");
      const chunk = await source.read(buffer, 0, buffer.length, position); if (!chunk.bytesRead) break;
      hash.update(buffer.subarray(0, chunk.bytesRead)); position += chunk.bytesRead;
    }
    const after = await source.stat(); requireValue(position === before.size && after.size === before.size && after.mtimeMs === before.mtimeMs, "source_changed");
    return { kind: "content", sha256: hash.digest("hex"), size_bytes: position };
  } finally { await source.close(); }
}
async function finishInitialization(root: string, client: CutmyClient): Promise<void> {
  let journal: unknown;
  try { journal = await readJSON(join(root, "initialize-source.json")); }
  catch (error) { if (object(error) && error.code === "ENOENT") return; throw error; }
  requireValue(object(journal) && journal.schema_version === 1, "source_state_invalid");
  const state = savedState(journal.state, client), owner = journal.owner;
  ownerFile(owner, state, client);
  requireValue(state.phase === "created" && !state.uncertain && !state.source && !state.error, "source_state_invalid");
  const missing: string[] = [];
  for (const [name, sealed] of [["owner.json", owner], ["state.json", state]] as const) {
    let current: unknown;
    try { current = await readJSON(join(root, name)); }
    catch (error) { if (!object(error) || error.code !== "ENOENT") throw error; }
    if (current === undefined) missing.push(name); else requireValue(canonical(current) === canonical(sealed), "initialization_conflict");
  }
  for (const [name, sealed] of [["owner.json", owner], ["state.json", state]] as const) if (missing.includes(name)) await atomicJSON(root, name, sealed, true);
  await unlink(join(root, "initialize-source.json")); await syncDirectory(root);
}
async function cycle(client: CutmyClient, directory: string, options: SourceRunOptions, input?: SourcePreparationInput): Promise<SourcePreparationReport> {
  const timeout = options.timeoutMs ?? 2 * 60 * 60_000, deadline = performance.now() + timeout;
  const scope = budget(timeout, options.signal);
  let root = "", state: State | undefined, owner: string | undefined, unlock: (() => Promise<void>) | undefined;
  const guard = () => {
    if (scope.controller.signal.aborted) throw abortedError(scope);
    if (owner) requireValue(client.exportSessionCookie() === owner, "owner_conflict");
  };
  const checkpoint = async (phase: string) => {
    state!.phase = phase; await atomicJSON(root, "state.json", state!);
    if (options.onProgress) { try { await abortable(Promise.resolve(options.onProgress(report(state))), scope.controller.signal); } catch { guard(); throw new ApiError("Preparation callback interrupted", { code: "callback_error" }); } }
  };
  try {
    root = await projectDirectory(directory, input !== undefined); unlock = await writerLock(root);
    if (input) {
      requireValue(!(await existsRegular(join(root, "owner.json"))) && !(await existsRegular(join(root, "state.json"))) && !(await existsRegular(join(root, "initialize-source.json"))), "project_exists");
      let intent: Intent;
      if (input.kind === "upload") { requireValue(Object.keys(input).length === 2 && typeof input.path === "string" && !!input.path, "invalid_source_input"); const path = resolve(input.path); intent = { kind: "upload", path, fingerprint: await hashInput(path, scope.controller.signal) }; }
      else { requireValue(input.kind === "import" && Object.keys(input).length === 2 && typeof input.url === "string" && Buffer.byteLength(input.url) > 0 && Buffer.byteLength(input.url) <= 16384, "invalid_source_input"); intent = { kind: "import", url: input.url }; }
      const key = options.idempotencyKey ?? "source-" + randomUUID(); preparationKey(key);
      await client.initializeSession({ signal: scope.controller.signal }); owner = client.exportSessionCookie();
      state = { schema_version: 1, sdk: "typescript", mode: "source", server_url: client.serverUrl, owner_sha256: sha256(owner), idempotency_key: key, intent, phase: "created", uncertain: false };
      await atomicJSON(root, "initialize-source.json", { schema_version: 1, owner: { schema_version: 1, server_url: client.serverUrl, session_cookie: owner }, state }, true);
      await finishInitialization(root, client); await checkpoint("created");
    } else {
      await finishInitialization(root, client);
      state = savedState(await readJSON(join(root, "state.json")), client);
      owner = ownerFile(await readJSON(join(root, "owner.json"), 4096), state, client);
      client.restoreSessionCookie(owner); await client.initializeSession({ signal: scope.controller.signal }); guard();
    }
    const accept = async (value: Source, expected?: SourceFingerprint) => {
      const source = publicPreparedSource(value), fingerprint = sourceFingerprint(expected ?? source.fingerprint);
      requireValue(!source.fingerprint || canonical(sourceFingerprint(source.fingerprint)) === canonical(fingerprint), "source_fingerprint_changed");
      requireValue(state!.intent.kind !== "upload" || canonical(fingerprint) === canonical(state!.intent.fingerprint), "source_fingerprint_changed");
      source.fingerprint = fingerprint; state!.source = source; state!.fingerprint = fingerprint; state!.uncertain = false; delete state!.error;
      await checkpoint("ready"); return report(state);
    };
    while (true) {
      guard();
      try {
        let receipt;
        try { receipt = await client.getSourcePreparation(state.idempotency_key, { signal: scope.controller.signal }); }
        catch (error) { if (!(error instanceof ApiError) || error.status !== 404) throw error; receipt = { state: "retryable" as const }; }
        if ("error" in receipt && receipt.error && (receipt.state === "failed" || receipt.state === "ready")) { state.error = receipt.error; await checkpoint("failed"); throw new ApiError("Preparation needs explicit repair", { code: receipt.error.code }); }
        if (receipt.state === "ready") { requireValue("source" in receipt && receipt.source, "invalid_response"); return await accept(receipt.source, receipt.fingerprint); }
        if (receipt.state === "processing") { await checkpoint("processing"); await delay(Math.max(1000, receipt.retry_after_ms ?? 1000), scope.controller.signal); continue; }
        const intent = state.intent;
        if (intent.kind === "upload") requireValue(canonical(await hashInput(intent.path, scope.controller.signal)) === canonical(intent.fingerprint), "source_changed");
        state.uncertain = true; await checkpoint("submitting");
        const requestOptions = { idempotencyKey: state.idempotency_key, timeoutMs: Math.max(1, deadline - performance.now()), signal: scope.controller.signal };
        const source = intent.kind === "upload" ? await client.upload(await openAsBlob(intent.path), basename(intent.path), { ...requestOptions, fingerprint: intent.fingerprint }) : await client.importSource(intent.url, requestOptions);
        return await accept(source);
      } catch (error) {
        guard();
        if (!(error instanceof ApiError) || (!["network_error","timeout"].includes(error.code) && ![408,429,500,502,503,504].includes(error.status ?? 0))) throw error;
        await checkpoint(state.uncertain ? "uncertain" : "waiting"); await delay(Math.max(1000, (error.retryAfterSeconds ?? 2) * 1000), scope.controller.signal);
      }
    }
  } catch (error) {
    const code = scope.controller.signal.aborted ? (scope.timeout() ? "source_preparation_timeout" : "source_preparation_cancelled") : error instanceof ApiError ? error.code : "source_state_invalid";
    throw new SourcePreparationError(code, report(state));
  } finally { scope.dispose(); if (unlock) await unlock(); }
}
export async function prepareSource(client: CutmyClient, input: SourcePreparationInput, directory: string, options: SourceRunOptions = {}): Promise<SourcePreparationReport> {
  if (options.idempotencyKey !== undefined) preparationKey(options.idempotencyKey);
  return cycle(client, directory, options, input);
}
export function resumeSource(client: CutmyClient, directory: string, options: Omit<SourceRunOptions, "idempotencyKey"> = {}): Promise<SourcePreparationReport> { return cycle(client, directory, options); }
