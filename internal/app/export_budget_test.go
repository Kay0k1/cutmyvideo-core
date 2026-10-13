package app

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPlatformHLSCacheReservationIsBoundedSharedAndDisabledOnSmallStores(t *testing.T) {
	request := ExportRequest{Format: "mp4", Quality: "720p", CutMode: "accurate", Ranges: []Range{{EndMS: 1000}, {StartMS: 2000, EndMS: 3000}}}
	for _, mode := range []string{"", "joined"} {
		request.OutputMode = mode
		job := Job{Request: request, Items: jobItems(request)}
		for _, storage := range []int64{0, 16 << 20, 256 << 20, 512 << 20, 1 << 30, 96 << 30} {
			c := Config{MaxStorageBytes: storage, MaxOutputBytes: 4 << 20, MaxSourceBytes: 4 << 20, MaxFetchBytes: 4 << 20}
			media, err := remainingJobMediaReserve(c, job, true)
			if err != nil {
				t.Fatal(err)
			}
			reserved, err := remainingJobReserve(c, job, true)
			if err != nil || reserved-media != hlsJobCacheBudget(c) || reserved-media > hlsJobCacheBytes || storage <= 256<<20 && reserved != media {
				t.Fatalf("HLS cache charge disagrees: mode=%s storage=%d media=%d reserve=%d %v", mode, storage, media, reserved, err)
			}
			local, err := remainingJobReserve(c, job, false)
			localMedia, mediaErr := remainingJobMediaReserve(c, job, false)
			if err != nil || mediaErr != nil || local != localMedia {
				t.Fatal("local export was charged for a remote cache", local, localMedia, err, mediaErr)
			}
		}
		for i := range job.Items {
			job.Items[i].Status = "succeeded"
			job.Items[i].Artifact = &Artifact{ID: "complete"}
		}
		if reserved, err := remainingJobReserve(Config{MaxStorageBytes: 1 << 30}, job, true); err != nil || reserved != 0 {
			t.Fatal("finished recovery job retained a new cache charge", reserved, err)
		}
	}
}

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
	sourceFile, err := os.Stat(source.Path)
	if err != nil {
		t.Fatal(err)
	}
	sourceBytes, initialReserved := ledgerBytes(t, s)
	if sourceFile.Size() <= 0 || sourceBytes != sourceFile.Size() || initialReserved != 0 {
		t.Fatal("completed source is not charged exactly once", sourceBytes, sourceFile.Size(), initialReserved)
	}
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
	if files-sourceBytes != 4096 || after != second {
		t.Fatalf("publication lost storage accounting: source=%d files=%d remaining=%d", sourceBytes, files, after)
	}
}

func TestAccurateJoinReservationOnlyIncludesActualProducers(t *testing.T) {
	c := Config{MaxOutputBytes: 16 << 30, MaxSourceBytes: 32 << 30, MaxFetchBytes: 32 << 30}
	r := ExportRequest{Format: "mp4", Quality: "1080p", CutMode: "accurate", OutputMode: "joined", Ranges: []Range{{StartMS: 3600_000, EndMS: 3602_000}, {StartMS: 1200_000, EndMS: 1203_000}}}
	for _, platform := range []bool{false, true} {
		for _, subtitles := range []bool{false, true} {
			request := r
			if subtitles {
				request.SubtitleMode = "preserve"
			}
			expected := joinedOutputBudget(c, request)
			subtitleBytes, err := subtitleWorkBudget(c, request, expected)
			if err != nil {
				t.Fatal(err)
			}
			expected += subtitleBytes
			if platform {
				for _, selected := range request.Ranges {
					expected += 2 * inputBudget(c, selected, request)
				}
			}
			reserve, err := joinedWorkBudget(c, request, platform)
			if err != nil || reserve != expected {
				t.Fatalf("accurate join reserved fictional segments or lost real producers: %d != %d, %v", reserve, expected, err)
			}
		}
	}
}

func TestAccurateJoinCanBeClaimedWithRoomForItsRealOutput(t *testing.T) {
	s := testStore(t)
	c := ledgerConfig(t)
	c.MaxOutputBytes = 16 << 30
	c.MaxStorageBytes = 32 << 20
	c.MaxOwnerBytes = 256 << 20
	s.ConfigureStorage(c)
	source := storedSource(t, s, "accurate-join-budget")
	request := requestFor(source)
	request.OutputMode = "joined"
	request.Ranges = []Range{{StartMS: 5000, EndMS: 6000}, {StartMS: 1000, EndMS: 2000}}
	oldReserve := joinedOutputBudget(c, request)
	for _, selected := range request.Ranges {
		oldReserve += outputBudget(c, selected, request)
	}
	if oldReserve <= c.MaxStorageBytes {
		t.Fatal("fixture does not reproduce the former excessive reservation")
	}
	if _, err := s.CreateJobLimited(context.Background(), source.Owner, request, "", 3, 32); err != nil {
		t.Fatalf("real output fits but admission rejected accurate join: %v", err)
	}
	job, _, err := s.Claim(context.Background())
	if err != nil || job.Request.OutputMode != "joined" {
		t.Fatalf("real output fits but accurate join could not claim: %+v, %v", job, err)
	}
	_, reserve := ledgerBytes(t, s)
	if reserve != joinedOutputBudget(c, request) {
		t.Fatalf("claim retained fictional intermediate files: %d", reserve)
	}
}

func TestSeparateSubtitleReservationChargesSharedSourceTextOnce(t *testing.T) {
	c := Config{MaxOutputBytes: 16 << 30}
	request := ExportRequest{Format: "mkv", Quality: "best", CutMode: "copy", SubtitleMode: "preserve", Ranges: []Range{{EndMS: 1000}, {StartMS: 1000, EndMS: 2000}, {StartMS: 2000, EndMS: 3000}}}
	job := Job{Request: request, Items: jobItems(request)}
	for done := range len(job.Items) + 1 {
		var expected int64
		for i, r := range request.Ranges {
			if i < done {
				job.Items[i].Status = "succeeded"
				job.Items[i].Artifact = &Artifact{ID: "retained"}
				continue
			}
			expected += 2*outputBudget(c, r, request) + subtitleTrackAllowance
		}
		if done < len(job.Items) {
			expected += subtitleTrackAllowance
		}
		got, err := remainingJobReserve(c, job, false)
		if err != nil || got != expected {
			t.Fatalf("%d completed: repeated source copies or missing unfinished producers: %d != %d, %v", done, got, expected, err)
		}
	}
}
