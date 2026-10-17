package localmedia

import (
	"math"
	"strconv"
	"strings"
)

// Only one metadata line and pending frame survive a write. No PCM, transcript
// or per-frame history accumulates, including on the local 24-hour maximum.
type loudnessCoverage struct {
	rate, windowMS int64
	line           [256]byte
	length         int
	frames         int64
	pending        bool
	pts            int64
	endMS          int64
}

func (p *loudnessCoverage) Write(data []byte) (int, error) {
	for _, b := range data {
		if b == '\n' {
			if err := p.record(string(p.line[:p.length])); err != nil {
				return 0, err
			}
			p.length = 0
			continue
		}
		if p.length == len(p.line) {
			return 0, analysisMalformed()
		}
		p.line[p.length] = b
		p.length++
	}
	return len(data), nil
}

func (p *loudnessCoverage) record(line string) error {
	if strings.HasPrefix(line, "frame:") {
		parts := strings.Fields(line)
		if p.pending || len(parts) != 3 || !strings.HasPrefix(parts[1], "pts:") || !strings.HasPrefix(parts[2], "pts_time:") {
			return analysisMalformed()
		}
		frame, err := strconv.ParseInt(strings.TrimPrefix(parts[0], "frame:"), 10, 64)
		if err != nil || frame != p.frames {
			return analysisMalformed()
		}
		pts, err := strconv.ParseInt(strings.TrimPrefix(parts[1], "pts:"), 10, 64)
		if err != nil || p.rate <= 0 || pts < 0 || pts > p.windowMS*p.rate/1000 {
			return analysisMalformed()
		}
		at, err := strconv.ParseFloat(strings.TrimPrefix(parts[2], "pts_time:"), 64)
		if err != nil || !finite(at) || at < 0 || at > float64(p.windowMS)/1000+1 {
			return analysisMalformed()
		}
		p.pending, p.pts = true, pts
		return nil
	}
	const prefix = "lavfi.astats.Overall.Number_of_samples="
	if !p.pending || !strings.HasPrefix(line, prefix) || p.rate <= 0 {
		return analysisMalformed()
	}
	samples, err := strconv.ParseFloat(strings.TrimPrefix(line, prefix), 64)
	if err != nil || !finite(samples) || samples < 1 || samples > math.MaxInt32 || samples != math.Trunc(samples) {
		return analysisMalformed()
	}
	// asettb fixes the branch time base to 1/rate. pts_time is only a bounded
	// diagnostic: FFmpeg prints it with insufficient precision for long inputs.
	end := ((p.pts+int64(samples))*1000 + p.rate/2) / p.rate
	if end > p.windowMS+1 {
		return analysisMalformed()
	}
	p.endMS = max(p.endMS, min(p.windowMS, end))
	p.pending = false
	p.frames++
	return nil
}

func (p *loudnessCoverage) finish() error {
	if p.length != 0 || p.pending || p.frames == 0 || p.endMS <= 0 {
		return analysisMalformed()
	}
	return nil
}
