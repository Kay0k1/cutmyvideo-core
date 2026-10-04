package app

import (
	"testing"
)

func TestEncoderSettingsRejectUnboundedThreadsAndUnknownProfiles(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://configuration-only")
	t.Setenv("FFMPEG_THREADS", "2")
	t.Setenv("FFMPEG_PROFILE", "fast")
	c, err := ConfigFromEnv()
	if err != nil || c.FFmpegThreads != 2 || c.FFmpegProfile != "fast" {
		t.Fatalf("default encoder settings: %+v %v", c, err)
	}
	for _, value := range []string{"0", "-1", "33", "auto", "999999999999999999999"} {
		t.Setenv("FFMPEG_THREADS", value)
		if _, err := ConfigFromEnv(); err == nil {
			t.Fatalf("accepted unbounded thread setting %q", value)
		}
	}
	t.Setenv("FFMPEG_THREADS", "2")
	t.Setenv("FFMPEG_PROFILE", "compact")
	if c, err = ConfigFromEnv(); err != nil || c.FFmpegProfile != "compact" {
		t.Fatalf("compact profile: %v", err)
	}
	t.Setenv("FFMPEG_PROFILE", "arbitrary")
	if _, err = ConfigFromEnv(); err == nil {
		t.Fatal("accepted unknown encoder profile")
	}
}
