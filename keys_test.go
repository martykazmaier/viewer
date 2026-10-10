package main

import "testing"

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
