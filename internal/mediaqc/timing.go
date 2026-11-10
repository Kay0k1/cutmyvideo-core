package mediaqc

import (
	"math"
	"slices"
	"strconv"
	"strings"
)

// Clock converts stream ticks to the source timeline: ms = ticks*Num/Den*1000
// - OriginMS, where OriginMS is the container start time.
type Clock struct {
	Num, Den int64
	OriginMS float64
}

// MS returns the timeline position of a timestamp in milliseconds.
func (c Clock) MS(ticks int64) float64 {
	return float64(ticks)*float64(c.Num)*1000/float64(c.Den) - c.OriginMS
}

func (c Clock) span(ticks int64) float64 {
	return float64(ticks) * float64(c.Num) * 1000 / float64(c.Den)
}

// MeasureTiming fills the frame timing fields of v from the presentation
// timestamps of every frame in the window, in any order. lastDuration is the
// declared duration of the latest frame in ticks (zero if unknown); the video
// ends no later than windowEndMS. declaredRate is the stream's r_frame_rate.
//
// Intervals equal to the median within one tick or 0.5 % are regular; the
// nominal interval is their mean. A frame interval longer than 1.5 nominal
// intervals is a gap; equal timestamps are duplicates. The stream is cfr when
// every interval is regular.
func MeasureTiming(v *Video, ticks []int64, clock Clock, lastDuration int64, windowEndMS int64, declaredRate float64) {
	slices.Sort(ticks)
	v.FrameCount = len(ticks)
	v.Gaps, v.Duplicates = make([]Gap, 0), make([]int64, 0)
	v.Timing = "unknown"
	if len(ticks) == 0 {
		return
	}
	v.FirstFrameMS = roundMS(clock.MS(ticks[0]))
	v.LastFrameMS = roundMS(clock.MS(ticks[len(ticks)-1]))
	positive := make([]int64, 0, len(ticks))
	for i := 1; i < len(ticks); i++ {
		if d := ticks[i] - ticks[i-1]; d > 0 {
			positive = append(positive, d)
		}
	}
	nominal := 0.0
	if len(positive) > 0 {
		sorted := slices.Clone(positive)
		slices.Sort(sorted)
		median := sorted[len(sorted)/2]
		tolerance := max(1, median/200)
		var sum, regular int64
		for _, d := range sorted {
			if d-median <= tolerance && median-d <= tolerance {
				sum += d
				regular++
			}
		}
		nominal = clock.span(sum) / float64(regular)
		v.IntervalMS = IntervalStats{Min: round3(clock.span(sorted[0])), Median: round3(clock.span(median)), Max: round3(clock.span(sorted[len(sorted)-1]))}
		fps := round3(1000 / nominal)
		v.FPS = &fps
		v.Timing = "cfr"
		if int(regular) != len(sorted) || len(positive) != len(ticks)-1 {
			v.Timing = "vfr"
		} else if onGrid(ticks, clock, declaredRate) {
			// A coarse time base (1 ms in Matroska) rounds every timestamp;
			// the declared rate that explains all of them is exact.
			fps = round3(declaredRate)
			v.FPS = &fps
		}
	}
	for i := 1; i < len(ticks); i++ {
		d := ticks[i] - ticks[i-1]
		if d == 0 {
			if v.DuplicateCount < MaxListed {
				v.Duplicates = append(v.Duplicates, roundMS(clock.MS(ticks[i])))
			}
			v.DuplicateCount++
			continue
		}
		if nominal > 0 && clock.span(d) > 1.5*nominal {
			missing := max(1, int(math.Round(clock.span(d)/nominal))-1)
			if v.GapCount < MaxListed {
				v.Gaps = append(v.Gaps, Gap{AtMS: roundMS(clock.MS(ticks[i-1]) + nominal), MissingFrames: missing})
			}
			v.GapCount++
			v.MissingFrames += missing
		}
	}
	last := clock.span(lastDuration)
	if lastDuration <= 0 {
		last = nominal
	}
	v.EndMS = min(windowEndMS, roundMS(clock.MS(ticks[len(ticks)-1])+last))
	v.DurationMS = max(0, v.EndMS-v.FirstFrameMS)
	if declaredRate > 0 && finite(declaredRate) {
		v.ExpectedFrames = int64(math.Round(float64(v.DurationMS) * declaredRate / 1000))
	}
}

// onGrid reports whether every timestamp lies within one tick of the grid
// first + i/rate.
func onGrid(ticks []int64, clock Clock, rate float64) bool {
	if rate <= 0 || !finite(rate) {
		return false
	}
	tick := clock.span(1)
	for i, t := range ticks {
		if math.Abs(clock.span(t-ticks[0])-float64(i)*1000/rate) > tick+1e-6 {
			return false
		}
	}
	return true
}

// WithoutBlack removes frozen intervals that lie inside a black interval, so a
// black picture is reported once. tolerance is usually one frame interval.
func WithoutBlack(freeze, black []Interval, tolerance int64) []Interval {
	kept := make([]Interval, 0, len(freeze))
	for _, f := range freeze {
		covered := false
		for _, b := range black {
			if f.StartMS >= b.StartMS-tolerance && f.EndMS <= b.EndMS+tolerance {
				covered = true
				break
			}
		}
		if !covered {
			kept = append(kept, f)
		}
	}
	return kept
}

// ParseRate reads an FFmpeg rational such as 30000/1001. It returns zero for
// unknown or invalid rates.
func ParseRate(value string) float64 {
	n, d, ok := strings.Cut(value, "/")
	num, numErr := strconv.ParseInt(n, 10, 64)
	den, denErr := strconv.ParseInt(d, 10, 64)
	if !ok || numErr != nil || denErr != nil || num <= 0 || den <= 0 {
		return 0
	}
	return float64(num) / float64(den)
}

func roundMS(ms float64) int64 { return int64(math.Round(ms)) }

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
