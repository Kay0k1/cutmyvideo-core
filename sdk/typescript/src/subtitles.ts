import { ApiError } from "./errors.js";
import { object } from "./protocol.js";
import type { SubtitleRequest, SubtitleResult } from "./types.js";

export function subtitleRequest(value: SubtitleRequest): SubtitleRequest {
  const encoded = JSON.stringify(value);
  if (new TextEncoder().encode(encoded).length > 4 << 20) throw new TypeError("Subtitle request exceeds 4 MiB");
  const request = JSON.parse(encoded) as SubtitleRequest;
  if (!object(request) || !([1,2] as number[]).includes(request.version) || !["srt","vtt"].includes(request.format) || typeof request.text !== "string" || new TextEncoder().encode(request.text).length > 512 << 10 || !["srt","vtt"].includes(request.output_format ?? request.format) || !Array.isArray(request.outputs) || !request.outputs.length || request.outputs.length > 128) throw new TypeError("Invalid subtitle request");
  const ids = request.outputs.map(output => output.clip_id);
  if (!ids.every(id => typeof id === "string" && /^[a-z0-9][a-z0-9_-]{0,63}$/.test(id) && !/^(con|prn|aux|nul|com[1-9]|lpt[1-9])$/.test(id)) || new Set(ids).size !== ids.length || request.outputs.some(output => !Array.isArray(output.segments) || !output.segments.length || output.segments.length > 128) || request.outputs.reduce((sum, output) => sum + output.segments.length, 0) > 4096) throw new TypeError("Invalid subtitle output maps");
  return request;
}
export async function subtitleResult(value: unknown, request: SubtitleRequest): Promise<SubtitleResult> {
  if (!object(value) || value.version !== 1 || !Array.isArray(value.outputs) || value.outputs.length !== request.outputs.length) throw new ApiError("Invalid subtitle response", { code: "invalid_response" });
  let size = 0;
  const outputs: SubtitleResult["outputs"] = [];
  for (let i = 0; i < value.outputs.length; i++) {
    const output: unknown = value.outputs[i];
    if (!object(output) || output.clip_id !== request.outputs[i]!.clip_id || output.format !== (request.output_format ?? request.format) || typeof output.text !== "string" || !Number.isSafeInteger(output.cue_count) || Number(output.cue_count) < 0 || Number(output.cue_count) > 100000 || typeof output.sha256 !== "string" || !/^[a-f0-9]{64}$/.test(output.sha256)) throw new ApiError("Invalid subtitle output", { code: "invalid_response" });
    const bytes = new TextEncoder().encode(output.text); size += bytes.length;
    if (size > 8 << 20) throw new ApiError("Subtitle output exceeds 8 MiB", { code: "response_too_large" });
    const digest = Array.from(new Uint8Array(await globalThis.crypto.subtle.digest("SHA-256", bytes)), byte => byte.toString(16).padStart(2, "0")).join("");
    if (digest !== output.sha256) throw new ApiError("Subtitle output hash differs", { code: "invalid_response" });
    outputs.push({ clip_id: String(output.clip_id), format: output.format as "srt" | "vtt", text: output.text, cue_count: Number(output.cue_count), sha256: output.sha256 });
  }
  return { version: 1, outputs };
}
