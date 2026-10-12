package app

import (
	"math"
	"path/filepath"
	"testing"
)

func TestCropRejectsInvalidCoordinatesAndModes(t *testing.T) {
	for _, crop := range []*Crop{
		{X: -.1, Width: 1, Height: 1}, {Y: .1, Width: 1, Height: 1},
		{Width: 0, Height: 1}, {Width: 1, Height: math.NaN()},
		{X: math.Inf(1), Width: 1, Height: 1}, {Width: 1.1, Height: 1},
	} {
		if err := crop.Validate(); err == nil {
			t.Fatalf("accepted invalid crop %+v", crop)
		}
	}
	good := &Crop{X: .25, Y: .25, Width: .5, Height: .5}
	for _, request := range []ExportRequest{
		{Format: "mp4", CutMode: "copy", Crop: good},
		{Format: "mkv", CutMode: "copy", Crop: good},
		{Format: "mp3", CutMode: "accurate", Crop: good},
	} {
		if err := validateCropMode(request); err == nil {
			t.Fatalf("silently accepted incompatible crop %+v", request)
		}
	}
	if err := validateCropMode(ExportRequest{Format: "mp4", CutMode: "accurate", Crop: good}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := cropPixelGeometry(160, 96, &Crop{Width: .001, Height: 1}); err == nil {
		t.Fatal("accepted subpixel output")
	}
}

func TestMediaCropDecodedRegionMatchesDisplayedRotationAndSAR(t *testing.T) {
	c, ctx := mcConfig(t)
	base := mcBaseVideo(t, ctx, c, "crop-corners", 160, 96, "")
	anamorphic := mcBaseVideo(t, ctx, c, "crop-anamorphic", 160, 96, "setsar=2")
	rotated := filepath.Join(c.DataDir, "crop-rotated.mp4")
	mcRotateFixture(t, ctx, c, anamorphic, rotated, 90)
	for _, tc := range []struct {
		name, input          string
		width, height, color int
	}{
		{"normal", base, 80, 48, 0},
		{"anamorphic", anamorphic, 160, 48, 0},
		{"rotated-anamorphic", rotated, 48, 160, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(c.DataDir, tc.name+"-crop.mp4")
			request := ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate", Crop: &Crop{Width: .5, Height: .5}}
			start, end, err := ExportLocal(ctx, c, tc.input, out, Range{StartMS: 200, EndMS: 1200}, request)
			if err != nil || start != 200 || math.Abs(float64(end-1200)) > 100 {
				t.Fatalf("crop export %d..%d: %v", start, end, err)
			}
			mcSquareGeometry(t, ctx, c, out, tc.width, tc.height)
			rgb, w, h := mcFrame(t, ctx, c, out, true)
			mcCheckCorners(t, rgb, w, h, [4]int{tc.color, tc.color, tc.color, tc.color})
			// Decode the whole result, including audio, to catch corrupt output.
			mcRun(t, ctx, c, "-i", out, "-f", "null", "-")
			joined := filepath.Join(c.DataDir, tc.name+"-joined.mp4")
			segments, err := ExportJoinedLocal(ctx, c, tc.input, joined, []Range{{StartMS: 1000, EndMS: 1500}, {StartMS: 200, EndMS: 700}}, request)
			if err != nil || len(segments) != 2 || segments[0].SourceStartMS != 1000 || segments[1].OutputStartMS != 500 {
				t.Fatalf("joined crop segments %+v: %v", segments, err)
			}
			mcSquareGeometry(t, ctx, c, joined, tc.width, tc.height)
			rgb, w, h = mcFrame(t, ctx, c, joined, true)
			mcCheckCorners(t, rgb, w, h, [4]int{tc.color, tc.color, tc.color, tc.color})
			mcRun(t, ctx, c, "-i", joined, "-f", "null", "-")
		})
	}
}

func TestCropResolutionCapIsAppliedAfterSelection(t *testing.T) {
	stream := &probeStream{Width: 1920, Height: 1080, SampleAspectRatio: "1:1"}
	width, height, err := videoOutputGeometryWithCrop(stream, "720p", false, &Crop{Width: .5, Height: .5})
	if err != nil || width != 960 || height != 540 {
		t.Fatalf("crop was scaled before selecting area: %dx%d %v", width, height, err)
	}
	width, height, err = videoOutputGeometryWithCrop(stream, "720p", false, &Crop{X: .25, Width: .5, Height: 1})
	if err != nil || width != 640 || height != 720 {
		t.Fatalf("crop cap ratio wrong: %dx%d %v", width, height, err)
	}
}

func TestCropFractionalBoundariesStayWithinOneDisplayedPixel(t *testing.T) {
	for _, dimensions := range [][2]int{{1280, 720}, {481, 271}} {
		width, height := dimensions[0], dimensions[1]
		for _, crop := range []*Crop{{X: .341796875, Width: .31640625, Height: 1}, {X: .1, Y: .15, Width: .7, Height: .8}, {X: 0, Y: 0, Width: 1, Height: 1}} {
			x, y, w, h, err := cropPixelGeometry(width, height, crop)
			if err != nil {
				t.Fatal(err)
			}
			for _, edge := range [][2]float64{{float64(x), crop.X * float64(width)}, {float64(y), crop.Y * float64(height)}, {float64(x + w), (crop.X + crop.Width) * float64(width)}, {float64(y + h), (crop.Y + crop.Height) * float64(height)}} {
				if math.Abs(edge[0]-edge[1]) > 1.000001 {
					t.Fatalf("crop boundary %v exceeds one pixel for %+v", edge, crop)
				}
			}
			if x%2 != 0 || y%2 != 0 || w%2 != 0 || h%2 != 0 || x+w > width || y+h > height {
				t.Fatal("unsafe crop pixels", x, y, w, h)
			}
		}
	}
}

func TestMediaCropRetainsHDRColorAndVariableFrameTiming(t *testing.T) {
	c, ctx := mcConfig(t)
	for _, name := range []string{"pq-10bit.mp4", "hlg-10bit.mp4", "mono-silence-vfr.mp4"} {
		t.Run(name, func(t *testing.T) {
			input := filepath.Join("testdata", "media-contract", name)
			out := filepath.Join(c.DataDir, name)
			request := ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate", Crop: &Crop{Width: 1, Height: .5}}
			if _, _, err := ExportLocal(ctx, c, input, out, Range{EndMS: 2000}, request); err != nil {
				t.Fatal(err)
			}
			if name != "mono-silence-vfr.mp4" {
				// Full horizontal extent retains the fixture's independent
				// known-luminance bars while removing half its vertical image.
				mcCheckSDRGray(t, ctx, c, out)
			} else {
				mcCheckPTS(t, mcFramePTS(t, ctx, c, out), []float64{0, .04, .2, .32, .6, .8, 1.04, 1.4, 1.96})
				mcPulseAt(t, ctx, c, out, .600, .700)
			}
			mcRun(t, ctx, c, "-i", out, "-f", "null", "-")
		})
	}
}
