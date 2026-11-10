package audiosync

import (
	"math"
	"math/rand"
	"testing"
)

// The fixtures are deterministic synthetic songs: drums on a tempo grid and a
// vocal-like line of harmonic syllables at irregular times.

func drums(seconds, bpm float64, seed int64) []float32 {
	r := rand.New(rand.NewSource(seed))
	out := make([]float32, int(seconds*PCMRate))
	sixteenth := 60 / bpm / 4
	kick := make([]bool, 16)
	snare := make([]bool, 16)
	hat := make([]bool, 16)
	for i := range kick {
		kick[i] = i%4 == 0 && r.Float64() < 0.8 || r.Float64() < 0.12
		snare[i] = i%8 == 4 || r.Float64() < 0.08
		hat[i] = r.Float64() < 0.7
	}
	for step := 0; float64(step)*sixteenth < seconds; step++ {
		at := int(float64(step) * sixteenth * PCMRate)
		// Bars vary so the pattern is not perfectly periodic.
		if r.Float64() < 0.15 {
			continue
		}
		if kick[step%16] {
			for i := 0; i < PCMRate/8 && at+i < len(out); i++ {
				t := float64(i) / PCMRate
				out[at+i] += float32(0.8 * math.Sin(2*math.Pi*(55+40*math.Exp(-t*40))*t) * math.Exp(-t*28))
			}
		}
		if snare[step%16] {
			for i := 0; i < PCMRate/8 && at+i < len(out); i++ {
				t := float64(i) / PCMRate
				out[at+i] += float32(0.4 * (r.Float64()*2 - 1) * math.Exp(-t*30))
			}
		}
		if hat[step%16] {
			previous := 0.0
			for i := 0; i < PCMRate/40 && at+i < len(out); i++ {
				t := float64(i) / PCMRate
				noise := r.Float64()*2 - 1
				out[at+i] += float32(0.25 * (noise - previous) * math.Exp(-t*160))
				previous = noise
			}
		}
	}
	return out
}

func vocal(seconds float64, seed int64) []float32 {
	r := rand.New(rand.NewSource(seed))
	out := make([]float32, int(seconds*PCMRate))
	for t := 0.3 + r.Float64(); t < seconds-0.6; {
		length := 0.12 + 0.3*r.Float64()
		pitch := 140 + 160*r.Float64()
		at := int(t * PCMRate)
		for i := 0; i < int(length*PCMRate) && at+i < len(out); i++ {
			x := float64(i) / PCMRate
			envelope := math.Min(1, x/0.008) * math.Exp(-x*4/length)
			var s float64
			for h := 1; h <= 9; h++ {
				s += math.Sin(2*math.Pi*pitch*float64(h)*x) / float64(h)
			}
			out[at+i] += float32(0.35 * envelope * s)
		}
		t += length + 0.03 + 0.35*r.Float64()*r.Float64()
		if r.Float64() < 0.12 {
			t += 0.8 + r.Float64()
		}
	}
	return out
}

func mix(parts ...[]float32) []float32 {
	n := 0
	for _, p := range parts {
		n = max(n, len(p))
	}
	out := make([]float32, n)
	for _, p := range parts {
		for i, v := range p {
			out[i] += v
		}
	}
	return out
}

// delayed returns x placed so that x time t is heard at t+seconds; a negative
// delay drops the beginning. Fractional sample delays are interpolated.
func delayed(x []float32, seconds float64, length int) []float32 {
	out := make([]float32, length)
	for i := range out {
		position := float64(i) - seconds*PCMRate
		index := int(math.Floor(position))
		if index < 0 || index+1 >= len(x) {
			continue
		}
		fraction := float32(position - float64(index))
		out[i] = x[index]*(1-fraction) + x[index+1]*fraction
	}
	return out
}

// faster plays x at the given speed.
func faster(x []float32, speed float64) []float32 {
	out := make([]float32, int(float64(len(x))/speed))
	for i := range out {
		position := float64(i) * speed
		index := int(position)
		if index+1 >= len(x) {
			break
		}
		fraction := float32(position - float64(index))
		out[i] = x[index]*(1-fraction) + x[index+1]*fraction
	}
	return out
}

// degraded adds noise and a different tonal balance.
func degraded(x []float32, seed int64) []float32 {
	r := rand.New(rand.NewSource(seed))
	out := make([]float32, len(x))
	var low float32
	for i, v := range x {
		low += 0.35 * (v - low)
		out[i] = 0.6*low + 0.25*v + float32(0.01*(r.Float64()*2-1))
	}
	return out
}

func features(t *testing.T, pcm []float32) *Features {
	t.Helper()
	f, err := Extract(pcm)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func song(seconds float64, seed int64, bpm float64) []float32 {
	return mix(drums(seconds, bpm, seed), vocal(seconds, seed+1000))
}

func TestAlignFindsShiftedCopy(t *testing.T) {
	ref := song(70, 1, 140)
	refFeatures := features(t, ref)
	for _, offset := range []float64{0, 3.38, 0.52, -12.25, 1.79052, -0.70013} {
		in := degraded(delayed(ref, offset, len(ref)+5*PCMRate), 7)
		matches := Align(refFeatures, features(t, in), Options{})
		if len(matches) != 1 {
			t.Fatalf("offset %v: %d matches", offset, len(matches))
		}
		m := matches[0]
		t.Logf("offset %v: %s/%s similarity %.2f agreeing %d/%d error %.3f ms", offset, m.Band, m.Method, m.Similarity, m.Agreeing, m.Chunks, (m.OffsetSeconds-offset)*1000)
		if math.Abs(m.OffsetSeconds-offset) > 0.002 || m.Confidence < 0.9 || m.Speed != 1 {
			t.Fatalf("offset %v: got %+v", offset, m)
		}
		if m.Method == "waveform" && math.Abs(m.OffsetSeconds-offset) > 0.0002 {
			t.Fatalf("offset %v: waveform refinement is off by %v s", offset, m.OffsetSeconds-offset)
		}
	}
}

func TestAlignFollowsVocalOfARemix(t *testing.T) {
	voice := vocal(80, 42)
	ref := mix(voice, drums(80, 140, 3))
	// Same vocal, another instrumental at another tempo.
	in := mix(delayed(voice, 1.79052, len(voice)+4*PCMRate), drums(84, 97, 99))
	matches := Align(features(t, ref), features(t, in), Options{})
	if len(matches) != 1 || math.Abs(matches[0].OffsetSeconds-1.79052) > 0.002 {
		t.Fatalf("remix: %+v", matches)
	}
	t.Logf("remix: %+v", matches[0])
}

func TestAlignRefusesUnrelatedMaterial(t *testing.T) {
	ref := features(t, song(70, 1, 140))
	for name, in := range map[string][]float32{
		"another tempo": song(75, 200, 123),
		"noise":         degraded(make([]float32, 60*PCMRate), 5),
		"silence":       make([]float32, 30*PCMRate),
	} {
		if matches := Align(ref, features(t, in), Options{}); len(matches) != 0 {
			t.Errorf("%s: reported %+v", name, matches)
		}
	}
	// Another song on the same tempo grid agrees chunk by chunk at every bar
	// shift. No single placement may be reported: several rivals or none.
	if matches := Align(ref, features(t, song(75, 300, 140)), Options{}); len(matches) != 0 && !Ambiguous(matches) {
		t.Errorf("same tempo grid: confident placement %+v", matches)
	}
}

func TestAlignMeasuresSpeed(t *testing.T) {
	ref := song(120, 5, 150)
	in := delayed(faster(ref, 1.001), 2.5, len(ref)+3*PCMRate)
	matches := Align(features(t, ref), features(t, in), Options{})
	if len(matches) != 1 {
		t.Fatalf("%d matches", len(matches))
	}
	m := matches[0]
	t.Logf("speed: %+v", m)
	if math.Abs(m.Speed-1.001) > 0.0002 {
		t.Fatalf("speed %v: %+v", m.Speed, m)
	}
	middle := (m.RefStart + m.RefEnd) / 2
	if want := 2.5 + middle/1.001 - middle; math.Abs(m.OffsetSeconds-want) > 0.005 {
		t.Fatalf("offset %v, want %v: %+v", m.OffsetSeconds, want, m)
	}
}

func TestAlignPlacesAnExcerpt(t *testing.T) {
	ref := song(90, 9, 128)
	in := degraded(ref[40*PCMRate:55*PCMRate], 3)
	matches := Align(features(t, ref), features(t, in), Options{})
	if len(matches) != 1 || math.Abs(matches[0].OffsetSeconds+40) > 0.002 {
		t.Fatalf("excerpt: %+v", matches)
	}
	if m := matches[0]; m.RefStart < 39.9 || m.RefEnd > 55.1 || m.InputStart < -0.1 {
		t.Fatalf("matched range: %+v", m)
	}
}

func TestAlignReportsEveryPlacement(t *testing.T) {
	hook := song(12, 11, 140)
	in := mix(delayed(hook, 8, 70*PCMRate), delayed(hook, 41.5, 70*PCMRate), delayed(song(70, 500, 90), 0, 70*PCMRate))
	matches := Align(features(t, hook), features(t, in), Options{Thorough: true})
	// Weaker bar-shifted placements may follow; the two real ones lead.
	if len(matches) < 2 || !Ambiguous(matches) {
		t.Fatalf("%d placements: %+v", len(matches), matches)
	}
	found := map[float64]bool{}
	for _, m := range matches[:2] {
		for _, want := range []float64{8, 41.5} {
			if math.Abs(m.OffsetSeconds-want) < 0.003 {
				found[want] = true
			}
		}
	}
	if len(found) != 2 {
		t.Fatalf("placements: %+v", matches)
	}
}

func TestAlignFindsRepeatInsideOneRecording(t *testing.T) {
	// verse, hook, verse, hook, outro: the hook returns 18.4845 s later.
	const gap = 18.4845
	hook := song(10, 21, 140)
	sections := [][]float32{song(14, 600, 140), hook, song(gap-10, 601, 140), hook, song(12, 602, 140)}
	var track []float32
	for _, section := range sections {
		track = append(track, section...)
	}
	f := features(t, track)
	matches := Align(f, f, Options{MinLagSeconds: 2})
	if len(matches) == 0 || math.Abs(matches[0].OffsetSeconds-gap) > 0.002 {
		t.Fatalf("repeat: %+v", matches)
	}
	// The range resolves to one three-second chunk and may keep one more
	// chunk of the following section, which shares the beat.
	if m := matches[0]; m.RefStart < 11 || m.RefStart > 15 || m.RefEnd < 23 || m.RefEnd > 29 {
		t.Fatalf("repeated range: %+v", m)
	}
}

func BenchmarkAlignTrackAgainstClip(b *testing.B) {
	track := song(180, 31, 140)
	clip := degraded(delayed(track, 37.25, 300*PCMRate), 4)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ref, _ := Extract(track)
		in, _ := Extract(clip)
		if matches := Align(ref, in, Options{}); len(matches) != 1 {
			b.Fatal(len(matches))
		}
	}
}

func TestAlignPlacesEveryPartOfAnEditedInput(t *testing.T) {
	// The input drops 1.4 s of silence into the middle of the reference.
	ref := song(80, 61, 132)
	in := make([]float32, len(ref)+2*PCMRate)
	copy(in, ref[:35*PCMRate])
	copy(in[35*PCMRate+PCMRate*14/10:], ref[35*PCMRate:])
	matches := Align(features(t, ref), features(t, degraded(in, 8)), Options{})
	if len(matches) != 2 || Ambiguous(matches) {
		t.Fatalf("edited input: %+v", matches)
	}
	for _, m := range matches {
		want := 0.0
		if m.RefStart > 30 {
			want = 1.4
		}
		if math.Abs(m.OffsetSeconds-want) > 0.002 {
			t.Fatalf("part at %v: %+v", m.RefStart, m)
		}
	}
}

// irregular is a recording without a tempo grid: tone and noise attacks at
// random times, the hardest unrelated material to refuse by rivalry alone.
func irregular(seconds float64, seed int64) []float32 {
	r := rand.New(rand.NewSource(seed))
	out := make([]float32, int(seconds*PCMRate))
	for t := 0.1; t < seconds-0.4; t += 0.1 + 0.35*r.Float64()*r.Float64() {
		pitch, at := 150+800*r.Float64(), int(t*PCMRate)
		for i := 0; i < PCMRate/8 && at+i < len(out); i++ {
			x := float64(i) / PCMRate
			out[at+i] += float32(0.3 * math.Min(1, x/0.005) * math.Exp(-x*30) * (math.Sin(2*math.Pi*pitch*x) + 0.3*(r.Float64()*2-1)))
		}
	}
	return out
}

func TestAlignRefusesManyUnrelatedPairs(t *testing.T) {
	reported := 0
	for seed := int64(0); seed < 24; seed++ {
		length := 12 + float64(seed%5)*7
		a, b := irregular(length, 1000+seed), irregular(length+3, 2000+seed)
		if seed%3 == 0 {
			a, b = song(length, 3000+seed, 90+float64(seed)), song(length+3, 4000+seed, 151-float64(seed))
		}
		for _, o := range []Options{{}, {Thorough: true}} {
			if matches := Align(features(t, a), features(t, b), o); len(matches) != 0 {
				reported++
				t.Errorf("pair %d %+v: %+v", seed, o, matches)
			}
		}
	}
	if reported != 0 {
		t.Fatalf("%d of 48 unrelated comparisons reported a placement", reported)
	}
}

func TestExtractRejectsShortAudio(t *testing.T) {
	if _, err := Extract(make([]float32, PCMRate/2)); err != ErrTooShort {
		t.Fatal(err)
	}
}
