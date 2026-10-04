package main

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

type sockHandle = syscall.Handle

// localAudio reports whether ANSI music can be played on the local machine.
const localAudio = true

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleMode             = kernel32.NewProc("SetConsoleMode")
	procReadConsoleInputW          = kernel32.NewProc("ReadConsoleInputW")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
	procGetConsoleOutputCP         = kernel32.NewProc("GetConsoleOutputCP")
	procSetConsoleOutputCP         = kernel32.NewProc("SetConsoleOutputCP")
	procGetHandleInformation       = kernel32.NewProc("GetHandleInformation")

	winmm               = syscall.NewLazyDLL("winmm.dll")
	procPlaySoundA      = winmm.NewProc("PlaySoundA")
	procTimeBeginPeriod = winmm.NewProc("timeBeginPeriod")
	procTimeEndPeriod   = winmm.NewProc("timeEndPeriod")
)

const (
	errWouldBlock = syscall.Errno(10035) // WSAEWOULDBLOCK

	solSocket = 0xffff
	soType    = 0x1008

	sndAsync     = 0x0001
	sndNoDefault = 0x0002
	sndMemory    = 0x0004
)

type coord struct{ X, Y int16 }

type consoleScreenBufferInfo struct {
	Size              coord
	CursorPosition    coord
	Attributes        uint16
	Left, Top         int16
	Right, Bottom     int16
	MaximumWindowSize coord
}

type inputRecord struct {
	EventType uint16
	_         uint16
	KeyDown   int32
	Repeat    uint16
	VK        uint16
	Scan      uint16
	Char      uint16
	Control   uint32
}

func setConsoleMode(h syscall.Handle, mode uint32) {
	procSetConsoleMode.Call(uintptr(h), uintptr(mode))
}

func consoleSize(h syscall.Handle) (w, ht int, ok bool) {
	var info consoleScreenBufferInfo
	r, _, _ := procGetConsoleScreenBufferInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return 0, 0, false
	}
	return int(info.Right-info.Left) + 1, int(info.Bottom-info.Top) + 1, true
}

func getConsoleOutputCP() uint32 {
	r, _, _ := procGetConsoleOutputCP.Call()
	return uint32(r)
}

func setConsoleOutputCP(cp uint32) {
	procSetConsoleOutputCP.Call(uintptr(cp))
}

func readConsoleInput(h syscall.Handle, recs []inputRecord) (int, bool) {
	var n uint32
	r, _, _ := procReadConsoleInputW.Call(uintptr(h), uintptr(unsafe.Pointer(&recs[0])), uintptr(len(recs)), uintptr(unsafe.Pointer(&n)))
	return int(n), r != 0
}

func sockRecv(h syscall.Handle, p []byte) (int, error) {
	wb := syscall.WSABuf{Len: uint32(len(p)), Buf: &p[0]}
	var n, flags uint32
	if err := syscall.WSARecv(h, &wb, 1, &n, &flags, nil, nil); err != nil {
		return 0, err
	}
	return int(n), nil
}

func sockSend(h syscall.Handle, p []byte) (int, error) {
	wb := syscall.WSABuf{Len: uint32(len(p)), Buf: &p[0]}
	var n uint32
	if err := syscall.WSASend(h, &wb, 1, &n, 0, nil, nil); err != nil {
		return 0, err
	}
	return int(n), nil
}

func sockValid(h syscall.Handle) error {
	var v int32
	l := int32(4)
	return syscall.Getsockopt(h, solSocket, soType, (*byte)(unsafe.Pointer(&v)), &l)
}

type procInfo struct {
	pid  uint32
	name string
}

// ancestors lists the parent, grandparent, ... of this process.
func ancestors(depth int) []procInfo {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer syscall.CloseHandle(snap)
	parent := map[uint32]uint32{}
	names := map[uint32]string{}
	var pe syscall.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = syscall.Process32First(snap, &pe); err == nil; err = syscall.Process32Next(snap, &pe) {
		parent[pe.ProcessID] = pe.ParentProcessID
		names[pe.ProcessID] = syscall.UTF16ToString(pe.ExeFile[:])
	}
	var out []procInfo
	seen := map[uint32]bool{}
	for pid := uint32(syscall.Getpid()); len(out) < depth; {
		pp, ok := parent[pid]
		if !ok || pp == 0 || seen[pp] {
			break
		}
		seen[pp] = true
		out = append(out, procInfo{pp, names[pp]})
		pid = pp
	}
	return out
}

func sockConnected(h syscall.Handle) bool {
	if sockValid(h) != nil {
		return false
	}
	_, err := syscall.Getpeername(h)
	return err == nil
}

// adoptSocket copies socket handle h out of a parent process (e.g. the door
// that launched us without handle inheritance) into this process.
func adoptSocket(h syscall.Handle) (syscall.Handle, procInfo, []procInfo, bool) {
	const processDupHandle = 0x0040
	self, _ := syscall.GetCurrentProcess()
	tried := ancestors(6)
	for _, p := range tried {
		ph, err := syscall.OpenProcess(processDupHandle, false, p.pid)
		if err != nil {
			continue
		}
		var dup syscall.Handle
		err = syscall.DuplicateHandle(ph, h, self, &dup, 0, false, syscall.DUPLICATE_SAME_ACCESS)
		syscall.CloseHandle(ph)
		if err != nil {
			continue
		}
		if sockConnected(dup) {
			return dup, p, tried, true
		}
		syscall.CloseHandle(dup)
	}
	return 0, procInfo{}, tried, false
}

// handleExists reports whether h is an open handle in this process.
func handleExists(h syscall.Handle) bool {
	var flags uint32
	r, _, _ := procGetHandleInformation.Call(uintptr(h), uintptr(unsafe.Pointer(&flags)))
	return r != 0
}

func playSoundMem(wav []byte) {
	procPlaySoundA.Call(uintptr(unsafe.Pointer(&wav[0])), 0, sndMemory|sndAsync|sndNoDefault)
}

func stopSound() {
	procPlaySoundA.Call(0, 0, 0)
}

func timeBeginPeriod(ms uint32) { procTimeBeginPeriod.Call(uintptr(ms)) }
func timeEndPeriod(ms uint32)   { procTimeEndPeriod.Call(uintptr(ms)) }

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
	go s.readLoop(func(p []byte) (int, error) { return sockRecv(s.sock, p) }, true)
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
	s.conOut = func(p []byte) {
		for len(p) > 0 {
			var n uint32
			if err := syscall.WriteFile(hOut, p, &n, nil); err != nil || n == 0 {
				return
			}
			p = p[n:]
		}
	}
	if w, h, ok := consoleSize(hOut); ok && w >= 40 && h >= 10 {
		s.W, s.H = w, h
	}
	s.sizeKnown = true
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
