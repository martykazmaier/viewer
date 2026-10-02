package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"time"
)

// Entry is one file inside an archive. Name is CP437 text with '/' separators.
type Entry struct {
	Name      string
	Size      int64
	Packed    int64
	SizeKnown bool
	Modified  time.Time
	CRC       uint32
	HasCRC    bool
	Method    string
	Attr      string
	HostOS    string
	IsDir     bool
	Encrypted bool
	Comment   string
	Info      [][2]string
	Open      func() (io.Reader, error)
	NoView    string
}

type Archive struct {
	Format  string
	Entries []*Entry
	Comment string
	Note    string
}

func (e *Entry) addInfo(label, value string) {
	e.Info = append(e.Info, [2]string{label, value})
}

// openArchive identifies and lists an archive. It returns nil, nil for non-archives.
func openArchive(head []byte, r io.ReaderAt, size int64) (*Archive, error) {
	switch {
	case bytes.HasPrefix(head, []byte("Rar!\x1a\x07\x01\x00")):
		return openRAR5(r, size)
	case bytes.HasPrefix(head, []byte("Rar!\x1a\x07\x00")):
		return openRAR4(r, size)
	case bytes.HasPrefix(head, []byte("7z\xbc\xaf\x27\x1c")):
		return nil, errors.New("7-Zip archives are not supported.")
	case len(head) >= 4 && head[0] == 0x60 && head[1] == 0xEA:
		return openARJ(r, size)
	case isLZH(head):
		return openLZH(r, size)
	case bytes.HasPrefix(head, []byte("PK\x03\x04")), bytes.HasPrefix(head, []byte("PK\x05\x06")),
		bytes.HasPrefix(head, []byte("PK\x07\x08")), bytes.HasPrefix(head, []byte("PK00")):
		return openZIP(r, size)
	case isTar(head):
		return openTar(r, size)
	}
	if arc, err := openZIP(r, size); err == nil && len(arc.Entries) > 0 {
		arc.Format = "ZIP (embedded)"
		return arc, nil
	}
	return nil, nil
}

func le16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
func le64(b []byte) uint64 { return binary.LittleEndian.Uint64(b) }

func readAt(r io.ReaderAt, off int64, n int) ([]byte, error) {
	if n < 0 || off < 0 {
		return nil, io.ErrUnexpectedEOF
	}
	buf := make([]byte, n)
	m, err := r.ReadAt(buf, off)
	if m < n {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return buf[:m], err
	}
	return buf, nil
}

func storedOpener(r io.ReaderAt, off, n int64) func() (io.Reader, error) {
	return func() (io.Reader, error) { return io.NewSectionReader(r, off, n), nil }
}

// dosTime decodes a packed MS-DOS timestamp (time in the low word, date in the high word).
func dosTime(v uint32) time.Time {
	t, d := v&0xFFFF, v>>16
	if d == 0 {
		return time.Time{}
	}
	day, mon, yr := int(d&31), time.Month((d>>5)&15), int(d>>9)+1980
	if day < 1 || mon < 1 || mon > 12 {
		return time.Time{}
	}
	return time.Date(yr, mon, day, int(t>>11), int((t>>5)&63), int(t&31)*2, 0, time.Local)
}

func unixTime(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

func fileTime(ft uint64) time.Time {
	const epochDiff = 116444736000000000
	if ft <= epochDiff {
		return time.Time{}
	}
	return time.Unix(0, int64(ft-epochDiff)*100)
}

func dosAttrString(a uint32) string {
	flags := []struct {
		bit uint32
		c   byte
	}{{0x20, 'A'}, {0x10, 'D'}, {0x08, 'V'}, {0x04, 'S'}, {0x02, 'H'}, {0x01, 'R'}}
	b := make([]byte, len(flags))
	for i, f := range flags {
		b[i] = '-'
		if a&f.bit != 0 {
			b[i] = f.c
		}
	}
	return string(b)
}

func unixModeString(m uint32) string {
	types := map[uint32]byte{0o040000: 'd', 0o120000: 'l', 0o010000: 'p', 0o020000: 'c', 0o060000: 'b', 0o140000: 's'}
	b := []byte("----------")
	if t, ok := types[m&0o170000]; ok {
		b[0] = t
	}
	const rwx = "rwxrwxrwx"
	for i := 0; i < 9; i++ {
		if m&(1<<uint(8-i)) != 0 {
			b[i+1] = rwx[i]
		}
	}
	return string(b)
}

func normPath(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	for strings.HasPrefix(name, "./") {
		name = name[2:]
	}
	return strings.TrimLeft(name, "/")
}

func cstr(b []byte) []byte {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		return b[:i]
	}
	return b
}
