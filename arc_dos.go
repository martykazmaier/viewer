package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

var arjHosts = []string{"MS-DOS", "PRIMOS", "Unix", "Amiga", "Mac OS", "OS/2", "Apple GS", "Atari ST", "NeXT", "VAX VMS", "Windows 95", "Win32"}

func openARJ(r io.ReaderAt, size int64) (*Archive, error) {
	arc := &Archive{Format: "ARJ"}
	pos := int64(0)
	first := true
	for pos+4 <= size {
		h, err := readAt(r, pos, 4)
		if err != nil || h[0] != 0x60 || h[1] != 0xEA {
			break
		}
		bsize := int64(le16(h[2:]))
		if bsize == 0 || bsize > 2600 {
			break
		}
		hdr, err := readAt(r, pos+4, int(bsize))
		if err != nil {
			break
		}
		p := pos + 4 + bsize + 4
		for i := 0; i < 64; i++ {
			x, err := readAt(r, p, 2)
			if err != nil {
				break
			}
			p += 2
			xs := int64(le16(x))
			if xs == 0 {
				break
			}
			p += xs + 4
		}
		fhs := int(hdr[0])
		if fhs < 30 || fhs > len(hdr) {
			break
		}
		name := cstr(hdr[fhs:])
		comment := ""
		if cs := fhs + len(name) + 1; cs < len(hdr) {
			comment = string(cstr(hdr[cs:]))
		}
		if first {
			first = false
			arc.Comment = comment
			pos = p
			continue
		}
		host := int(hdr[3])
		flags, method, ftype := hdr[4], int(hdr[5]), hdr[6]
		comp := int64(le32(hdr[12:]))
		e := &Entry{
			Name:      normPath(string(name)),
			Size:      int64(le32(hdr[16:])),
			Packed:    comp,
			SizeKnown: true,
			Modified:  dosTime(le32(hdr[8:])),
			CRC:       le32(hdr[20:]),
			HasCRC:    true,
			Attr:      dosAttrString(uint32(le16(hdr[26:]))),
			IsDir:     ftype == 3,
			Encrypted: flags&0x01 != 0,
			Comment:   comment,
		}
		if host < len(arjHosts) {
			e.HostOS = arjHosts[host]
		} else {
			e.HostOS = "Unknown (" + strconv.Itoa(host) + ")"
		}
		if host == 2 {
			e.Attr = unixModeString(uint32(le16(hdr[26:])))
		}
		switch method {
		case 0:
			e.Method = "Stored"
		case 1, 2, 3:
			e.Method = "Method " + strconv.Itoa(method)
		case 4:
			e.Method = "Fastest"
		default:
			e.Method = "Unknown (" + strconv.Itoa(method) + ")"
		}
		e.addInfo("File type", map[byte]string{0: "Binary", 1: "7-bit text", 3: "Directory", 4: "Volume label"}[ftype])
		e.addInfo("Created by", fmt.Sprintf("ARJ version %d", hdr[1]))
		switch {
		case e.IsDir || ftype == 4:
		case flags&0x04 != 0:
			e.NoView = "File continues in another volume."
		case e.Encrypted:
			e.NoView = "File is garbled (password protected)."
		case method != 0:
			e.NoView = "ARJ compression is not supported for viewing (stored files only)."
		default:
			e.Open = storedOpener(r, p, comp)
		}
		arc.Entries = append(arc.Entries, e)
		pos = p + comp
	}
	return arc, nil
}

func isLZH(h []byte) bool {
	return len(h) >= 22 && h[2] == '-' && h[6] == '-' && (h[3] == 'l' || h[3] == 'p') && h[20] <= 2
}

var lzhMethods = map[string]string{
	"-lh0-": "Stored", "-lz4-": "Stored", "-pm0-": "Stored", "-lhd-": "Directory",
	"-lh1-": "LH1 (4K)", "-lh2-": "LH2 (8K)", "-lh3-": "LH3 (8K)", "-lh4-": "LH4 (4K)",
	"-lh5-": "LH5 (8K)", "-lh6-": "LH6 (32K)", "-lh7-": "LH7 (64K)", "-lzs-": "LArc LZS",
	"-lz5-": "LArc LZ5", "-pm1-": "PMarc 1", "-pm2-": "PMarc 2",
}

var lzhHosts = map[byte]string{
	'M': "MS-DOS", 'U': "Unix", 'w': "Windows 95", 'W': "Windows NT", '2': "OS/2",
	'm': "Macintosh", 'J': "Java", 'K': "OS-9/68K", 'A': "Amiga", 'F': "FLEX",
	'H': "Human68K", 'C': "CP/M", 'T': "TownsOS", 'X': "XOSK", 'R': "RUNser", 0: "Generic",
}

func lzhName(b []byte) string {
	out := []byte(string(b))
	for i, c := range out {
		if c == 0xFF {
			out[i] = '/'
		}
	}
	return string(out)
}

func openLZH(r io.ReaderAt, size int64) (*Archive, error) {
	arc := &Archive{Format: "LHA"}
	pos := int64(0)
	for pos < size {
		b, err := readAt(r, pos, 26)
		if len(b) < 1 || b[0] == 0 {
			break
		}
		if err != nil && len(b) < 22 {
			break
		}
		level := b[20]
		method := string(b[2:7])
		packed := int64(le32(b[7:]))
		e := &Entry{Size: int64(le32(b[11:])), SizeKnown: true, Attr: dosAttrString(uint32(b[19]))}
		var name, dir string
		var dataStart, extStart int64
		var firstExt int
		switch level {
		case 0, 1:
			hsize := int64(b[0])
			hdr, err := readAt(r, pos, int(hsize)+2)
			if err != nil || len(hdr) < 24 {
				return arc, nil
			}
			e.Modified = dosTime(le32(hdr[15:]))
			nl := int(hdr[21])
			if 22+nl > len(hdr) {
				return arc, nil
			}
			name = lzhName(hdr[22 : 22+nl])
			dataStart = pos + hsize + 2
			if level == 1 && 22+nl+3 <= len(hdr) {
				e.HostOS = lzhHosts[hdr[22+nl+2]]
				firstExt = int(le16(hdr[hsize:]))
				extStart = dataStart
			}
		case 2:
			total := int64(le16(b[0:]))
			if len(b) < 26 || total < 26 {
				return arc, nil
			}
			e.Modified = unixTime(int64(le32(b[15:])))
			e.HostOS = lzhHosts[b[23]]
			firstExt = int(le16(b[24:]))
			extStart = pos + 26
			dataStart = pos + total
		}
		var extTotal int64
		for xs, off, n := firstExt, extStart, 0; xs >= 3 && n < 64; n++ {
			blk, err := readAt(r, off, xs)
			if err != nil {
				break
			}
			data := blk[1 : xs-2]
			switch blk[0] {
			case 0x01:
				name = lzhName(data)
			case 0x02:
				dir = lzhName(data)
			case 0x3F:
				e.Comment = string(cstr(data))
			case 0x40:
				if len(data) >= 2 {
					e.Attr = dosAttrString(uint32(le16(data)))
				}
			case 0x41:
				if len(data) >= 16 {
					e.Modified = fileTime(le64(data[8:]))
				}
			case 0x50:
				if len(data) >= 2 {
					e.Attr = unixModeString(uint32(le16(data)))
				}
			case 0x54:
				if len(data) >= 4 {
					e.Modified = unixTime(int64(le32(data)))
				}
			}
			off += int64(xs)
			extTotal += int64(xs)
			xs = int(le16(blk[xs-2:]))
		}
		if level == 1 {
			dataStart += extTotal
			packed -= extTotal
		}
		if level > 2 || packed < 0 {
			arc.Note = "Unsupported LHA header level."
			break
		}
		if dir != "" && !strings.HasSuffix(dir, "/") {
			dir += "/"
		}
		e.Name = normPath(dir + name)
		e.Packed = packed
		e.IsDir = method == "-lhd-"
		e.Method = lzhMethods[method]
		if e.Method == "" {
			e.Method = safeText(method)
		}
		if e.HostOS == "" {
			e.HostOS = "MS-DOS"
		}
		e.addInfo("Header", fmt.Sprintf("Level %d, %s", level, safeText(method)))
		switch {
		case e.IsDir:
		case method == "-lh0-" || method == "-lz4-" || method == "-pm0-":
			e.Open = storedOpener(r, dataStart, packed)
		default:
			e.NoView = "LHA compression is not supported for viewing (stored files only)."
		}
		arc.Entries = append(arc.Entries, e)
		pos = dataStart + packed
	}
	return arc, nil
}
