package app

import (
	"context"

	"golang.org/x/sync/semaphore"
)

type mediaBudgetKey struct{}
type mediaBudget struct {
	slots    int64
	weighted *semaphore.Weighted
}

// A refreshed platform URL can select a different input resolution. Drop the
// old reservation before acquiring the new weight, so upgrades cannot deadlock
// by waiting for slots still held by the same job.
type mediaProcessingPermit struct{ release func() }

func (p *mediaProcessingPermit) Acquire(ctx context.Context, source Source, streams []platformFormat, request ExportRequest) error {
	p.Release()
	release, err := acquireMediaProcessing(ctx, source, streams, request)
	if err == nil {
		p.release = release
	}
	return err
}

func (p *mediaProcessingPermit) Release() {
	if p.release != nil {
		p.release()
		p.release = nil
	}
}

func withMediaBudget(ctx context.Context, concurrency int) context.Context {
	slots := int64(concurrency)
	if slots < 1 {
		slots = 1
	}
	if slots > 8 {
		slots = 8
	}
	return context.WithValue(ctx, mediaBudgetKey{}, &mediaBudget{slots: slots, weighted: semaphore.NewWeighted(slots)})
}

// Decoder memory depends on input dimensions even when output is scaled down.
// This is conservative scheduling, not a substitute for a cgroup RAM limit:
// sources above 8,388,608 pixels or of unknown dimensions use the whole pool.
func acquireMediaProcessing(ctx context.Context, source Source, streams []platformFormat, request ExportRequest) (func(), error) {
	budget, ok := ctx.Value(mediaBudgetKey{}).(*mediaBudget)
	if !ok {
		return func() {}, nil
	}
	weight := int64(1)
	if request.Format != "mp3" {
		width, height := source.Width, source.Height
		if source.Path == "" {
			width, height = 0, 0
			for _, stream := range streams {
				if stream.VCodec != "none" && stream.Height > 0 {
					width, height = stream.Width, stream.Height
					break
				}
			}
		}
		// Division avoids overflow from untrusted provider dimensions.
		if width <= 0 || height <= 0 || width > 8_388_608/height {
			weight = budget.slots
		}
	}
	if err := budget.weighted.Acquire(ctx, weight); err != nil {
		return nil, err
	}
	return func() { budget.weighted.Release(weight) }, nil
}
