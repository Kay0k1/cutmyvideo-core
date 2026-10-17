package app

import (
	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

// This private compatibility writer delegates to the single backend parser.
// State mirrors only the fields inspected by the existing bounded-line tests.
type mediaProgressWriter struct {
	totalMS int64
	report  func(int64)
	writer  *localmedia.ProgressWriter
	length  int
	discard bool
}

func (p *mediaProgressWriter) Write(data []byte) (int, error) {
	if p.writer == nil {
		p.writer = localmedia.NewProgressWriter(p.totalMS, p.report)
	}
	n, e := p.writer.Write(data)
	p.length, p.discard = p.writer.State()
	return n, e
}
