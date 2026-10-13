//go:build linux

package app

// Overlay this identical, opt-in diagnostic into baseline and candidate. It
// evaluates packet-counting cost and actual probe resources without changing
// producer ceilings or inventing a container-overhead bound.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type copyDiagnosticInput struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

func diagnosticFileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err = io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func TestCoreCopyBudgetDiagnostic(t *testing.T) {
	manifestPath := os.Getenv("CUTMY_CORE_PERF_COPY_MANIFEST")
	output := os.Getenv("CUTMY_CORE_PERF_COPY_RESULT")
	if manifestPath == "" || output == "" {
		t.Skip("requires a frozen manifest and exclusive task-owned result path")
	}
	if os.Getenv("CUTMY_CORE_PERF_IDLE_WINDOW") == "" {
		t.Fatal("coordinator idle window is required")
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Fixtures  map[string]copyDiagnosticInput `json:"fixtures"`
		Tools     map[string]copyDiagnosticInput `json:"tools"`
		Scenarios []struct {
			ID, Fixture string
			Ranges      []Range
			OutputMode  string `json:"output_mode"`
			Options     struct {
				Format  string
				CutMode string `json:"cut_mode"`
			}
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile(strings.TrimSuffix(manifestPath, filepath.Ext(manifestPath)) + ".sha256")
	digest := sha256.Sum256(data)
	if err != nil || strings.TrimSpace(string(expected)) != hex.EncodeToString(digest[:]) {
		t.Fatal("frozen manifest digest mismatch")
	}
	tool := manifest.Tools["ffprobe"]
	if got, err := diagnosticFileHash(tool.Path); err != nil || got != tool.SHA256 {
		t.Fatal("frozen ffprobe bytes changed")
	}
	inputs := map[string]copyDiagnosticInput{"long": manifest.Fixtures["long"], "complex": manifest.Fixtures["complex"]}
	if extra := os.Getenv("CUTMY_CORE_PERF_COPY_EXTRA_INPUT"); extra != "" {
		var input copyDiagnosticInput
		if err := json.Unmarshal([]byte(extra), &input); err != nil {
			t.Fatal("extra input requires recorded path/hash/size")
		}
		inputs["8k-probe"] = input
	}
	var observations []map[string]any
	defer func() {
		result := map[string]any{"manifest_sha256": hex.EncodeToString(digest[:]), "idle_window": os.Getenv("CUTMY_CORE_PERF_IDLE_WINDOW"), "complete": !t.Failed(), "samples": observations,
			"scope": "Full-file packet counting and actual production inspection flags. No mux overhead proof; no product budget change. Hashing is outside timers; OS caches remain warm. Child RSS is waited Linux maxrss, not Go RSS or concurrent tree memory."}
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			t.Error(err)
			return
		}
		file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Error("exclusive diagnostic publication failed", err)
			return
		}
		if _, err := file.Write(append(encoded, '\n')); err != nil {
			t.Error(err)
		}
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	c := Config{FFprobe: tool.Path, FFmpegThreads: 2, MaxSourceBytes: 32 << 30, MaxFetchBytes: 32 << 30, MaxOutputBytes: 16 << 30}
	for _, name := range []string{"long", "complex", "8k-probe"} {
		input, present := inputs[name]
		if !present {
			continue
		}
		original, err := os.Stat(input.Path)
		if err != nil || !original.Mode().IsRegular() || original.Size() != input.Bytes {
			t.Fatal("frozen input size/identity mismatch", name)
		}
		if got, err := diagnosticFileHash(input.Path); err != nil || got != input.SHA256 {
			t.Fatal("frozen input bytes changed", name)
		}
		ceilings := make(map[string]int64)
		for _, scenario := range manifest.Scenarios {
			if scenario.Fixture != name || scenario.Options.CutMode != "copy" {
				continue
			}
			request := ExportRequest{Format: scenario.Options.Format, CutMode: "copy", Quality: "best", Ranges: scenario.Ranges}
			var ceiling int64
			if scenario.OutputMode == "joined" {
				ceiling = joinedOutputBudget(c, request)
			} else {
				for _, selected := range scenario.Ranges {
					ceiling += outputBudget(c, selected, request)
				}
			}
			ceilings[scenario.ID] = ceiling
		}
		for sample := range 3 {
			for _, mode := range []string{"inspection", "count-packets"} {
				args := []string{"-v", "error", "-max_alloc", "268435456", "-protocol_whitelist", "file", "-format_whitelist", mediaFormats}
				args = append(args, mediaInputBounds(c)...)
				fields := mediaProbeEntries
				if mode == "count-packets" {
					args = append(args, "-count_packets")
					fields = "stream=index,codec_type,codec_name,nb_read_packets"
				}
				args = append(args, "-show_entries", fields, "-show_data_hash", "sha256", "-of", "json", input.Path)
				ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
				cmd := exec.CommandContext(ctx, tool.Path, args...)
				cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
				cmd.WaitDelay = 2 * time.Second
				stdout, stderr := &limitedBuffer{limit: 1 << 20}, &limitedBuffer{limit: 16 << 10}
				cmd.Stdout, cmd.Stderr = stdout, stderr
				started := time.Now()
				err := runProcess(cmd)
				elapsed := time.Since(started)
				cancel()
				observation := map[string]any{"fixture": name, "mode": mode, "sample": sample, "source_bytes": input.Bytes, "wall_seconds": elapsed.Seconds(), "current_output_ceilings_bytes": ceilings, "output_limit_bytes": c.MaxOutputBytes, "command": args, "successful": err == nil}
				if cmd.ProcessState != nil {
					observation["child_user_seconds"], observation["child_system_seconds"] = cmd.ProcessState.UserTime().Seconds(), cmd.ProcessState.SystemTime().Seconds()
					if usage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
						observation["child_maxrss_bytes"] = usage.Maxrss * 1024
						observation["input_blocks"], observation["output_blocks"] = usage.Inblock, usage.Oublock
					}
				}
				observations = append(observations, observation)
				if err != nil {
					t.Fatal("diagnostic ffprobe failed", name, mode)
				}
				var payload struct {
					Streams []struct {
						Index     int
						CodecName string `json:"codec_name"`
						CodecType string `json:"codec_type"`
						Count     string `json:"nb_read_packets"`
					}
				}
				if err := json.Unmarshal(stdout.buffer.Bytes(), &payload); err != nil {
					t.Fatal("diagnostic response is invalid", err)
				}
				if mode == "count-packets" {
					counts := make(map[int]int64)
					for _, stream := range payload.Streams {
						count, err := strconv.ParseInt(stream.Count, 10, 64)
						if err != nil || count < 0 {
							t.Fatal("packet count unavailable", name, stream.Index)
						}
						counts[stream.Index] = count
					}
					observation["packet_counts"] = counts
				}
				after, err := os.Stat(input.Path)
				if err != nil || !os.SameFile(original, after) || original.Size() != after.Size() || !original.ModTime().Equal(after.ModTime()) {
					t.Fatal("input changed during diagnostics", name)
				}
			}
		}
	}
}
