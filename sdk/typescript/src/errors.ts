import type { Artifact, BatchResult, CollectionDiagnostic, CollectionReport, Job } from "./types.js";

export class CutmyError extends Error {
  readonly code: string;
  readonly status: number | undefined;
  readonly jobId: string | undefined;
  readonly idempotencyKey: string | undefined;
  constructor(message: string, options: { code: string; status?: number; jobId?: string; idempotencyKey?: string }) {
    super(message);
    this.name = "CutmyError";
    this.code = options.code;
    this.status = options.status;
    this.jobId = options.jobId;
    this.idempotencyKey = options.idempotencyKey;
  }
}
export class ApiError extends CutmyError {
  readonly retryAfterSeconds: number | undefined;
  readonly diagnostic: CollectionDiagnostic | undefined;
  constructor(message: string, options: { code: string; status?: number; jobId?: string; idempotencyKey?: string; retryAfterSeconds?: number; diagnostic?: CollectionDiagnostic }) {
    super(message, options); this.name = "ApiError"; this.retryAfterSeconds = options.retryAfterSeconds;
    const diagnostic = options.diagnostic;
    this.diagnostic = diagnostic ? { code: diagnostic.code, message: diagnostic.message, ...(typeof diagnostic.clip_id === "string" ? { clip_id: diagnostic.clip_id } : {}), ...(typeof diagnostic.field === "string" ? { field: diagnostic.field } : {}) } : undefined;
  }
}
export class JobError extends CutmyError {
  readonly job: Job;
  readonly artifacts: Artifact[];
  constructor(job: Job) {
    super(`Job ${job.status}`, { code: job.status, jobId: job.id });
    this.name = "JobError"; this.job = job;
    this.artifacts = readyArtifacts(job);
  }
}
/** Durable cycle failures retain a public partial report; credentials are absent. */
export class CollectionError extends CutmyError {
  readonly report: CollectionReport;
  constructor(code: string, report: CollectionReport, message = "Collection execution stopped; saved work can be resumed") {
    super(message, { code }); this.name = "CollectionError"; this.report = report;
  }
}
export class LocalError extends CutmyError {
  readonly exitCode: number | null;
  readonly result: BatchResult | undefined;
  constructor(message: string, options: { code: string; exitCode?: number | null; result?: BatchResult }) {
    super(message, { code: options.code }); this.name = "LocalError";
    this.exitCode = options.exitCode ?? null; this.result = options.result;
  }
}
/** Only explicitly succeeded items have usable published files. */
export function readyArtifacts(job: Job): Artifact[] {
  return job.items.flatMap(item => item.status === "succeeded" && item.artifact ? [item.artifact] : []);
}
