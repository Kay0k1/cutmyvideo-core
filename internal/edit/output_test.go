package edit

import "testing"

func TestSpeedClockCollectionInheritanceAndExplicitResets(t *testing.T) {
	speed := &Speed{Numerator: 6, Denominator: 4}
	plan := CollectionPlan{Version: 1, Options: CollectionOptions{Speed: speed, TargetSizeBytes: 1000000}, Clips: []CollectionClip{
		{ID: "inherited", Ranges: []Range{{StartMS: 1000, EndMS: 2000}}},
		{ID: "reset", Ranges: []Range{{EndMS: 1000}}, Options: CollectionOptions{ClearSpeed: true, ClearTargetSize: true}},
	}}
	result, err := NormalizeCollection(plan, CollectionExecution{}, Limits{MaxRanges: 128, MaxRangeMS: 10000, MaxJobMS: 10000}, Source{DurationMS: 5000, HasLocalMedia: true})
	if err != nil {
		t.Fatal(err)
	}
	if result[0].Request.Speed.Numerator != 3 || result[0].Request.Speed.Denominator != 2 || result[0].Request.TargetSizeBytes != 1000000 || speed.Numerator != 6 || speed.Denominator != 4 {
		t.Fatal("inheritance/normalization mutated saved plan", result, speed)
	}
	if result[1].Request.Speed != nil || result[1].Request.TargetSizeBytes != 0 {
		t.Fatal("clear flags did not restore legacy behavior", result[1])
	}
	// Cumulative mapping avoids a whole extra millisecond from independently
	// rounding each of three 1ms pieces at1.5x.
	end, err := OutputDurationMS(3, &Speed{Numerator: 3, Denominator: 2})
	if err != nil || end != 2 {
		t.Fatal(end, err)
	}
	for _, bad := range []*Speed{{Numerator: 0, Denominator: 1}, {Numerator: 1, Denominator: 0}, {Numerator: 3, Denominator: 1}, {Numerator: 1, Denominator: 3}, {Numerator: 1001, Denominator: 1000}} {
		if _, err := NormalizeSpeed(bad); err == nil {
			t.Fatal("invalid rate admitted", bad)
		}
	}
	plan.Clips[0].Options = CollectionOptions{Speed: &Speed{Numerator: 1, Denominator: 1}, ClearSpeed: true}
	if _, err := NormalizeCollection(plan, CollectionExecution{}, Limits{MaxRanges: 128, MaxRangeMS: 10000, MaxJobMS: 10000}, Source{DurationMS: 5000, HasLocalMedia: true}); err == nil {
		t.Fatal("contradictory reset accepted")
	}
}
