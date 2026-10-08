package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func BenchmarkSourceList20(b *testing.B) {
	s := metadataCacheBenchmarkStore(b)
	s.ConfigureStorage(Config{SourceTTL: time.Hour})
	ctx := context.Background()
	if _, err := s.DB.Exec(ctx, `INSERT INTO sources(id,owner,title,duration_ms,kind) SELECT 'fixture-'||i,'owner','Video '||i,10000,'upload' FROM generate_series(1,20) i`); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		sources, err := s.Sources(ctx, "owner")
		if err != nil || len(sources) != 20 {
			b.Fatal("incomplete source list", err)
		}
	}
}

func BenchmarkStorageDeleteBatch200(b *testing.B) {
	s := metadataCacheBenchmarkStore(b)
	c := Config{DataDir: b.TempDir()}
	ctx := context.Background()
	paths := make([]string, storageBatch)
	for i := range paths {
		paths[i] = filepath.Join(c.DataDir, fmt.Sprintf("missing-%d", i))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		if _, err := s.DB.Exec(ctx, `INSERT INTO storage_files(path,kind,size_bytes,delete_pending) SELECT path,'tombstone',1024,true FROM unnest($1::text[]) AS f(path)`, paths); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if err := s.DrainStorageDeletes(ctx, c, paths); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStorageReconcileUnchanged128(b *testing.B) {
	s := metadataCacheBenchmarkStore(b)
	c := Config{DataDir: b.TempDir(), SourceTTL: time.Hour, ArtifactTTL: time.Hour}
	ctx := context.Background()
	for i := range storageScanBatchSize {
		path := filepath.Join(c.DataDir, fmt.Sprintf("orphan-%d", i))
		if err := os.WriteFile(path, []byte("media"), 0600); err != nil {
			b.Fatal(err)
		}
	}
	if err := s.reconcileDirectory(ctx, c.DataDir); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := s.reconcileDirectory(ctx, c.DataDir); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStorageRetentionSources200(b *testing.B) {
	s := metadataCacheBenchmarkStore(b)
	ctx := context.Background()
	paths := make([]string, storageBatch)
	dir := b.TempDir()
	for i := range paths {
		paths[i] = filepath.Join(dir, fmt.Sprintf("missing-%d", i))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		if _, err := s.DB.Exec(ctx, "DELETE FROM storage_files; DELETE FROM sources"); err != nil {
			b.Fatal(err)
		}
		if _, err := s.DB.Exec(ctx, `INSERT INTO sources(id,owner,title,duration_ms,kind,path,created_at) SELECT 'fixture-'||ordinal,'owner','Video',10000,'upload',path,now()-interval '2 hours' FROM unnest($1::text[]) WITH ORDINALITY AS f(path,ordinal)`, paths); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		removed, err := s.Cleanup(ctx, time.Hour, time.Hour)
		if err != nil || len(removed) != storageBatch {
			b.Fatal("incomplete retention batch", err)
		}
	}
}
