package app

import (
	"fmt"
	"testing"
	"time"
)

func BenchmarkRateLimitAtCapacity(b *testing.B) {
	s := NewServer(Config{}, nil)
	for i := 0; i < 10001; i++ {
		s.rate[fmt.Sprintf("fixture:%d", i)] = &rateEntry{reset: time.Now().Add(time.Minute)}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.allow("unseen", 1)
	}
}
