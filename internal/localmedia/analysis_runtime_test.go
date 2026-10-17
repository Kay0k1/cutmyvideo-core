package localmedia

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
)

func TestAnalysisRuntimeCancelsLiveDecoderAndBudgetRefusal(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	o, _ := silenceOptions(SilenceOptions{})
	c := Config{FFmpeg: "ffmpeg"}
	// The decoder deliberately runs at realtime, proving that cancellation
	// interrupts a running producer instead of waiting for a completed fixture.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	started := time.Now()
	err := runAnalysis(ctx, c, []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-re", "-f", "lavfi", "-i", "sine=sample_rate=48000", "-t", "10", "-c:a", "pcm_f32le", "-f", "f32le", "pipe:1"}, newSilencePCM(0, 10000, 1, o))
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 3*time.Second {
		t.Fatal("live decoder did not honor deadline", err, time.Since(started))
	}
	// Output refusal has to cancel the producer too; a reader error alone leaves
	// os/exec's child blocked on its pipe. Its original limit error must survive.
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started = time.Now()
	err = runAnalysis(ctx, c, []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-re", "-f", "lavfi", "-i", "sine=sample_rate=48000", "-t", "10", "-c:a", "pcm_f32le", "-f", "f32le", "pipe:1"}, newSilencePCM(0, 10, 1, o))
	if mediaerror.LocalErrorCode(err) != "output_limit" || time.Since(started) > 3*time.Second {
		t.Fatal("writer refusal did not stop producer or lost cause", err, time.Since(started))
	}
}
