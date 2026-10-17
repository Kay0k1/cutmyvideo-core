package subtitles

import (
	"reflect"
	"testing"
)

func TestIndexPreservesSpanningCuesAndFallbackOrder(t *testing.T) {
	t.Run("sorted spanning and half open boundaries", func(t *testing.T) {
		cues := []Cue{{0, 10000, "spanning"}, {1000, 2000, "earlier"}, {3000, 4000, "inside"}, {4000, 5000, "at end"}}
		before := append([]Cue(nil), cues...)
		index := NewIndex(cues)
		candidates := index.Intersecting(2000, 4000)
		if !reflect.DeepEqual(candidates, cues[:3]) {
			t.Fatalf("candidates lost the spanning cue or included the ending boundary: %#v", candidates)
		}
		if &index.Cues[0] != &cues[0] || &candidates[0] != &cues[0] || !reflect.DeepEqual(cues, before) {
			t.Fatal("index copied or changed source cues")
		}
		if got := index.Intersecting(10000, 11000); got != nil {
			t.Fatalf("cue ending exactly at the interval start remained: %#v", got)
		}
	})
	t.Run("unsorted fallback preserves the original slice", func(t *testing.T) {
		cues := []Cue{{4000, 5000, "later"}, {0, 10000, "spanning"}, {1000, 2000, "earlier"}}
		index := NewIndex(cues)
		candidates := index.Intersecting(2000, 3000)
		if index.MaxEnd != nil || !reflect.DeepEqual(candidates, cues) || &candidates[0] != &cues[0] {
			t.Fatal("unsorted fallback reordered, indexed or copied source cues")
		}
	})
	t.Run("oversized fallback stays bounded", func(t *testing.T) {
		cues := make([]Cue, IndexCacheBytes/8+1)
		cues[0], cues[len(cues)-1] = Cue{0, 1, "first"}, Cue{0, 2, "last"}
		index := NewIndex(cues)
		candidates := index.Intersecting(10, 20)
		if index.MaxEnd != nil || len(candidates) != len(cues) || &candidates[0] != &cues[0] {
			t.Fatal("oversized fallback allocated an endpoint index or changed source candidates")
		}
	})
	t.Run("empty track", func(t *testing.T) {
		index := NewIndex(nil)
		if len(index.Cues) != 0 || len(index.MaxEnd) != 0 || index.Intersecting(0, 1000) != nil {
			t.Fatal("empty track produced candidates")
		}
	})
}
