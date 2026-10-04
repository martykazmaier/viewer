//go:build linux

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

type sockHandle = int

// localAudio reports whether ANSI music can be played on the local machine.
const localAudio = false

const errWouldBlock = syscall.EAGAIN

func sockRecv(fd int, p []byte) (int, error) {
	n, err := syscall.Read(fd, p)
	if err == syscall.EINTR {
		return 0, errWouldBlock
	}
	return n, err
}

func sockSend(fd int, p []byte) (int, error) {
	n, err := syscall.Write(fd, p)
	if err == syscall.EINTR {
		return 0, errWouldBlock
	}
	return n, err
}

func (s *Session) initSocket(h int64) error {
	fd := int(h)
	if _, err := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_TYPE); err != nil {
		return fmt.Errorf("file descriptor %d is not a socket (%v). The BBS must pass the "+
			"connection's socket descriptor (DOOR32.SYS line 2 or -H) and leave it open for the door", fd, err)
	}
	s.sock = fd
	s.raw = make(chan byte, 8192)
	go s.readLoop(func(p []byte) (int, error) { return sockRecv(fd, p) }, true)
	return nil
}

type winsize struct{ Row, Col, X, Y uint16 }

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

// initConsole uses stdin/stdout, switching a terminal to raw mode. When stdin is
// not a terminal this doubles as a stdio door connection.
func (s *Session) initConsole() error {
	var old syscall.Termios
	if ioctl(0, syscall.TCGETS, unsafe.Pointer(&old)) == nil {
		raw := old
		raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
			syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
		raw.Oflag &^= syscall.OPOST
		raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
		raw.Cflag &^= syscall.CSIZE | syscall.PARENB
		raw.Cflag |= syscall.CS8
		raw.Cc[syscall.VMIN] = 1
		raw.Cc[syscall.VTIME] = 0
		ioctl(0, syscall.TCSETS, unsafe.Pointer(&raw))
		s.restore = func() { ioctl(0, syscall.TCSETS, unsafe.Pointer(&old)) }
	}
	var ws winsize
	if ioctl(1, syscall.TIOCGWINSZ, unsafe.Pointer(&ws)) == nil && ws.Col >= 40 && ws.Row >= 10 {
		s.W, s.H = int(ws.Col), int(ws.Row)
		s.sizeKnown = true
	}
	s.conOut = func(p []byte) {
		for len(p) > 0 {
			n, err := syscall.Write(1, p)
			if err == syscall.EINTR || err == syscall.EAGAIN {
				continue
			}
			if err != nil || n <= 0 {
				return
			}
			p = p[n:]
		}
	}
	s.raw = make(chan byte, 8192)
	go s.readLoop(func(p []byte) (int, error) { return sockRecv(0, p) }, false)
	return nil
}

func playSoundMem([]byte)    {}
func stopSound()             {}
func timeBeginPeriod(uint32) {}
func timeEndPeriod(uint32)   {}
