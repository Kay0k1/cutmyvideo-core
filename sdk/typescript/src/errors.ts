import type { Artifact, BatchResult, Job } from "./types.js";

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
  constructor(message: string, options: { code: string; status?: number; jobId?: string; idempotencyKey?: string; retryAfterSeconds?: number }) {
    super(message, options); this.name = "ApiError"; this.retryAfterSeconds = options.retryAfterSeconds;
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
