package engine

import (
	"errors"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

type CutList = edit.CutList
type CutListEntry = edit.CutListEntry

// ParseCutList imports bounded CSV segments/markers or FFmpeg chapter metadata.
func ParseCutList(format string, data []byte) (CutList, error) {
	list, err := edit.ParseCutList(format, data)
	if err != nil {
		return list, invalidArgument(err)
	}
	return list, nil
}

func EncodeCutList(format string, list CutList) ([]byte, error) {
	data, err := edit.EncodeCutList(format, list)
	if err != nil {
		return nil, invalidArgument(err)
	}
	return data, nil
}

// BuildCutListPlan creates an ordinary editable plan, without opening input or
// executing exports. durationMS is required to expand a final open marker.
// Execution retains the ordinary source, media and aggregate plan validation.
func BuildCutListPlan(input string, list CutList, durationMS int64, options AdvancedOptions, outputMode string) (Plan, error) {
	if outputMode == "" {
		outputMode = "joined"
	}
	if outputMode != "joined" && outputMode != "separate" {
		return Plan{}, invalidArgument(errors.New("output_mode must be joined or separate"))
	}
	ranges, err := edit.CutListRanges(list, durationMS)
	if err != nil {
		return Plan{}, invalidArgument(err)
	}
	if options.Format == "" {
		options.Format = "mp4"
	}
	if options.Quality == "" {
		options.Quality = "best"
	}
	if options.CutMode == "" {
		options.CutMode = "accurate"
	}
	plan := Plan{Version: 1, Input: input, Options: options, OutputMode: outputMode, Ranges: make([]NamedRange, len(ranges))}
	for i, interval := range ranges {
		plan.Ranges[i] = NamedRange{StartMS: interval.StartMS, EndMS: interval.EndMS, Label: interval.Label}
	}
	return plan, nil
}

// CutListFromPlan preserves source interval order, including repeated edits.
func CutListFromPlan(plan Plan) (CutList, error) {
	if plan.Version != 1 {
		return CutList{}, invalidArgument(errors.New("plan version must be 1"))
	}
	list := CutList{Version: 1, Entries: make([]CutListEntry, 0, len(plan.Ranges))}
	for _, interval := range plan.Ranges {
		end := interval.EndMS
		list.Entries = append(list.Entries, CutListEntry{StartMS: interval.StartMS, EndMS: &end, Label: interval.Label})
	}
	if err := edit.ValidateCutList(list); err != nil {
		return CutList{}, invalidArgument(err)
	}
	return list, nil
}

// CutListFromSegments uses verified output timestamps, including actual copy
// boundaries, for chapters on an exported file's timeline.
func CutListFromSegments(segments []Segment) (CutList, error) {
	list, err := edit.CutListFromSegments(segments)
	if err != nil {
		return CutList{}, invalidArgument(err)
	}
	return list, nil
}
