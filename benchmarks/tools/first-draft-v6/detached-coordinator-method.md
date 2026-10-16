# Detached coordinator launch — prepared, not executed

Root observed an SSH disconnect while the previous profile controller kept running. The prospective formal controller therefore uses a separately registered systemd service with its own private file output. This helper has not started any unit, backend, process workload or remote operation.

Freeze launch-cohort.py's final hash in v6 config as coordinator_launcher_path/sha256. The controller freezes policy: outside all measured task parents, memory128MiB/swap0, CPU10%, TasksMax64, RemainAfterExit=yes, RuntimeMaxSec14400 and log file-size limit8MiB. Actual unit and kernel placement/limits are independently checked by the controller before any sample. All v6 runs share that policy; its separate real cost is retained.

After inventory+registration are complete and the current timing/native window is explicitly closed, create one private mode0600 request with:

```json
{
 "kind":"cutmy_first_draft_detached_coordinator",
 "nonce":"NEW_CRYPTOGRAPHIC_32_LOWERCASE_HEX",
 "registration_path":"EXACT_NEW_PRIVATE_V6_REGISTRATION",
 "registration_sha256":"EXACT_REGISTRATION_SHA",
 "controller_path":"/root/.cache/cutmy-first-draft-20261016/inputs/controller-cohort-v6.py",
 "controller_sha256":"FINAL_CONTROLLER_SHA",
 "window":"EXACT_REGISTERED_WINDOW",
 "python_path":"/usr/bin/python3"
}
```

Invocation, performed only by Root after authorization of the new window:

```
python3 -I -B /.../inputs/launch-cohort.py launch PRIVATE_REQUEST
```

The helper verifies request/registration/controller/launcher hashes and registered instrumentation, a new cohort/log directory/unit, and availability of the shared exclusive formal/native lock. It syncs an immutable run-once marker `proofs/COORDINATOR-<cohort>-LAUNCH.json` and a new mode0600 log before requesting systemd-run. It releases the shared lock immediately before start; a competing window makes the controller refuse safely, without takeover/retry.

The flat unit is exactly cutmyfirstdraftcoordinator<nonce>.service under system.slice, with an exact owned description binding nonce and registration. stdout/stderr append to private proofs/coordinator-<nonce>/coordinator.log, independent of the SSH pipe. The service's actual8MiB RLIMIT_FSIZE bounds this file; exceeding it fails the coordinator rather than truncating evidence or affecting production files. SDK and Docker workers are separately created services/containers and do not inherit this coordinator-only file-size limit. The service remains retained for inspection, rather than using --collect.

Controller first checks its own real cgroup, memory/cpu/pid/file-size limits, invocation and intent, then syncs ACTUAL-COORDINATOR.json. It records COORDINATOR-FINAL-COST.json on a normal full24 or planned negative completion. Launcher additionally records LAUNCH-ACCEPTED.json with positively checked actual unit metadata. These are private operational objects; only SHA references and numeric policy/cost aggregates belong in public evidence.

Read-only reconnect/status uses the **same** request:

```
python3 -I -B /.../inputs/launch-cohort.py status PRIVATE_REQUEST
```

It checks the immutable intent/unit again and emits only state/result/exit code aggregates. Launch is not a resume operation. Lost start reply, controller failure, timeout, duplicate invocation or a partial intent preserves its own files and retained unit for Root review; the helper never stops, restarts, deletes or takes over anything. It does not create a replacement cohort or accept partial evidence.

Primary references: [systemd-run service semantics](https://github.com/systemd/systemd/blob/v255/man/systemd-run.xml) describe manager-owned transient service execution; [systemd execution settings](https://github.com/systemd/systemd/blob/v255/man/systemd.exec.xml) define append file output and process resource limits. This design uses a detached service with explicit file output, rather than a connected terminal or pipe. Actual host policy remains positively checked; unfamiliar metadata formatting is a safe refusal requiring review.
