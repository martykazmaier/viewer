package main

import (
	"fmt"
	"io"
	"strconv"
	"unicode/utf16"
)

var rarHosts = []string{"MS-DOS", "OS/2", "Windows", "Unix", "Mac OS", "BeOS"}

func rarHost(h int) string {
	if h >= 0 && h < len(rarHosts) {
		return rarHosts[h]
	}
	return "Unknown (" + strconv.Itoa(h) + ")"
}

var rarMethods = []string{"Stored", "Fastest", "Fast", "Normal", "Good", "Best"}

func rarMethod(m int) string {
	if m >= 0 && m < len(rarMethods) {
		return rarMethods[m]
	}
	return "Method " + strconv.Itoa(m)
}

// decodeRAR4Unicode expands the compressed UTF-16 name used by RAR 3.x.
func decodeRAR4Unicode(ascii, enc []byte) string {
	if len(enc) == 0 {
		return string(ascii)
	}
	var out []uint16
	pos := 0
	high := uint16(enc[pos])
	pos++
	var flags byte
	bits := 0
	for pos < len(enc) && len(out) < 1024 {
		if bits == 0 {
			flags = enc[pos]
			pos++
			bits = 8
		}
		switch flags >> 6 {
		case 0:
			if pos >= len(enc) {
				break
			}
			out = append(out, uint16(enc[pos]))
			pos++
		case 1:
			if pos >= len(enc) {
				break
			}
			out = append(out, uint16(enc[pos])|high<<8)
			pos++
		case 2:
			if pos+1 >= len(enc) {
				pos = len(enc)
				break
			}
			out = append(out, uint16(enc[pos])|uint16(enc[pos+1])<<8)
			pos += 2
		case 3:
			if pos >= len(enc) {
				break
			}
			n := int(enc[pos])
			pos++
			if n&0x80 != 0 {
				if pos >= len(enc) {
					break
				}
				corr := enc[pos]
				pos++
				for n = n&0x7f + 2; n > 0; n-- {
					d := len(out)
					if d >= len(ascii) {
						break
					}
					out = append(out, uint16(ascii[d]+corr)|high<<8)
				}
			} else {
				for n += 2; n > 0; n-- {
					d := len(out)
					if d >= len(ascii) {
						break
					}
					out = append(out, uint16(ascii[d]))
				}
			}
		}
		flags <<= 2
		bits -= 2
	}
	return string(utf16.Decode(out))
}

func openRAR4(r io.ReaderAt, size int64) (*Archive, error) {
	arc := &Archive{Format: "RAR 4"}
	pos := int64(7)
	for pos+7 <= size {
		base, err := readAt(r, pos, 7)
		if err != nil {
			break
		}
		typ, flags, hsize := base[2], le16(base[3:]), int64(le16(base[5:]))
		if hsize < 7 {
			break
		}
		hdr, err := readAt(r, pos, int(hsize))
		if err != nil {
			break
		}
		var add int64
		if flags&0x8000 != 0 && hsize >= 11 {
			add = int64(le32(hdr[7:]))
		}
		switch typ {
		case 0x73:
			if flags&0x80 != 0 {
				arc.Note = "File headers are encrypted; the list needs the password."
				return arc, nil
			}
			if flags&0x08 != 0 {
				arc.Format += " (solid)"
			}
		case 0x74:
			if hsize < 32 {
				return arc, nil
			}
			e, packed := parseRAR4File(hdr, flags)
			add = packed
			if !e.IsDir && e.NoView == "" {
				e.Open = storedOpener(r, pos+hsize, packed)
			}
			arc.Entries = append(arc.Entries, e)
		case 0x7B:
			return arc, nil
		}
		next := pos + hsize + add
		if next <= pos {
			break
		}
		pos = next
	}
	return arc, nil
}

func parseRAR4File(hdr []byte, flags uint16) (*Entry, int64) {
	packed := int64(le32(hdr[7:]))
	unp := int64(le32(hdr[11:]))
	host := int(hdr[15])
	method := int(hdr[25]) - 0x30
	nameSize := int(le16(hdr[26:]))
	attr := le32(hdr[28:])
	off := 32
	if flags&0x100 != 0 && len(hdr) >= 40 {
		packed |= int64(le32(hdr[32:])) << 32
		unp |= int64(le32(hdr[36:])) << 32
		off = 40
	}
	raw := hdr[off:min(off+nameSize, len(hdr))]
	name := string(raw)
	if flags&0x200 != 0 {
		if z := indexByte(raw, 0); z >= 0 {
			name = utf8ToCP437(decodeRAR4Unicode(raw[:z], raw[z+1:]))
		} else {
			name = textName(name, true)
		}
	}
	e := &Entry{
		Name:      normPath(name),
		Size:      unp,
		Packed:    packed,
		SizeKnown: true,
		Modified:  dosTime(le32(hdr[20:])),
		CRC:       le32(hdr[16:]),
		HasCRC:    true,
		Method:    rarMethod(method),
		HostOS:    rarHost(host),
		IsDir:     flags&0xE0 == 0xE0,
		Encrypted: flags&0x04 != 0,
	}
	if host == 3 {
		e.Attr = unixModeString(attr)
	} else {
		e.Attr = dosAttrString(attr)
	}
	if !e.IsDir {
		e.addInfo("Dictionary", fmt.Sprintf("%d KB", 64<<((flags>>5)&7)))
	}
	e.addInfo("Needs", fmt.Sprintf("RAR %d.%d to extract", hdr[24]/10, hdr[24]%10))
	if flags&0x10 != 0 {
		e.addInfo("Solid", "Yes")
	}
	switch {
	case flags&0x03 != 0:
		e.addInfo("Volume", "Split across volumes")
		e.NoView = "File is split across volumes."
	case e.Encrypted:
		e.NoView = "File is encrypted."
	case method != 0:
		e.NoView = "RAR compression is not supported for viewing (stored files only)."
	}
	return e, packed
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

func vint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * uint(i))
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}

// vreader walks a RAR5 header buffer.
type vreader struct {
	b   []byte
	p   int
	bad bool
}

func (v *vreader) next() uint64 {
	if v.bad {
		return 0
	}
	x, n := vint(v.b[v.p:])
	if n == 0 {
		v.bad = true
		return 0
	}
	v.p += n
	return x
}

func (v *vreader) u32() uint32 {
	if v.bad || v.p+4 > len(v.b) {
		v.bad = true
		return 0
	}
	x := le32(v.b[v.p:])
	v.p += 4
	return x
}

func (v *vreader) bytes(n int) []byte {
	if v.bad || n < 0 || v.p+n > len(v.b) {
		v.bad = true
		return nil
	}
	x := v.b[v.p : v.p+n]
	v.p += n
	return x
}

func openRAR5(r io.ReaderAt, size int64) (*Archive, error) {
	arc := &Archive{Format: "RAR 5"}
	pos := int64(8)
	for pos+5 < size {
		pre, _ := readAt(r, pos, 7)
		if len(pre) < 5 {
			break
		}
		hsize, n := vint(pre[4:])
		if n == 0 || hsize == 0 || hsize > 2<<20 {
			break
		}
		hdrStart := pos + 4 + int64(n)
		hdr, err := readAt(r, hdrStart, int(hsize))
		if err != nil {
			break
		}
		v := &vreader{b: hdr}
		typ := v.next()
		hflags := v.next()
		var extraSize, dataSize uint64
		if hflags&1 != 0 {
			extraSize = v.next()
		}
		if hflags&2 != 0 {
			dataSize = v.next()
		}
		if v.bad {
			break
		}
		switch typ {
		case 1:
			if v.next()&4 != 0 {
				arc.Format += " (solid)"
			}
		case 2:
			e := parseRAR5File(v, hflags, extraSize, dataSize)
			if e == nil {
				return arc, nil
			}
			if !e.IsDir && e.NoView == "" {
				e.Open = storedOpener(r, hdrStart+int64(hsize), int64(dataSize))
			}
			arc.Entries = append(arc.Entries, e)
		case 4:
			arc.Note = "File headers are encrypted; the list needs the password."
			return arc, nil
		case 5:
			return arc, nil
		}
		next := hdrStart + int64(hsize) + int64(dataSize)
		if next <= pos {
			break
		}
		pos = next
	}
	return arc, nil
}

func parseRAR5File(v *vreader, hflags, extraSize, dataSize uint64) *Entry {
	ff := v.next()
	unp := v.next()
	attr := v.next()
	e := &Entry{Packed: int64(dataSize), Size: int64(unp), SizeKnown: ff&8 == 0}
	if ff&2 != 0 {
		e.Modified = unixTime(int64(v.u32()))
	}
	if ff&4 != 0 {
		e.CRC, e.HasCRC = v.u32(), true
	}
	comp := v.next()
	host := v.next()
	name := v.bytes(int(v.next()))
	if v.bad {
		return nil
	}
	e.Name = normPath(textName(string(name), true))
	e.IsDir = ff&1 != 0
	method := int(comp>>7) & 7
	e.Method = rarMethod(method)
	switch host {
	case 0:
		e.HostOS = "Windows"
		e.Attr = dosAttrString(uint32(attr))
	case 1:
		e.HostOS = "Unix"
		e.Attr = unixModeString(uint32(attr))
	default:
		e.HostOS = "Unknown (" + strconv.FormatUint(host, 10) + ")"
	}
	if !e.IsDir {
		e.addInfo("Dictionary", fmt.Sprintf("%d KB", 128<<((comp>>10)&15)))
	}
	e.addInfo("Format", fmt.Sprintf("RAR %d algorithm", 5+2*(comp&0x3F)))
	if comp&0x40 != 0 {
		e.addInfo("Solid", "Yes")
	}

	if extraSize > 0 && int(extraSize) <= len(v.b) {
		ex := &vreader{b: v.b[len(v.b)-int(extraSize):]}
		for ex.p < len(ex.b) && !ex.bad {
			rsize := int(ex.next())
			start := ex.p
			if ex.bad || rsize <= 0 || start+rsize > len(ex.b) {
				break
			}
			rec := &vreader{b: ex.b[start : start+rsize]}
			switch rec.next() {
			case 1:
				e.Encrypted = true
			case 3:
				tf := rec.next()
				if tf&2 != 0 {
					if tf&1 != 0 {
						e.Modified = unixTime(int64(rec.u32()))
					} else if b := rec.bytes(8); b != nil {
						e.Modified = fileTime(le64(b))
					}
				}
			case 5:
				e.addInfo("Link", "Symbolic link or junction")
			}
			ex.p = start + rsize
		}
	}
	switch {
	case hflags&0x18 != 0:
		e.addInfo("Volume", "Split across volumes")
		e.NoView = "File is split across volumes."
	case e.Encrypted:
		e.NoView = "File is encrypted."
	case method != 0:
		e.NoView = "RAR compression is not supported for viewing (stored files only)."
	}
	return e
}
