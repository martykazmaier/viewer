package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

const (
	longUTF8 = "Docs/Ünïcödé long file name for the BBS viewer – résumé.txt"
	payload  = "Hello from inside the archive!\r\nSecond line.\r\n"
)

func wantCP437() string { return utf8ToCP437(longUTF8) }

func le16b(v int) []byte { return binary.LittleEndian.AppendUint16(nil, uint16(v)) }
func le32b(v uint32) []byte {
	return binary.LittleEndian.AppendUint32(nil, v)
}

func crc16(b []byte) uint16 {
	var c uint16
	for _, x := range b {
		c ^= uint16(x)
		for i := 0; i < 8; i++ {
			if c&1 != 0 {
				c = c>>1 ^ 0xA001
			} else {
				c >>= 1
			}
		}
	}
	return c
}

func dosStamp(t time.Time) uint32 {
	d := uint32(t.Year()-1980)<<9 | uint32(t.Month())<<5 | uint32(t.Day())
	tm := uint32(t.Hour())<<11 | uint32(t.Minute())<<5 | uint32(t.Second()/2)
	return d<<16 | tm
}

var stamp = time.Date(1996, 7, 14, 21, 30, 10, 0, time.Local)

func buildARJ(name string) []byte {
	var out bytes.Buffer
	block := func(hdr []byte) {
		out.Write([]byte{0x60, 0xEA})
		out.Write(le16b(len(hdr)))
		out.Write(hdr)
		out.Write(le32b(crc32.ChecksumIEEE(hdr)))
		out.Write(le16b(0))
	}
	main := make([]byte, 30)
	main[0], main[1], main[2], main[6] = 30, 11, 1, 2
	copy(main[8:], le32b(dosStamp(stamp)))
	copy(main[12:], le32b(dosStamp(stamp)))
	main = append(main, "TEST.ARJ\x00archive comment\x00"...)
	block(main)

	fh := make([]byte, 30)
	fh[0], fh[1], fh[2], fh[3] = 30, 11, 1, 11
	copy(fh[8:], le32b(dosStamp(stamp)))
	copy(fh[12:], le32b(uint32(len(payload))))
	copy(fh[16:], le32b(uint32(len(payload))))
	copy(fh[20:], le32b(crc32.ChecksumIEEE([]byte(payload))))
	copy(fh[26:], le16b(0x20))
	fh = append(fh, name...)
	fh = append(fh, 0, 0)
	block(fh)
	out.WriteString(payload)
	out.Write([]byte{0x60, 0xEA, 0, 0})
	return out.Bytes()
}

func buildLZH2(dir, name string) []byte {
	var ext bytes.Buffer
	addExt := func(typ byte, data []byte) {
		ext.Write(le16b(len(data) + 3))
		ext.WriteByte(typ)
		ext.Write(data)
	}
	addExt(0x00, []byte{0, 0}) // header CRC, patched below
	addExt(0x01, []byte(name))
	addExt(0x02, []byte(strings.ReplaceAll(dir, "/", "\xff")))
	ext.Write(le16b(0))
	e := ext.Bytes()

	h := make([]byte, 24)
	copy(h[2:], "-lh0-")
	copy(h[7:], le32b(uint32(len(payload))))
	copy(h[11:], le32b(uint32(len(payload))))
	copy(h[15:], le32b(uint32(stamp.Unix())))
	h[19], h[20] = 0x20, 2
	copy(h[21:], le16b(int(crc16([]byte(payload)))))
	h[23] = 'W'
	hdr := append(h[:24], e...)
	total := len(hdr)
	if total&0xFF == 0 {
		hdr = append(hdr[:len(hdr)-2], 0, 0, 0)
		total++
	}
	copy(hdr[0:], le16b(total))
	c := crc16(hdr)
	copy(hdr[24+2+1:], le16b(int(c)))
	return append(append(hdr, payload...), 0)
}

func vintb(v uint64) []byte {
	var b []byte
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b = append(b, c|0x80)
		} else {
			return append(b, c)
		}
	}
}

func buildRAR5(name string) []byte {
	var out bytes.Buffer
	out.WriteString("Rar!\x1a\x07\x01\x00")
	header := func(body []byte) {
		sz := vintb(uint64(len(body)))
		out.Write(le32b(crc32.ChecksumIEEE(append(append([]byte{}, sz...), body...))))
		out.Write(sz)
		out.Write(body)
	}
	header([]byte{1, 0, 0})
	var f []byte
	f = append(f, 2, 2)
	f = append(f, vintb(uint64(len(payload)))...)
	f = append(f, 0x06)
	f = append(f, vintb(uint64(len(payload)))...)
	f = append(f, vintb(0x20)...)
	f = append(f, le32b(uint32(stamp.Unix()))...)
	f = append(f, le32b(crc32.ChecksumIEEE([]byte(payload)))...)
	f = append(f, 0, 0)
	f = append(f, vintb(uint64(len(name)))...)
	f = append(f, name...)
	header(f)
	out.WriteString(payload)
	header([]byte{5, 0, 0})
	return out.Bytes()
}

func buildRAR4(name string) []byte {
	var out bytes.Buffer
	out.WriteString("Rar!\x1a\x07\x00")
	block := func(b []byte) {
		c := crc32.ChecksumIEEE(b[2:]) & 0xFFFF
		copy(b, le16b(int(c)))
		out.Write(b)
	}
	block([]byte{0, 0, 0x73, 0, 0, 13, 0, 0, 0, 0, 0, 0, 0})

	u := utf16.Encode([]rune(name))
	ascii := []byte(strings.Map(func(r rune) rune {
		if r < 128 {
			return r
		}
		return '_'
	}, name))
	enc := []byte{0}
	for i := 0; i < len(u); i += 4 {
		enc = append(enc, 0xAA)
		for j := i; j < min(i+4, len(u)); j++ {
			enc = append(enc, byte(u[j]), byte(u[j]>>8))
		}
	}
	nm := append(append(ascii, 0), enc...)
	h := make([]byte, 32)
	h[2] = 0x74
	copy(h[3:], le16b(0x8000|0x200))
	copy(h[5:], le16b(32+len(nm)))
	copy(h[7:], le32b(uint32(len(payload))))
	copy(h[11:], le32b(uint32(len(payload))))
	h[15] = 2
	copy(h[16:], le32b(crc32.ChecksumIEEE([]byte(payload))))
	copy(h[20:], le32b(dosStamp(stamp)))
	h[24], h[25] = 29, 0x30
	copy(h[26:], le16b(len(nm)))
	copy(h[28:], le32b(0x20))
	block(append(h, nm...))
	out.WriteString(payload)
	block([]byte{0, 0, 0x7B, 0x00, 0x40, 7, 0})
	return out.Bytes()
}

func checkEntry(t *testing.T, arc *Archive, wantName string) {
	t.Helper()
	if len(arc.Entries) != 1 {
		t.Fatalf("%s: got %d entries, want 1 (note %q)", arc.Format, len(arc.Entries), arc.Note)
	}
	e := arc.Entries[0]
	if e.Name != wantName {
		t.Errorf("%s: name %q, want %q", arc.Format, e.Name, wantName)
	}
	if e.Size != int64(len(payload)) {
		t.Errorf("%s: size %d", arc.Format, e.Size)
	}
	if e.Modified.IsZero() {
		t.Errorf("%s: missing timestamp", arc.Format)
	}
	if e.Open == nil {
		t.Fatalf("%s: stored entry not viewable: %s", arc.Format, e.NoView)
	}
	rd, err := e.Open()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rd)
	if string(got) != payload {
		t.Errorf("%s: extracted %q", arc.Format, got)
	}
}

// sevenZipList cross-checks that 7-Zip accepts the synthetic archive.
func sevenZipList(t *testing.T, data []byte, ext, wantPath string) {
	t.Helper()
	exe := `C:\Program Files\7-Zip\7z.exe`
	if _, err := os.Stat(exe); err != nil {
		t.Log("7-Zip not installed; skipping cross-check")
		return
	}
	p := filepath.Join(t.TempDir(), "x"+ext)
	os.WriteFile(p, data, 0o644)
	out, err := exec.Command(exe, "l", "-slt", "-sccUTF-8", p).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Path = "+wantPath) {
		t.Errorf("7-Zip rejected %s archive (%v):\n%s", ext, err, out)
	}
	out, err = exec.Command(exe, "t", p).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Everything is Ok") {
		t.Errorf("7-Zip test failed for %s:\n%s", ext, out)
	}
}

func TestARJ(t *testing.T) {
	data := buildARJ("DOCS\\LONG FILE NAME.TXT")
	sevenZipList(t, data, ".arj", `DOCS\LONG FILE NAME.TXT`)
	arc, err := openArchive(data[:min(len(data), 512)], bytes.NewReader(data), int64(len(data)))
	if err != nil || arc == nil {
		t.Fatal(err)
	}
	checkEntry(t, arc, "DOCS/LONG FILE NAME.TXT")
	if arc.Comment != "archive comment" {
		t.Errorf("comment %q", arc.Comment)
	}
}

func TestLZH(t *testing.T) {
	data := buildLZH2("Some Folder/Sub Folder", "A really long LHA file name.txt")
	sevenZipList(t, data, ".lzh", `Some Folder\Sub Folder\A really long LHA file name.txt`)
	arc, err := openArchive(data[:min(len(data), 512)], bytes.NewReader(data), int64(len(data)))
	if err != nil || arc == nil {
		t.Fatal(err)
	}
	checkEntry(t, arc, "Some Folder/Sub Folder/A really long LHA file name.txt")
}

func TestRAR5(t *testing.T) {
	data := buildRAR5(longUTF8)
	sevenZipList(t, data, ".rar", strings.ReplaceAll(longUTF8, "/", `\`))
	arc, err := openArchive(data[:min(len(data), 512)], bytes.NewReader(data), int64(len(data)))
	if err != nil || arc == nil {
		t.Fatal(err)
	}
	checkEntry(t, arc, wantCP437())
	if !arc.Entries[0].HasCRC || arc.Entries[0].CRC != crc32.ChecksumIEEE([]byte(payload)) {
		t.Error("RAR5 CRC mismatch")
	}
}

func TestRAR4Unicode(t *testing.T) {
	name := strings.ReplaceAll(longUTF8, "/", `\`)
	data := buildRAR4(name)
	sevenZipList(t, data, ".rar", name)
	arc, err := openArchive(data[:min(len(data), 512)], bytes.NewReader(data), int64(len(data)))
	if err != nil || arc == nil {
		t.Fatal(err)
	}
	checkEntry(t, arc, wantCP437())
}

func TestZIP(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.CreateHeader(&zip.FileHeader{Name: longUTF8, Method: zip.Deflate, Modified: stamp})
	io.WriteString(w, payload)
	w, _ = zw.CreateHeader(&zip.FileHeader{Name: "OLD\\CAF\x82.TXT", Method: zip.Store, NonUTF8: true})
	io.WriteString(w, payload)
	zw.SetComment("ZIP comment")
	zw.Close()
	data := buf.Bytes()
	arc, err := openArchive(data[:512], bytes.NewReader(data), int64(len(data)))
	if err != nil || arc == nil {
		t.Fatal(err)
	}
	if len(arc.Entries) != 2 || arc.Entries[1].Name != "OLD/CAF\x82.TXT" {
		t.Fatalf("entries: %+v", arc.Entries)
	}
	arc.Entries = arc.Entries[:1]
	checkEntry(t, arc, wantCP437())
	if arc.Comment != "ZIP comment" {
		t.Errorf("comment %q", arc.Comment)
	}
}

func TestTarGz(t *testing.T) {
	long := strings.Repeat("deep folder/", 12) + "file with a very long name.ans"
	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	tw.WriteHeader(&tar.Header{Name: long, Mode: 0o644, Size: int64(len(payload)), ModTime: stamp, Format: tar.FormatPAX})
	io.WriteString(tw, payload)
	tw.Close()
	var gb bytes.Buffer
	gz := gzip.NewWriter(&gb)
	gz.Write(tb.Bytes())
	gz.Close()
	data := gb.Bytes()
	inner, innerName, ok, err := unwrapCompressed("pack.tgz", data[:10], bytes.NewReader(data), int64(len(data)))
	if !ok || err != nil || innerName != "pack.tar" {
		t.Fatalf("unwrap: %v %v %q", ok, err, innerName)
	}
	arc, err := openArchive(inner[:512], bytes.NewReader(inner), int64(len(inner)))
	if err != nil || arc == nil {
		t.Fatal(err)
	}
	checkEntry(t, arc, long)
}

func TestSplitANSIMusic(t *testing.T) {
	art := "\x1b[2J\x1b[1;33mHi\x1b[2M" +
		"\x1b[MFT120O4L8CDEFG\x0e" +
		"text\x1b[N T200 O3 C D E\x0e" +
		"\x1b[|MB L16 >C<C\x0e" +
		"\x1b[MHello world\x0e" +
		"\x1b[0m"
	var music []string
	for _, sg := range splitANSI([]byte(art)) {
		if sg.music {
			music = append(music, sg.body)
		}
	}
	want := []string{"MFT120O4L8CDEFG", " T200 O3 C D E", "MB L16 >C<C"}
	if strings.Join(music, "|") != strings.Join(want, "|") {
		t.Fatalf("music segments %q", music)
	}
	tones, bg := parseMML(want[0])
	if bg || len(tones) != 5 {
		t.Fatalf("tones %d bg %v", len(tones), bg)
	}
	if f := tones[0].freq; f < 523 || f > 524 {
		t.Errorf("O4 C = %.1f Hz, want ~523.3", f)
	}
	if _, bg := parseMML(want[2]); !bg {
		t.Error("MB should play in background")
	}
	wav, dur := synthWAV(tones)
	if len(wav) < 1000 || dur < 1200*time.Millisecond || dur > 1300*time.Millisecond {
		t.Errorf("wav %d bytes, %v", len(wav), dur)
	}
	conv := musicSequence(segment{music: true, body: want[0]}, "sync")
	if string(conv) != "\x1b[|MFT120O4L8CDEFG\x0e" {
		t.Errorf("sync conversion %q", conv)
	}
}
