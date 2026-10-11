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

func decodePPM(data []byte) (image.Image, error) {
	return readPPM(bufio.NewReader(bytes.NewReader(data)))
}

// readPPM reads one binary (P6) 8-bit PPM; ffmpeg's image2pipe output is a
// stream of them. io.EOF means the stream ended cleanly between frames.
func readPPM(r *bufio.Reader) (image.Image, error) {
	if _, err := r.Peek(1); err != nil {
		return nil, io.EOF
	}
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

const (
	playFPS    = 4   // frames decoded per second of video
	segmentLen = 120 // seconds of video and sound fetched at a time
)

type frame struct {
	t   float64 // position in the video, seconds
	img image.Image
}

// streamFrames decodes segmentLen seconds of video from start at playFPS.
// The channel closes at the end of the segment, the video, or ctx.
func streamFrames(ctx context.Context, ff, file string, start float64, w, h int) <-chan frame {
	ch := make(chan frame, 2)
	go func() {
		defer close(ch)
		vf := fmt.Sprintf("fps=%d,scale=iw*sar:ih,scale=%d:%d:force_original_aspect_ratio=decrease", playFPS, w, h)
		cmd := exec.CommandContext(ctx, ff, "-nostdin", "-v", "error", "-ss", strconv.FormatFloat(start, 'f', 3, 64),
			"-i", file, "-t", strconv.Itoa(segmentLen), "-an", "-sn", "-vf", vf, "-f", "image2pipe", "-c:v", "ppm", "-")
		out, err := cmd.StdoutPipe()
		if err != nil || cmd.Start() != nil {
			return
		}
		defer cmd.Wait()
		r := bufio.NewReaderSize(out, 1<<20)
		for i := 0; ; i++ {
			img, err := readPPM(r)
			if err != nil {
				return
			}
			select {
			case ch <- frame{start + float64(i)/playFPS, img}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
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
	args       []string
}

var audioEncoders = []audioEnc{
	{"Opus", 32, 100, []string{"-c:a", "libopus", "-b:a", "32k", "-ac", "2", "-ar", "48000", "-f", "ogg"}},
	{"Vorbis", 32, 96, []string{"-c:a", "libvorbis", "-q:a", "2", "-ac", "2", "-ar", "44100", "-f", "ogg"}},
	{"FLAC", 23, 2, []string{"-c:a", "flac", "-sample_fmt", "s16", "-ac", "1", "-ar", "11025", "-f", "flac"}},
	{"WAV", 1, 5, []string{"-c:a", "pcm_u8", "-ac", "1", "-ar", "8000", "-f", "wav"}},
}

type soundClip struct {
	data []byte
	kind string
	err  error
	done chan struct{}
}

// extractSound encodes segmentLen seconds of the soundtrack from start with
// the first encoder the terminal accepts and this ffmpeg build provides.
func extractSound(ctx context.Context, ff, file string, start float64, encs []audioEnc) *soundClip {
	c := &soundClip{done: make(chan struct{})}
	go func() {
		defer close(c.done)
		for _, e := range encs {
			// libsndfile rejects Ogg Opus whose first page starts at or
			// before sample 0, which ffmpeg writes for audio that begins a
			// hair early (common in MP4s); start it 1ms in instead.
			args := append([]string{"-nostdin", "-v", "error", "-ss", strconv.FormatFloat(start, 'f', 3, 64),
				"-i", file, "-vn", "-sn", "-t", strconv.Itoa(segmentLen), "-af", "asetpts=N/SR/TB+0.001/TB"}, e.args...)
			out, err := exec.CommandContext(ctx, ff, append(args, "-")...).Output()
			if err == nil && len(out) > 0 {
				c.data, c.kind = out, e.name
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

// playSound starts slot 1 on channel 2 and waits until the terminal reports
// the channel playing, so the picture clock can start with the sound.
func (s *Session) playSound() {
	s.rawSend(apc("A;Flush;C=2"))
	s.rawSend(apc("A;Queue;C=2;S=1"))
	s.rawSend([]byte("\x1b[=7;2n"))
	s.awaitAudio("2;", 3*time.Second)
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

	var encs []audioEnc
	if vi.acodec != "" && s.detectAudio() {
		for _, e := range audioEncoders {
			if s.audioFormatOK(e.major, e.sub) {
				encs = append(encs, e)
			}
		}
	}

	bw, bh, _, _ := s.pixelArea()
	step := max(vi.duration/16, 10)
	clamp := func(t float64) float64 {
		if vi.duration > 0 {
			t = min(t, max(vi.duration-1, 0))
		}
		return max(t, 0)
	}
	pos, paused, ended, soundOn := 0.0, false, false, true
	soundKind, soundNote, shown := "", "", ""
	var last image.Image
	playing := false
	halt := func() {
		if playing {
			s.stopSound()
			playing = false
		}
	}
	defer halt()
	draw := func(img image.Image) {
		last = img
		var ok bool
		if useSixel {
			ok, shown = s.drawSixel(img, true)
		} else {
			ok, shown = s.drawBlocks(img, true)
		}
		if !ok {
			shown = "interrupted"
		}
	}
	redraw := func() {
		s.Cls()
		if last != nil {
			draw(last)
		}
	}
	bar := func(t float64, state string) {
		left := fmt.Sprintf(" %s/%s %s  %s", fmtDur(t), fmtDur(vi.duration), state, safeText(name))
		right := "P=Pause <-/->=Seek I=Info Q=Quit "
		if len(encs) > 0 {
			right = "P=Pause <-/->=Seek A=Sound I=Info Q=Quit "
		}
		s.Bar(s.H, fit(left, max(s.W-2-len(right), 0)), right, 15, 1)
		s.flush()
	}
	info := func() {
		s.videoInfo(name, ff, vi, shown, len(encs) > 0, soundOn, soundKind, soundNote)
		redraw()
	}

	for {
		if paused {
			state := "Paused"
			if ended {
				state = "End"
			}
			bar(pos, state)
			switch k := s.waitKey(); k {
			case 'p', 'P', ' ':
				if ended {
					pos = 0
				}
				paused, ended = false, false
			case kLeft, kRight, kHome, kEnd:
				switch k {
				case kLeft:
					pos = clamp(pos - step)
				case kRight:
					pos = clamp(pos + step)
				case kHome:
					pos = 0
				case kEnd:
					pos = clamp(vi.duration)
				}
				ended = false
				var img image.Image
				var err error
				if !s.busy("Seeking to "+fmtDur(pos), func() { img, err = grabFrame(ctx, ff, file, pos, bw, bh) }) {
					return
				}
				if err == nil {
					draw(img)
				}
			case 'a', 'A':
				soundOn = !soundOn
			case 'm', 'M':
				useSixel = !useSixel
				redraw()
			case 'r', 'R':
				redraw()
			case 'i', 'I':
				info()
			case kEnter, kEsc, 'q', 'Q':
				return
			}
			continue
		}

		// Play one segment from pos. Frames are timed against a clock that
		// starts once the terminal reports the sound playing.
		segStart := pos
		segEnd := segStart + segmentLen
		if vi.duration > 0 {
			segEnd = min(segEnd, vi.duration)
		}
		segCtx, segCancel := context.WithCancel(ctx)
		frames := streamFrames(segCtx, ff, file, segStart, bw, bh)
		var sound *soundClip
		if soundOn && len(encs) > 0 {
			sound = extractSound(segCtx, ff, file, segStart, encs)
		}
		var first frame
		var ok bool
		if !s.busy("Loading video at "+fmtDur(segStart), func() { first, ok = <-frames }) {
			segCancel()
			return
		}
		if !ok {
			segCancel()
			if last == nil {
				s.message(safeText(name), "Cannot preview video: ffmpeg could not decode any frames.")
				return
			}
			paused, ended = true, true
			continue
		}
		draw(first.img)
		if sound != nil {
			if !s.busy("Preparing sound", func() { <-sound.done }) {
				segCancel()
				return
			}
			if sound.err == nil {
				soundKind = sound.kind
				s.sendSound(sound)
				s.playSound()
				playing = true
			} else {
				soundNote = sound.err.Error()
				encs = nil
			}
		}
		clock, base := time.Now(), first.t
		now := func() float64 { return base + time.Since(clock).Seconds() }
		var pending *frame
		done, lead := false, 0.0
		var lastBar time.Time
	play:
		for {
			// Show the newest frame that is due, skipping any the
			// connection was too slow for.
			var due *frame
			for {
				if pending == nil && !done {
					select {
					case f, ok := <-frames:
						if ok {
							pending = &f
						} else {
							done = true
						}
					default:
					}
				}
				if pending == nil || pending.t > now()+lead {
					break
				}
				due, pending = pending, nil
			}
			if due != nil {
				t0 := time.Now()
				draw(due.img)
				lead = 0.7*lead + 0.3*time.Since(t0).Seconds()
			}
			if done && pending == nil && now() >= segEnd {
				pos = segEnd
				if vi.duration > 0 && segEnd >= vi.duration-0.5 {
					pos, paused, ended = clamp(vi.duration), true, true
				}
				break
			}
			if time.Since(lastBar) >= time.Second {
				bar(now(), "Playing")
				lastBar = time.Now()
			}

			wait := 40 * time.Millisecond
			if pending != nil {
				wait = min(max(time.Duration((pending.t-lead-now())*float64(time.Second)), 5*time.Millisecond), 250*time.Millisecond)
			}
			switch k := s.waitKeyUntil(time.Now().Add(wait), nil); k {
			case 'p', 'P', ' ':
				pos, paused = clamp(now()), true
				break play
			case kLeft:
				pos = clamp(now() - step)
				break play
			case kRight:
				pos = clamp(now() + step)
				break play
			case kHome:
				pos = 0
				break play
			case 'a', 'A':
				if len(encs) > 0 {
					soundOn = !soundOn
					pos = clamp(now())
					break play
				}
			case 'm', 'M':
				useSixel = !useSixel
				redraw()
				lastBar = time.Time{}
			case 'r', 'R':
				redraw()
				lastBar = time.Time{}
			case 'i', 'I':
				pos, paused = clamp(now()), true
				segCancel()
				halt()
				info()
				break play
			case kEnter, kEsc, 'q', 'Q':
				segCancel()
				return
			}
		}
		segCancel()
		halt()
	}
}

func (s *Session) videoInfo(name, ff string, vi videoInfo, shown string, canSound, soundOn bool, kind, note string) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "File:     %s\n", safeText(name))
	fmt.Fprintf(&sb, "Length:   %s\n", fmtDur(vi.duration))
	fmt.Fprintf(&sb, "Video:    %s, %dx%d\n", vi.vcodec, vi.w, vi.h)
	if vi.acodec != "" {
		fmt.Fprintf(&sb, "Audio:    %s\n", vi.acodec)
	}
	fmt.Fprintf(&sb, "Picture:  %d frames/s, %s\n", playFPS, shown)
	switch {
	case vi.acodec == "":
		sb.WriteString("Sound:    none in this video\n")
	case !s.audioOK:
		sb.WriteString("Sound:    terminal has no BBS audio support\n")
	case note != "":
		fmt.Fprintf(&sb, "Sound:    %s\n", note)
	case !canSound:
		sb.WriteString("Sound:    no format both ffmpeg and the terminal support\n")
	case !soundOn:
		sb.WriteString("Sound:    off\n")
	default:
		fmt.Fprintf(&sb, "Sound:    %s, sent %d seconds at a time\n", kind, segmentLen)
	}
	fmt.Fprintf(&sb, "ffmpeg:   %s\n\n", ff)
	sb.WriteString("Keys:     P/Space=Pause Left/Right=Seek Home A=Sound\n")
	sb.WriteString("          M=Sixel/ANSI R=Redraw Q=Quit")
	s.message("Video", sb.String())
}
