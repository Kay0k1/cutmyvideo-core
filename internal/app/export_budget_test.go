package app

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLongExportDefaultsAndAdmission(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://configuration-only")
	c, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	for _, duration := range []int64{42 * 60_000, 35_270_274, 12 * 60 * 60_000} {
		r := ExportRequest{Format: "mp4", Quality: "1080p", CutMode: "accurate", Ranges: []Range{{EndMS: duration}}}
		if err := r.Validate(c, Source{DurationMS: duration}); err != nil {
			t.Fatalf("long recording rejected (%d ms): %v", duration, err)
		}
		reserve, err := remainingJobReserve(c, Job{Request: r, Items: []JobItem{{EndMS: duration}}}, true)
		if err != nil || reserve > c.MaxStorageBytes {
			t.Fatalf("admitted range cannot fit default storage: %d, %v", reserve, err)
		}
	}
	if c.JobTimeout < 12*time.Hour || c.MaxOutputBytes < 16<<30 || c.MaxFetchBytes < 32<<30 {
		t.Fatal("long ranges still have short processing or byte ceilings")
	}
}

func TestExportReservationsFollowDurationAndReleasePublishedItems(t *testing.T) {
	c := Config{MaxOutputBytes: 16 << 30, MaxSourceBytes: 32 << 30, MaxFetchBytes: 32 << 30}
	r := ExportRequest{Format: "mp4", Quality: "1080p", CutMode: "accurate"}
	j := Job{Request: r, Items: make([]JobItem, 32)}
	for i := range j.Items {
		j.Items[i].EndMS = 10_000
	}
	reserve, err := remainingJobReserve(c, j, true)
	if err != nil || reserve >= 2<<30 {
		t.Fatalf("short batch reserved large-file ceilings: %d, %v", reserve, err)
	}
	j.Items[0].Status = "succeeded"
	j.Items[0].Artifact = &Artifact{ID: "published"}
	after, err := remainingJobReserve(c, j, true)
	if err != nil || reserve-after != outputBudget(c, Range{EndMS: 10_000}, r) {
		t.Fatalf("published item did not release its matching budget: %d -> %d, %v", reserve, after, err)
	}
	long := Range{EndMS: 35_270_274}
	if rate := exportVideoBitrate(c, long, r); rate < 1_000_000 || rate >= 8_000_000 {
		t.Fatalf("long encoding did not adapt to output ceiling: %d", rate)
	}
	if budget := outputBudget(c, long, r); budget <= 1<<30 || budget > c.MaxOutputBytes {
		t.Fatalf("long output budget: %d", budget)
	}
	if got := boundedMediaBytes(math.MaxInt64, math.MaxInt64, 1024, c.MaxOutputBytes); got != c.MaxOutputBytes {
		t.Fatal("overflow reduced the reservation")
	}
}

func TestMediaExportsOverAnHourAccurateAndCopy(t *testing.T) {
	c := mediaConfig(t)
	c.MaxOutputBytes = 16 << 30
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	source := filepath.Join(c.DataDir, "seventy-minutes.mp4")
	if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "color=navy:size=160x90:rate=1", "-t", "4200", "-c:v", "libx264", "-preset", "ultrafast", "-threads", "1", "-g", "2", "-an", source); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"accurate", "copy"} {
		t.Run(mode, func(t *testing.T) {
			out := filepath.Join(c.DataDir, mode+".mp4")
			r := Range{StartMS: 60_000, EndMS: 3_960_000}
			start, end, err := exportInputs(ctx, c, []mediaInput{{Path: source}}, r, ExportRequest{Format: "mp4", Quality: "1080p", CutMode: mode}, out)
			if err != nil || start != r.StartMS || end != r.EndMS {
				t.Fatalf("65-minute export failed or truncated: %d-%d, %v", start, end, err)
			}
			if stat, err := os.Stat(out); err != nil || stat.Size() == 0 {
				t.Fatalf("no exported media: %v", err)
			}
		})
	}
}

func TestDurationBasedBudgetIsUsedForAdmissionClaimAndPublication(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	c.MaxOutputBytes = 16 << 30
	c.MaxStorageBytes = 256 << 20
	s.ConfigureStorage(c)
	ctx := context.Background()
	source := storedSource(t, s, "duration-budget-owner")
	request := requestFor(source)
	request.Ranges = []Range{{StartMS: 1000, EndMS: 3000}, {StartMS: 1000, EndMS: 5000}}
	if _, err := s.CreateJobLimited(ctx, source.Owner, request, "", 3, 32); err != nil {
		t.Fatalf("short job rejected because large output cap was multiplied: %v", err)
	}
	job, token, err := s.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, before := ledgerBytes(t, s)
	first := outputBudget(c, request.Ranges[0], request)
	second := outputBudget(c, request.Ranges[1], request)
	if before != first+second {
		t.Fatalf("claim disagrees with duration budget: %d != %d + %d", before, first, second)
	}
	path := writeLedgerFile(t, c, "artifacts", "short.mp4", 4096)
	artifact := Artifact{ID: newID("art"), Filename: "short.mp4", SizeBytes: 4096, ActualStartMS: 1000, ActualEndMS: 3000}
	if err := s.PublishArtifact(ctx, publicationSnapshot(job, artifact), path, token, artifact); err != nil {
		t.Fatalf("publication still tried to debit the global output ceiling: %v", err)
	}
	files, after := ledgerBytes(t, s)
	if files != 4096 || after != second {
		t.Fatalf("publication lost storage accounting: files=%d remaining=%d", files, after)
	}
}
