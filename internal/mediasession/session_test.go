package mediasession

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

func pinnedSession(t *testing.T) *Session {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, []byte("original local source"), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(path)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func pinSession(t *testing.T, s *Session) {
	t.Helper()
	if _, err := s.Inspect(context.Background(), func(context.Context, string, bool) (mediainfo.Info, error) {
		return mediainfo.Info{}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionPreservesProbeIdentityAndCancellation(t *testing.T) {
	s := pinnedSession(t)
	ctx := context.WithValue(context.Background(), struct{}{}, "original context")
	var mu sync.Mutex
	calls := 0
	inspect := func(got context.Context, path string, remote bool) (mediainfo.Info, error) {
		if got != ctx || path != s.Path || remote {
			t.Error("inspection arguments changed")
		}
		mu.Lock()
		calls++
		mu.Unlock()
		return mediainfo.Info{Streams: []mediainfo.Stream{{Index: 3, CodecType: "audio"}}}, nil
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			p, err := s.Inspect(ctx, inspect)
			if err != nil || len(p.Streams) != 1 || p.Streams[0].Index != 3 {
				t.Error("shared probe result changed", p, err)
			}
		}()
	}
	workers.Wait()
	if calls != 1 {
		t.Fatal("one operation inspected the same source more than once", calls)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Inspect(cancelled, inspect); err != context.Canceled || calls != 1 {
		t.Fatal("cached metadata bypassed cancellation", err)
	}
	if err := os.WriteFile(s.Path, []byte("changed source with a different size"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Inspect(ctx, inspect); err == nil || calls != 1 {
		t.Fatal("changed source reused metadata or was reprobed", err)
	} else if p, ok := err.(*mediainfo.Problem); !ok || p.Code != "source_changed" {
		t.Fatal("source identity error changed", err)
	}

	failed := pinnedSession(t)
	cause := errors.New("original probe cause")
	if _, err := failed.Inspect(ctx, func(context.Context, string, bool) (mediainfo.Info, error) {
		return mediainfo.Info{}, cause
	}); err != cause || failed.File != nil {
		t.Fatal("failed probe was wrapped or retained", err)
	}
	mutated := pinnedSession(t)
	if _, err := mutated.Inspect(ctx, func(context.Context, string, bool) (mediainfo.Info, error) {
		if err := os.WriteFile(mutated.Path, []byte("changed while probing with a different byte count"), 0600); err != nil {
			t.Fatal(err)
		}
		return mediainfo.Info{}, nil
	}); err == nil || mutated.File != nil {
		t.Fatal("source changed during inspection was certified", err)
	}
}

func TestSessionPreservesBoundedKeyframeLookahead(t *testing.T) {
	s := pinnedSession(t)
	s.SetRanges([]edit.Range{{StartMS: -1}, {StartMS: 1000}, {StartMS: 4000}, {StartMS: 16000}, {StartMS: 16001}})
	var calls [][2]int64
	probe := func(ctx context.Context, path string, remote bool, stream int, from, until int64) ([]mediaexport.Keyframe, error) {
		if path != s.Path || remote || stream != 2 {
			t.Fatal("keyframe probe identity changed")
		}
		calls = append(calls, [2]int64{from, until})
		if until > 1001 {
			return nil, errors.New("damaged optional lookahead")
		}
		return []mediaexport.Keyframe{{PTS: 0, DTS: -80, HasDTS: true}}, nil
	}
	got, err := s.NearestKeyframe(context.Background(), "probe-tool", 1000, 2, probe)
	if err != nil || got.PTS != 0 || got.DTS != -80 || len(calls) != 2 || calls[0] != [2]int64{0, 16001} || calls[1] != [2]int64{0, 1001} {
		t.Fatal("bounded optional lookahead changed", got, calls, err)
	}
	if _, err := s.NearestKeyframe(context.Background(), "probe-tool", 1000, 2, probe); err != nil || len(calls) != 2 {
		t.Fatal("bounded fallback was not reusable", calls, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.NearestKeyframe(cancelled, "probe-tool", 1000, 2, probe); err != context.Canceled || len(calls) != 2 {
		t.Fatal("keyframe cache ignored cancellation", err)
	}
	s.SetRanges(nil)
	for i := int64(1); i <= 40; i++ {
		start := i * 60000
		_, err := s.NearestKeyframe(context.Background(), fmt.Sprint(i), start, 3,
			func(context.Context, string, bool, int, int64, int64) ([]mediaexport.Keyframe, error) {
				return []mediaexport.Keyframe{{PTS: start, DTS: start, HasDTS: true}}, nil
			})
		if err != nil {
			t.Fatal(err)
		}
	}
	entries := 0
	for _, window := range s.Keyframes {
		entries += len(window.Frames)
	}
	if len(s.Keyframes) != 16 || entries > 4096 {
		t.Fatal("keyframe cache bounds changed", len(s.Keyframes), entries)
	}
	oversized := make([]mediaexport.Keyframe, 4097)
	oversized[0] = mediaexport.Keyframe{PTS: 9000000, DTS: 9000000, HasDTS: true}
	_, err = s.NearestKeyframe(context.Background(), "oversized", 9000000, 0,
		func(context.Context, string, bool, int, int64, int64) ([]mediaexport.Keyframe, error) {
			return oversized, nil
		})
	if err != nil || len(s.Keyframes) != 16 || s.Keyframes[0].Tool == "oversized" {
		t.Fatal("oversized keyframe result entered the cache", err)
	}
}

func TestSessionPreservesSubtitleCacheOwnershipAndCleanup(t *testing.T) {
	s := pinnedSession(t)
	pinSession(t, s)
	s.SetWorkspace(t.TempDir())
	data := []byte("1\r\n00:00:00,500 --> 00:00:02,000\r\nsource text\r\n")
	extractions := 0
	extract := func() ([]byte, error) {
		return s.ExtractText(context.Background(), 3, func() ([]byte, error) {
			extractions++
			return data, nil
		})
	}
	first, err := s.SourceSubtitleIndex(context.Background(), 3, "ffmpeg-a", extract)
	if err != nil || len(first.Cues) != 1 || first.Cues[0].StartMS != 500 {
		t.Fatal("source-clock subtitle parse changed", first, err)
	}
	second, err := s.SourceSubtitleIndex(context.Background(), 3, "ffmpeg-a", extract)
	if err != nil || second != first || extractions != 1 {
		t.Fatal("one bounded track was not reused", extractions, err)
	}
	third, err := s.SourceSubtitleIndex(context.Background(), 3, "ffmpeg-b", extract)
	if err != nil || third == first || extractions != 1 || s.SubtitleTool != "ffmpeg-b" {
		t.Fatal("tool-keyed index or disk text reuse changed", extractions, err)
	}
	// Index construction calls back into ExtractText, which takes the same
	// mutex. Keeping parsing outside that mutex is required for this to finish.
	if _, err := s.SourceSubtitleIndex(context.Background(), 4, "ffmpeg-b", func() ([]byte, error) {
		return s.ExtractText(context.Background(), 4, func() ([]byte, error) { return data, nil })
	}); err != nil || s.SubtitleTrack != 4 {
		t.Fatal("second track could not replace the single resident index", err)
	}
	large := []byte("1\n00:00:00,000 --> 00:00:01,000\n" + strings.Repeat("x", 2<<20))
	if _, err := s.SourceSubtitleIndex(context.Background(), 5, "ffmpeg-b", func() ([]byte, error) { return large, nil }); err != nil || s.SubtitleIndex != nil {
		t.Fatal("large source text retained an unbounded parsed index", err)
	}
	cueDir := s.CueDir
	if cueDir == "" {
		t.Fatal("text was not staged inside the owned workspace")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cueDir); !os.IsNotExist(err) || len(s.Cues) != 0 || s.SubtitleIndex != nil || s.Keyframes != nil || s.RangeStarts != nil {
		t.Fatal("session close left owned staging or indexes", err)
	}
}

func TestSessionReusePreservesIndependentFilesAndFallback(t *testing.T) {
	s := pinnedSession(t)
	pinSession(t, s)
	dir := t.TempDir()
	prior, output := filepath.Join(dir, "prior"), filepath.Join(dir, "output")
	if err := os.WriteFile(prior, []byte("verified result bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Stat(prior)
	if err != nil {
		t.Fatal(err)
	}
	lookup := func() *Session { return s }
	if ok, err := Reuse(context.Background(), lookup, prior, output, expected, 1024); !ok || err != nil {
		t.Fatal("verified result was not copied", ok, err)
	}
	actual, err := os.Stat(output)
	if err != nil || os.SameFile(expected, actual) {
		t.Fatal("duplicate output aliases the original inode", err)
	}
	if err := os.WriteFile(output, []byte("edited duplicate"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(prior); err != nil || string(data) != "verified result bytes" {
		t.Fatal("editing one output changed another", string(data), err)
	}
	if ok, err := Reuse(context.Background(), lookup, prior, output, expected, 1024); ok || !errors.Is(err, os.ErrExist) {
		t.Fatal("reuse overwrote an existing output", ok, err)
	}
	if ok, err := Reuse(context.Background(), lookup, prior, filepath.Join(dir, "too-small"), expected, 1); ok || err != nil {
		t.Fatal("ineligible reuse did not fall back", ok, err)
	}
	if err := os.Rename(prior, prior+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prior, []byte("verified result bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	fallback := filepath.Join(dir, "fallback")
	if ok, err := Reuse(context.Background(), lookup, prior, fallback, expected, 1024); ok || err != nil {
		t.Fatal("replaced published result was reused", ok, err)
	}
	if _, err := os.Stat(fallback); !os.IsNotExist(err) {
		t.Fatal("ineligible result left an output", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if ok, err := Reuse(cancelled, func() *Session { t.Fatal("lookup preceded cancellation"); return nil }, prior, fallback, expected, 1024); ok || err != context.Canceled {
		t.Fatal("cancellation precedence changed", ok, err)
	}
}
