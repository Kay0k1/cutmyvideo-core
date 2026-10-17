package mediapreflight

import (
	"context"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

// BatchRuntime fences the caller's fresh source/tool snapshot and checks
// capability observations without moving filesystem or subprocess work here.
type BatchRuntime interface {
	Unchanged(context.Context) error
	Check(context.Context, []CapabilityGroup, *Result, map[string]map[string]bool) error
}

// Batch prepares independently owned reports and delays each request's error
// until its turn. The caller validates batch size and supplies a fresh source.
func Batch(ctx context.Context, c edit.Limits, absolute string, requests []edit.ExportRequest, info mediainfo.Info, duration int64, runtime BatchRuntime) ([]Result, int, error) {
	// Compute the relevant-name union without retaining raw tool output or all
	// parsed names. Delay each request's error until its turn so an earlier
	// missing capability still precedes a later invalid range or track choice.
	type prepared struct {
		result Result
		groups []CapabilityGroup
		err    error
	}
	preparedRequests := make([]prepared, len(requests))
	wanted := make(map[string]map[string]bool)
	for i, request := range requests {
		if err := runtime.Unchanged(ctx); err != nil {
			return nil, i, err
		}
		item := &preparedRequests[i]
		item.result, item.groups, item.err = Prepare(c, absolute, request, info, duration)
		for _, group := range item.groups {
			if wanted[group.Listing] == nil {
				wanted[group.Listing] = make(map[string]bool)
			}
			for _, name := range group.Required {
				wanted[group.Listing][name] = true
			}
		}
		if err := runtime.Unchanged(ctx); err != nil {
			return nil, i, err
		}
	}
	results := make([]Result, len(requests))
	for i := range preparedRequests {
		if err := runtime.Unchanged(ctx); err != nil {
			return nil, i, err
		}
		item := &preparedRequests[i]
		if item.err != nil {
			return nil, i, item.err
		}
		if err := runtime.Check(ctx, item.groups, &item.result, wanted); err != nil {
			return nil, i, err
		}
		if err := runtime.Unchanged(ctx); err != nil {
			return nil, i, err
		}
		Finish(&item.result)
		results[i] = item.result
	}
	if err := runtime.Unchanged(ctx); err != nil {
		return nil, len(requests) - 1, err
	}
	return results, -1, nil
}
