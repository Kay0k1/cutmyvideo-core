package mediasession

import (
	"context"
	"io"
	"os"
)

// Reuse copies only within the caller's pinned operation and creates an independent file.
func Reuse(ctx context.Context, lookup func() *Session, prior, output string, expected os.FileInfo, limit int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	session := lookup()
	if session == nil || expected == nil || expected.Size() <= 0 || expected.Size() > limit {
		return false, nil
	}
	session.Mu.Lock()
	verified := session.File != nil
	session.Mu.Unlock()
	if !verified {
		return false, nil
	}
	checkSource := func() error {
		session.Mu.Lock()
		defer session.Mu.Unlock()
		_, err := session.Unchanged()
		return err
	}
	if err := checkSource(); err != nil {
		return false, err
	}
	input, err := os.Open(prior)
	if err != nil {
		return false, nil
	}
	defer input.Close()
	unchanged := func(info os.FileInfo) bool {
		return info != nil && info.Mode().IsRegular() && os.SameFile(info, expected) && info.Size() == expected.Size() && info.ModTime().Equal(expected.ModTime())
	}
	info, err := input.Stat()
	if err != nil || !unchanged(info) {
		return false, nil
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return false, err
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(output)
		}
	}()
	count, copyErr := io.Copy(file, io.LimitReader(contextReadSeeker{context: ctx, ReadSeeker: input}, expected.Size()+1))
	closeErr := file.Close()
	if copyErr != nil {
		return false, copyErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	if err := checkSource(); err != nil {
		return false, err
	}
	info, err = input.Stat()
	if err != nil || !unchanged(info) || count != expected.Size() {
		// A user may replace or edit a published result. Discard this copy and
		// let the caller regenerate from its still verified original source.
		if err := os.Remove(output); err != nil && !os.IsNotExist(err) {
			return false, err
		}
		return false, nil
	}
	keep = true
	return true, nil
}

type contextReadSeeker struct {
	context context.Context
	io.ReadSeeker
}

func (r contextReadSeeker) Read(p []byte) (int, error) {
	if err := r.context.Err(); err != nil {
		return 0, err
	}
	return r.ReadSeeker.Read(p)
}
func (r contextReadSeeker) Seek(offset int64, whence int) (int64, error) {
	if err := r.context.Err(); err != nil {
		return 0, err
	}
	return r.ReadSeeker.Seek(offset, whence)
}
