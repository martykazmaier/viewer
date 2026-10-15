package main

import (
	"strings"
	"testing"
	"time"
)

func TestDetectSixel(t *testing.T) {
	cases := []struct {
		name, reply string
		want        bool
	}{
		{"SyncTERM", "\x1b[<0;1;2;3;4;5;6;7;8c\x1b[=67;84;101;114;109;1;332c", true},
		{"SyncTERM without graphics", "\x1b[<0;1;2;3;5;6c\x1b[=67;84;101;114;109;1;332c", false},
		{"xterm with Sixel", "\x1b[4;400;640t\x1b[6;16;8t\x1b[?63;1;2;4;6;9;15;22c", true},
		{"plain VT100", "\x1b[?1;2c", false},
	}
	for _, c := range cases {
		s := &Session{raw: make(chan byte), pending: []byte(c.reply), conOut: func([]byte) {}, Graphics: "auto"}
		start := time.Now()
		s.detectGraphics()
		if s.sixel != c.want {
			t.Errorf("%s: sixel = %v, want %v", c.name, s.sixel, c.want)
		}
		if el := time.Since(start); el > 500*time.Millisecond {
			t.Errorf("%s: detection took %v; the DA reply should end it at once", c.name, el)
		}
	}
	s := &Session{raw: make(chan byte), conOut: func([]byte) {}, Graphics: "auto"}
	start := time.Now()
	s.detectGraphics()
	if s.sixel || time.Since(start) > 2*time.Second {
		t.Errorf("no reply: sixel = %v after %v, want ANSI after the timeout", s.sixel, time.Since(start))
	}
}

func TestIdleDisconnect(t *testing.T) {
	s := &Session{
		raw:     make(chan byte),
		conOut:  func([]byte) {},
		Idle:    20 * time.Millisecond,
		lastKey: time.Now().Add(-time.Second),
	}
	defer func() {
		r := recover()
		er, ok := r.(exitReason)
		if !ok || !er.drop || !strings.Contains(er.msg, "Idle") {
			t.Fatalf("idle: %#v", r)
		}
	}()
	s.waitKeyUntil(time.Now().Add(time.Second), nil)
	t.Fatal("waitKeyUntil returned instead of disconnecting")
}

func TestIdleResetsOnKey(t *testing.T) {
	s := &Session{raw: make(chan byte, 1), Idle: 50 * time.Millisecond, lastKey: time.Now()}
	s.raw <- 'x'
	if k := s.waitKeyUntil(time.Now().Add(20*time.Millisecond), nil); k != 'x' {
		t.Fatalf("got %#x", k)
	}
	if time.Since(s.lastKey) > 20*time.Millisecond {
		t.Fatal("lastKey was not updated")
	}
}

func TestNavigationKeys(t *testing.T) {
	cases := []struct {
		seq  string
		want int
	}{
		{"\x1b[V", kPgUp}, {"\x1b[U", kPgDn}, {"\x1b[H", kHome}, {"\x1b[K", kEnd},
		{"\x1b[5~", kPgUp}, {"\x1b[6~", kPgDn}, {"\x1b[1~", kHome}, {"\x1b[4~", kEnd},
		{"\x1b[7~", kHome}, {"\x1b[8~", kEnd}, {"\x1b[F", kEnd},
		{"\x1bOH", kHome}, {"\x1bOF", kEnd}, {"\x1b[A", kUp}, {"\x1b[B", kDown},
		{"\r", kEnter}, {"d", 'd'},
	}
	for _, c := range cases {
		s := &Session{raw: make(chan byte), pending: []byte(c.seq)}
		if got := s.getKey(0); got != c.want {
			t.Errorf("%q = %#x, want %#x", c.seq, got, c.want)
		}
	}
}
