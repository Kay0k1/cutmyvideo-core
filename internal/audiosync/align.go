package audiosync

import (
	"math"
	"sort"
	"sync"
)

// Options selects the voting bands and how many placements are examined.
type Options struct {
	// Band is auto, full, vocal or drums. Empty means auto: the full and
	// vocal sets are tried and the better placement wins. Drums alone are
	// never chosen automatically: different songs at one tempo share a grid.
	Band string
	// Thorough examines more coarse placements. Use it to list every
	// occurrence of a short reference or repeats inside one recording.
	Thorough bool
	// MaxMatches bounds the result; zero means 8.
	MaxMatches int
	// MinLagSeconds ignores placements closer than this to zero offset. It is
	// used to find repeats of a recording inside itself.
	MinLagSeconds float64
	// MinSimilarity drops placements whose agreeing chunks sound less alike.
	MinSimilarity float64
	// Workers bounds concurrent chunk correlation; zero means 4.
	Workers int
}

// Match places the reference inside the input: input_time = ref_time + offset
// over the matched range. When Speed differs from 1 the mapping is linear
// between the range endpoints and OffsetSeconds holds at the range middle.
type Match struct {
	OffsetSeconds float64
	// Speed is how much faster the reference content plays in the input.
	Speed      float64
	Confidence float64
	Band       string
	// Method is waveform when both recordings share a master and the offset
	// was refined on samples, envelope when only attacks agree.
	Method                 string
	RefStart, RefEnd       float64
	InputStart, InputEnd   float64
	Chunks, Agreeing       int
	Similarity             float64
	coarseScore, coarseLag float64
}

// MinConfidence separates a reported match from no match. A wrong offset is
// worse than a refusal, so the bar is deliberately high.
const MinConfidence = 0.6

const (
	searchMS      = 300 // fine search half-width around a coarse placement
	toleranceMS   = 4.0 // chunks agree when their offsets differ by less
	maxChunks     = 48
	maxCandidates = 6
	// A second placement at least this strong relative to the best one is a
	// rival: the answer is ambiguous. A thorough listing keeps weaker ones.
	rivalRatio    = 0.7
	thoroughRatio = 0.5
)

type candidate struct {
	lag   int // fine samples: input index minus reference index
	slope float64
	score float64
}

// Align returns verified placements, strongest first. One placement is the
// answer. Several are either parts of an edited input, each over its own
// range, or rivals for the same range; Ambiguous tells which, and a caller
// that expects one answer must not pick the first rival. An empty result means
// the recordings share no audible material that can be trusted.
func Align(ref, in *Features, o Options) []Match {
	sets := selectedSets(o.Band)
	if len(sets) == 0 {
		return nil
	}
	self := ref == in
	minLag := int(math.Round(o.MinLagSeconds * fineRate))
	limit := maxCandidates
	if o.Thorough || self {
		limit = 4 * maxCandidates
	}
	correlations := coarseCorrelations(ref, in)
	var candidates []candidate
	for _, set := range sets {
		candidates = mergeCandidates(candidates, coarsePeaks(ref, in, correlations, set.bands, minLag, self, limit))
	}
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	workers := o.Workers
	if workers <= 0 {
		workers = 4
	}
	var matches []Match
	for _, c := range candidates {
		m, ok := verify(ref, in, c, sets, workers, o.Thorough || self)
		if !ok || m.Confidence < MinConfidence || m.Similarity < o.MinSimilarity {
			continue
		}
		if minLag > 0 && math.Abs(m.OffsetSeconds)*fineRate < float64(minLag) {
			continue
		}
		matches = appendDistinct(matches, m)
	}
	if len(matches) == 0 && !self {
		if c, ok := driftCandidate(ref, in, sets); ok {
			if m, ok := verify(ref, in, c, sets, workers, false); ok && m.Confidence >= MinConfidence {
				matches = append(matches, m)
			}
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].strength() > matches[j].strength() })
	if len(matches) > 1 {
		// Bar-shifted placements of loop-based music agree chunk by chunk
		// but are clearly weaker: keep only real rivals of the best one.
		// A well supported placement of another part of the reference is
		// kept as well: the input is an edit with its own offset per part.
		ratio := rivalRatio
		if o.Thorough || self {
			ratio = thoroughRatio
		}
		kept := matches[:1:1]
		for _, m := range matches[1:] {
			separate := !self && m.Agreeing >= 3 && m.Confidence >= 0.9
			for _, k := range kept {
				if overlapShare(m, k) > 0.5 || inputOverlapShare(m, k) > 0.5 {
					separate = false
				}
			}
			if separate || m.strength() >= ratio*matches[0].strength() {
				kept = append(kept, m)
			}
		}
		matches = kept
	}
	maximum := o.MaxMatches
	if maximum <= 0 {
		maximum = 8
	}
	if len(matches) > maximum {
		matches = matches[:maximum]
	}
	return matches
}

// overlapShare is the part of a's reference range that b's range covers.
func overlapShare(a, b Match) float64 {
	if a.RefEnd <= a.RefStart {
		return 0
	}
	return math.Max(0, math.Min(a.RefEnd, b.RefEnd)-math.Max(a.RefStart, b.RefStart)) / (a.RefEnd - a.RefStart)
}

func inputOverlapShare(a, b Match) float64 {
	if a.InputEnd <= a.InputStart {
		return 0
	}
	return math.Max(0, math.Min(a.InputEnd, b.InputEnd)-math.Max(a.InputStart, b.InputStart)) / (a.InputEnd - a.InputStart)
}

// Ambiguous reports placements that put the same part of the reference, or
// of the input, at different offsets. Placements of different parts on both
// sides describe one edited input.
func Ambiguous(matches []Match) bool {
	for i := range matches {
		for j := i + 1; j < len(matches); j++ {
			if overlapShare(matches[i], matches[j]) > 0.5 || overlapShare(matches[j], matches[i]) > 0.5 || inputOverlapShare(matches[i], matches[j]) > 0.5 || inputOverlapShare(matches[j], matches[i]) > 0.5 {
				return true
			}
		}
	}
	return false
}

// strength ranks placements by how alike the agreeing chunks sound, slightly
// discounting placements supported by very few chunks.
func (m Match) strength() float64 {
	return m.Similarity * (1 - math.Pow(0.5, float64(m.Agreeing)))
}

func selectedSets(band string) []struct {
	name  string
	bands []int
} {
	if band == "" || band == "auto" {
		return bandSets[:2]
	}
	for i := range bandSets {
		if bandSets[i].name == band {
			return bandSets[i : i+1]
		}
	}
	return nil
}

func appendDistinct(matches []Match, m Match) []Match {
	for i := range matches {
		if math.Abs(matches[i].OffsetSeconds-m.OffsetSeconds) < 0.05 && m.RefStart < matches[i].RefEnd && matches[i].RefStart < m.RefEnd {
			if m.Confidence > matches[i].Confidence {
				matches[i] = m
			}
			return matches
		}
	}
	return append(matches, m)
}

func mergeCandidates(all, more []candidate) []candidate {
	for _, c := range more {
		duplicate := false
		for i := range all {
			if abs(all[i].lag-c.lag) <= 50 {
				if c.score > all[i].score {
					all[i] = c
				}
				duplicate = true
				break
			}
		}
		if !duplicate {
			all = append(all, c)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })
	return all
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// coarseCorrelations correlates every band over all lags at the coarse rate.
// Index l >= 0 is lag l; index n+l is negative lag l.
func coarseCorrelations(ref, in *Features) [][]float32 {
	nr, ni := len(ref.coarse[0]), len(in.coarse[0])
	n := nextPow2(nr + ni)
	out := make([][]float32, len(bandEdges))
	for band := range bandEdges {
		if !ref.active[band] || !in.active[band] {
			continue
		}
		sum := make([]complex128, n)
		addCrossSpectrum(sum, spectrum(ref.coarse[band], 0, n), spectrum(in.coarse[band], 0, n))
		fft(sum, true)
		values := make([]float32, n)
		for i := range sum {
			values[i] = float32(real(sum[i]))
		}
		out[band] = values
	}
	return out
}

// coarsePeaks scores every lag by correlation per square root of overlap, so
// that unrelated material has comparable spread at every lag, and returns the
// strongest separated peaks as robust z-scores.
func coarsePeaks(ref, in *Features, correlations [][]float32, bands []int, minLag int, self bool, limit int) []candidate {
	nr, ni := len(ref.coarse[0]), len(in.coarse[0])
	used := 0
	for _, band := range bands {
		if correlations[band] != nil {
			used++
		}
	}
	if used == 0 {
		return nil
	}
	n := 0
	for _, band := range bands {
		if correlations[band] != nil {
			n = len(correlations[band])
		}
	}
	minOverlap := min(500, min(nr, ni))
	first, last := -(nr - minOverlap), ni-minOverlap
	if self {
		first = max(1, minLag/coarseStep)
	}
	if last < first {
		return nil
	}
	scores := make([]float64, last-first+1)
	for lag := first; lag <= last; lag++ {
		if !self && minLag > 0 && abs(lag)*coarseStep < minLag {
			scores[lag-first] = math.Inf(-1)
			continue
		}
		index := lag
		if lag < 0 {
			index += n
		}
		var sum float64
		for _, band := range bands {
			if correlations[band] != nil {
				sum += float64(correlations[band][index])
			}
		}
		overlap := min(nr, ni-lag) - max(0, -lag)
		scores[lag-first] = sum / math.Sqrt(float64(overlap*used))
	}
	median, spread := robustSpread(scores)
	if !(spread > 0) {
		return nil
	}
	const separation = 50 // coarse frames
	var peaks []candidate
	taken := make([]bool, len(scores))
	for len(peaks) < limit {
		best, bestScore := -1, math.Inf(-1)
		for i, score := range scores {
			if !taken[i] && score > bestScore {
				best, bestScore = i, score
			}
		}
		if best < 0 {
			break
		}
		z := (bestScore - median) / spread
		if z < 4.5 {
			break
		}
		peaks = append(peaks, candidate{lag: (best + first) * coarseStep, score: z})
		for i := max(0, best-separation); i <= min(len(scores)-1, best+separation); i++ {
			taken[i] = true
		}
	}
	return peaks
}

func robustSpread(values []float64) (median, spread float64) {
	step := max(1, len(values)/50000)
	sample := make([]float64, 0, len(values)/step+1)
	for i := 0; i < len(values); i += step {
		if !math.IsInf(values[i], 0) {
			sample = append(sample, values[i])
		}
	}
	if len(sample) < 8 {
		return 0, 0
	}
	sort.Float64s(sample)
	median = sample[len(sample)/2]
	for i := range sample {
		sample[i] = math.Abs(sample[i] - median)
	}
	sort.Float64s(sample)
	return median, 1.4826 * sample[len(sample)/2]
}

type chunk struct {
	start, length int // reference fine samples
	predicted     int // input start for the predicted lag
	lagMin        int // first searched lag relative to predicted
	correlation   [][]float64
	refEnergy     []float64
	inEnergy      [][]float64
}

type vote struct {
	time   float64 // reference seconds at the chunk middle
	offset float64 // milliseconds
	rho    float64
	chunk  int
}

// verify splits the overlap into chunks, lets each chunk find its own fine
// offset near the coarse placement and accepts the placement only when chunks
// agree on one line. Agreement by chance is priced with a binomial tail.
func verify(ref, in *Features, c candidate, sets []struct {
	name  string
	bands []int
}, workers int, thorough bool) (Match, bool) {
	nr, ni := len(ref.fine[0]), len(in.fine[0])
	scale := 1 + c.slope
	lo := max(0, int(math.Ceil(-float64(c.lag)/scale)))
	hi := min(nr, int(math.Floor(float64(ni-c.lag)/scale)))
	overlap := hi - lo
	if overlap < 3000 {
		return Match{}, false
	}
	length := min(5000, max(1000, overlap/6))
	count := min(maxChunks, overlap/length)
	if thorough {
		// A short repeat must contain several chunks: shorter, half
		// overlapping and twice as many.
		length = min(length, 3000)
		count = min(2*maxChunks, 2*overlap/length-1)
	}
	spacing := float64(overlap-length) / float64(max(1, count-1))
	chunks := make([]*chunk, 0, count)
	for k := 0; k < count; k++ {
		start := lo + int(float64(k)*spacing)
		predicted := start + c.lag + int(math.Round(c.slope*float64(start+length/2)))
		if predicted < 0 || predicted+length > ni {
			continue
		}
		chunks = append(chunks, &chunk{start: start, length: length, predicted: predicted})
	}
	if len(chunks) < 2 {
		return Match{}, false
	}
	jobs := make(chan *chunk)
	var wait sync.WaitGroup
	for w := 0; w < workers; w++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for ch := range jobs {
				ch.correlate(ref, in)
			}
		}()
	}
	for _, ch := range chunks {
		jobs <- ch
	}
	close(jobs)
	wait.Wait()

	// More placements examined means more chances to agree by accident.
	family := 2 * maxCandidates
	if thorough {
		family *= 4
	}
	var best Match
	found := false
	for _, set := range sets {
		m, ok := consensus(ref, in, chunks, set.bands, family)
		if !ok {
			continue
		}
		m.Band = set.name
		if !found || m.Confidence > best.Confidence || m.Confidence == best.Confidence && m.Similarity > best.Similarity {
			best, found = m, true
		}
	}
	if found {
		best.coarseScore, best.coarseLag = c.score, float64(c.lag)
	}
	return best, found
}

func (ch *chunk) correlate(ref, in *Features) {
	ni := len(in.fine[0])
	windowStart := max(0, ch.predicted-searchMS)
	windowEnd := min(ni, ch.predicted+ch.length+searchMS)
	ch.lagMin = windowStart - ch.predicted
	lags := windowEnd - windowStart - ch.length + 1
	n := nextPow2(windowEnd - windowStart + ch.length)
	ch.correlation = make([][]float64, len(bandEdges))
	ch.inEnergy = make([][]float64, len(bandEdges))
	ch.refEnergy = make([]float64, len(bandEdges))
	for band := range bandEdges {
		if !ref.active[band] || !in.active[band] {
			continue
		}
		a := ref.fine[band][ch.start : ch.start+ch.length]
		b := in.fine[band][windowStart:windowEnd]
		meanA, meanB := mean32(a), mean32(b)
		sum := make([]complex128, n)
		addCrossSpectrum(sum, spectrum(a, meanA, n), spectrum(b, meanB, n))
		fft(sum, true)
		correlation := make([]float64, lags)
		for i := range correlation {
			correlation[i] = real(sum[i])
		}
		ch.correlation[band] = correlation
		for _, v := range a {
			d := float64(v) - meanA
			ch.refEnergy[band] += d * d
		}
		// Sliding energy of the input under the chunk for normalisation.
		energy := make([]float64, lags)
		var running float64
		for i := 0; i < ch.length; i++ {
			d := float64(b[i]) - meanB
			running += d * d
		}
		energy[0] = running
		for i := 1; i < lags; i++ {
			out := float64(b[i-1]) - meanB
			into := float64(b[i+ch.length-1]) - meanB
			running += into*into - out*out
			energy[i] = running
		}
		ch.inEnergy[band] = energy
	}
}

func mean32(values []float32) float64 {
	var sum float64
	for _, v := range values {
		sum += float64(v)
	}
	return sum / float64(len(values))
}

// peak returns this chunk's best lag for a band set, or ok=false when the
// chunk is quiet in the reference or has no outstanding peak.
func (ch *chunk) peak(bands []int) (v vote, ok bool) {
	var total []float64
	var refEnergy float64
	used := 0
	for _, band := range bands {
		if ch.correlation[band] == nil {
			continue
		}
		if total == nil {
			total = make([]float64, len(ch.correlation[band]))
		}
		for i, value := range ch.correlation[band] {
			total[i] += value
		}
		refEnergy += ch.refEnergy[band]
		used++
	}
	// Features have unit variance over a recording; a chunk far below that
	// is a quiet passage with nothing to align.
	if used == 0 || len(total) < 3 || refEnergy < 0.05*float64(ch.length*used) {
		return v, false
	}
	best := 0
	for i, value := range total {
		if value > total[best] {
			best = i
		}
	}
	var inEnergy float64
	for _, band := range bands {
		if ch.inEnergy[band] != nil {
			inEnergy += ch.inEnergy[band][best]
		}
	}
	if !(inEnergy > 0) || !(total[best] > 0) {
		return v, false
	}
	rho := total[best] / math.Sqrt(refEnergy*inEnergy)
	var sum, sumSquares float64
	others := 0
	for i, value := range total {
		if abs(i-best) > 20 {
			sum += value
			sumSquares += value * value
			others++
		}
	}
	if others < 20 {
		return v, false
	}
	meanOthers := sum / float64(others)
	deviation := math.Sqrt(math.Max(sumSquares/float64(others)-meanOthers*meanOthers, 0))
	if !(deviation > 0) || (total[best]-meanOthers)/deviation < 3 || rho < 0.1 {
		return v, false
	}
	position := float64(best)
	if best > 0 && best < len(total)-1 {
		left, right := total[best-1], total[best+1]
		if curvature := left - 2*total[best] + right; curvature < 0 {
			position += 0.5 * (left - right) / curvature
		}
	}
	lag := float64(ch.predicted-ch.start+ch.lagMin) + position
	return vote{time: float64(ch.start+ch.length/2) / fineRate, offset: lag * 1000 / fineRate, rho: rho}, true
}

// consensus finds the line most chunk offsets agree on and prices it.
func consensus(ref, in *Features, chunks []*chunk, bands []int, family int) (Match, bool) {
	votes := make([]vote, 0, len(chunks))
	evaluated := 0
	for i, ch := range chunks {
		v, ok := ch.peak(bands)
		if ok {
			v.chunk = i
			votes = append(votes, v)
		}
		if ok || ch.refEnergyTotal(bands) >= 0.05*float64(ch.length) {
			evaluated++
		}
	}
	if len(votes) < 2 {
		return Match{}, false
	}
	// Flat hypotheses: every vote proposes its own offset. Votes weigh by how
	// alike the chunk sounds, so a few chunks of the same audio outweigh more
	// chunks that merely share a tempo grid at another offset.
	bestWeight, bestIntercept, bestSlope := 0.0, 0.0, 0.0
	for _, v := range votes {
		if _, weight := countAgreeing(votes, v.offset, 0, v.time); weight > bestWeight {
			bestWeight, bestIntercept, bestSlope = weight, v.offset, 0
		}
	}
	// Sloped hypotheses through pairs at least ten seconds apart, up to 2 %.
	flatWeight := bestWeight
	for i := range votes {
		for j := i + 1; j < len(votes); j++ {
			span := votes[j].time - votes[i].time
			if span < 10 {
				continue
			}
			slope := (votes[j].offset - votes[i].offset) / span
			// A slope must move chunks well beyond their own scatter.
			if math.Abs(slope) > 20 || math.Abs(slope)*span < 3*toleranceMS {
				continue
			}
			if count, weight := countAgreeing(votes, votes[i].offset, slope, votes[i].time); weight > bestWeight && weight >= 1.3*flatWeight && count >= 6 {
				bestWeight, bestIntercept, bestSlope = weight, votes[i].offset-slope*votes[i].time, slope
			}
		}
	}
	agreeing := make([]vote, 0, len(votes))
	for _, v := range votes {
		if math.Abs(v.offset-(bestIntercept+bestSlope*v.time)) <= toleranceMS {
			agreeing = append(agreeing, v)
		}
	}
	if len(agreeing) < 2 {
		return Match{}, false
	}
	rhos := make([]float64, len(agreeing))
	for i, v := range agreeing {
		rhos[i] = v.rho
	}
	sort.Float64s(rhos)
	similarity := rhos[len(rhos)/2]

	// The matched range is the longest run of chunks that agree and sound
	// alike, allowing one weaker chunk inside. Material that merely shares the
	// beat after a repeated section agrees too, but clearly less alike, and
	// must not stretch the range or bend the line. Its resolution is one chunk.
	strong := 0.6 * rhos[len(rhos)*3/4]
	core := make([]vote, 0, len(agreeing))
	for _, v := range agreeing {
		if v.rho >= strong {
			core = append(core, v)
		}
	}
	runStart, runEnd, bestStart, bestEnd := 0, 0, 0, 0
	for i := 1; i <= len(core); i++ {
		if i < len(core) && core[i].chunk-core[i-1].chunk <= 2 {
			runEnd = i
			continue
		}
		if runEnd-runStart > bestEnd-bestStart {
			bestStart, bestEnd = runStart, runEnd
		}
		runStart, runEnd = i, i
	}
	run := core[bestStart : bestEnd+1]
	// A slope must move the run's chunks well beyond their own scatter.
	intercept, slope := fitLine(run, bestSlope != 0)
	if math.Abs(slope)*(run[len(run)-1].time-run[0].time) < 3*toleranceMS {
		intercept, slope = fitLine(run, false)
	}
	method := "envelope"
	if refined, ok := refineOnWaveform(ref, in, run, intercept, slope); ok {
		intercept, slope = fitLine(refined, slope != 0)
		method = "waveform"
	}
	first, last := chunks[run[0].chunk], chunks[run[len(run)-1].chunk]
	refStart := float64(first.start) / fineRate
	refEnd := float64(last.start+last.length) / fineRate
	at := func(t float64) float64 { return (intercept + slope*t) / 1000 }
	middle := (refStart + refEnd) / 2
	// Price the run against chance. A chunk of unrelated material lands
	// within tolerance of a given line with probability p; a run of k
	// independent chunks in a row can start at any of the evaluated chunks and
	// is searched for under family coarse placements and band sets. Chunks
	// that overlap in time are not independent and count once.
	independent, covered := 0, -1
	for _, v := range run {
		if ch := chunks[v.chunk]; ch.start >= covered {
			independent++
			covered = ch.start + ch.length
		}
	}
	positions, covered := 0, -1
	for _, ch := range chunks {
		if ch.start >= covered {
			positions++
			covered = ch.start + ch.length
		}
	}
	p := 2 * toleranceMS / (2 * searchMS)
	chance := math.Min(1, float64(family*positions)*math.Pow(p, float64(independent-1)))
	quality := math.Min(1, math.Max(0, (similarity-0.15)/0.15))
	confidence := (1 - chance) * quality
	// The coarse search already picked the lag where attacks coincide best,
	// so a few neighbouring chunks of unrelated material can agree there.
	// Attacks that are only loosely alike need a longer run to count, unless
	// the samples themselves agree.
	required := 3
	if similarity < 0.5 && method != "waveform" {
		required = 5
	}
	if independent < required {
		confidence *= 0.5
	}
	return Match{
		OffsetSeconds: at(middle),
		Speed:         1 / (1 + slope/1000),
		Confidence:    confidence,
		Method:        method,
		RefStart:      refStart,
		RefEnd:        refEnd,
		InputStart:    refStart + at(refStart),
		InputEnd:      refEnd + at(refEnd),
		Chunks:        evaluated,
		Agreeing:      len(run),
		Similarity:    similarity,
	}, true
}

func (ch *chunk) refEnergyTotal(bands []int) float64 {
	var total float64
	used := 0
	for _, band := range bands {
		if ch.correlation[band] != nil {
			total += ch.refEnergy[band]
			used++
		}
	}
	if used == 0 {
		return 0
	}
	return total / float64(used)
}

func countAgreeing(votes []vote, offset, slope, at float64) (count int, weight float64) {
	for _, v := range votes {
		if math.Abs(v.offset-(offset+slope*(v.time-at))) <= toleranceMS {
			count++
			weight += v.rho
		}
	}
	return count, weight
}

// fitLine returns offset = intercept + slope*time in milliseconds. A flat fit
// uses the median so one stray chunk cannot move the answer.
func fitLine(votes []vote, sloped bool) (intercept, slope float64) {
	if sloped && len(votes) >= 4 {
		var st, so, stt, sto float64
		for _, v := range votes {
			st += v.time
			so += v.offset
			stt += v.time * v.time
			sto += v.time * v.offset
		}
		n := float64(len(votes))
		if denominator := n*stt - st*st; denominator > 1e-9 {
			slope = (n*sto - st*so) / denominator
			return (so - slope*st) / n, slope
		}
	}
	offsets := make([]float64, len(votes))
	for i, v := range votes {
		offsets[i] = v.offset
	}
	sort.Float64s(offsets)
	if len(offsets)%2 == 1 {
		return offsets[len(offsets)/2], 0
	}
	return (offsets[len(offsets)/2-1] + offsets[len(offsets)/2]) / 2, 0
}

// refineOnWaveform correlates one second of raw samples per agreeing chunk
// within ±4 ms of the envelope answer. Recordings cut from one master agree to
// the sample; a remix or another take does not and keeps the envelope offset.
func refineOnWaveform(ref, in *Features, votes []vote, intercept, slope float64) ([]vote, bool) {
	const window = PCMRate
	const reach = PCMRate * 4 / 1000
	step := max(1, len(votes)/16)
	var refined []vote
	tested := 0
	for i := 0; i < len(votes); i += step {
		v := votes[i]
		start := int(v.time*PCMRate) - window/2
		offset := int(math.Round((intercept + slope*v.time) * PCMRate / 1000))
		if start < 0 || start+window > len(ref.pcm) || start+offset-reach < 0 || start+offset+reach+window > len(in.pcm) {
			continue
		}
		tested++
		a := ref.pcm[start : start+window]
		b := in.pcm[start+offset-reach : start+offset+reach+window]
		meanA, meanB := mean32(a), mean32(b)
		var energyA, energyB float64
		for _, s := range a {
			d := float64(s) - meanA
			energyA += d * d
		}
		if !(energyA > 1e-9) {
			continue
		}
		for _, s := range b[:window] {
			d := float64(s) - meanB
			energyB += d * d
		}
		values := make([]float64, 2*reach+1)
		bestLag, bestRho := 0, -1.0
		for lag := -reach; lag <= reach; lag++ {
			shifted := b[lag+reach : lag+reach+window]
			var cross float64
			for k, s := range a {
				cross += (float64(s) - meanA) * (float64(shifted[k]) - meanB)
			}
			if energyB > 1e-12 {
				values[lag+reach] = cross / math.Sqrt(energyA*energyB)
			}
			if values[lag+reach] > bestRho {
				bestLag, bestRho = lag, values[lag+reach]
			}
			if lag < reach {
				out, into := float64(b[lag+reach])-meanB, float64(b[lag+reach+window])-meanB
				energyB += into*into - out*out
			}
		}
		if bestRho < 0.5 || bestLag == -reach || bestLag == reach {
			continue
		}
		position := float64(bestLag)
		left, right := values[bestLag+reach-1], values[bestLag+reach+1]
		if curvature := left - 2*bestRho + right; curvature < 0 {
			position += 0.5 * (left - right) / curvature
		}
		v.offset = (float64(offset) + position) * 1000 / PCMRate
		refined = append(refined, v)
	}
	if len(refined) < 2 || 2*len(refined) < tested {
		return nil, false
	}
	return refined, true
}

// driftCandidate handles a recording played at a slightly different speed,
// which smears the whole-recording correlation. Twenty-second reference chunks
// are placed independently and a line is fitted through their lags.
func driftCandidate(ref, in *Features, sets []struct {
	name  string
	bands []int
}) (candidate, bool) {
	const length = 2000 // coarse frames
	nr := len(ref.coarse[0])
	if nr < 2*length {
		return candidate{}, false
	}
	count := min(16, nr/length)
	type placed struct{ time, lag float64 }
	var points []placed
	bands := sets[0].bands
	for k := 0; k < count; k++ {
		start := k * (nr - length) / max(1, count-1)
		part := &Features{coarse: make([][]float32, len(bandEdges)), active: ref.active}
		for band := range bandEdges {
			part.coarse[band] = ref.coarse[band][start : start+length]
		}
		peaks := coarsePeaks(part, in, coarseCorrelations(part, in), bands, 0, false, 1)
		if len(peaks) == 0 {
			continue
		}
		points = append(points, placed{time: float64(start + length/2), lag: float64(peaks[0].lag)/coarseStep - float64(start)})
	}
	bestCount := 0
	var best candidate
	for i := range points {
		for j := i + 1; j < len(points); j++ {
			slope := (points[j].lag - points[i].lag) / (points[j].time - points[i].time)
			if math.Abs(slope) > 0.02 {
				continue
			}
			count := 0
			for _, p := range points {
				if math.Abs(p.lag-(points[i].lag+slope*(p.time-points[i].time))) <= 3 {
					count++
				}
			}
			if count > bestCount {
				bestCount = count
				intercept := points[i].lag - slope*points[i].time
				best = candidate{lag: int(math.Round(intercept * coarseStep)), slope: slope, score: float64(count)}
			}
		}
	}
	return best, bestCount >= 3
}
