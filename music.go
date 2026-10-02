package main

import (
	"bytes"
	"encoding/binary"
	"math"
	"strings"
	"sync"
	"time"
)

// ANSI music: a GW-BASIC PLAY (MML) string carried in one of these sequences,
// all terminated by ^N (0x0E):
//
//	ESC [ M ...      classic ANSI music (often written ESC[MF... / ESC[MB...)
//	ESC [ N ...      BananaCom music
//	ESC [ | ...      SyncTERM / CTerm music
const musicEnd = 0x0E

type segment struct {
	data  []byte
	music bool
	body  string // MML for music segments
}

func isMMLByte(c byte) bool {
	if c >= 'a' && c <= 'z' {
		c -= 32
	}
	return strings.IndexByte("0123456789ABCDEFGLMNOPSTX #+-.<>;,=\r\n\t", c) >= 0
}

// splitANSI cuts an ANSI file into plain output and music segments.
func splitANSI(data []byte) []segment {
	var segs []segment
	start := 0
	for i := 0; i+2 < len(data); i++ {
		if data[i] != 27 || data[i+1] != '[' {
			continue
		}
		intro := data[i+2]
		if intro != 'M' && intro != 'N' && intro != '|' {
			continue
		}
		end := bytes.IndexByte(data[i+3:min(len(data), i+3+8192)], musicEnd)
		if end < 0 {
			continue
		}
		bodyStart := i + 3
		if intro == 'M' {
			bodyStart = i + 2 // the M doubles as the MF/MB/MN/ML/MS command letter
		}
		body := data[bodyStart : i+3+end]
		valid := true
		for _, c := range body {
			if !isMMLByte(c) {
				valid = false
				break
			}
		}
		if !valid || (intro == 'M' && len(body) < 2) {
			continue
		}
		if i > start {
			segs = append(segs, segment{data: data[start:i]})
		}
		stop := i + 3 + end + 1
		segs = append(segs, segment{data: data[i:stop], music: true, body: string(body)})
		start = stop
		i = stop - 1
	}
	if start < len(data) {
		segs = append(segs, segment{data: data[start:]})
	}
	return segs
}

// musicSequence re-encodes a music segment for the remote terminal per mode.
func musicSequence(sg segment, mode string) []byte {
	body := sg.body
	switch mode {
	case "strip":
		return nil
	case "sync":
		return []byte("\x1b[|" + body + "\x0e")
	case "banana":
		return []byte("\x1b[N" + body + "\x0e")
	case "ansi":
		if strings.HasPrefix(strings.ToUpper(body), "M") {
			return []byte("\x1b[" + body + "\x0e")
		}
		return []byte("\x1b[M" + body + "\x0e")
	}
	return sg.data
}

func nextMusicMode(m string) string {
	modes := []string{"pass", "sync", "banana", "ansi", "strip"}
	for i, v := range modes {
		if v == m {
			return modes[(i+1)%len(modes)]
		}
	}
	return "pass"
}

type tone struct {
	freq  float64 // 0 = rest
	sound time.Duration
	gap   time.Duration
}

// parseMML interprets GW-BASIC PLAY syntax. Octave 3 starts at middle C.
func parseMML(body string) (tones []tone, background bool) {
	s := strings.ToUpper(body)
	octave, length, tempo := 4, 4, 120
	style := 7.0 / 8.0
	i := 0
	num := func() (int, bool) {
		j := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == j || i-j > 5 {
			return 0, false
		}
		n := 0
		for _, c := range s[j:i] {
			n = n*10 + int(c-'0')
		}
		return n, true
	}
	dots := func() int {
		d := 0
		for i < len(s) && s[i] == '.' {
			d++
			i++
		}
		return d
	}
	duration := func(l, d int) time.Duration {
		secs := 240.0 / float64(tempo) / float64(l)
		secs *= 2 - math.Pow(0.5, float64(d))
		return time.Duration(secs * float64(time.Second))
	}
	emit := func(idx, l, d int) {
		dur := duration(l, d)
		if idx < 0 {
			tones = append(tones, tone{gap: dur})
			return
		}
		freq := 440 * math.Pow(2, float64(idx-45)/12)
		snd := time.Duration(float64(dur) * style)
		tones = append(tones, tone{freq: freq, sound: snd, gap: dur - snd})
	}
	semis := map[byte]int{'C': 0, 'D': 2, 'E': 4, 'F': 5, 'G': 7, 'A': 9, 'B': 11}
	for i < len(s) {
		c := s[i]
		i++
		switch c {
		case 'A', 'B', 'C', 'D', 'E', 'F', 'G':
			semi := semis[c]
			if i < len(s) && (s[i] == '#' || s[i] == '+') {
				semi++
				i++
			} else if i < len(s) && s[i] == '-' {
				semi--
				i++
			}
			l := length
			if n, ok := num(); ok && n >= 1 && n <= 64 {
				l = n
			}
			emit(max(octave*12+semi, 0), l, dots())
		case 'N':
			n, _ := num()
			d := dots()
			if n <= 0 || n > 84 {
				emit(-1, length, d)
			} else {
				emit(n-1, length, d)
			}
		case 'O':
			if n, ok := num(); ok && n <= 6 {
				octave = n
			}
		case '<':
			octave = max(octave-1, 0)
		case '>':
			octave = min(octave+1, 6)
		case 'L':
			if n, ok := num(); ok && n >= 1 && n <= 64 {
				length = n
			}
		case 'T':
			if n, ok := num(); ok && n >= 32 && n <= 255 {
				tempo = n
			}
		case 'P':
			l := length
			if n, ok := num(); ok && n >= 1 && n <= 64 {
				l = n
			}
			emit(-1, l, dots())
		case 'M':
			if i < len(s) {
				switch s[i] {
				case 'N':
					style = 7.0 / 8.0
				case 'L':
					style = 1
				case 'S':
					style = 3.0 / 4.0
				case 'F':
					background = false
				case 'B':
					background = true
				default:
					continue
				}
				i++
			}
		}
	}
	return tones, background
}

const sampleRate = 22050

// synthWAV renders tones as an 8-bit PC-speaker style square wave.
func synthWAV(tones []tone) ([]byte, time.Duration) {
	var pcm []byte
	var total time.Duration
	samples := func(d time.Duration) int { return int(d.Seconds() * sampleRate) }
	for _, t := range tones {
		total += t.sound + t.gap
		if total > 10*time.Minute {
			break
		}
		n := samples(t.sound)
		if t.freq > 0 && t.freq < 12000 {
			half := sampleRate / t.freq / 2
			for k := 0; k < n; k++ {
				if int(float64(k)/half)%2 == 0 {
					pcm = append(pcm, 128+40)
				} else {
					pcm = append(pcm, 128-40)
				}
			}
		} else {
			pcm = append(pcm, bytes.Repeat([]byte{128}, n)...)
		}
		pcm = append(pcm, bytes.Repeat([]byte{128}, samples(t.gap))...)
	}
	if len(pcm) == 0 {
		return nil, 0
	}
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + len(pcm)))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1))
	w(uint16(1))
	w(uint32(sampleRate))
	w(uint32(sampleRate))
	w(uint16(1))
	w(uint16(8))
	b.WriteString("data")
	w(uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes(), total
}

// musicPlayer plays queued tunes one after another on the local sound device.
type musicPlayer struct {
	mu   sync.Mutex
	gen  int
	jobs chan musicJob
	stop chan struct{}
}

type musicJob struct {
	wav  []byte
	dur  time.Duration
	gen  int
	done chan struct{}
}

func newMusicPlayer() *musicPlayer {
	p := &musicPlayer{jobs: make(chan musicJob, 64), stop: make(chan struct{}, 1)}
	go p.run()
	return p
}

func (p *musicPlayer) run() {
	for j := range p.jobs {
		p.mu.Lock()
		cur := p.gen
		p.mu.Unlock()
		if j.gen == cur {
			select {
			case <-p.stop:
			default:
			}
			playSoundMem(j.wav)
			select {
			case <-time.After(j.dur):
			case <-p.stop:
				stopSound()
			}
		}
		close(j.done)
	}
}

func (p *musicPlayer) play(wav []byte, dur time.Duration) chan struct{} {
	p.mu.Lock()
	g := p.gen
	p.mu.Unlock()
	done := make(chan struct{})
	p.jobs <- musicJob{wav: wav, dur: dur, gen: g, done: done}
	return done
}

func (p *musicPlayer) stopAll() {
	p.mu.Lock()
	p.gen++
	p.mu.Unlock()
	select {
	case p.stop <- struct{}{}:
	default:
	}
	stopSound()
}

// playLocal plays an MML string on the sysop's PC. Foreground music blocks
// until finished; returns false if a keypress interrupted it.
func (s *Session) playLocal(body string) bool {
	tones, bg := parseMML(body)
	wav, dur := synthWAV(tones)
	if wav == nil {
		return true
	}
	done := s.player.play(wav, dur)
	if bg {
		return true
	}
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return true
		case <-tick.C:
			if s.keyAvailable() {
				s.player.stopAll()
				return false
			}
		}
	}
}
