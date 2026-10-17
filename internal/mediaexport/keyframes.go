package mediaexport

import "errors"

func KeyframeProbeBounds(start int64) (from, until int64) {
	from = start - 30000
	if from < 0 {
		from = 0
	}
	until = start + 1
	if start == 0 {
		// Matroska can place its first video packet after zero (for example
		// because audio starts first). Read the same bounded initial window
		// accepted below; a 1 ms interval can otherwise contain no keyframe.
		until = 2001
	}
	return from, until
}

func SelectCopyKeyframe(frames []Keyframe, start int64) (Keyframe, error) {
	found := Keyframe{PTS: -1}
	for _, frame := range frames {
		if (frame.PTS <= start || start == 0 && found.PTS < 0 && frame.PTS <= 2000) && frame.PTS > found.PTS {
			found = frame
		}
	}
	if found.PTS < 0 || !found.HasDTS && found.PTS > 2000 {
		return Keyframe{}, errors.New("no nearby keyframe found; use accurate mode")
	}
	return found, nil
}
