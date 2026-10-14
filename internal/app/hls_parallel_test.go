package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParallelHLSAdvancesPastStragglerInsideBoundedWindow(t *testing.T) {
	guard, err := newNetworkGuard(1024)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	head := make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(head) })
	seen := make(chan int, 16)
	guard.client.Transport = hlsTestTransport(func(r *http.Request) (*http.Response, error) {
		index, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		if index == 0 {
			select {
			case <-head:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		} else {
			seen <- index
		}
		body := fmt.Sprintf("%02d", index)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), ContentLength: 2, Header: http.Header{}}, nil
	})
	segments := make([]hlsSegment, 12)
	for i := range segments {
		segments[i].URL = fmt.Sprintf("https://media.example/%d", i)
	}
	dir := t.TempDir()
	var output bytes.Buffer
	var downloaded atomic.Int64
	done := make(chan error, 1)
	go func() {
		done <- fetchHLSSegments(context.Background(), guard, platformFormat{}, segments, dir, 0, &output, &downloaded, 1024, nil)
	}()
	for range 7 {
		select {
		case index := <-seen:
			if index >= 8 {
				t.Fatalf("unbounded lookahead fetched segment %d", index)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("freed downloads waited for the first straggler")
		}
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) > 8 {
		t.Fatal("reorder window exceeded eight disk files", err, len(files))
	}
	release.Do(func() { close(head) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("download workers did not finish")
	}
	if output.String() != "000102030405060708091011" {
		t.Fatal("straggler changed segment order", output.String())
	}
	files, _ = os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatal("straggler left pending files")
	}
}

func TestParallelHLSMaintainsOrderAndBoundsConcurrency(t *testing.T) {
	g, err := newNetworkGuard(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	var active, peak atomic.Int32
	g.client.Transport = hlsTestTransport(func(r *http.Request) (*http.Response, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); current > old && !peak.CompareAndSwap(old, current); old = peak.Load() {
		}
		select {
		case <-time.After(20 * time.Millisecond):
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		body := strings.TrimPrefix(r.URL.Path, "/")
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Header: http.Header{}}, nil
	})
	var segments []hlsSegment
	for i := range 12 {
		segments = append(segments, hlsSegment{URL: fmt.Sprintf("https://media.example/%02d", i)})
	}
	dir := t.TempDir()
	var output bytes.Buffer
	var downloaded atomic.Int64
	lastProgress := 0
	err = fetchHLSSegments(context.Background(), g, platformFormat{}, segments, dir, 0, &output, &downloaded, 1024, func(done, total int) {
		if done != lastProgress+1 || total != len(segments) {
			t.Errorf("invalid preparation progress: %d/%d", done, total)
		}
		lastProgress = done
	})
	if err != nil || output.String() != "000102030405060708091011" || peak.Load() != 4 || lastProgress != 12 {
		t.Fatalf("parallel staging: output=%q concurrency=%d progress=%d error=%v", output.String(), peak.Load(), lastProgress, err)
	}
	if files, _ := os.ReadDir(dir); len(files) != 0 {
		t.Fatal("segment files leaked after concatenation")
	}
}

func TestParallelHLSRejectsAggregateStagingOverflowAndCleansFiles(t *testing.T) {
	g, err := newNetworkGuard(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	g.client.Transport = hlsTestTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("12345")), ContentLength: 5, Header: http.Header{}}, nil
	})
	dir := t.TempDir()
	var downloaded atomic.Int64
	segments := []hlsSegment{{URL: "https://media.example/1"}, {URL: "https://media.example/2"}}
	err = fetchHLSSegments(context.Background(), g, platformFormat{}, segments, dir, 0, io.Discard, &downloaded, 8, nil)
	if err == nil {
		t.Fatal("aggregate interval size exceeded its storage reservation")
	}
	if files, _ := os.ReadDir(dir); len(files) != 0 {
		t.Fatal("failed download leaked staged segments")
	}
}
