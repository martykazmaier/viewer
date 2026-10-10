package main

import (
	"fmt"
	"path"
	"strings"
)

func ratioText(e *Entry) string {
	if !e.SizeKnown || e.Size <= 0 || e.IsDir {
		return ""
	}
	return fmt.Sprintf("%d%%", max(100-e.Packed*100/e.Size, 0))
}

func displayName(e *Entry) string {
	n := safeText(e.Name)
	if e.IsDir && !strings.HasSuffix(n, "/") {
		n += "/"
	}
	return n
}

func (s *Session) listRow(e *Entry) string {
	w := s.W - 1
	size := "<DIR>"
	if !e.IsDir {
		size = "?"
		if e.SizeKnown {
			size = fmtShort(e.Size)
		}
	}
	nameW := w - 34
	if nameW < 20 {
		return " " + fitTail(displayName(e), w-12) + " " + rjust(size, 10)
	}
	date, tm := "", ""
	if !e.Modified.IsZero() {
		date, tm = e.Modified.Format("2006-01-02"), e.Modified.Format("15:04")
	}
	return fmt.Sprintf(" %s %10s %4s %10s %5s", fitTail(displayName(e), nameW), rjust(size, 10), ratioText(e), date, tm)
}

func (s *Session) listHeader() string {
	w := s.W - 1
	if w-34 < 20 {
		return " " + fit("Name", w-12) + " " + rjust("Size", 10)
	}
	return fmt.Sprintf(" %s %10s %4s %10s %5s", fit("Name", w-34), "Size", "Cmp", "Date", "Time")
}

func (s *Session) drawRow(arc *Archive, i, top int, selected bool) {
	if i < 0 || i >= len(arc.Entries) {
		return
	}
	e := arc.Entries[i]
	switch {
	case selected:
		s.Color(15, 1)
	case e.IsDir:
		s.Color(14, 0)
	case e.Encrypted:
		s.Color(12, 0)
	default:
		s.Color(7, 0)
	}
	s.GotoXY(1, 3+i-top)
	s.Print(fit(s.listRow(e), s.W-1))
	s.ClrEol()
}

// arcView shows the archive contents with a light bar.
func arcView(s *Session, name string, arc *Archive, depth int) {
	if len(arc.Entries) == 0 {
		msg := "The archive contains no files."
		if arc.Note != "" {
			msg = arc.Note
		}
		s.message(safeText(name), msg)
		return
	}
	var total, packed int64
	for _, e := range arc.Entries {
		total += e.Size
		packed += e.Packed
	}
	sel, top, prev := 0, 0, -1
	full := true
	for {
		rows := max(s.H-4, 1)
		if sel < top {
			top, full = sel, true
		}
		if sel >= top+rows {
			top, full = sel-rows+1, true
		}
		if full {
			s.Cls()
			right := fmt.Sprintf("%s  %d files  %s bytes ", arc.Format, len(arc.Entries), fmtNum(total))
			s.Bar(1, " "+safeText(name), right, 15, 1)
			s.GotoXY(1, 2)
			s.Color(11, 0)
			s.Print(fit(s.listHeader(), s.W-1))
			for i := top; i < top+rows; i++ {
				if i < len(arc.Entries) {
					s.drawRow(arc, i, top, i == sel)
				}
			}
			full = false
		} else if prev != sel {
			s.drawRow(arc, prev, top, false)
			s.drawRow(arc, sel, top, true)
		}
		prev = sel

		info := displayName(arc.Entries[sel])
		if arc.Note != "" {
			info = arc.Note
		}
		s.GotoXY(1, s.H-1)
		s.Color(14, 0)
		s.Print(fitTail(" "+info, s.W-1))
		s.ClrEol()
		help := " Up/Dn PgUp/PgDn Home/End  Enter=View  D=Details"
		if arc.Comment != "" {
			help += "  C=Comment"
		}
		s.Bar(s.H, help+"  Q=Quit", fmt.Sprintf("%d/%d ", sel+1, len(arc.Entries)), 0, 3)

		switch k := s.waitKey(); k {
		case kUp:
			sel--
		case kDown:
			sel++
		case kPgUp:
			sel -= rows
		case kPgDn:
			sel += rows
		case kHome:
			sel = 0
		case kEnd:
			sel = len(arc.Entries) - 1
		case kEnter, 'v', 'V':
			if e := arc.Entries[sel]; e.IsDir || e.Open == nil {
				sel = s.arcDetails(name, arc, sel, depth)
			} else {
				s.viewEntry(e, depth)
			}
			s.settle()
			full = true
		case 'd', 'D', kRight:
			sel = s.arcDetails(name, arc, sel, depth)
			s.settle()
			full = true
		case 'c', 'C':
			if arc.Comment != "" {
				s.pager(name+" - comment", textLines([]byte(arc.Comment), s.W), "")
				s.settle()
				full = true
			}
		case 'q', 'Q', kEsc, kLeft:
			return
		}
		sel = min(max(sel, 0), len(arc.Entries)-1)
	}
}

// arcDetails shows everything known about an entry; returns the entry index
// the user ended on.
func (s *Session) arcDetails(name string, arc *Archive, idx, depth int) int {
	for {
		e := arc.Entries[idx]
		s.Cls()
		s.Bar(1, " "+safeText(name)+" - file details", fmt.Sprintf("%d of %d ", idx+1, len(arc.Entries)), 15, 1)

		boxW := min(s.W-2, 100)
		x := (s.W-boxW)/2 + 1
		labelW := 12
		valW := boxW - labelW - 5
		var lines []string
		field := func(label, value string) {
			if value == "" {
				return
			}
			for i, part := range wrapText(value, valW) {
				l := ""
				if i == 0 {
					l = label
				}
				lines = append(lines, fit(l, labelW)+" "+part)
			}
		}
		full := displayName(e)
		dir, base := path.Split(strings.TrimSuffix(full, "/"))
		field("Name", base)
		if dir == "" {
			dir = "(archive root)"
		}
		field("Folder", dir)
		kind := "File"
		if e.IsDir {
			kind = "Directory"
		}
		if e.Encrypted {
			kind += ", encrypted"
		}
		field("Type", kind)
		if !e.IsDir {
			if e.SizeKnown {
				field("Size", fmtNum(e.Size)+" bytes")
			} else {
				field("Size", "Unknown")
			}
			p := fmtNum(e.Packed) + " bytes"
			if r := ratioText(e); r != "" {
				p += " (" + r + " saved)"
			}
			field("Packed", p)
			field("Method", e.Method)
		}
		if !e.Modified.IsZero() {
			field("Modified", e.Modified.Format("2006-01-02 15:04:05"))
		}
		if e.HasCRC && !e.IsDir {
			field("CRC-32", fmt.Sprintf("%08X", e.CRC))
		}
		field("Attributes", e.Attr)
		field("Host OS", e.HostOS)
		for _, kv := range e.Info {
			field(kv[0], safeText(kv[1]))
		}
		field("Comment", safeText(e.Comment))
		if !e.IsDir {
			if e.Open != nil {
				field("Viewable", "Yes - press Enter")
			} else {
				field("Viewable", "No - "+e.NoView)
			}
		}

		maxLines := max(s.H-5, 1)
		if len(lines) > maxLines {
			lines = lines[:maxLines]
		}
		s.Color(9, 0)
		s.GotoXY(x, 2)
		s.Print("\xDA", strings.Repeat("\xC4", boxW-2), "\xBF")
		for i, l := range lines {
			s.GotoXY(x, 3+i)
			s.Color(9, 0)
			s.Print("\xB3 ")
			s.Color(11, 0)
			s.Print(fit(l[:labelW], labelW))
			s.Color(15, 0)
			s.Print(fit(l[labelW:], boxW-labelW-3))
			s.Color(9, 0)
			s.Print("\xB3")
		}
		s.GotoXY(x, 3+len(lines))
		s.Print("\xC0", strings.Repeat("\xC4", boxW-2), "\xD9")

		help := " Up/Dn=Prev/Next  PgUp/PgDn Home/End"
		if e.Open != nil {
			help += "  Enter=View"
		}
		s.Bar(s.H, help+"  Esc/Q=Back to list", "", 0, 3)

		switch k := s.waitKey(); k {
		case kUp, kLeft, 'p', 'P':
			idx = max(idx-1, 0)
		case kDown, kRight, 'n', 'N':
			idx = min(idx+1, len(arc.Entries)-1)
		case kPgUp:
			idx = max(idx-max(s.H-4, 1), 0)
		case kPgDn:
			idx = min(idx+max(s.H-4, 1), len(arc.Entries)-1)
		case kHome:
			idx = 0
		case kEnd:
			idx = len(arc.Entries) - 1
		case 'v', 'V', kEnter:
			if e.Open != nil {
				s.viewEntry(e, depth)
				s.settle()
			} else if k == kEnter {
				return idx
			}
		case 'q', 'Q', kEsc, kBack:
			return idx
		}
	}
}
