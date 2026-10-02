package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
)

var ansiExts = map[string]bool{
	".ans": true, ".asc": true, ".ice": true, ".mus": true, ".ams": true,
	".drk": true, ".cia": true,
}

func isANSIName(name string) bool {
	return ansiExts[strings.ToLower(filepath.Ext(name))]
}

type sauceInfo struct {
	title, author, group, date, font string
	width, height                    int
	ice                              bool
}

func parseSAUCE(data []byte) (sauceInfo, bool) {
	if len(data) < 128 {
		return sauceInfo{}, false
	}
	r := data[len(data)-128:]
	if string(r[:7]) != "SAUCE00" {
		return sauceInfo{}, false
	}
	str := func(a, b int) string { return safeText(strings.TrimRight(string(r[a:b]), " \x00")) }
	si := sauceInfo{
		title:  str(7, 42),
		author: str(42, 62),
		group:  str(62, 82),
		date:   str(82, 90),
		font:   str(106, 128),
	}
	if r[94] == 1 { // character data
		si.width = int(r[96]) | int(r[97])<<8
		si.height = int(r[98]) | int(r[99])<<8
		si.ice = r[105]&1 != 0
	}
	if len(si.date) == 8 {
		si.date = si.date[:4] + "-" + si.date[4:6] + "-" + si.date[6:]
	}
	return si, true
}

func (s *Session) ansiView(name string, data []byte) {
	sauce, hasSauce := parseSAUCE(data)
	if i := bytes.IndexByte(data, 0x1A); i >= 0 {
		data = data[:i]
	}
	segs := splitANSI(data)
	tunes := 0
	for _, sg := range segs {
		if sg.music {
			tunes++
		}
	}
	defer s.player.stopAll()
	for {
		s.Cls()
		s.flush()
		if !s.playANSI(segs) {
			s.getKey(0)
		}
		s.ResetAttr()
		s.Print("\r\n")
		for {
			prompt := fmt.Sprintf(" [Enter] Done  [R] Replay  [B] Baud: %s ", baudLabel(s.Baud))
			if tunes > 0 {
				prompt += fmt.Sprintf(" [M] Music: %s (%d) ", s.Music, tunes)
			}
			if hasSauce {
				prompt += " [I] Info "
			}
			s.Print("\r")
			s.Color(15, 1)
			s.Print(fit(prompt, s.W-1))
			s.ResetAttr()
			s.ClrEol()
			k := s.waitKey()
			switch k {
			case 'r', 'R':
			case 'b', 'B':
				s.Baud = nextBaud(s.Baud)
				continue
			case 'm', 'M':
				if tunes > 0 {
					s.Music = nextMusicMode(s.Music)
				}
				continue
			case 'i', 'I':
				if hasSauce {
					s.showSauce(name, sauce)
					break
				}
				continue
			case kEnter, kEsc, 'q', 'Q', ' ':
				return
			default:
				continue
			}
			break
		}
		s.player.stopAll()
	}
}

// playANSI streams the segments; returns false if the user interrupted.
func (s *Session) playANSI(segs []segment) bool {
	for _, sg := range segs {
		if !sg.music {
			if !s.paced(sg.data, true) {
				return false
			}
			continue
		}
		if s.Local {
			if s.Music != "strip" && !s.playLocal(sg.body) {
				return false
			}
			continue
		}
		if seq := musicSequence(sg, s.Music); seq != nil {
			s.paced(seq, false)
		}
	}
	s.flush()
	return true
}

func (s *Session) showSauce(name string, si sauceInfo) {
	var b strings.Builder
	line := func(label, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%-8s %s\n", label+":", v)
		}
	}
	line("File", safeText(name))
	line("Title", si.title)
	line("Author", si.author)
	line("Group", si.group)
	line("Date", si.date)
	if si.width > 0 {
		line("Size", fmt.Sprintf("%d x %d", si.width, si.height))
	}
	line("Font", si.font)
	if si.ice {
		line("Colors", "iCE (high intensity backgrounds)")
	}
	s.message("SAUCE", strings.TrimRight(b.String(), "\n"))
}
