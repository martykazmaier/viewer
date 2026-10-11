package main

import (
	"context"
	"os"
	"testing"
)

const probeSample = `Input #0, mov,mp4,m4a,3gp,3g2,mj2, from 'clip.mp4':
  Duration: 00:02:05.48, start: 0.000000, bitrate: 2905 kb/s
  Stream #0:0[0x1](und): Video: h264 (High) (avc1 / 0x31637661), yuv420p(progressive), 1280x720 [SAR 1:1 DAR 16:9], 2770 kb/s, 25 fps, 25 tbr, 12800 tbn (default)
  Stream #0:1[0x2](und): Audio: aac (LC) (mp4a / 0x6134706D), 44100 Hz, mono, fltp, 69 kb/s (default)
At least one output file must be specified
`

func TestParseProbe(t *testing.T) {
	vi, err := parseProbe(probeSample)
	if err != nil {
		t.Fatal(err)
	}
	if vi.duration < 125.4 || vi.duration > 125.5 || vi.vcodec != "h264" || vi.w != 1280 || vi.h != 720 || vi.acodec != "aac" {
		t.Errorf("got %+v", vi)
	}
	mp3 := "  Duration: 00:03:00.00\n  Stream #0:0: Audio: mp3, 44100 Hz, stereo\n  Stream #0:1: Video: mjpeg (Baseline), yuvj420p, 500x500, 90k tbn (attached pic)\n"
	if _, err := parseProbe(mp3); err == nil {
		t.Error("an MP3 with cover art was accepted as video")
	}
}

func TestDecodePPM(t *testing.T) {
	data := append([]byte("P6\n# comment\n2 1\n255\n"), 255, 0, 0, 0, 0, 255)
	img, err := decodePPM(data)
	if err != nil {
		t.Fatal(err)
	}
	if r, _, _, _ := img.At(0, 0).RGBA(); r>>8 != 255 {
		t.Error("first pixel is not red")
	}
	if _, _, b, _ := img.At(1, 0).RGBA(); b>>8 != 255 {
		t.Error("second pixel is not blue")
	}
	if _, err := decodePPM(data[:len(data)-1]); err == nil {
		t.Error("truncated frame accepted")
	}
}

func TestIsVideo(t *testing.T) {
	cases := []struct {
		name string
		head string
		want bool
	}{
		{"clip.mp4", "", true},
		{"x.bin", "\x00\x00\x00\x20ftypisom", true},
		{"song.m4a", "\x00\x00\x00\x20ftypM4A ", false},
		{"photo.bin", "\x00\x00\x00\x20ftypheic", false},
		{"x.bin", "\x1a\x45\xdf\xa3", true},
		{"x.bin", "RIFF\x00\x00\x00\x00AVI LIST", true},
		{"x.bin", "RIFF\x00\x00\x00\x00WAVEfmt ", false},
		{"readme.txt", "hello", false},
		{"app.ts", "import x from 'y'", false},
		{"show.ts", "\x47\x40\x00\x10", true},
	}
	for _, c := range cases {
		if got := isVideo(c.name, []byte(c.head)); got != c.want {
			t.Errorf("isVideo(%q, %q) = %v", c.name, c.head, got)
		}
	}
}

func TestFrameTimes(t *testing.T) {
	if n := len(frameTimes(10)); n != 4 {
		t.Errorf("10s video: %d frames, want 4", n)
	}
	if n := len(frameTimes(3600)); n != 16 {
		t.Errorf("1h video: %d frames, want 16", n)
	}
	ts := frameTimes(80)
	if len(ts) != 10 || ts[0] != 4 || ts[9] != 76 {
		t.Errorf("80s video: %v", ts)
	}
}

// TestFFmpeg runs against a real ffmpeg when VIEWER_FFMPEG and VIEWER_VIDEO are set.
func TestFFmpeg(t *testing.T) {
	ff, file := os.Getenv("VIEWER_FFMPEG"), os.Getenv("VIEWER_VIDEO")
	if ff == "" || file == "" {
		t.Skip("set VIEWER_FFMPEG and VIEWER_VIDEO to run")
	}
	ctx := context.Background()
	vi, err := probeVideo(ctx, ff, file)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("probe: %+v", vi)
	img, err := grabFrame(ctx, ff, file, vi.duration/2, 640, 378)
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() > 640 || b.Dy() > 378 || b.Dx() < 600 {
		t.Errorf("frame size %v", b)
	}
	for _, e := range audioEncoders {
		c := extractSound(ctx, ff, file, []audioEnc{e})
		<-c.done
		t.Logf("%s: %d bytes, err %v", e.name, len(c.data), c.err)
	}
}
