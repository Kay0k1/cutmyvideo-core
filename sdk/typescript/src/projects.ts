/** Portable edit documents contain no authority, server IDs, keys or paths. */
import { ApiError } from "./errors.js";
import { object, JSON_LIMIT } from "./protocol.js";
import { publicPreparedSource, sourceFingerprint } from "./source_protocol.js";
import type { AgentProject, CollectionRequest, PortableProject, Source } from "./types.js";

function copy<T>(value: T): T {
  const text = JSON.stringify(value);
  if (new TextEncoder().encode(text).length > JSON_LIMIT) throw new TypeError("Project JSON exceeds 1 MiB");
  return JSON.parse(text) as T;
}
function request(value: unknown): CollectionRequest {
  if (!object(value) || typeof value.source_id !== "string" || !/^[A-Za-z0-9_-]{1,160}$/.test(value.source_id) || !object(value.plan) || value.plan.version !== 1 || value.plan.input || !Array.isArray(value.plan.clips) || !value.plan.clips.length || value.plan.clips.length > 128) throw new TypeError("Invalid project request");
  const ids = value.plan.clips.map(clip => object(clip) ? clip.id : undefined);
  if (!ids.every(id => typeof id === "string" && /^[a-z0-9][a-z0-9_-]{0,63}$/.test(id) && !/^(con|prn|aux|nul|com[1-9]|lpt[1-9])$/.test(id)) || new Set(ids).size !== ids.length || value.rendition !== undefined && !["final","draft"].includes(String(value.rendition)) || value.clip_ids !== undefined && (!Array.isArray(value.clip_ids) || new Set(value.clip_ids).size !== value.clip_ids.length || !value.clip_ids.every(id => ids.includes(id)))) throw new TypeError("Invalid project selection");
  return value as unknown as CollectionRequest;
}
function portablePlan(value: CollectionRequest["plan"]): CollectionRequest["plan"] {
  const fields = (item: unknown, allowed: string[]) => { if (!object(item) || Object.entries(item).some(([key, value]) => !allowed.includes(key) || !["options","crop","clips","ranges"].includes(key) && value !== null && typeof value === "object")) throw new TypeError("Portable project contains unknown edit fields"); };
  const options = (item: unknown) => {
    fields(item, ["format","quality","cut_mode","audio_track","no_audio","subtitle_mode","crop","clear_crop"]);
    if (object(item) && item.crop !== undefined) fields(item.crop, ["x","y","width","height"]);
  };
  fields(value, ["version","input","options","clips"]);
  if (!Array.isArray(value.clips)) throw new TypeError("Portable project clips must be an array");
  if (value.options !== undefined) options(value.options);
  for (const clip of value.clips) {
    fields(clip, ["id","label","ranges","options"]);
    if (clip.options !== undefined) options(clip.options);
    if (!Array.isArray(clip.ranges)) throw new TypeError("Portable project ranges must be an array");
    for (const range of clip.ranges) fields(range, ["start_ms","end_ms","label"]);
  }
  return value;
}
export function exportPortableProject(value: CollectionRequest, source?: Source): PortableProject {
  const body = request(copy(value));
  const metadata: PortableProject["source"] = {};
  if (source) { const checked = publicPreparedSource(source); metadata.duration_ms = checked.duration_ms; metadata.title = checked.title; if (checked.fingerprint) metadata.fingerprint = sourceFingerprint(checked.fingerprint); }
  return { kind: "cutmy.collection.project", version: 1, source: metadata, plan: portablePlan(body.plan), ...(body.clip_ids ? { clip_ids: body.clip_ids } : {}), ...(body.rendition ? { rendition: body.rendition } : {}) };
}
export function importPortableProject(document: PortableProject, source: Source): CollectionRequest {
  const value = copy(document);
  if (!object(value) || Object.keys(value).some(key => !["kind","version","source","plan","clip_ids","rendition"].includes(key)) || value.kind !== "cutmy.collection.project" || value.version !== 1 || !object(value.source) || Object.keys(value.source).some(key => !["fingerprint","duration_ms","title"].includes(key))) throw new TypeError("Invalid portable project");
  const trusted = publicPreparedSource(source);
  if (value.source.fingerprint && (!trusted.fingerprint || JSON.stringify(sourceFingerprint(value.source.fingerprint)) !== JSON.stringify(sourceFingerprint(trusted.fingerprint)))) throw new TypeError("Portable source fingerprint differs from the explicitly supplied source");
  if (value.source.duration_ms !== undefined && value.source.duration_ms !== trusted.duration_ms) throw new TypeError("Portable source duration differs");
  return request({ source_id: trusted.id, plan: portablePlan(value.plan), ...(value.clip_ids ? { clip_ids: value.clip_ids } : {}), ...(value.rendition ? { rendition: value.rendition } : {}) });
}
export function projectResult(value: unknown): AgentProject {
  if (!object(value) || value.version !== 1 || typeof value.id !== "string" || !/^[A-Za-z0-9_-]{1,160}$/.test(value.id) || !Number.isSafeInteger(value.revision) || Number(value.revision) < 1 || !Array.isArray(value.clips) || !Array.isArray(value.jobs) || value.jobs.length > 128) throw new ApiError("Invalid project response", { code: "invalid_response" });
  try {
    const body = request({ source_id: value.source_id, plan: value.plan, rendition: value.rendition, ...(value.clip_ids === undefined ? {} : { clip_ids: value.clip_ids }) }), source = publicPreparedSource(value.source);
    const ids = body.plan.clips.filter(clip => !body.clip_ids?.length || body.clip_ids.includes(clip.id)).map(clip => clip.id);
    const allIDs = body.plan.clips.map(clip => clip.id);
    if (source.id !== body.source_id || JSON.stringify(value.clips.map(clip => object(clip) ? clip.clip_id : undefined)) !== JSON.stringify(ids)) throw new Error("scope");
    const jobs: AgentProject["jobs"] = [], clips: AgentProject["clips"] = [];
    for (const clip of value.clips) {
      if (!object(clip) || !object(clip.request) || clip.request.source_id !== source.id) throw new Error("scope");
      clips.push({ clip_id: String(clip.clip_id), request: clip.request as unknown as AgentProject["clips"][number]["request"] });
    }
    for (const item of value.jobs) {
      if (!object(item) || typeof item.clip_id !== "string" || !allIDs.includes(item.clip_id) || typeof item.current !== "boolean" || item.current && !ids.includes(item.clip_id) || !object(item.request) || item.request.source_id !== source.id || !object(item.job) || typeof item.job.id !== "string" || !/^[A-Za-z0-9_-]{1,160}$/.test(item.job.id)) throw new Error("scope");
      jobs.push({ clip_id: item.clip_id, current: item.current, request: item.request as unknown as AgentProject["jobs"][number]["request"], job: item.job as unknown as AgentProject["jobs"][number]["job"] });
    }
    if (new Set(jobs.map(job => job.clip_id)).size !== jobs.length) throw new Error("scope");
    return { version: 1, id: value.id, revision: Number(value.revision), source, ...body, rendition: body.rendition ?? "final", clips, jobs, ...(typeof value.expires_at === "string" ? { expires_at: value.expires_at } : {}), ...(typeof value.access_expires_at === "string" ? { access_expires_at: value.access_expires_at } : {}) };
  } catch { throw new ApiError("Project source, plan and jobs differ in scope", { code: "invalid_response" }); }
}
