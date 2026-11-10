package audiosync

import (
	"errors"
	"math"
)

// PCMRate is the only accepted sample rate. It keeps onsets up to 8 kHz and
// gives 62.5 µs waveform resolution.
const PCMRate = 16000

// fineRate is the onset envelope rate; coarseStep fine samples make one coarse
// frame used by the whole-recording search.
const fineRate = 1000
const coarseStep = 10

// Band edges in Hz. Sets select which bands vote: a remix that keeps the vocal
// over another instrumental aligns on the vocal bands, a live take on drums.
var bandEdges = [][2]float64{{40, 120}, {120, 300}, {300, 700}, {700, 1500}, {1500, 3000}, {3000, 5500}, {5500, 7600}}

var bandSets = []struct {
	name  string
	bands []int
}{
	{"full", []int{0, 1, 2, 3, 4, 5, 6}},
	{"vocal", []int{2, 3, 4}},
	{"drums", []int{0, 5, 6}},
}

// Features is the analysed form of one recording. PCM is retained for the
// sample-accurate waveform refinement of recordings that share a master.
type Features struct {
	pcm    []float32
	fine   [][]float32 // per band, standardized onset strength at fineRate
	coarse [][]float32 // per band, standardized, fineRate/coarseStep
	active []bool      // band carries signal
}

// DurationSeconds reports the analysed length.
func (f *Features) DurationSeconds() float64 { return float64(len(f.pcm)) / PCMRate }

// ErrTooShort means a recording has less than one second of audio.
var ErrTooShort = errors.New("audio is shorter than one second")

// Extract analyses mono PCM sampled at PCMRate. The slice is retained.
func Extract(pcm []float32) (*Features, error) {
	if len(pcm) < PCMRate {
		return nil, ErrTooShort
	}
	f := &Features{pcm: pcm, fine: make([][]float32, len(bandEdges)), coarse: make([][]float32, len(bandEdges)), active: make([]bool, len(bandEdges))}
	for band, edges := range bandEdges {
		f.fine[band], f.coarse[band], f.active[band] = onsetStrength(pcm, edges[0], edges[1])
	}
	return f, nil
}

// onsetStrength band-passes the signal, follows its energy with a 4 ms time
// constant, and keeps the positive change of log energy: a gain-invariant
// attack detector that ignores sustained level and release.
func onsetStrength(pcm []float32, lo, hi float64) (fine, coarse []float32, active bool) {
	const decimate = PCMRate / fineRate
	f0 := math.Sqrt(lo * hi)
	w0 := 2 * math.Pi * f0 / PCMRate
	alpha := math.Sin(w0) / (2 * f0 / (hi - lo))
	a0 := 1 + alpha
	b0, b2 := alpha/a0, -alpha/a0
	a1, a2 := -2*math.Cos(w0)/a0, (1-alpha)/a0
	smooth := 1 - math.Exp(-1/(0.004*PCMRate))
	n := len(pcm) / decimate
	energy := make([]float64, n)
	var x1, x2, y1, y2, u1, u2, v1, v2, follower, block, total float64
	for i := 0; i < n*decimate; i++ {
		x := float64(pcm[i])
		y := b0*x + b2*x2 - a1*y1 - a2*y2
		x2, x1, y2, y1 = x1, x, y1, y
		v := b0*y + b2*u2 - a1*v1 - a2*v2
		u2, u1, v2, v1 = u1, y, v1, v
		follower += smooth * (v*v - follower)
		block += follower
		if i%decimate == decimate-1 {
			energy[i/decimate] = block / decimate
			total += block / decimate
			block = 0
		}
	}
	fine = make([]float32, n)
	coarse = make([]float32, n/coarseStep)
	mean := total / float64(n)
	if !(mean > 1e-14) {
		return fine, coarse, false
	}
	floor := mean * 1e-3
	const span = 5
	logs := make([]float64, n)
	for i, e := range energy {
		logs[i] = math.Log(e + floor)
	}
	var sum, sumSquares float64
	rise := make([]float64, n)
	for i := span; i < n; i++ {
		if d := logs[i] - logs[i-span]; d > 0 {
			rise[i] = d
			sum += d
			sumSquares += d * d
		}
	}
	fineMean := sum / float64(n)
	deviation := math.Sqrt(math.Max(sumSquares/float64(n)-fineMean*fineMean, 0))
	if !(deviation > 1e-9) {
		return fine, coarse, false
	}
	for i, value := range rise {
		fine[i] = float32((value - fineMean) / deviation)
	}
	sum, sumSquares = 0, 0
	frames := make([]float64, len(coarse))
	for i := range frames {
		var s float64
		for _, value := range rise[i*coarseStep : (i+1)*coarseStep] {
			s += value
		}
		frames[i] = s / coarseStep
		sum += frames[i]
		sumSquares += frames[i] * frames[i]
	}
	if len(frames) == 0 {
		return fine, coarse, false
	}
	coarseMean := sum / float64(len(frames))
	deviation = math.Sqrt(math.Max(sumSquares/float64(len(frames))-coarseMean*coarseMean, 0))
	if !(deviation > 1e-9) {
		return fine, coarse, false
	}
	for i, value := range frames {
		coarse[i] = float32((value - coarseMean) / deviation)
	}
	return fine, coarse, true
}
