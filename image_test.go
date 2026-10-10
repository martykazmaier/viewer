package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestImageFormatDetection(t *testing.T) {
	cases := map[string][]byte{
		"JPEG":    {0xFF, 0xD8, 0xFF, 0xE0},
		"PNG":     []byte("\x89PNG\r\n\x1a\n...."),
		"GIF":     []byte("GIF89a......"),
		"TIFF":    []byte("II*\x00...."),
		"WebP":    []byte("RIFF\x00\x00\x00\x00WEBPVP8 "),
		"JPEG XL": {0xFF, 0x0A, 0x00},
		"BMP":     append([]byte("BM"), append(make([]byte, 12), 40, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)...),
		"":        []byte("BMW owners club newsletter\r\n"),
	}
	for want, head := range cases {
		if got := imageFormat(head); got != want {
			t.Errorf("imageFormat(%q) = %q, want %q", head[:min(len(head), 8)], got, want)
		}
	}
}

func TestDecodeJXL(t *testing.T) {
	for _, f := range []string{"tiny2.jxl", "bbb-small.jxl", "upsampling.jxl"} {
		data, err := os.ReadFile("testdata/" + f)
		if err != nil {
			t.Fatal(err)
		}
		format := imageFormat(data)
		if format != "JPEG XL" {
			t.Fatalf("%s detected as %q", f, format)
		}
		start := time.Now()
		img, err := decodeImage(data, format)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		b := img.Bounds()
		t.Logf("%s: %dx%d decoded in %v", f, b.Dx(), b.Dy(), time.Since(start))
		if b.Dx() < 1 || b.Dy() < 1 {
			t.Errorf("%s: empty image", f)
		}
	}
	if _, err := decodeImage([]byte{0xFF, 0x0A, 1, 2, 3, 4, 5}, "JPEG XL"); err == nil {
		t.Error("damaged JPEG XL should fail cleanly")
	}
}

func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), uint8((x + y) * 7), 255})
		}
	}
	return img
}

func TestDecodePNG(t *testing.T) {
	var buf bytes.Buffer
	png.Encode(&buf, gradient(64, 48))
	img, err := decodeImage(buf.Bytes(), imageFormat(buf.Bytes()))
	if err != nil || img.Bounds().Dx() != 64 {
		t.Fatalf("png decode: %v", err)
	}
}

// decodeSixel is a minimal Sixel interpreter used to verify the encoder.
func decodeSixel(t *testing.T, data []byte) (map[int][3]int, [][]int) {
	t.Helper()
	if !bytes.HasPrefix(data, []byte("\x1bP0;1;0q")) || !bytes.HasSuffix(data, []byte("\x1b\\")) {
		t.Fatalf("missing DCS framing")
	}
	s := data[len("\x1bP0;1;0q") : len(data)-2]
	num := func(i int) (int, int) {
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		n, _ := strconv.Atoi(string(s[i:j]))
		return n, j
	}
	var w, h int
	if s[0] == '"' {
		var i int
		_, i = num(1)
		_, i = num(i + 1)
		w, i = num(i + 1)
		h, i = num(i + 1)
		s = s[i:]
	}
	pix := make([][]int, h)
	for y := range pix {
		pix[y] = make([]int, w)
		for x := range pix[y] {
			pix[y][x] = -1
		}
	}
	regs := map[int][3]int{}
	cur, x, band := 0, 0, 0
	put := func(c byte, n int) {
		bits := int(c) - 63
		for k := 0; k < n; k++ {
			for r := 0; r < 6; r++ {
				if bits&(1<<r) != 0 && band*6+r < h && x < w {
					pix[band*6+r][x] = cur
				}
			}
			x++
		}
	}
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == '#':
			var n int
			n, i = num(i + 1)
			if i < len(s) && s[i] == ';' {
				var r, g, b int
				_, i = num(i + 1)
				r, i = num(i + 1)
				g, i = num(i + 1)
				b, i = num(i + 1)
				regs[n] = [3]int{r, g, b}
			}
			cur = n
		case c == '!':
			var n int
			n, i = num(i + 1)
			put(s[i], n)
			i++
		case c == '$':
			x = 0
			i++
		case c == '-':
			x, band = 0, band+1
			i++
		case c >= 63 && c <= 126:
			put(c, 1)
			i++
		default:
			t.Fatalf("unexpected byte %q at %d", c, i)
		}
	}
	return regs, pix
}

func TestSixelRoundTrip(t *testing.T) {
	src := gradient(53, 29)
	p := dither(src, medianCut(src, 256))
	var all []byte
	chunks := sixelEncode(p)
	for _, c := range chunks {
		all = append(all, c...)
	}
	if len(chunks) != 2+(29+5)/6 {
		t.Errorf("got %d chunks, want header + %d bands + terminator", len(chunks), (29+5)/6)
	}
	regs, pix := decodeSixel(t, all)
	for y := 0; y < 29; y++ {
		for x := 0; x < 53; x++ {
			want := int(p.ColorIndexAt(x, y))
			if pix[y][x] != want {
				t.Fatalf("pixel %d,%d = register %d, want %d", x, y, pix[y][x], want)
			}
			r, g, b, _ := p.Palette[want].RGBA()
			if got := regs[want]; got != [3]int{int(r * 100 / 0xffff), int(g * 100 / 0xffff), int(b * 100 / 0xffff)} {
				t.Fatalf("register %d color %v", want, got)
			}
		}
	}
}

func TestSixelStrips(t *testing.T) {
	for _, c := range []struct{ h, ch, want int }{{378, 16, 48}, {378, 8, 48}, {600, 12, 60}, {90, 16, 48}, {378, 16, 378}} {
		if got := sixelStripHeight(c.h, c.ch, c.want != c.h); got != c.want {
			t.Errorf("sixelStripHeight(%d, %d) = %d, want %d", c.h, c.ch, got, c.want)
		}
	}
	src := gradient(53, 70)
	p := dither(src, medianCut(src, 256))
	const step = 24
	for y0 := 0; y0 < 70; y0 += step {
		var all []byte
		for _, c := range sixelEncode(p.SubImage(image.Rect(0, y0, 53, min(y0+step, 70))).(*image.Paletted)) {
			all = append(all, c...)
		}
		regs, pix := decodeSixel(t, all)
		for _, row := range pix {
			for _, ix := range row {
				if _, ok := regs[ix]; !ok {
					t.Fatalf("strip at %d uses undefined register %d", y0, ix)
				}
			}
		}
		if len(pix) != min(step, 70-y0) {
			t.Fatalf("strip at %d has %d rows", y0, len(pix))
		}
		for y := range pix {
			for x := 0; x < 53; x++ {
				if want := int(p.ColorIndexAt(x, y0+y)); pix[y][x] != want {
					t.Fatalf("strip %d pixel %d,%d = %d, want %d", y0, x, y, pix[y][x], want)
				}
			}
		}
	}
}

func TestFitSize(t *testing.T) {
	if w, h := fitSize(1920, 1080, 640, 378); w != 640 || h != 360 {
		t.Errorf("1920x1080 -> %dx%d", w, h)
	}
	if w, h := fitSize(100, 50, 640, 400); w != 200 || h != 100 {
		t.Errorf("small image should enlarge at most 2x, got %dx%d", w, h)
	}
}
