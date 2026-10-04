package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMediaProgressIsBoundedMonotonicAndPrivate(t *testing.T) {
	var got []int64
	p := &mediaProgressWriter{totalMS: 3000, report: func(ms int64) { got = append(got, ms) }}
	// Exercise chunk boundaries, invalid integers, backward timestamps and
	// oversized untrusted diagnostics without retaining any subprocess text.
	for _, chunk := range []string{
		"out_time_", "us=1000000\nhttps://secret.example/?token=private\n",
		"out_time_us=-1000\nout_time_us=N/A\nout_time_us=9223372036854775808\n",
		"out_time_us=500000\nout_time_us=1000000\nout_time_us=2500123\n",
		strings.Repeat("x", 1<<20) + "out_time_us=2700000\n",
		"out_time_us=999999999\nout_time_us=4000000\nprogress=end\n",
	} {
		if n, err := p.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("short progress write: %d %v", n, err)
		}
	}
	if want := []int64{1000, 2500, 3000}; !reflect.DeepEqual(got, want) {
		t.Fatalf("progress %v; want %v", got, want)
	}
	if p.length != 0 || p.discard {
		t.Fatal("oversized line did not release parser state")
	}
}

func TestMediaExportReportsActualProgressAndStopsOnCancellation(t *testing.T) {
	c := mediaConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	source := filepath.Join(c.DataDir, "progress-source.mp4")
	if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25", "-t", "4", "-c:v", "libx264", "-threads", "1", source); err != nil {
		t.Fatal(err)
	}
	r := Range{StartMS: 1000, EndMS: 3000}
	options := ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate"}
	var got []int64
	out := filepath.Join(c.DataDir, "progress-result.mp4")
	start, end, err := exportInputsProgress(ctx, c, []mediaInput{{Path: source}}, r, options, out, func(ms int64) { got = append(got, ms) })
	if err != nil || start != r.StartMS || end != r.EndMS || len(got) == 0 {
		t.Fatalf("actual progress export: bounds %d-%d, progress %v, error %v", start, end, got, err)
	}
	for i, ms := range got {
		if ms <= 0 || ms > 2000 || (i > 0 && ms <= got[i-1]) {
			t.Fatalf("unbounded or backward progress: %v", got)
		}
	}
	// Cancellation from the progress consumer must stop the process and skip
	// output validation/publication, even if the encoder already wrote a file.
	cancelCtx, cancelExport := context.WithCancel(ctx)
	defer cancelExport()
	_, _, err = exportInputsProgress(cancelCtx, c, []mediaInput{{Path: source}}, r, options, filepath.Join(c.DataDir, "cancelled.mp4"), func(int64) { cancelExport() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("progress cancellation did not stop export: %v", err)
	}
}

func TestOnlyCachedUpstreamDenialsAreRefreshable(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want bool
	}{
		{&mediaProcessFailure{category: "upstream_denied"}, true},
		{&sourceProblem{"media_upstream_denied", "Refused"}, true},
		{&mediaProcessFailure{category: "network_timeout"}, false},
		{&mediaProcessFailure{category: "unknown"}, false},
		{errOutputLimit, false},
		{context.Canceled, false},
		{context.DeadlineExceeded, false},
	} {
		if got := cachedAddressDenied(tt.err); got != tt.want {
			t.Fatalf("refresh category %T: got %v; want %v", tt.err, got, tt.want)
		}
	}
}
