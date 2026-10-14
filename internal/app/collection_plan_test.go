package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestCollectionNormalizationInheritanceAndSelection(t *testing.T) {
	muted := true
	unmuted := false
	track := 3
	automatic := -1
	plan := CollectionPlan{Version: 1, Options: CollectionOptions{Format: "mp4", Quality: "1080p", NoAudio: &muted, AudioTrack: &track, Crop: &Crop{Width: 0.5, Height: 1}}, Clips: []CollectionClip{
		{ID: "first", Label: " One ", Ranges: []Range{{StartMS: 0, EndMS: 1000}}, Options: CollectionOptions{AudioTrack: &automatic}},
		{ID: "second", Ranges: []Range{{StartMS: 1000, EndMS: 2000}}, Options: CollectionOptions{NoAudio: &unmuted, ClearCrop: true}},
	}}
	before, _ := json.Marshal(plan)
	c := Config{MaxRanges: 128, MaxRangeMS: 10000, MaxJobMS: 20000}
	clips, err := NormalizeCollection(plan, CollectionExecution{ClipIDs: []string{"second", "first"}}, c, Source{Path: "source.mp4", DurationMS: 4000})
	if err != nil || len(clips) != 2 {
		t.Fatalf("normalize %+v %v", clips, err)
	}
	if clips[0].ID != "first" || clips[0].Label != "One" || !clips[0].Request.NoAudio || clips[0].Request.AudioTrack != nil || clips[0].Request.Crop == nil || clips[0].Request.Quality != "1080p" {
		t.Fatalf("first %+v", clips[0])
	}
	if clips[1].Request.NoAudio || clips[1].Request.AudioTrack == nil || *clips[1].Request.AudioTrack != 3 || clips[1].Request.Crop != nil {
		t.Fatalf("second %+v", clips[1])
	}
	clips[0].Request.Ranges[0].EndMS = 999
	clips[0].Request.Crop.Width = 0.25
	*clips[1].Request.AudioTrack = 99
	after, _ := json.Marshal(plan)
	if !bytes.Equal(before, after) {
		t.Fatal("normalized requests alias reusable plan")
	}
	plan.Clips[0].Options.Crop = &Crop{Width: 1, Height: 1}
	plan.Clips[0].Options.ClearCrop = true
	_, err = NormalizeCollection(plan, CollectionExecution{}, c, Source{Path: "source.mp4", DurationMS: 4000})
	var location *CollectionFieldError
	if !errors.As(err, &location) || location.ClipID != "first" {
		t.Fatalf("conflicting reset accepted %v", err)
	}
	reserved := []string{"con", "prn", "aux", "nul"}
	for i := 1; i <= 9; i++ {
		reserved = append(reserved, fmt.Sprintf("com%d", i), fmt.Sprintf("lpt%d", i))
	}
	for _, id := range reserved {
		t.Run("reserved-"+id, func(t *testing.T) {
			p := CollectionPlan{Version: 1, Clips: []CollectionClip{{ID: "valid", Ranges: []Range{{EndMS: 1000}}}, {ID: id, Ranges: []Range{{EndMS: 1000}}}}}
			_, err := NormalizeCollection(p, CollectionExecution{ClipIDs: []string{"valid"}}, c, Source{Path: "source.mp4", DurationMS: 4000})
			var location *CollectionFieldError
			if !errors.As(err, &location) || location.ClipID != id || location.Field != "clips[1].id" {
				t.Fatalf("reserved unselected ID accepted: %q %+v %v", id, location, err)
			}
		})
	}
	for _, id := range []string{"con-safe", "com10", "lpt10", "null", "auxiliary"} {
		t.Run("allowed-"+id, func(t *testing.T) {
			p := CollectionPlan{Version: 1, Clips: []CollectionClip{{ID: id, Ranges: []Range{{EndMS: 1000}}}}}
			if _, err := NormalizeCollection(p, CollectionExecution{}, c, Source{Path: "source.mp4", DurationMS: 4000}); err != nil {
				t.Fatalf("valid ID rejected %q: %v", id, err)
			}
		})
	}
}
