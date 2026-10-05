package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHighResolutionAndUnknownVideoOwnWholePool(t *testing.T) {
	for _, source := range []Source{{Path: "local", Width: 7680, Height: 4320}, {Path: "local"}, {Path: "local", Width: int(^uint(0) >> 1), Height: 2}} {
		ctx := withMediaBudget(context.Background(), 3)
		release, err := acquireMediaProcessing(ctx, source, nil, ExportRequest{Format: "mp4"})
		if err != nil {
			t.Fatal(err)
		}
		bounded, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		other, err := acquireMediaProcessing(bounded, Source{Path: "local", Width: 1920, Height: 1080}, nil, ExportRequest{Format: "mp4"})
		cancel()
		if other != nil {
			other()
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			release()
			t.Fatal("wide/unknown video did not own decoder budget", err)
		}
		release()
		next, err := acquireMediaProcessing(ctx, Source{Path: "local", Width: 1920, Height: 1080}, nil, ExportRequest{Format: "mp4"})
		if err != nil {
			t.Fatal("canceled waiter leaked slot", err)
		}
		next()
	}
}

func TestKnownSelectedPlatformStreamsAndAudioShareSlots(t *testing.T) {
	ctx := withMediaBudget(context.Background(), 3)
	var releases []func()
	for range 3 {
		release, err := acquireMediaProcessing(ctx, Source{Width: 7680, Height: 4320}, []platformFormat{{Width: 1920, Height: 1080, VCodec: "h264"}}, ExportRequest{Format: "mp4"})
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	for _, release := range releases {
		release()
	}
	audio, err := acquireMediaProcessing(ctx, Source{}, nil, ExportRequest{Format: "mp3"})
	if err != nil {
		t.Fatal(err)
	}
	defer audio()
	video, err := acquireMediaProcessing(ctx, Source{Path: "local", Width: 3840, Height: 2160}, nil, ExportRequest{Format: "mp4"})
	if err != nil {
		t.Fatal(err)
	}
	video()
}

func TestRefreshedInputUpgradesBudgetAndCanceledUpgradeReleasesOldSlot(t *testing.T) {
	ctx := withMediaBudget(context.Background(), 3)
	var permit mediaProcessingPermit
	request := ExportRequest{Format: "mp4"}
	low := []platformFormat{{Width: 1920, Height: 1080, VCodec: "h264"}}
	high := []platformFormat{{Width: 7680, Height: 4320, VCodec: "h264"}}
	if err := permit.Acquire(ctx, Source{}, low, request); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	if err := permit.Acquire(bounded, Source{}, high, request); err != nil {
		cancel()
		t.Fatal("upgrade kept its old slot and blocked itself", err)
	}
	cancel()
	wait, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	other, err := acquireMediaProcessing(wait, Source{}, low, request)
	stop()
	if other != nil {
		other()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		permit.Release()
		t.Fatal("refreshed 8K stream did not own all slots", err)
	}
	permit.Release()
	permit.Release() // Deferred cleanup after a canceled refresh must be safe.
	if err := permit.Acquire(ctx, Source{}, low, request); err != nil {
		t.Fatal(err)
	}
	blocker, err := acquireMediaProcessing(ctx, Source{}, low, request)
	if err != nil {
		t.Fatal(err)
	}
	wait, stop = context.WithTimeout(ctx, 20*time.Millisecond)
	err = permit.Acquire(wait, Source{}, high, request)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("upgrade should wait for another decoder", err)
	}
	permit.Release()
	blocker()
	if err := permit.Acquire(ctx, Source{}, high, request); err != nil {
		t.Fatal("canceled upgrade leaked an old slot", err)
	}
	permit.Release()
}
