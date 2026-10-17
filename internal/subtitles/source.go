package subtitles

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ParseSourceTime and ParseSourceSRT preserve the source-extraction grammar.
// Supplied sidecars continue to use the distinct strict request parser.
func ParseSourceTime(raw string) (int64, error) {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), ",", ".")
	parts := strings.Split(raw, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid subtitle timestamp")
	}
	h, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil || h < 0 || h > 720 {
		return 0, fmt.Errorf("invalid subtitle timestamp")
	}
	m, e := strconv.ParseInt(parts[1], 10, 64)
	if e != nil || m < 0 || m > 59 {
		return 0, fmt.Errorf("invalid subtitle timestamp")
	}
	sec, e := strconv.ParseFloat(parts[2], 64)
	if e != nil || math.IsNaN(sec) || math.IsInf(sec, 0) || sec < 0 || sec >= 60 {
		return 0, fmt.Errorf("invalid subtitle timestamp")
	}
	return h*3600000 + m*60000 + int64(sec*1000+0.5), nil
}

func ParseSourceSRT(data []byte) ([]Cue, error) {
	raw := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n"))
	if raw == "" {
		return nil, nil
	}
	var cues []Cue
	for _, block := range strings.Split(raw, "\n\n") {
		lines := strings.Split(block, "\n")
		if len(lines) < 2 {
			continue
		}
		position := 0
		if !strings.Contains(lines[0], " --> ") {
			position = 1
		}
		if len(lines) <= position+1 {
			return nil, fmt.Errorf("invalid text subtitle cue")
		}
		bounds := strings.Split(lines[position], " --> ")
		if len(bounds) != 2 {
			return nil, fmt.Errorf("invalid text subtitle cue")
		}
		start, e := ParseSourceTime(bounds[0])
		if e != nil {
			return nil, e
		}
		endFields := strings.Fields(bounds[1])
		if len(endFields) == 0 {
			return nil, fmt.Errorf("invalid text subtitle cue")
		}
		end, e := ParseSourceTime(endFields[0])
		if e != nil || end < start {
			return nil, fmt.Errorf("invalid text subtitle cue")
		}
		cues = append(cues, Cue{StartMS: start, EndMS: end, Text: strings.Join(lines[position+1:], "\n")})
	}
	return cues, nil
}
