"""Stable error attributes; human messages and server codes may evolve."""

from typing import Any, Mapping

from .types import Artifact, BatchOutput, Job


class CutmyError(Exception):
    def __init__(self, message: str, *, code: str = "sdk_error"):
        super().__init__(message)
        self.code = code


class APIError(CutmyError):
    def __init__(self, message: str, *, code: str, status: int | None = None,
                 idempotency_key: str | None = None, retry_after: float | None = None):
        super().__init__(message, code=code)
        self.status = status
        self.idempotency_key = idempotency_key
        self.retry_after = retry_after


class JobError(CutmyError):
    def __init__(self, job: Job):
        super().__init__("Export job ended with " + str(job.get("status", "unknown")),
                         code="job_" + str(job.get("status", "unknown")))
        self.job = job
        self.job_id = job.get("id", "")
        self.artifacts = ready_artifacts(job)


class WaitTimeout(CutmyError):
    def __init__(self, job_id: str, job: Job | None = None):
        super().__init__("Timed out waiting for job; the server job was not cancelled", code="wait_timeout")
        self.job_id, self.job = job_id, job
        self.artifacts = ready_artifacts(job) if job is not None else []


class WaitCancelled(CutmyError):
    def __init__(self, job_id: str, job: Job | None = None):
        super().__init__("Stopped waiting; the server job was not cancelled", code="wait_cancelled")
        self.job_id, self.job = job_id, job
        self.artifacts = ready_artifacts(job) if job is not None else []


class LocalError(CutmyError):
    def __init__(self, message: str, *, code: str, exit_code: int | None = None,
                 outputs: list[BatchOutput] | None = None):
        super().__init__(message, code=code)
        self.exit_code = exit_code
        self.outputs = outputs or []


def ready_artifacts(job: Mapping[str, Any]) -> list[Artifact]:
    """Only succeeded items expose downloadable artifacts, including partial jobs."""
    return [item["artifact"] for item in job.get("items", [])
            if isinstance(item, dict) and item.get("status") == "succeeded"
            and isinstance(item.get("artifact"), dict)]
