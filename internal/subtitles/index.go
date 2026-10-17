package subtitles

import "sort"

// Keep at most one small parsed track per operation. Larger source tracks
// retain their existing disk-backed, one-track-at-a-time processing path.
const IndexCacheBytes = 1 << 20

type Index struct {
	Cues   []Cue
	MaxEnd []int64
}

func NewIndex(cues []Cue) *Index {
	index := &Index{Cues: cues}
	if len(cues) > IndexCacheBytes/8 {
		return index
	}
	for i := 1; i < len(cues); i++ {
		if cues[i].StartMS < cues[i-1].StartMS {
			// Preserve the original order even for unsorted valid text tracks.
			return index
		}
	}
	index.MaxEnd = make([]int64, len(cues))
	var end int64
	for i, cue := range cues {
		end = max(end, cue.EndMS)
		index.MaxEnd[i] = end
	}
	return index
}

func (index *Index) Intersecting(start, end int64) []Cue {
	if index.MaxEnd == nil {
		return index.Cues
	}
	first := sort.Search(len(index.Cues), func(i int) bool { return index.MaxEnd[i] > start })
	last := sort.Search(len(index.Cues), func(i int) bool { return index.Cues[i].StartMS >= end })
	if first >= last {
		return nil
	}
	return index.Cues[first:last]
}
