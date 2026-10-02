package main

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	kTimeout = -1
	kNone    = 0
	kBack    = 8
	kEnter   = 13
	kEsc     = 27
	kUp      = 0x101
	kDown    = 0x102
	kLeft    = 0x103
	kRight   = 0x104
	kHome    = 0x105
	kEnd     = 0x106
	kPgUp    = 0x107
	kPgDn    = 0x108
	kIns     = 0x109
	kDel     = 0x10A
	kCPR     = 0x1FF
)

var baudRates = []int{0, 300, 1200, 2400, 9600, 14400, 19200, 28800, 38400, 57600, 115200}

// exitReason is raised with panic to unwind out of the UI on hangup or timeout.
type exitReason struct {
	msg    string
	hungup bool
}

type Session struct {
	Local    bool
	W, H     int
	Baud     int
	Music    string
	Idle     time.Duration
	Deadline time.Time

	sock     syscall.Handle
	hOut     syscall.Handle
	out      []byte
	lastAttr int
	raw      chan byte
	keys     chan int
	pending  []byte
	lastCR   bool
	hungup   bool
	escGuard time.Time
	cprRow   int
	cprCol   int

	paceStart time.Time
	paceSent  int64
	paceLast  time.Time

	player  *musicPlayer
	restore func()
}

func newSession(cfg config) (*Session, error) {
	s := &Session{
		Local:    cfg.local,
		Baud:     cfg.baud,
		Music:    cfg.music,
		Idle:     time.Duration(cfg.idle) * time.Minute,
		lastAttr: -1,
		W:        80,
		H:        24,
	}
	if cfg.timeLeft > 0 {
		s.Deadline = time.Now().Add(time.Duration(cfg.timeLeft) * time.Minute)
	}
	if s.Local {
		if err := s.initConsole(); err != nil {
			return nil, err
		}
	} else if err := s.initSocket(cfg.handle); err != nil {
		return nil, err
	}
	timeBeginPeriod(1)
	s.player = newMusicPlayer()
	if cfg.width > 0 {
		s.W, s.H = cfg.width, cfg.height
	} else if !s.Local {
		s.detectSize()
	}
	return s, nil
}

func (s *Session) initSocket(h int64) error {
	var d syscall.WSAData
	if err := syscall.WSAStartup(0x0202, &d); err != nil {
		return fmt.Errorf("WSAStartup failed: %v", err)
	}
	s.sock = syscall.Handle(h)
	if !sockConnected(s.sock) {
		dup, _, tried, ok := adoptSocket(s.sock)
		if !ok {
			var names []string
			for _, p := range tried {
				names = append(names, fmt.Sprintf("%s (pid %d)", p.name, p.pid))
			}
			state := "does not exist in this process"
			if handleExists(s.sock) {
				state = "is open in this process but is not a connected socket"
			}
			return fmt.Errorf("socket handle %d %s, and it could not be copied from a parent process "+
				"[tried: %s]. The launching door must start viewer.exe with handle inheritance, "+
				"or pass the right handle with -H", h, state, strings.Join(names, ", "))
		}
		s.sock = dup
	}
	s.raw = make(chan byte, 8192)
	go s.readLoop()
	return nil
}

func (s *Session) initConsole() error {
	hIn, err := syscall.GetStdHandle(syscall.STD_INPUT_HANDLE)
	if err != nil {
		return err
	}
	hOut, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE)
	if err != nil {
		return err
	}
	var inMode, outMode uint32
	if err := syscall.GetConsoleMode(hIn, &inMode); err != nil {
		return fmt.Errorf("local mode needs a console: %v", err)
	}
	syscall.GetConsoleMode(hOut, &outMode)
	oldCP := getConsoleOutputCP()

	const enableExtendedFlags = 0x0080
	const enableProcessedOutput = 0x0001
	const enableVTProcessing = 0x0004
	setConsoleMode(hIn, enableExtendedFlags)
	setConsoleMode(hOut, outMode|enableProcessedOutput|enableVTProcessing)
	setConsoleOutputCP(437)
	s.restore = func() {
		setConsoleMode(hIn, inMode)
		setConsoleMode(hOut, outMode)
		setConsoleOutputCP(oldCP)
	}
	s.hOut = hOut
	if w, h, ok := consoleSize(hOut); ok && w >= 40 && h >= 10 {
		s.W, s.H = w, h
	}
	s.keys = make(chan int, 256)
	go s.consoleLoop(hIn)
	return nil
}

func (s *Session) consoleLoop(h syscall.Handle) {
	recs := make([]inputRecord, 16)
	for {
		n, ok := readConsoleInput(h, recs)
		if !ok {
			close(s.keys)
			return
		}
		for _, r := range recs[:n] {
			if r.EventType != 1 || r.KeyDown == 0 {
				continue
			}
			k := mapVirtualKey(r)
			if k == kNone {
				continue
			}
			for i := 0; i < int(max(r.Repeat, 1)); i++ {
				s.keys <- k
			}
		}
	}
}

func mapVirtualKey(r inputRecord) int {
	switch r.VK {
	case 0x26:
		return kUp
	case 0x28:
		return kDown
	case 0x25:
		return kLeft
	case 0x27:
		return kRight
	case 0x21:
		return kPgUp
	case 0x22:
		return kPgDn
	case 0x24:
		return kHome
	case 0x23:
		return kEnd
	case 0x2D:
		return kIns
	case 0x2E:
		return kDel
	}
	c := rune(r.Char)
	switch {
	case c == 0:
		return kNone
	case c < 0x80:
		return int(c)
	}
	if b, ok := runeToCP437[c]; ok {
		return int(b)
	}
	return kNone
}

const (
	tnData = iota
	tnIAC
	tnOption
	tnSub
	tnSubIAC
)

func (s *Session) readLoop() {
	buf := make([]byte, 2048)
	state := tnData
	for {
		n, err := sockRecv(s.sock, buf)
		if err == errWouldBlock {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if err != nil || n == 0 {
			close(s.raw)
			return
		}
		for _, b := range buf[:n] {
			switch state {
			case tnData:
				if b == 255 {
					state = tnIAC
				} else {
					s.raw <- b
				}
			case tnIAC:
				switch {
				case b == 255:
					s.raw <- b
					state = tnData
				case b == 250:
					state = tnSub
				case b >= 251:
					state = tnOption
				default:
					state = tnData
				}
			case tnOption:
				state = tnData
			case tnSub:
				if b == 255 {
					state = tnSubIAC
				}
			case tnSubIAC:
				if b == 240 {
					state = tnData
				} else {
					state = tnSub
				}
			}
		}
	}
}

func (s *Session) hangup() {
	s.hungup = true
	panic(exitReason{hungup: true})
}

func (s *Session) fail(msg string) {
	panic(exitReason{msg: msg})
}

// run executes fn, converting hangups and timeouts into a clean shutdown.
func (s *Session) run(fn func()) (code int) {
	defer func() {
		if r := recover(); r != nil {
			er, ok := r.(exitReason)
			if !ok {
				s.close()
				panic(r)
			}
			if !er.hungup && er.msg != "" {
				s.safely(func() {
					s.ResetAttr()
					s.Print("\r\n\r\n" + er.msg + "\r\n")
					s.flush()
					time.Sleep(1500 * time.Millisecond)
				})
			}
			if er.hungup {
				code = 2
			}
		}
		s.close()
	}()
	fn()
	return 0
}

func (s *Session) safely(fn func()) {
	defer func() { recover() }()
	fn()
}

func (s *Session) close() {
	if s.player != nil {
		s.player.stopAll()
	}
	if !s.hungup {
		s.safely(func() {
			s.Cls()
			s.flush()
		})
	}
	timeEndPeriod(1)
	if s.restore != nil {
		s.restore()
		s.restore = nil
	}
}

func (s *Session) detectSize() {
	s.Print("\x1b[s\x1b[999;999H\x1b[6n\x1b[u")
	s.flush()
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if s.getKey(time.Until(deadline)) == kCPR {
			if s.cprCol >= 40 && s.cprRow >= 10 {
				s.W, s.H = min(s.cprCol, 255), min(s.cprRow, 200)
			}
			return
		}
	}
}

// ---- input ----

func (s *Session) unread(b byte) {
	s.pending = append([]byte{b}, s.pending...)
}

func (s *Session) readByte(timeout time.Duration) (byte, bool) {
	if len(s.pending) > 0 {
		b := s.pending[0]
		s.pending = s.pending[1:]
		return b, true
	}
	if timeout <= 0 {
		select {
		case b, ok := <-s.raw:
			if !ok {
				s.hangup()
			}
			return b, true
		default:
			return 0, false
		}
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case b, ok := <-s.raw:
		if !ok {
			s.hangup()
		}
		return b, true
	case <-t.C:
		return 0, false
	}
}

func (s *Session) keyAvailable() bool {
	if s.Local {
		return len(s.keys) > 0
	}
	return len(s.pending) > 0 || len(s.raw) > 0
}

// getKey returns the next key, kTimeout if none arrived in time, or kNone/kCPR for
// input that is not a keypress.
func (s *Session) getKey(timeout time.Duration) int {
	if s.Local {
		var t <-chan time.Time
		if timeout > 0 {
			tm := time.NewTimer(timeout)
			defer tm.Stop()
			t = tm.C
		} else {
			c := make(chan time.Time)
			close(c)
			t = c
		}
		select {
		case k, ok := <-s.keys:
			if !ok {
				s.hangup()
			}
			return k
		case <-t:
			return kTimeout
		}
	}
	b, ok := s.readByte(timeout)
	if !ok {
		return kTimeout
	}
	wasCR := s.lastCR
	s.lastCR = b == 13
	switch b {
	case 27:
		return s.decodeEsc()
	case 13:
		return kEnter
	case 10:
		if wasCR {
			return kNone
		}
		return kEnter
	case 0:
		return kNone
	case 127:
		return kBack
	}
	return int(b)
}

func (s *Session) decodeEsc() int {
	const wait = 80 * time.Millisecond
	b, ok := s.readByte(wait)
	if !ok {
		return kEsc
	}
	if b == 27 {
		return kEsc
	}
	if b != '[' && b != 'O' {
		s.unread(b)
		return kEsc
	}
	var params []byte
	for i := 0; i < 16; i++ {
		c, ok := s.readByte(wait)
		if !ok {
			return kEsc
		}
		if c >= 0x40 && c <= 0x7E {
			return s.mapSeq(b, string(params), c)
		}
		params = append(params, c)
	}
	return kNone
}

func (s *Session) mapSeq(intro byte, params string, final byte) int {
	switch final {
	case 'A':
		return kUp
	case 'B':
		return kDown
	case 'C':
		return kRight
	case 'D':
		return kLeft
	case 'H':
		return kHome
	case 'F', 'K':
		return kEnd
	case 'R':
		if intro == '[' {
			parts := strings.Split(params, ";")
			if len(parts) == 2 {
				s.cprRow, _ = strconv.Atoi(parts[0])
				s.cprCol, _ = strconv.Atoi(parts[1])
				return kCPR
			}
		}
	case '~':
		n, _ := strconv.Atoi(strings.Split(params, ";")[0])
		switch n {
		case 1, 7:
			return kHome
		case 4, 8:
			return kEnd
		case 5:
			return kPgUp
		case 6:
			return kPgDn
		case 2:
			return kIns
		case 3:
			return kDel
		}
	}
	return kNone
}

// settle is called when returning from a sub-screen: it discards typeahead
// and briefly ignores Esc, so one Esc press that some terminals deliver twice
// cannot also close the screen being returned to.
func (s *Session) settle() {
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		if s.getKey(time.Until(deadline)) == kTimeout {
			break
		}
	}
	s.escGuard = time.Now().Add(350 * time.Millisecond)
}

// waitKey blocks for a real keypress while enforcing the idle and time limits.
func (s *Session) waitKey() int {
	s.flush()
	start := time.Now()
	for {
		k := s.getKey(time.Second)
		if k == kEsc && time.Now().Before(s.escGuard) {
			continue
		}
		if k != kTimeout && k != kNone && k != kCPR {
			return k
		}
		if s.Idle > 0 && time.Since(start) > s.Idle {
			s.fail("Idle time limit exceeded. Returning to the BBS.")
		}
		if !s.Deadline.IsZero() && time.Now().After(s.Deadline) {
			s.fail("Your time online has expired.")
		}
	}
}

// ---- output ----

func (s *Session) Print(parts ...string) {
	for _, p := range parts {
		s.out = append(s.out, p...)
	}
}

func (s *Session) Printf(format string, args ...any) {
	s.out = fmt.Appendf(s.out, format, args...)
}

func (s *Session) flush() {
	if len(s.out) == 0 {
		return
	}
	s.rawSend(s.out)
	s.out = s.out[:0]
}

func (s *Session) takeOut() []byte {
	b := append([]byte(nil), s.out...)
	s.out = s.out[:0]
	return b
}

func (s *Session) rawSend(p []byte) {
	if len(p) == 0 {
		return
	}
	if s.Local {
		for len(p) > 0 {
			var n uint32
			if err := syscall.WriteFile(s.hOut, p, &n, nil); err != nil || n == 0 {
				return
			}
			p = p[n:]
		}
		return
	}
	if bytes.IndexByte(p, 0xFF) >= 0 {
		p = bytes.ReplaceAll(p, []byte{0xFF}, []byte{0xFF, 0xFF})
	}
	for len(p) > 0 {
		n, err := sockSend(s.sock, p)
		if err == errWouldBlock {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if err != nil {
			s.hangup()
		}
		p = p[n:]
	}
}

// escTracker follows escape sequences so paced output is never cut mid-sequence.
type escTracker int

func (e *escTracker) feed(b byte) {
	switch *e {
	case 0:
		if b == 27 {
			*e = 1
		}
	case 1:
		if b == '[' {
			*e = 2
		} else {
			*e = 0
		}
	case 2:
		if b >= 0x40 && b <= 0x7E {
			*e = 0
		}
	}
}

// paced sends p at the emulated baud rate. When abortable, a keypress stops the
// output early and paced returns false; the key stays queued for the caller.
func (s *Session) paced(p []byte, abortable bool) bool {
	s.flush()
	if s.Baud <= 0 {
		s.rawSend(p)
		return true
	}
	cps := float64(s.Baud) / 10
	chunk := max(int(cps/100), 1)
	if now := time.Now(); now.Sub(s.paceLast) > 100*time.Millisecond {
		s.paceStart, s.paceSent = now, 0
	}
	var esc escTracker
	for i := 0; i < len(p); {
		if abortable && s.keyAvailable() {
			j := i
			for j < len(p) && esc != 0 {
				esc.feed(p[j])
				j++
			}
			s.rawSend(p[i:j])
			s.lastAttr = -1
			s.paceLast = time.Now()
			return false
		}
		end := min(i+chunk, len(p))
		for _, b := range p[i:end] {
			esc.feed(b)
		}
		s.rawSend(p[i:end])
		s.paceSent += int64(end - i)
		i = end
		due := s.paceStart.Add(time.Duration(float64(s.paceSent) / cps * float64(time.Second)))
		if d := time.Until(due); d > 0 {
			time.Sleep(d)
		}
	}
	s.paceLast = time.Now()
	return true
}

var dosToANSI = [8]int{0, 4, 2, 6, 1, 5, 3, 7}

// Color selects a DOS-style attribute: fg 0-15, bg 0-7.
func (s *Session) Color(fg, bg int) {
	a := fg&15 | (bg&7)<<4
	if a == s.lastAttr {
		return
	}
	s.lastAttr = a
	bold := ""
	if fg >= 8 {
		bold = "1;"
	}
	s.Printf("\x1b[0;%s3%d;4%dm", bold, dosToANSI[fg&7], dosToANSI[bg&7])
}

func (s *Session) ResetAttr() {
	s.Print("\x1b[0m")
	s.lastAttr = 7
}

func (s *Session) Cls() {
	s.Print("\x1b[0m\x1b[2J\x1b[H")
	s.lastAttr = 7
}

func (s *Session) GotoXY(x, y int) {
	s.Printf("\x1b[%d;%dH", y, x)
}

func (s *Session) ClrEol() {
	s.Print("\x1b[K")
}

// Bar draws a full-width status line with left and right aligned text.
func (s *Session) Bar(y int, left, right string, fg, bg int) {
	w := s.W - 1
	gap := w - len(left) - len(right)
	line := left
	if gap > 0 {
		line += strings.Repeat(" ", gap) + right
	}
	s.GotoXY(1, y)
	s.Color(fg, bg)
	s.Print(fit(line, w))
	s.ClrEol()
}

func baudLabel(b int) string {
	if b <= 0 {
		return "Off"
	}
	return strconv.Itoa(b)
}

func nextBaud(b int) int {
	for i, r := range baudRates {
		if r == b {
			return baudRates[(i+1)%len(baudRates)]
		}
	}
	return 0
}

// message shows a centered box and waits for a key.
func (s *Session) message(title, text string) {
	width := min(64, s.W-8)
	lines := wrapText(text, width-4)
	bw := width
	bh := len(lines) + 4
	x := (s.W-bw)/2 + 1
	y := max((s.H-bh)/2, 1)
	s.Color(15, 1)
	s.GotoXY(x, y)
	t := " " + fit(title, bw-6) + " "
	s.Print("\xDA\xC4", t, strings.Repeat("\xC4", max(bw-2-len(t)-1, 0)), "\xBF")
	for i := 0; i < bh-2; i++ {
		s.GotoXY(x, y+1+i)
		txt := ""
		if i >= 1 && i-1 < len(lines) {
			txt = lines[i-1]
		}
		if i == bh-3 {
			txt = "Press any key..."
		}
		s.Print("\xB3 ", pad(txt, bw-4), " \xB3")
	}
	s.GotoXY(x, y+bh-1)
	s.Print("\xC0", strings.Repeat("\xC4", bw-2), "\xD9")
	s.waitKey()
}
