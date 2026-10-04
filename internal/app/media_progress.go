package app

import (
	"bytes"
	"strconv"
)

// FFmpeg's machine-readable progress is untrusted subprocess output. Only a
// bounded integer media time reaches the job; diagnostics and URLs never do.
type mediaProgressWriter struct {
	line    [128]byte
	length  int
	discard bool
	totalMS int64
	lastMS  int64
	report  func(int64)
}

func (p *mediaProgressWriter) Write(data []byte) (int, error) {
	for _, b := range data {
		if b == '\n' {
			if !p.discard {
				line := p.line[:p.length]
				if bytes.HasPrefix(line, []byte("out_time_us=")) {
					us, err := strconv.ParseInt(string(line[len("out_time_us="):]), 10, 64)
					ms := us / 1000
					if err == nil && ms > p.lastMS && ms >= 0 {
						if ms > p.totalMS {
							ms = p.totalMS
						}
						if ms > p.lastMS {
							p.lastMS = ms
							p.report(ms)
						}
					}
				}
			}
			p.length, p.discard = 0, false
			continue
		}
		if p.length == len(p.line) {
			p.discard = true
		}
		if !p.discard {
			p.line[p.length] = b
			p.length++
		}
	}
	return len(data), nil
}
