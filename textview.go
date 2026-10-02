package main

import (
	"bytes"
	"fmt"
)

// textLines splits CP437 text into display lines wrapped at width, stopping at ^Z.
func textLines(data []byte, width int) [][]byte {
	if i := bytes.IndexByte(data, 0x1A); i >= 0 {
		data = data[:i]
	}
	var lines [][]byte
	for _, raw := range bytes.Split(data, []byte{'\n'}) {
		raw = bytes.TrimSuffix(raw, []byte{'\r'})
		var ln []byte
		for _, c := range raw {
			switch {
			case c == '\t':
				ln = append(ln, bytes.Repeat([]byte{' '}, 8-len(ln)%8)...)
			case c < 32 || c == 127:
				ln = append(ln, ' ')
			default:
				ln = append(ln, c)
			}
		}
		for len(ln) > width {
			lines = append(lines, ln[:width])
			ln = ln[width:]
		}
		lines = append(lines, ln)
	}
	for len(lines) > 1 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func hexLines(data []byte) [][]byte {
	lines := make([][]byte, 0, len(data)/16+1)
	for off := 0; off < len(data); off += 16 {
		row := data[off:min(off+16, len(data))]
		b := fmt.Appendf(nil, "%08X  ", off)
		for i := 0; i < 16; i++ {
			if i == 8 {
				b = append(b, ' ')
			}
			if i < len(row) {
				b = fmt.Appendf(b, "%02X ", row[i])
			} else {
				b = append(b, "   "...)
			}
		}
		b = append(b, ' ')
		for _, c := range row {
			if c < 32 || c == 127 || c == 255 {
				c = '.'
			}
			b = append(b, c)
		}
		lines = append(lines, b)
	}
	return lines
}

// isBinary guesses whether data is binary by counting control bytes.
func isBinary(data []byte) bool {
	sample := data[:min(len(data), 8192)]
	if len(sample) == 0 {
		return false
	}
	ctrl := 0
	for _, c := range sample {
		if c < 32 && c != '\t' && c != '\n' && c != '\r' && c != 12 && c != 26 && c != 27 {
			ctrl++
		}
	}
	return ctrl*20 > len(sample)
}

// pager is a scrollable full-screen viewer for prepared lines.
func (s *Session) pager(title string, lines [][]byte, mode string) {
	if len(lines) == 0 {
		lines = [][]byte{nil}
	}
	top := 0
	dirty := true
	for {
		rows := max(s.H-2, 1)
		maxTop := max(len(lines)-rows, 0)
		top = min(max(top, 0), maxTop)
		if dirty {
			s.Cls()
			s.drawPagerBars(title, mode, top, rows, len(lines))
			s.flush()
			s.Color(7, 0)
			for i := 0; i < rows; i++ {
				s.GotoXY(1, 2+i)
				if top+i < len(lines) {
					ln := lines[top+i]
					s.out = append(s.out, ln...)
					if len(ln) < s.W {
						s.ClrEol()
					}
				}
			}
			s.paced(s.takeOut(), true)
			dirty = false
		}
		old := top
		switch k := s.waitKey(); k {
		case kUp, 'k', 'K':
			top--
		case kDown, kEnter, 'j', 'J':
			top++
		case kPgUp, '-':
			top -= rows
		case kPgDn, ' ', '+':
			top += rows
		case kHome:
			top = 0
		case kEnd:
			top = maxTop
		case 'b', 'B':
			s.Baud = nextBaud(s.Baud)
			dirty = true
		case 'q', 'Q', kEsc:
			return
		}
		top = min(max(top, 0), maxTop)
		if top != old {
			dirty = true
		}
	}
}

func (s *Session) drawPagerBars(title, mode string, top, rows, total int) {
	last := min(top+rows, total)
	pct := 100
	if total > rows {
		pct = top * 100 / (total - rows)
	}
	right := fmt.Sprintf("%s Lines %d-%d of %d (%d%%) ", mode, top+1, last, total, pct)
	s.Bar(1, " "+safeText(title), right, 15, 1)
	s.Bar(s.H, " Up/Dn PgUp/PgDn Home/End  B=Baud:"+baudLabel(s.Baud)+"  Q=Quit", "", 0, 3)
}
