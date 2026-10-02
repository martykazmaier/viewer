package main

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func init() {
	zip.RegisterDecompressor(12, func(r io.Reader) io.ReadCloser { return io.NopCloser(bzip2.NewReader(r)) })
}

var zipMethods = map[uint16]string{
	0: "Stored", 1: "Shrunk", 2: "Reduced (1)", 3: "Reduced (2)", 4: "Reduced (3)",
	5: "Reduced (4)", 6: "Imploded", 7: "Tokenized", 8: "Deflated", 9: "Deflate64",
	10: "PKWARE DCL", 12: "BZip2", 14: "LZMA", 18: "IBM TERSE", 19: "IBM LZ77",
	93: "Zstandard", 94: "MP3", 95: "XZ", 96: "JPEG", 97: "WavPack", 98: "PPMd", 99: "AES",
}

var zipHosts = map[uint16]string{
	0: "MS-DOS / FAT", 1: "Amiga", 2: "OpenVMS", 3: "Unix", 4: "VM/CMS", 5: "Atari ST",
	6: "OS/2 HPFS", 7: "Macintosh", 8: "Z-System", 9: "CP/M", 10: "Windows NTFS",
	11: "MVS", 12: "VSE", 13: "Acorn RISC", 14: "VFAT", 15: "Alternate MVS", 16: "BeOS",
	17: "Tandem", 18: "OS/400", 19: "macOS",
}

// zipUnicodePath returns the UTF-8 name from an Info-ZIP Unicode Path extra field.
func zipUnicodePath(extra []byte) string {
	for len(extra) >= 4 {
		id, n := le16(extra), int(le16(extra[2:]))
		if 4+n > len(extra) {
			break
		}
		data := extra[4 : 4+n]
		if id == 0x7075 && n > 5 && data[0] == 1 {
			return string(data[5:])
		}
		extra = extra[4+n:]
	}
	return ""
}

func openZIP(r io.ReaderAt, size int64) (*Archive, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return nil, err
	}
	arc := &Archive{Format: "ZIP", Comment: textName(zr.Comment, false)}
	for _, f := range zr.File {
		utf := f.Flags&0x800 != 0
		name := f.Name
		if up := zipUnicodePath(f.Extra); up != "" {
			name, utf = up, true
		}
		e := &Entry{
			Name:      normPath(textName(name, utf)),
			Size:      int64(f.UncompressedSize64),
			Packed:    int64(f.CompressedSize64),
			SizeKnown: true,
			Modified:  f.Modified,
			CRC:       f.CRC32,
			HasCRC:    true,
			Comment:   textName(f.Comment, utf),
			Encrypted: f.Flags&1 != 0 || f.Method == 99,
		}
		e.IsDir = strings.HasSuffix(f.Name, "/") || strings.HasSuffix(f.Name, "\\")
		e.Method = zipMethods[f.Method]
		if e.Method == "" {
			e.Method = "Method " + strconv.Itoa(int(f.Method))
		}
		if f.Method == 8 {
			e.Method += [...]string{" (Normal)", " (Maximum)", " (Fast)", " (Super Fast)"}[(f.Flags>>1)&3]
		}
		host := f.CreatorVersion >> 8
		e.HostOS = zipHosts[host]
		if e.HostOS == "" {
			e.HostOS = "Unknown (" + strconv.Itoa(int(host)) + ")"
		}
		if (host == 3 || host == 19) && f.ExternalAttrs>>16 != 0 {
			e.Attr = unixModeString(f.ExternalAttrs >> 16)
		} else {
			e.Attr = dosAttrString(f.ExternalAttrs & 0xFF)
		}
		e.addInfo("Created by", fmt.Sprintf("%s, ZIP %d.%d", e.HostOS, (f.CreatorVersion&0xFF)/10, (f.CreatorVersion&0xFF)%10))
		e.addInfo("Needs", fmt.Sprintf("ZIP %d.%d to extract", f.ReaderVersion/10, f.ReaderVersion%10))
		if utf {
			e.addInfo("Name enc.", "UTF-8")
		}
		switch {
		case e.IsDir:
		case e.Encrypted:
			e.NoView = "File is encrypted."
		case f.Method == 0 || f.Method == 8 || f.Method == 12:
			zf := f
			e.Open = func() (io.Reader, error) { return zf.Open() }
		default:
			e.NoView = e.Method + " compression is not supported for viewing."
		}
		arc.Entries = append(arc.Entries, e)
	}
	return arc, nil
}

func isTar(head []byte) bool {
	if len(head) < 512 {
		return false
	}
	if string(head[257:262]) == "ustar" {
		return true
	}
	field := strings.TrimRight(strings.TrimSpace(string(cstr(head[148:156]))), " ")
	want, err := strconv.ParseInt(field, 8, 64)
	if err != nil || head[0] == 0 {
		return false
	}
	var sum int64
	for i, c := range head[:512] {
		if i >= 148 && i < 156 {
			c = ' '
		}
		sum += int64(c)
	}
	return sum == want
}

func openTar(r io.ReaderAt, size int64) (*Archive, error) {
	arc := &Archive{Format: "TAR"}
	tr := tar.NewReader(io.NewSectionReader(r, 0, size))
	for idx := 0; ; idx++ {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil && !errors.Is(err, tar.ErrInsecurePath) {
			if len(arc.Entries) > 0 {
				arc.Note = "Archive is truncated or damaged: " + err.Error()
				break
			}
			return nil, err
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		e := &Entry{
			Name:      normPath(textName(h.Name, false)),
			Size:      h.Size,
			Packed:    h.Size,
			SizeKnown: true,
			Modified:  h.ModTime,
			Method:    "Stored",
			Attr:      h.FileInfo().Mode().String(),
			IsDir:     h.Typeflag == tar.TypeDir,
		}
		if h.Uname != "" || h.Gname != "" {
			e.addInfo("Owner", fmt.Sprintf("%s:%s", safeText(h.Uname), safeText(h.Gname)))
		} else {
			e.addInfo("Owner", fmt.Sprintf("%d:%d", h.Uid, h.Gid))
		}
		if h.Linkname != "" {
			e.addInfo("Link to", textName(h.Linkname, false))
		}
		switch h.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			n := idx
			e.Open = func() (io.Reader, error) {
				tr2 := tar.NewReader(io.NewSectionReader(r, 0, size))
				for j := 0; j <= n; j++ {
					if _, err := tr2.Next(); err != nil && !errors.Is(err, tar.ErrInsecurePath) {
						return nil, err
					}
				}
				return tr2, nil
			}
		case tar.TypeDir:
		default:
			e.NoView = "Entry is not a regular file."
		}
		arc.Entries = append(arc.Entries, e)
	}
	return arc, nil
}
