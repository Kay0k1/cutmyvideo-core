// Package audiosync aligns two recordings of the same performance by sound.
// It works on decoded mono PCM, has no process, filesystem or network access
// and depends only on the standard library.
package audiosync

import (
	"math"
	"sync"
)

// twiddles caches exp(-2*pi*i*k/n) for k < n/2 per transform size.
var twiddles sync.Map

func twiddleTable(n int) []complex128 {
	if table, ok := twiddles.Load(n); ok {
		return table.([]complex128)
	}
	table := make([]complex128, n/2)
	for k := range table {
		angle := -2 * math.Pi * float64(k) / float64(n)
		table[k] = complex(math.Cos(angle), math.Sin(angle))
	}
	twiddles.Store(n, table)
	return table
}

// fft transforms x in place. len(x) must be a power of two.
func fft(x []complex128, inverse bool) {
	n := len(x)
	if n < 2 {
		return
	}
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	table := twiddleTable(n)
	for length := 2; length <= n; length <<= 1 {
		half, stride := length>>1, n/length
		for start := 0; start < n; start += length {
			for k := 0; k < half; k++ {
				w := table[k*stride]
				if inverse {
					w = complex(real(w), -imag(w))
				}
				u := x[start+k]
				v := x[start+k+half] * w
				x[start+k] = u + v
				x[start+k+half] = u - v
			}
		}
	}
	if inverse {
		scale := 1 / float64(n)
		for i := range x {
			x[i] = complex(real(x[i])*scale, imag(x[i])*scale)
		}
	}
}

func nextPow2(n int) int {
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}

// spectrum returns the FFT of v after subtracting mean and zero-padding to n.
func spectrum(v []float32, mean float64, n int) []complex128 {
	x := make([]complex128, n)
	for i, value := range v {
		x[i] = complex(float64(value)-mean, 0)
	}
	fft(x, false)
	return x
}

// addCrossSpectrum accumulates conj(a)*b, whose inverse transform is the
// correlation c[l] = sum_k a[k]*b[k+l] with negative lags wrapped to n+l.
func addCrossSpectrum(sum, a, b []complex128) {
	for i := range sum {
		sum[i] += complex(real(a[i]), -imag(a[i])) * b[i]
	}
}
