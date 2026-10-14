//go:build linux

package app

// Optional exact CLI processes. The coordinator-owned launcher supplies real
// per-role cgroups; neither database credentials nor command arguments enter
// the resource/stage receipt.
import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type corePerformanceRole struct {
	name string
	cmd  *exec.Cmd
	log  *corePerformanceRoleLogWriter
	done chan error
	pid  int
}

func corePerformanceStartRoles(t *testing.T, ctx context.Context, c Config, privateDSN, root string) (string, func(), func() map[string]any) {
	t.Helper()
	cli, launcher := os.Getenv("CUTMY_CORE_PERF_SERVER_CLI"), os.Getenv("CUTMY_CORE_PERF_ROLE_LAUNCHER")
	if !filepath.IsAbs(cli) {
		t.Fatal("exact server CLI path must be absolute")
	}
	parsed, err := url.Parse(privateDSN)
	if err != nil || parsed.Scheme == "" {
		t.Fatal("owned database URL is required for independent roles")
	}
	query := parsed.Query()
	query.Set("pool_max_conns", "4")
	parsed.RawQuery = query.Encode()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("owned loopback address allocation failed")
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	// ConfigFromEnv is the real CLI interface. Explicitly clear ambient product
	// options, then freeze every non-default setting shared by both revisions.
	settings := map[string]string{
		"DATABASE_URL": parsed.String(), "DATA_DIR": c.DataDir, "LISTEN_ADDR": address,
		"WORKER_HEALTH_PATH": c.WorkerHealthPath, "FFMPEG_PATH": c.FFmpeg, "FFPROBE_PATH": c.FFprobe,
		"YTDLP_PATH": "false", "PUBLIC_ORIGIN": "", "COOKIE_SECURE": "false", "TRUST_PROXY": "false",
		"FFMPEG_PROFILE": c.FFmpegProfile, "FFMPEG_THREADS": "2", "WORKER_CONCURRENCY": "2",
		"WORKER_PREPARATION_CONCURRENCY": os.Getenv("CUTMY_CORE_PERF_SERVER_PREPARATION"),
		"MAX_SOURCE_BYTES":               strconv.FormatInt(c.MaxSourceBytes, 10), "MAX_FETCH_BYTES": strconv.FormatInt(c.MaxFetchBytes, 10),
		"MAX_OUTPUT_BYTES": strconv.FormatInt(c.MaxOutputBytes, 10), "MAX_STORAGE_BYTES": strconv.FormatInt(c.MaxStorageBytes, 10),
		"MAX_OWNER_BYTES": strconv.FormatInt(c.MaxOwnerBytes, 10), "STORAGE_SAFETY_BYTES": strconv.FormatInt(c.StorageSafetyBytes, 10),
		"MAX_RANGES": strconv.Itoa(c.MaxRanges), "MAX_ACTIVE_JOBS": strconv.Itoa(c.MaxActiveJobs),
		"MUTATIONS_PER_MINUTE": strconv.Itoa(c.MutationsPerMinute), "MAX_RANGE_MS": strconv.FormatInt(c.MaxRangeMS, 10),
		"MAX_JOB_MS": strconv.FormatInt(c.MaxJobMS, 10), "SOURCE_TIMEOUT": c.SourceTimeout.String(),
		"JOB_TIMEOUT": c.JobTimeout.String(), "UPLOAD_TIMEOUT": c.UploadTimeout.String(),
		"SOURCE_TTL": c.SourceTTL.String(), "ARTIFACT_TTL": c.ArtifactTTL.String(), "STORAGE_WAIT_TIMEOUT": c.StorageWaitTimeout.String(),
		"GOMAXPROCS": "2", "GOMEMLIMIT": "128MiB",
	}
	var base []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := settings[key]; !replaced {
			base = append(base, entry)
		}
	}
	for key, value := range settings {
		base = append(base, key+"="+value)
	}
	var roles []*corePerformanceRole
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		// Stop worker first so completed workspace cleanup is observed before SQL
		// accounting assertions. Launcher forwards TERM and waits for descendants.
		for index := len(roles) - 1; index >= 0; index-- {
			role := roles[index]
			_ = role.cmd.Process.Signal(syscall.SIGTERM)
			select {
			case err := <-role.done:
				if err != nil {
					t.Error("owned CLI role did not drain successfully", role.name)
				}
			case <-time.After(15 * time.Second):
				_ = syscall.Kill(-role.pid, syscall.SIGKILL)
				<-role.done
				t.Error("owned CLI role exceeded drain deadline", role.name)
			}
			_ = role.log.Close()
		}
	}
	t.Cleanup(stop)
	for _, name := range []string{"api", "worker"} {
		command := "worker"
		if name == "api" {
			command = "server"
		}
		var cmd *exec.Cmd
		if launcher != "" {
			cmd = exec.Command("python3", launcher, "--role", name, "--", cli, command)
		} else {
			cmd = exec.Command(cli, command)
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Env = append(append([]string(nil), base...), "CUTMY_CORE_PERF_ROLE_RESULT="+filepath.Join(root, name+"-resources.json"), "CUTMY_CORE_PERF_ROLE_PID_FILE="+filepath.Join(root, name+"-pid.json"))
		log, err := os.OpenFile(filepath.Join(root, name+"-cli.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		stages, err := os.OpenFile(filepath.Join(root, name+"-observed-stages.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			log.Close()
			t.Fatal("owned stage observation file failed")
		}
		observed := &corePerformanceRoleLogWriter{raw: log, stages: stages}
		cmd.Stdout, cmd.Stderr = observed, observed
		if err := cmd.Start(); err != nil {
			observed.Close()
			t.Fatal("owned CLI role failed to start", name)
		}
		role := &corePerformanceRole{name: name, cmd: cmd, log: observed, done: make(chan error, 1), pid: cmd.Process.Pid}
		roles = append(roles, role)
		go func() { role.done <- cmd.Wait() }()
	}
	serverURL := "http://" + address
	client := &http.Client{Timeout: time.Second}
	ready := time.Now().Add(20 * time.Second)
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if ctx.Err() != nil || time.Now().After(ready) {
			t.Fatal("owned API CLI did not become ready")
		}
		time.Sleep(25 * time.Millisecond)
	}
	results := func() map[string]any {
		result := map[string]any{"mode": "independent_cli_processes", "launcher": launcher, "runtime": map[string]any{"GOMAXPROCS": 2, "GOMEMLIMIT": "128MiB"}, "api_pool_max_conns": 4, "worker_pool_max_conns": 4}
		for _, role := range roles {
			entry := map[string]any{"launcher_or_role_pid": role.pid, "verb": role.cmd.Args[len(role.cmd.Args)-1]}
			for _, suffix := range []string{"resources", "pid"} {
				data, err := os.ReadFile(filepath.Join(root, role.name+"-"+suffix+".json"))
				if err != nil {
					if launcher != "" {
						t.Error("owned role receipt missing", role.name, suffix)
					}
					continue
				}
				var value any
				if json.Unmarshal(data, &value) != nil {
					t.Error("owned role receipt invalid", role.name, suffix)
					continue
				}
				entry[suffix] = value
			}
			result[role.name] = entry
		}
		if launcher == "" {
			result["resource_scope"] = "Independent CLI children; only outer helper-tree resource accounting, no per-role cgroup observations"
		}
		return result
	}
	return serverURL, stop, results
}

// Complete-line receipt timestamps are an external observation clock. Pipe
// serialization and Go scheduling contribute delay; this is not a DB ACK clock.
type corePerformanceRoleLogWriter struct {
	mu          sync.Mutex
	raw, stages *os.File
	pending     []byte
}

func (w *corePerformanceRoleLogWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.raw.Write(data); err != nil {
		return 0, err
	}
	w.pending = append(w.pending, data...)
	for {
		end := bytes.IndexByte(w.pending, '\n')
		if end < 0 {
			break
		}
		if end > 65536 {
			return 0, fmt.Errorf("owned role diagnostic line exceeds bound")
		}
		line := w.pending[:end]
		w.pending = w.pending[end+1:]
		if event, ok := corePerformanceParseRoleLog(string(line), time.Now()); ok {
			if err := json.NewEncoder(w.stages).Encode(event); err != nil {
				return 0, err
			}
		}
	}
	if len(w.pending) > 65536 {
		return 0, fmt.Errorf("owned role diagnostic line exceeds bound")
	}
	return len(data), nil
}
func (w *corePerformanceRoleLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	first := w.raw.Close()
	if err := w.stages.Close(); first == nil {
		first = err
	}
	return first
}

var corePerformanceLogFields = regexp.MustCompile(`(?:^| )([a-z_]+)=("(?:[^"\\]|\\.)*"|[^ ]+)`)
var corePerformanceDefaultLog = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} (?:DEBUG|INFO|WARN|ERROR) ([a-z_]+)(?: |$)`)

func corePerformanceParseRoleLog(line string, observed time.Time) (corePerfServerLog, bool) {
	fields := map[string]string{}
	for _, match := range corePerformanceLogFields.FindAllStringSubmatch(line, -1) {
		value := match[2]
		if strings.HasPrefix(value, `"`) {
			if decoded, err := strconv.Unquote(value); err == nil {
				value = decoded
			}
		}
		fields[match[1]] = value
	}
	if match := corePerformanceDefaultLog.FindStringSubmatch(line); len(match) > 1 {
		fields["msg"] = match[1]
	}
	switch fields["msg"] {
	case "job_processing_stopped", "job_claimed", "source_inspected", "source_streams_resolved", "source_preparation_finished", "media_processing_wait", "media_processing_finished", "artifact_publication_finished", "media_subprocess_finished":
	default:
		return corePerfServerLog{}, false
	}
	elapsed, _ := strconv.ParseInt(fields["elapsed_ms"], 10, 64)
	entry := corePerfServerLog{Job: fields["job_id"], At: observed, ElapsedMS: elapsed, Event: fields["msg"], Fields: map[string]any{}, TimestampSource: "complete_log_line_observed"}
	for _, key := range []string{"elapsed_ms", "claim_elapsed_ms", "network_bytes", "size_bytes", "cache_hit", "success", "reused", "scope", "tool", "item_id"} {
		if value, ok := fields[key]; ok {
			if n, err := strconv.ParseInt(value, 10, 64); err == nil {
				entry.Fields[key] = n
			} else if b, err := strconv.ParseBool(value); err == nil {
				entry.Fields[key] = b
			} else {
				entry.Fields[key] = value
			}
		}
	}
	return entry, true
}
func corePerformanceRoleLogs(t *testing.T, path string) []corePerfServerLog {
	t.Helper()
	observedPath := strings.TrimSuffix(path, "-cli.log") + "-observed-stages.jsonl"
	file, err := os.Open(observedPath)
	if err != nil {
		t.Fatal("owned observed stage log unavailable")
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 65536)
	var logs []corePerfServerLog
	for scanner.Scan() {
		var event corePerfServerLog
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			t.Fatal("owned observed stage log invalid")
		}
		logs = append(logs, event)
	}
	if scanner.Err() != nil {
		t.Fatal("owned observed stage log scan failed")
	}
	return logs
}
