package localmedia

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
)

func checkPassWorkspace(path string, limit int64) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	var size int64
	for _, entry := range entries {
		// Only x264's fixed two pass-statistics filenames are permitted. Paths
		// and names are generated privately; users never supply a prefix.
		if entry.Name() != "video-0.log" && entry.Name() != "video-0.log.mbtree" && entry.Name() != "video-0.log.temp" && entry.Name() != "video-0.log.mbtree.temp" {
			return &mediaerror.Problem{Code: "target_size_unattainable", Message: "two-pass workspace contains an unexpected statistics file"}
		}
		info, err := os.Lstat(filepath.Join(path, entry.Name()))
		if os.IsNotExist(err) {
			continue
		} // x264 atomically renames completed logs.
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > limit-size {
			return &mediaerror.Problem{Code: "target_size_unattainable", Message: "two-pass statistics exceed the supported private workspace limit"}
		}
		size += info.Size()
	}
	return nil
}

// RunBoundedWorkspace supervises opaque FFmpeg file writes on all platforms.
// Polling is not an instantaneous filesystem hard limit; admission reserves
// separate burst headroom. Both passes and the final observation enforce the
// same cap before an output can be published.
func (runtime joinedMediaRuntime) RunBoundedWorkspace(ctx context.Context, args []string, total int64, report func(int64), path string, limit int64) error {
	if err := checkPassWorkspace(path, limit); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop, done := make(chan struct{}), make(chan struct{})
	var observed error
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if err := checkPassWorkspace(path, limit); err != nil {
					observed = err
					cancel()
					return
				}
			}
		}
	}()
	err := runtime.Run(runCtx, args, total, report)
	close(stop)
	<-done
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if observed != nil {
		return observed
	}
	if e := checkPassWorkspace(path, limit); e != nil {
		return e
	}
	return err
}
