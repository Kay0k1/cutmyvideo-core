package toolrun

import (
	"io"
	"strings"
	"testing"
)

func TestDiagnosticCopyCannotBypassMemoryBound(t *testing.T) {
	dst := &limitedBuffer{limit: 16 << 10}
	input := strings.Repeat("private fixture", 10000)
	n, err := io.Copy(dst, io.NopCloser(strings.NewReader(input)))
	if err != nil || n != int64(len(input)) || len(dst.String()) != 16<<10 {
		t.Fatalf("diagnostic limit bypassed: read=%d retained=%d err=%v", n, len(dst.String()), err)
	}
}
