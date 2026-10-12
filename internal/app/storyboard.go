package app

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	"net/http"
	"os"
	"strconv"
)

const storyboardStepMS int64 = 5000
const storyboardOutputBytes int64 = 256 << 10
const storyboardCellWidth = 160
const storyboardCellHeight = 90
const storyboardColumns = 6

// Storyboards use the same durable reservations, cancellation, cache bounds
// and source deletion as video windows. They prepare only the requested 30s
// interval, so a long recording never starts an unbounded thumbnail job.
func (s *Server) sourceStoryboard(w http.ResponseWriter, r *http.Request, owner string) {
	s.sourceWindowWithPublisher(w, r, owner, true, s.Store.publishPreview)
}

func renderStoryboardOptions(ctx context.Context, c Config, inputs []mediaInput, interval Range, out string, _ ExportRequest) error {
	if len(inputs) < 1 || len(inputs) > 2 {
		return errUnsupportedStream
	}
	selection, err := probeInputSelectionOptions(ctx, c, inputs[:1], ExportRequest{NoAudio: true})
	if err != nil {
		return err
	}
	if selection.Video == nil {
		return errUnsupportedStream
	}
	width, height, err := videoDisplayGeometry(selection.Video)
	if err != nil {
		return err
	}
	factor := min(1.0, float64(storyboardCellWidth)/float64(width), float64(storyboardCellHeight)/float64(height))
	width, height = max(2, int(float64(width)*factor)/2*2), max(2, int(float64(height)*factor)/2*2)
	// Sample before resizing/tone mapping, keeping decoder work bounded by
	// the requested interval and transforming only six displayed frames.
	filter := fmt.Sprintf("fps=1/5:start_time=0:round=up,scale=%d:%d,setsar=1", width, height)
	filter, hdr, err := videoColorRenderFilter(selection.Video, filter)
	if err != nil {
		return err
	}
	filter += fmt.Sprintf(",pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,tile=%dx1:nb_frames=%d", storyboardCellWidth, storyboardCellHeight, storyboardColumns, storyboardColumns)
	input := inputs[0]
	if interval.StartMS < input.OffsetMS {
		return errUnsupportedStream
	}
	protocols := "file"
	if input.Remote {
		protocols = "http,tcp"
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-max_alloc", "268435456", "-filter_threads", "1", "-protocol_whitelist", protocols, "-format_whitelist", mediaFormats}
	args = append(args, mediaInputBounds(c)...)
	// Bound input decoding too: the tile emits only after collecting its
	// samples, so an output-only -t would read past a short selected window.
	args = append(args, "-ss", seconds(interval.StartMS-input.OffsetMS), "-t", seconds(interval.EndMS-interval.StartMS), "-i", input.Path, "-map", fmt.Sprintf("0:%d", selection.Video.Index), "-an", "-vf", filter, "-frames:v", "1", "-c:v", "mjpeg", "-q:v", "5", "-threads", "1", "-map_metadata", "-1", "-map_chapters", "-1", "-fs", strconv.FormatInt(storyboardOutputBytes, 10), "-f", "image2", out)
	if _, err := runCommand(ctx, c.FFmpeg, args...); err != nil {
		return renderedMediaFailure(err, hdr)
	}
	file, err := os.Open(out)
	if err != nil {
		return err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	if stat.Size() <= 0 || stat.Size() >= storyboardOutputBytes {
		return errOutputLimit
	}
	shape, format, err := image.DecodeConfig(file)
	if err != nil {
		return err
	}
	if format != "jpeg" || shape.Width != storyboardColumns*storyboardCellWidth || shape.Height != storyboardCellHeight {
		return fmt.Errorf("storyboard does not match the requested image geometry")
	}
	return nil
}
