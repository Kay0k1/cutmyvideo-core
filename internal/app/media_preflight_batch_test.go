package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func preflightBatchFixture(t *testing.T) (Config, string) {
	t.Helper()
	c := preflightTestConfig(t)
	c.FFprobe = filepath.Join(filepath.Dir(c.FFmpeg), strings.Replace(filepath.Base(c.FFmpeg), "ffmpeg", "ffprobe", 1))
	if err := os.WriteFile(c.FFprobe, []byte("fresh inspection executable identity"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source.mp4")
	if err := os.WriteFile(path, []byte("source identity"), 0600); err != nil {
		t.Fatal(err)
	}
	return c, path
}

func preflightBatchRequests(count int) []ExportRequest {
	requests := make([]ExportRequest, count)
	for i := range requests {
		requests[i] = preflightTestRequest()
		requests[i].OutputMode = "joined"
		requests[i].Ranges = []Range{{StartMS: int64(i * 20), EndMS: int64(i*20 + 500), Label: "  independent clip  "}}
		if i%3 == 1 {
			requests[i].Crop = &Crop{X: .25, Y: 0, Width: .5, Height: 1}
		}
		if i%3 == 2 {
			requests[i].NoAudio = true
		}
	}
	return requests
}

func TestPreflightLocalBatchSharesFreshInspectionAndCapabilities(t *testing.T) {
	c, path := preflightBatchFixture(t)
	requests := preflightBatchRequests(15)
	before, _ := json.Marshal(requests)
	ctx, prior := BeginLocalExport(context.Background(), path)
	defer prior.Close()
	inspections := 0
	queries := make(map[string]int)
	inspect := func(ctx context.Context, _ Config, got string, remote bool) (probeInfo, int64, error) {
		inspections++
		if got != path || remote || localExportSession(ctx, path, false) == nil || localExportSession(ctx, path, false) == prior {
			t.Fatal("batch reused an earlier inspection session")
		}
		return preflightTestInfo(), 2000, nil
	}
	query := func(ctx context.Context, path string, args ...string) ([]byte, error) {
		queries[args[len(args)-1]]++
		return preflightTestQuery(ctx, path, args...)
	}
	results, failed, err := preflightLocalBatchWithQueries(ctx, c, path, requests, inspect, query)
	if err != nil || failed != -1 || len(results) != 15 || inspections != 1 || !reflect.DeepEqual(queries, map[string]int{"-encoders": 1, "-muxers": 1, "-filters": 1, "-demuxers": 1}) {
		t.Fatalf("one request did not share exactly one fresh source and four listings: inspect=%d queries=%v failed=%d err=%v", inspections, queries, failed, err)
	}
	for i, request := range requests {
		single, err := preflightLocalWithQueries(ctx, c, path, request, func(context.Context, Config, string, bool) (probeInfo, int64, error) {
			return preflightTestInfo(), 2000, nil
		}, preflightTestQuery)
		if err != nil || !reflect.DeepEqual(results[i], single) {
			t.Fatalf("clip %d changed its independent single-request report: batch=%+v single=%+v err=%v", i, results[i], single, err)
		}
	}
	after, _ := json.Marshal(requests)
	if string(before) != string(after) {
		t.Fatal("batch normalization modified caller requests")
	}
	results[0].Request.Ranges[0].Label = "changed"
	results[0].Video.Width = -1
	results[0].TrackManifest[0].Included = false
	results[0].Checks[0].Status = "changed"
	if results[1].Request.Ranges[0].Label != "independent clip" || results[1].Video.Width < 0 || !results[1].TrackManifest[0].Included || results[1].Checks[0].Status != "passed" {
		t.Fatal("separate clip reports shared mutable fields")
	}
	if _, _, err := preflightLocalBatchWithQueries(ctx, c, path, requests, inspect, query); err != nil || inspections != 2 || queries["-encoders"] != 2 || queries["-muxers"] != 2 || queries["-filters"] != 2 || queries["-demuxers"] != 2 {
		t.Fatalf("a second operation reused an earlier batch snapshot: inspect=%d queries=%v err=%v", inspections, queries, err)
	}
}

func TestPreflightLocalBatchPreservesClipErrorsAndOrdering(t *testing.T) {
	for _, tc := range []struct {
		name, missing, code string
		failed              int
		change              func([]ExportRequest)
	}{
		{"later_crop", "crop", "filter_unavailable", 1, nil},
		{"earlier_encoder_before_later_audio", "libx264", "encoder_unavailable", 0, func(r []ExportRequest) { index := 99; r[1].AudioTrack = &index }},
		{"later_audio", "", "audio_track_unavailable", 1, func(r []ExportRequest) { index := 99; r[1].AudioTrack = &index }},
		{"later_range", "", "invalid_argument", 1, func(r []ExportRequest) { r[1].Ranges[0].EndMS = 3000 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, path := preflightBatchFixture(t)
			requests := preflightBatchRequests(2)
			if tc.change != nil {
				tc.change(requests)
			}
			query := func(ctx context.Context, path string, args ...string) ([]byte, error) {
				data, err := preflightTestQuery(ctx, path, args...)
				var kept []string
				for _, line := range strings.Split(string(data), "\n") {
					fields := strings.Fields(line)
					if len(fields) > 1 && fields[1] == tc.missing {
						continue
					}
					kept = append(kept, line)
				}
				return []byte(strings.Join(kept, "\n")), err
			}
			results, failed, err := preflightLocalBatchWithQueries(context.Background(), c, path, requests, func(context.Context, Config, string, bool) (probeInfo, int64, error) {
				return preflightTestInfo(), 2000, nil
			}, query)
			if len(results) != 0 || failed != tc.failed || LocalErrorCode(err) != tc.code {
				t.Fatalf("wrong failed clip/error precedence: results=%d failed=%d code=%s err=%v", len(results), failed, LocalErrorCode(err), err)
			}
		})
	}
}

func TestPreflightLocalBatchDeferredFailuresAndCancellation(t *testing.T) {
	for _, failure := range []string{"timeout", "malformed", "oversized", "failed"} {
		t.Run(failure, func(t *testing.T) {
			c, path := preflightBatchFixture(t)
			queries := make(map[string]int)
			results, failed, err := preflightLocalBatchWithQueries(context.Background(), c, path, preflightBatchRequests(3), func(context.Context, Config, string, bool) (probeInfo, int64, error) {
				return preflightTestInfo(), 2000, nil
			}, func(ctx context.Context, path string, args ...string) ([]byte, error) {
				deadline, bounded := ctx.Deadline()
				if !bounded || time.Until(deadline) > 2*time.Second {
					t.Fatal("query lost its two-second bound")
				}
				kind := args[len(args)-1]
				queries[kind]++
				if kind != "-encoders" {
					return preflightTestQuery(ctx, path, args...)
				}
				switch failure {
				case "timeout":
					return nil, context.DeadlineExceeded
				case "oversized":
					return []byte(strings.Repeat("x", preflightCapabilityBytes+1)), nil
				case "failed":
					return preflightTestLists(kind), errors.New("private diagnostic must not escape")
				default:
					return []byte("unknown capability format"), nil
				}
			})
			if err != nil || failed != -1 || queries["-encoders"] != 1 || len(results) != 3 {
				t.Fatalf("deferred listing was rerun or failed the batch: %v %d %v", queries, failed, err)
			}
			for _, result := range results {
				encoded, _ := json.Marshal(result)
				if preflightCheckStatus(result, "tool_encoders") != "deferred" || strings.Contains(string(encoded), "private diagnostic") {
					t.Fatal("failed optional query certified capabilities or leaked diagnostic", result)
				}
			}
		})
	}
	c, path := preflightBatchFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	queries := 0
	results, failed, err := preflightLocalBatchWithQueries(ctx, c, path, preflightBatchRequests(3), func(context.Context, Config, string, bool) (probeInfo, int64, error) {
		return preflightTestInfo(), 2000, nil
	}, func(ctx context.Context, path string, args ...string) ([]byte, error) {
		queries++
		cancel()
		return preflightTestQuery(ctx, path, args...)
	})
	if !errors.Is(err, context.Canceled) || failed != 0 || queries != 1 || len(results) != 0 {
		t.Fatalf("cancellation was deferred or produced reports: queries=%d failed=%d results=%d err=%v", queries, failed, len(results), err)
	}
}

func replacePreflightIdentity(t *testing.T, path string) {
	t.Helper()
	before, err := preflightFileIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := path + ".replacement"
	if err := os.WriteFile(replacement, data, before.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightLocalBatchRejectsChangedSourceAndTools(t *testing.T) {
	for _, target := range []string{"source", "ffprobe", "ffmpeg"} {
		for _, mutation := range []string{"identity", "content"} {
			t.Run(target+"/"+mutation, func(t *testing.T) {
				c, path := preflightBatchFixture(t)
				before, err := preflightFileIdentity(path)
				if err != nil {
					t.Fatal(err)
				}
				probeTool, err := pinPreflightExecutable(c.FFprobe)
				if err != nil {
					t.Fatal(err)
				}
				scope := preflightBatchScope{path: path, source: before, probe: probeTool}
				result, groups, err := prepareLocalPreflight(c, path, preflightBatchRequests(1)[0], preflightTestInfo(), 2000)
				if err != nil {
					t.Fatal(err)
				}
				wanted := make(map[string]map[string]bool)
				for _, group := range groups {
					wanted[group.listing] = make(map[string]bool)
					for _, name := range group.required {
						wanted[group.listing][name] = true
					}
				}
				snapshot := preflightCapabilitySnapshot{scope: &scope, wanted: wanted, listings: make(map[string]preflightCapabilityListing)}
				queries := 0
				query := func(ctx context.Context, path string, args ...string) ([]byte, error) {
					queries++
					return preflightTestQuery(ctx, path, args...)
				}
				if err := snapshot.check(context.Background(), c, groups, &result, query); err != nil || queries != 4 {
					t.Fatalf("could not establish fresh snapshot: queries=%d err=%v", queries, err)
				}
				changed := path
				code := "source_changed"
				if target == "ffprobe" {
					changed, code = c.FFprobe, "tool_unavailable"
				} else if target == "ffmpeg" {
					changed, code = c.FFmpeg, "tool_unavailable"
				}
				if mutation == "identity" {
					replacePreflightIdentity(t, changed)
				} else if err := os.WriteFile(changed, []byte("changed with a different length"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := snapshot.check(context.Background(), c, groups, &PreflightResult{}, query); LocalErrorCode(err) != code || queries != 4 {
					t.Fatalf("reused snapshot after %s changed: code=%s queries=%d err=%v", target, LocalErrorCode(err), queries, err)
				}
			})
		}
	}
	for _, target := range []string{"source", "ffprobe", "ffmpeg"} {
		t.Run("during_initial_"+target, func(t *testing.T) {
			c, path := preflightBatchFixture(t)
			code := "tool_unavailable"
			queries := 0
			results, failed, err := preflightLocalBatchWithQueries(context.Background(), c, path, preflightBatchRequests(3), func(context.Context, Config, string, bool) (probeInfo, int64, error) {
				if target == "source" {
					code = "source_changed"
					replacePreflightIdentity(t, path)
				} else if target == "ffprobe" {
					replacePreflightIdentity(t, c.FFprobe)
				}
				return preflightTestInfo(), 2000, nil
			}, func(ctx context.Context, path string, args ...string) ([]byte, error) {
				queries++
				if target == "ffmpeg" {
					replacePreflightIdentity(t, c.FFmpeg)
				}
				return preflightTestQuery(ctx, path, args...)
			})
			if len(results) != 0 || failed != 0 || LocalErrorCode(err) != code || queries > 1 {
				t.Fatalf("mixed source/tool snapshot was certified: results=%d failed=%d code=%s queries=%d err=%v", len(results), failed, LocalErrorCode(err), queries, err)
			}
		})
	}
}

func TestPreflightLocalBatchRetainsOnlyRelevantCapabilityNames(t *testing.T) {
	c, path := preflightBatchFixture(t)
	before, err := preflightFileIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	probeTool, err := pinPreflightExecutable(c.FFprobe)
	if err != nil {
		t.Fatal(err)
	}
	scope := preflightBatchScope{path: path, source: before, probe: probeTool}
	snapshot := preflightCapabilitySnapshot{scope: &scope, wanted: map[string]map[string]bool{"encoders": {"libx264": true, "aac": true}}, listings: make(map[string]preflightCapabilityListing)}
	groups := []preflightCapabilityGroup{{listing: "encoders", code: "encoder_unavailable", required: []string{"libx264", "aac"}}}
	queries := 0
	query := func(context.Context, string, ...string) ([]byte, error) {
		queries++
		return append(preflightTestLists("-encoders"), []byte(strings.Repeat(" V..... irrelevant_codec Long irrelevant encoder description\n", 10000))...), nil
	}
	if err := snapshot.check(context.Background(), c, groups, &PreflightResult{}, query); err != nil {
		t.Fatal(err)
	}
	listing := snapshot.listings["encoders"]
	if listing.deferred || len(listing.available) != 2 || !listing.available["libx264"] || !listing.available["aac"] {
		t.Fatal("snapshot retained unrelated parsed names or lost required names", listing)
	}
	if err := snapshot.check(context.Background(), c, groups, &PreflightResult{}, query); err != nil || queries != 1 {
		t.Fatal("relevant parsed snapshot was not reused", queries, err)
	}
}
