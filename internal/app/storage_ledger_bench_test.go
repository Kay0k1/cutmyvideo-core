package app

import (
	"context"
	"testing"
)

func BenchmarkDurableStorageAdmission(b *testing.B) {
	for _, count := range []int{0, 10000} {
		b.Run(map[int]string{0: "empty", 10000: "10000_files"}[count], func(b *testing.B) {
			s := metadataCacheBenchmarkStore(b)
			c := Config{DataDir: b.TempDir(), MaxStorageBytes: 1 << 30}
			ctx := context.Background()
			if count > 0 {
				if _, err := s.DB.Exec(ctx, `INSERT INTO storage_files(path,kind,size_bytes) SELECT 'fixture-'||i,'source',1024 FROM generate_series(1,$1) i`, count); err != nil {
					b.Fatal(err)
				}
			}
			tx, err := s.storageTx(ctx)
			if err != nil {
				b.Fatal(err)
			}
			defer rollbackStorage(tx)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				fit, err := storageFits(ctx, tx, c, 1<<20, "")
				if err != nil || !fit {
					b.Fatal(err)
				}
			}
		})
	}
}
