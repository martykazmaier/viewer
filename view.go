package main

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const maxView = 32 << 20

func viewPath(s *Session, p string) {
	f, err := os.Open(p)
	if err != nil {
		s.message("File not found", safeText(p))
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		s.message("Cannot view", safeText(p)+" is not a file.")
		return
	}
	viewFile(s, filepath.Base(p), f, st.Size(), 0)
}

func viewFile(s *Session, name string, r io.ReaderAt, size int64, depth int) {
	head, _ := readAt(r, 0, 512)
	if depth < 8 {
		if data, inner, ok, err := unwrapCompressed(name, head, r, size); ok {
			if err != nil {
				s.message(safeText(name), err.Error())
				return
			}
			viewFile(s, inner, bytes.NewReader(data), int64(len(data)), depth+1)
			return
		}
		if format := imageFormat(head); format != "" && size <= maxView {
			data, _ := readAt(r, 0, int(size))
			s.imageView(name, format, data)
			return
		}
		arc, err := openArchive(head, r, size)
		if err != nil {
			s.message(safeText(name), err.Error())
			return
		}
		if arc != nil {
			arcView(s, name, arc, depth)
			return
		}
	}
	data, _ := readAt(r, 0, int(min(size, maxView)))
	viewData(s, name, data)
}

func viewData(s *Session, name string, data []byte) {
	switch {
	case isANSIName(name):
		s.ansiView(name, data)
	case isBinary(data):
		s.pager(name, hexLines(data), "HEX")
	case bytes.Contains(data, []byte("\x1b[")):
		s.ansiView(name, data)
	default:
		s.pager(name, textLines(data, s.W), "")
	}
}

func (s *Session) viewEntry(e *Entry, depth int) {
	if e.IsDir {
		return
	}
	if e.Open == nil {
		s.message("Cannot view", e.NoView)
		return
	}
	rd, err := e.Open()
	if err == nil {
		var data []byte
		data, err = io.ReadAll(io.LimitReader(rd, maxView))
		if c, ok := rd.(io.Closer); ok {
			c.Close()
		}
		if err == nil || (errors.Is(err, io.ErrUnexpectedEOF) && len(data) > 0) {
			viewFile(s, path.Base(e.Name), bytes.NewReader(data), int64(len(data)), depth+1)
			return
		}
	}
	s.message("Cannot view", "Extraction failed: "+err.Error())
}

// unwrapCompressed decompresses gzip and bzip2 files so their contents
// (often a TAR archive) can be viewed.
func unwrapCompressed(name string, head []byte, r io.ReaderAt, size int64) ([]byte, string, bool, error) {
	lower := strings.ToLower(name)
	inner := func(exts map[string]string) string {
		for ext, repl := range exts {
			if strings.HasSuffix(lower, ext) {
				return name[:len(name)-len(ext)] + repl
			}
		}
		return name + ".out"
	}
	var rd io.Reader
	var innerName string
	switch {
	case len(head) >= 3 && head[0] == 0x1F && head[1] == 0x8B:
		gz, err := gzip.NewReader(io.NewSectionReader(r, 0, size))
		if err != nil {
			return nil, "", true, err
		}
		defer gz.Close()
		rd = gz
		innerName = inner(map[string]string{".tgz": ".tar", ".gz": "", ".gzip": ""})
		if gz.Name != "" && !strings.HasSuffix(lower, ".tgz") {
			innerName = latin1ToCP437(gz.Name)
		}
	case len(head) >= 10 && string(head[:3]) == "BZh" && string(head[4:10]) == "1AY&SY":
		rd = bzip2.NewReader(io.NewSectionReader(r, 0, size))
		innerName = inner(map[string]string{".tbz2": ".tar", ".tbz": ".tar", ".bz2": ""})
	default:
		return nil, "", false, nil
	}
	data, err := io.ReadAll(io.LimitReader(rd, maxView+1))
	if err != nil && len(data) == 0 {
		return nil, "", true, err
	}
	if len(data) > maxView {
		return nil, "", true, errors.New("Decompressed data is too large to view.")
	}
	return data, innerName, true, nil
}
