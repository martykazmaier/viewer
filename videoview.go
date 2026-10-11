package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const slideDelay = 3 * time.Second

var videoExts = map[string]bool{
	".mp4": true, ".m4v": true, ".mkv": true, ".webm": true, ".avi": true, ".mov": true,
	".wmv": true, ".asf": true, ".flv": true, ".mpg": true, ".mpeg": true, ".m2ts": true,
	".mts": true, ".ts": true, ".vob": true, ".ogv": true, ".3gp": true, ".3g2": true,
	".divx": true, ".rm": true, ".rmvb": true, ".f4v": true,
}

// isVideo recognizes video files by extension or container signature.
func isVideo(name string, h []byte) bool {
	switch ext := strings.ToLower(filepath.Ext(name)); {
	case ext == ".ts" || ext == ".mts" || ext == ".m2ts":
		// MPEG transport stream packets start with 0x47; .ts is also TypeScript.
		return len(h) > 0 && h[0] == 0x47 || len(h) > 4 && h[4] == 0x47
	case videoExts[ext]:
		return true
	}
	switch {
	case len(h) >= 12 && string(h[4:8]) == "ftyp":
		switch string(h[8:12]) {
		case "M4A ", "M4B ", "M4P ", "heic", "heix", "mif1", "msf1", "avif", "crx ":
			return false
		}
		return true
	case bytes.HasPrefix(h, []byte{0x1A, 0x45, 0xDF, 0xA3}):
		return true
	case len(h) >= 12 && string(h[:4]) == "RIFF" && string(h[8:12]) == "AVI ":
		return true
	case bytes.HasPrefix(h, []byte("FLV\x01")), bytes.HasPrefix(h, []byte{0, 0, 1, 0xBA}):
		return true
	}
	return false
}

// findFFmpeg looks for ffmpeg at the -F location, beside the viewer, then on PATH.
func (s *Session) findFFmpeg() string {
	exe := "ffmpeg"
	if runtime.GOOS == "windows" {
		exe = "ffmpeg.exe"
	}
	var cands []string
	if s.FFmpeg != "" {
		if st, err := os.Stat(s.FFmpeg); err == nil && st.IsDir() {
			cands = append(cands, filepath.Join(s.FFmpeg, exe))
		} else {
			cands = append(cands, s.FFmpeg)
		}
	}
	if self, err := os.Executable(); err == nil {
		cands = append(cands, filepath.Join(filepath.Dir(self), exe))
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	if p, err := exec.LookPath(exe); err == nil {
		return p
	}
	return ""
}

type videoInfo struct {
	duration float64
	vcodec   string
	w, h     int
	acodec   string
}

var (
	reDuration = regexp.MustCompile(`Duration: (\d+):(\d+):(\d+(?:\.\d+)?)`)
	reVideo    = regexp.MustCompile(`Stream #[^:]+:[^:]*: Video: (\w+)[^\n]*?, (\d{2,5})x(\d{2,5})[^\n]*`)
	reAudio    = regexp.MustCompile(`Stream #[^:]+:[^:]*: Audio: (\w+)`)
)

// parseProbe reads the stream summary ffmpeg prints for "ffmpeg -i file".
func parseProbe(out string) (videoInfo, error) {
	var vi videoInfo
	if m := reDuration.FindStringSubmatch(out); m != nil {
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		sec, _ := strconv.ParseFloat(m[3], 64)
		vi.duration = float64(h*3600+mi*60) + sec
	}
	for _, m := range reVideo.FindAllStringSubmatch(out, -1) {
		if strings.Contains(m[0], "attached pic") {
			continue
		}
		vi.vcodec = m[1]
		vi.w, _ = strconv.Atoi(m[2])
		vi.h, _ = strconv.Atoi(m[3])
		break
	}
	if m := reAudio.FindStringSubmatch(out); m != nil {
		vi.acodec = m[1]
	}
	if vi.vcodec == "" {
		return vi, errors.New("no video stream was found")
	}
	return vi, nil
}

func probeVideo(ctx context.Context, ff, file string) (videoInfo, error) {
	out, _ := exec.CommandContext(ctx, ff, "-nostdin", "-hide_banner", "-i", file).CombinedOutput()
	return parseProbe(string(out))
}

// grabFrame decodes one frame at t seconds, scaled by ffmpeg to fit w x h.
func grabFrame(ctx context.Context, ff, file string, t float64, w, h int) (image.Image, error) {
	vf := fmt.Sprintf("scale=iw*sar:ih,scale=%d:%d:force_original_aspect_ratio=decrease", w, h)
	out, err := exec.CommandContext(ctx, ff, "-nostdin", "-v", "error", "-ss", strconv.FormatFloat(t, 'f', 3, 64),
		"-i", file, "-frames:v", "1", "-an", "-sn", "-vf", vf, "-f", "image2pipe", "-c:v", "ppm", "-").Output()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("ffmpeg could not decode a frame at %s", fmtDur(t))
	}
	return decodePPM(out)
}

// decodePPM reads a binary (P6) 8-bit PPM.
func decodePPM(data []byte) (image.Image, error) {
	r := bufio.NewReader(bytes.NewReader(data))
	var vals [4]int
	for i := 0; i < 4; {
		var tok []byte
		for {
			b, err := r.ReadByte()
			if err != nil {
				return nil, errors.New("truncated PPM frame")
			}
			if b == '#' {
				r.ReadString('\n')
				continue
			}
			if b == ' ' || b == '\t' || b == '\r' || b == '\n' {
				if len(tok) > 0 {
					break
				}
				continue
			}
			tok = append(tok, b)
		}
		if i == 0 {
			if string(tok) != "P6" {
				return nil, errors.New("not a P6 PPM frame")
			}
		} else {
			n, err := strconv.Atoi(string(tok))
			if err != nil || n <= 0 {
				return nil, errors.New("bad PPM header")
			}
			vals[i] = n
		}
		i++
	}
	w, h := vals[1], vals[2]
	if vals[3] != 255 || w*h > maxImagePixels {
		return nil, errors.New("unsupported PPM frame")
	}
	pix := make([]byte, w*h*3)
	if _, err := io.ReadFull(r, pix); err != nil {
		return nil, errors.New("truncated PPM frame")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i, j := 0, 0; i < len(pix); i, j = i+3, j+4 {
		img.Pix[j], img.Pix[j+1], img.Pix[j+2], img.Pix[j+3] = pix[i], pix[i+1], pix[i+2], 255
	}
	return img, nil
}

func fmtDur(sec float64) string {
	t := int(sec + 0.5)
	if t >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", t/3600, t/60%60, t%60)
	}
	return fmt.Sprintf("%d:%02d", t/60, t%60)
}

// frameTimes spreads n frames evenly across the video, one per 8 seconds,
// between 4 and 16 frames. Without a known duration it samples every 10s.
func frameTimes(duration float64) []float64 {
	if duration <= 0 {
		t := make([]float64, 8)
		for i := range t {
			t[i] = float64(i * 10)
		}
		return t
	}
	n := min(max(int(duration/8), 4), 16)
	t := make([]float64, n)
	for i := range t {
		t[i] = duration * (float64(i) + 0.5) / float64(n)
	}
	return t
}

// ---- terminal audio (SyncTERM APC) ----

func apc(body string) []byte { return []byte("\x1b_SyncTERM:" + body + "\x1b\\") }

// awaitAudio waits for a CSI = 7 ... n report whose fields start with prefix.
func (s *Session) awaitAudio(prefix string, wait time.Duration) (string, bool) {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if s.getKey(time.Until(deadline)) == kAudio && strings.HasPrefix(s.audioReport, prefix) {
			return s.audioReport, true
		}
	}
	return "", false
}

// detectAudio asks a CTerm-compatible terminal (SyncTERM, IcyTERM, ...)
// whether it can decode audio files sent by the BBS.
func (s *Session) detectAudio() bool {
	if s.audioChecked {
		return s.audioOK
	}
	s.audioChecked = true
	s.detectGraphics()
	if s.keys != nil || (s.ctermFeat == "" && !strings.HasPrefix(s.daParams, "=67;84;101;114;109")) {
		return false
	}
	s.rawSend(apc("Q;libsndfile"))
	r, ok := s.awaitAudio("100;", time.Second)
	s.audioOK = ok && r == "100;1"
	return s.audioOK
}

func (s *Session) audioFormatOK(major, sub int) bool {
	s.rawSend(apc(fmt.Sprintf("Q;libsndfileFormat;%d;%d", major, sub)))
	r, ok := s.awaitAudio(fmt.Sprintf("101;%d;%d;", major, sub), time.Second)
	return ok && strings.HasSuffix(r, ";1")
}

type audioEnc struct {
	name       string
	major, sub int
	maxSec     int
	args       []string
}

var audioEncoders = []audioEnc{
	{"Opus", 32, 100, 120, []string{"-c:a", "libopus", "-b:a", "32k", "-ac", "2", "-ar", "48000", "-f", "ogg"}},
	{"Vorbis", 32, 96, 120, []string{"-c:a", "libvorbis", "-q:a", "2", "-ac", "2", "-ar", "44100", "-f", "ogg"}},
	{"FLAC", 23, 2, 60, []string{"-c:a", "flac", "-sample_fmt", "s16", "-ac", "1", "-ar", "22050", "-f", "flac"}},
	{"WAV", 1, 5, 60, []string{"-c:a", "pcm_u8", "-ac", "1", "-ar", "11025", "-f", "wav"}},
}

type soundClip struct {
	data []byte
	kind string
	secs int
	err  error
	done chan struct{}
}

// extractSound encodes the start of the soundtrack with the first encoder
// the terminal accepts and this ffmpeg build provides.
func extractSound(ctx context.Context, ff, file string, encs []audioEnc) *soundClip {
	c := &soundClip{done: make(chan struct{})}
	go func() {
		defer close(c.done)
		for _, e := range encs {
			args := append([]string{"-nostdin", "-v", "error", "-i", file, "-vn", "-sn", "-t", strconv.Itoa(e.maxSec)}, e.args...)
			out, err := exec.CommandContext(ctx, ff, append(args, "-")...).Output()
			if err == nil && len(out) > 0 {
				c.data, c.kind, c.secs = out, e.name, e.maxSec
				return
			}
			if ctx.Err() != nil {
				return
			}
		}
		c.err = errors.New("ffmpeg could not encode the soundtrack in a format the terminal accepts")
	}()
	return c
}

// sendSound uploads the clip into patch slot 1. Nothing else may be written
// until the APC string ends, so the status line is updated only before it.
func (s *Session) sendSound(c *soundClip) {
	b64 := base64.StdEncoding.EncodeToString(c.data)
	s.Bar(s.H, fmt.Sprintf(" Sending sound (%s, %s KB)...", c.kind, fmtNum(int64(len(b64)/1024))), "", 15, 1)
	s.flush()
	s.rawSend(apc("A;LoadBlob;S=1;" + b64))
}

func (s *Session) playSound() {
	s.rawSend(apc("A;Flush;C=2"))
	s.rawSend(apc("A;Copy;S=1;D=0"))
	s.rawSend(apc("A;Queue;C=2;S=0;I=200"))
}

func (s *Session) stopSound() {
	s.rawSend(apc("A;Flush;C=2;O=300"))
}

// ---- slideshow ----

func (s *Session) videoView(name string, r io.ReaderAt, size int64) {
	ff := s.findFFmpeg()
	if ff == "" {
		s.message(safeText(name), "Video previews need ffmpeg. Put ffmpeg.exe beside viewer.exe, add it to PATH, or use -F<path>.")
		return
	}
	file := ""
	if f, ok := r.(*os.File); ok {
		file = f.Name()
	} else {
		tmp, err := os.CreateTemp("", "viewer-*"+filepath.Ext(name))
		if err != nil {
			s.message(safeText(name), "Cannot create a temporary file: "+err.Error())
			return
		}
		defer os.Remove(tmp.Name())
		_, err = io.Copy(tmp, io.NewSectionReader(r, 0, size))
		tmp.Close()
		if err != nil {
			s.message(safeText(name), "Cannot write a temporary file: "+err.Error())
			return
		}
		file = tmp.Name()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.Cls()
	var vi videoInfo
	var err error
	if !s.busy("Reading video", func() { vi, err = probeVideo(ctx, ff, file) }) {
		return
	}
	if err != nil {
		s.message(safeText(name), "Cannot preview video: "+err.Error())
		return
	}
	s.detectGraphics()
	useSixel := s.sixel

	var sound *soundClip
	soundOn, soundSent := true, false
	if vi.acodec != "" && s.detectAudio() {
		var encs []audioEnc
		for _, e := range audioEncoders {
			if s.audioFormatOK(e.major, e.sub) {
				encs = append(encs, e)
			}
		}
		if len(encs) > 0 {
			sound = extractSound(ctx, ff, file, encs)
		}
	}
	defer func() {
		if soundSent {
			s.stopSound()
		}
	}()
	soundReady := func() bool {
		if sound == nil || !soundOn || soundSent {
			return false
		}
		select {
		case <-sound.done:
			return true
		default:
			return false
		}
	}

	times := frameTimes(vi.duration)
	bw, bh, _, _ := s.pixelArea()
	frames := make([]image.Image, len(times))
	errs := make([]error, len(times))
	ready := make([]chan struct{}, len(times))
	for i := range ready {
		ready[i] = make(chan struct{})
	}
	go func() {
		for i, t := range times {
			if ctx.Err() != nil {
				return
			}
			frames[i], errs[i] = grabFrame(ctx, ff, file, t, bw, bh)
			close(ready[i])
		}
	}()

	idx, paused, redraw := 0, false, true
	shown := ""
	for {
		if redraw {
			if !s.busy(fmt.Sprintf("Extracting frame %d of %d", idx+1, len(times)), func() { <-ready[idx] }) {
				return
			}
			if errs[idx] != nil {
				shown = "no picture"
				s.Cls()
				s.GotoXY(2, 2)
				s.Color(12, 0)
				s.Print(fit(errs[idx].Error(), s.W-2))
			} else {
				var ok bool
				if useSixel {
					ok, shown = s.drawSixel(frames[idx])
				} else {
					ok, shown = s.drawBlocks(frames[idx])
				}
				if !ok {
					shown = "interrupted"
				}
			}
			redraw = false
		}
		if soundReady() {
			if sound.err == nil {
				s.sendSound(sound)
				s.playSound()
				soundSent = true
			} else {
				sound = nil
			}
		}

		state := "Playing"
		if paused {
			state = "Paused"
		}
		left := fmt.Sprintf(" %d/%d %s/%s %s  %s", idx+1, len(times), fmtDur(times[idx]), fmtDur(vi.duration), state, safeText(name))
		right := "<-/->=Frame P=Pause I=Info Q=Quit "
		if sound != nil {
			right = "<-/->=Frame P=Pause A=Sound I=Info Q=Quit "
		}
		s.Bar(s.H, fit(left, max(s.W-2-len(right), 0)), right, 15, 1)
		s.flush()

		var until time.Time
		if !paused && idx < len(times)-1 {
			until = time.Now().Add(slideDelay)
		}
		k := s.waitKeyUntil(until, soundReady)
		if k == 0 {
			if !soundReady() {
				idx++
				redraw = true
			}
			continue
		}
		switch k {
		case kRight, kDown, ' ', 'n', 'N':
			if idx < len(times)-1 {
				idx++
				redraw = true
			}
		case kLeft, kUp, 'b', 'B':
			if idx > 0 {
				idx--
				redraw = true
			}
		case kHome:
			idx, redraw = 0, true
		case kEnd:
			idx, redraw = len(times)-1, true
		case 'p', 'P':
			paused = !paused
		case 'a', 'A':
			if sound != nil {
				soundOn = !soundOn
				switch {
				case !soundOn && soundSent:
					s.stopSound()
				case soundOn && soundSent:
					s.playSound()
				}
			}
		case 'm', 'M':
			useSixel = !useSixel
			s.Cls()
			redraw = true
		case 'r', 'R':
			s.Cls()
			redraw = true
		case 'i', 'I':
			s.videoInfo(name, ff, vi, len(times), shown, sound, soundOn)
			s.Cls()
			redraw = true
		case kEnter, kEsc, 'q', 'Q':
			return
		}
	}
}

func (s *Session) videoInfo(name, ff string, vi videoInfo, frames int, shown string, sound *soundClip, soundOn bool) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "File:     %s\n", safeText(name))
	fmt.Fprintf(&sb, "Length:   %s\n", fmtDur(vi.duration))
	fmt.Fprintf(&sb, "Video:    %s, %dx%d\n", vi.vcodec, vi.w, vi.h)
	if vi.acodec != "" {
		fmt.Fprintf(&sb, "Audio:    %s\n", vi.acodec)
	}
	fmt.Fprintf(&sb, "Preview:  %d frames, %s\n", frames, shown)
	switch {
	case vi.acodec == "":
		sb.WriteString("Sound:    none in this video\n")
	case !s.audioOK:
		sb.WriteString("Sound:    terminal has no BBS audio support\n")
	case sound == nil:
		sb.WriteString("Sound:    no format both ffmpeg and the terminal support\n")
	default:
		state := "on"
		if !soundOn {
			state = "off"
		}
		select {
		case <-sound.done:
			secs := float64(sound.secs)
			if vi.duration > 0 {
				secs = min(secs, vi.duration)
			}
			fmt.Fprintf(&sb, "Sound:    %s, first %s, %s (%s KB)\n", sound.kind, fmtDur(secs), state, fmtNum(int64(len(sound.data)/1024)))
		default:
			sb.WriteString("Sound:    preparing\n")
		}
	}
	fmt.Fprintf(&sb, "ffmpeg:   %s\n\n", ff)
	sb.WriteString("Keys:     Left/Right=Frame Home/End P=Pause A=Sound\n")
	sb.WriteString("          M=Sixel/ANSI R=Redraw Q=Quit")
	s.message("Video", sb.String())
}
