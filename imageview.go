package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"sort"
	"strings"
	"time"

	jxl "github.com/kpfaulkner/jxl-go"
	"github.com/sirupsen/logrus"
	_ "golang.org/x/image/bmp"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

func init() {
	logrus.SetOutput(io.Discard)
}

const maxImagePixels = 80_000_000

// imageFormat identifies a supported graphics file from its first bytes.
func imageFormat(h []byte) string {
	switch {
	case bytes.HasPrefix(h, []byte{0xFF, 0xD8, 0xFF}):
		return "JPEG"
	case bytes.HasPrefix(h, []byte("\x89PNG\r\n\x1a\n")):
		return "PNG"
	case bytes.HasPrefix(h, []byte("GIF87a")), bytes.HasPrefix(h, []byte("GIF89a")):
		return "GIF"
	case bytes.HasPrefix(h, []byte("II*\x00")), bytes.HasPrefix(h, []byte("MM\x00*")):
		return "TIFF"
	case len(h) >= 12 && string(h[:4]) == "RIFF" && string(h[8:12]) == "WEBP":
		return "WebP"
	case bytes.HasPrefix(h, []byte{0xFF, 0x0A}), bytes.HasPrefix(h, []byte("\x00\x00\x00\x0cJXL \r\n\x87\n")):
		return "JPEG XL"
	case len(h) >= 26 && string(h[:2]) == "BM":
		switch le32(h[14:]) {
		case 12, 40, 52, 56, 64, 108, 124:
			return "BMP"
		}
	}
	return ""
}

// decodeImage decodes any supported format, guarding against oversized or
// malformed files that would otherwise panic inside a decoder.
func decodeImage(data []byte, format string) (img image.Image, err error) {
	defer func() {
		if r := recover(); r != nil {
			img, err = nil, fmt.Errorf("the %s data is damaged or uses unsupported features", format)
		}
	}()
	tooBig := func(w, h int) error {
		if w <= 0 || h <= 0 || w*h > maxImagePixels {
			return fmt.Errorf("image is too large to view (%dx%d)", w, h)
		}
		return nil
	}
	if format == "JPEG XL" {
		// DecodeConfig cannot read multi-box containers, so only use it when it works.
		if cfg, err := jxl.DecodeConfig(bytes.NewReader(data)); err == nil {
			if err := tooBig(cfg.Width, cfg.Height); err != nil {
				return nil, err
			}
		}
		if img, err = jxl.Decode(bytes.NewReader(data)); err != nil {
			return nil, err
		}
		return img, tooBig(img.Bounds().Dx(), img.Bounds().Dy())
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if err := tooBig(cfg.Width, cfg.Height); err != nil {
		return nil, err
	}
	img, _, err = image.Decode(bytes.NewReader(data))
	return img, err
}

// fitSize scales w x h to fit inside bw x bh, enlarging small images up to 2x.
func fitSize(w, h, bw, bh int) (int, int) {
	scale := min(float64(bw)/float64(w), float64(bh)/float64(h), 2)
	return max(int(float64(w)*scale+0.5), 1), max(int(float64(h)*scale+0.5), 1)
}

// scaleImage resizes img onto a black background, flattening transparency.
func scaleImage(img image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.Black, image.Point{}, draw.Src)
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), xdraw.Over, nil)
	return dst
}

// medianCut builds an adaptive palette of up to n colors.
func medianCut(img *image.RGBA, n int) color.Palette {
	b := img.Bounds()
	step := max(b.Dx()*b.Dy()/65536, 1)
	var pts [][3]uint8
	for i := 0; i < b.Dx()*b.Dy(); i += step {
		o := (i/b.Dx())*img.Stride + (i%b.Dx())*4
		pts = append(pts, [3]uint8{img.Pix[o], img.Pix[o+1], img.Pix[o+2]})
	}
	boxes := [][][3]uint8{pts}
	spread := func(p [][3]uint8) (int, int) {
		best, ch := -1, 0
		for c := 0; c < 3; c++ {
			lo, hi := 255, 0
			for _, v := range p {
				lo, hi = min(lo, int(v[c])), max(hi, int(v[c]))
			}
			if hi-lo > best {
				best, ch = hi-lo, c
			}
		}
		return best, ch
	}
	for len(boxes) < n {
		pick, pickCh, score := -1, 0, 0
		for i, bx := range boxes {
			if len(bx) < 2 {
				continue
			}
			sp, ch := spread(bx)
			if s := sp * len(bx); sp > 0 && s > score {
				pick, pickCh, score = i, ch, s
			}
		}
		if pick < 0 {
			break
		}
		bx := boxes[pick]
		sort.Slice(bx, func(i, j int) bool { return bx[i][pickCh] < bx[j][pickCh] })
		mid := len(bx) / 2
		boxes[pick] = bx[:mid]
		boxes = append(boxes, bx[mid:])
	}
	pal := make(color.Palette, 0, len(boxes))
	for _, bx := range boxes {
		var r, g, bl int
		for _, v := range bx {
			r, g, bl = r+int(v[0]), g+int(v[1]), bl+int(v[2])
		}
		k := max(len(bx), 1)
		pal = append(pal, color.RGBA{uint8(r / k), uint8(g / k), uint8(bl / k), 255})
	}
	return pal
}

func dither(img *image.RGBA, pal color.Palette) *image.Paletted {
	p := image.NewPaletted(img.Bounds(), pal)
	draw.FloydSteinberg.Draw(p, p.Bounds(), img, image.Point{})
	return p
}

// sixelEncode returns the DCS header, one chunk per six-pixel band, and the
// string terminator, so output can be paced and interrupted between bands.
// p may be a sub-image; only the colors it uses are defined.
func sixelEncode(p *image.Paletted) [][]byte {
	w, h := p.Rect.Dx(), p.Rect.Dy()
	row := func(y int) []uint8 {
		o := p.PixOffset(p.Rect.Min.X, p.Rect.Min.Y+y)
		return p.Pix[o : o+w]
	}
	used := make([]bool, len(p.Palette))
	for y := 0; y < h; y++ {
		for _, ix := range row(y) {
			used[ix] = true
		}
	}
	var hdr bytes.Buffer
	fmt.Fprintf(&hdr, "\x1bP0;1;0q\"1;1;%d;%d", w, h)
	for i, c := range p.Palette {
		if used[i] {
			r, g, b, _ := c.RGBA()
			fmt.Fprintf(&hdr, "#%d;2;%d;%d;%d", i, r*100/0xffff, g*100/0xffff, b*100/0xffff)
		}
	}
	chunks := [][]byte{hdr.Bytes()}
	bits := make([]byte, w)
	for y0 := 0; y0 < h; y0 += 6 {
		rows := min(6, h-y0)
		var seen [256]bool
		var order []uint8
		for r := 0; r < rows; r++ {
			for _, ix := range row(y0 + r) {
				if !seen[ix] {
					seen[ix] = true
					order = append(order, ix)
				}
			}
		}
		var band bytes.Buffer
		var band6 [6][]uint8
		for r := 0; r < rows; r++ {
			band6[r] = row(y0 + r)
		}
		for _, c := range order {
			last := -1
			for x := 0; x < w; x++ {
				var v byte
				for r := 0; r < rows; r++ {
					if band6[r][x] == c {
						v |= 1 << r
					}
				}
				bits[x] = v
				if v != 0 {
					last = x
				}
			}
			fmt.Fprintf(&band, "#%d", c)
			for x := 0; x <= last; {
				run := 1
				for x+run <= last && bits[x+run] == bits[x] {
					run++
				}
				ch := 63 + bits[x]
				if run > 3 {
					fmt.Fprintf(&band, "!%d%c", run, ch)
				} else {
					band.Write(bytes.Repeat([]byte{ch}, run))
				}
				x += run
			}
			band.WriteByte('$')
		}
		if y0+6 < h {
			band.WriteByte('-')
		}
		chunks = append(chunks, band.Bytes())
	}
	return append(chunks, []byte("\x1b\\"))
}

var cgaPalette = color.Palette{
	color.RGBA{0x00, 0x00, 0x00, 255}, color.RGBA{0x00, 0x00, 0xAA, 255},
	color.RGBA{0x00, 0xAA, 0x00, 255}, color.RGBA{0x00, 0xAA, 0xAA, 255},
	color.RGBA{0xAA, 0x00, 0x00, 255}, color.RGBA{0xAA, 0x00, 0xAA, 255},
	color.RGBA{0xAA, 0x55, 0x00, 255}, color.RGBA{0xAA, 0xAA, 0xAA, 255},
	color.RGBA{0x55, 0x55, 0x55, 255}, color.RGBA{0x55, 0x55, 0xFF, 255},
	color.RGBA{0x55, 0xFF, 0x55, 255}, color.RGBA{0x55, 0xFF, 0xFF, 255},
	color.RGBA{0xFF, 0x55, 0x55, 255}, color.RGBA{0xFF, 0x55, 0xFF, 255},
	color.RGBA{0xFF, 0xFF, 0x55, 255}, color.RGBA{0xFF, 0xFF, 0xFF, 255},
}

var bayer4 = [4][4]int{{0, 8, 2, 10}, {12, 4, 14, 6}, {3, 11, 1, 9}, {15, 7, 13, 5}}

// orderedCGA maps img to the 16 ANSI colors with 4x4 ordered dithering and a
// perceptual ("redmean") color distance. At block resolution error diffusion
// turns into speckle, while an ordered pattern reads like hand-made ANSI art.
func orderedCGA(img *image.RGBA) *image.Paletted {
	b := img.Bounds()
	p := image.NewPaletted(b, cgaPalette)
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			o := y*img.Stride + x*4
			bias := (bayer4[y&3][x&3] - 8) * 5
			var c [3]int
			for i := range c {
				c[i] = min(max(int(img.Pix[o+i])+bias, 0), 255)
			}
			best, bestD := 0, 1<<30
			for i, pc := range cgaPalette {
				q := pc.(color.RGBA)
				rm := (c[0] + int(q.R)) / 2
				dr, dg, db := c[0]-int(q.R), c[1]-int(q.G), c[2]-int(q.B)
				d := (512+rm)*dr*dr>>8 + 4*dg*dg + (767-rm)*db*db>>8
				if d < bestD {
					best, bestD = i, d
				}
			}
			p.Pix[y*p.Stride+x] = uint8(best)
		}
	}
	return p
}

// pixelArea returns the drawable pixel size above the status line and the
// character cell size.
func (s *Session) pixelArea() (w, h, cw, ch int) {
	cw, ch = 8, 16
	switch {
	case s.pxW > 0 && s.pxH > 0:
		cw, ch = s.pxW/s.W, s.pxH/s.H
	case s.cellW > 0 && s.cellH > 0:
		cw, ch = s.cellW, s.cellH
	}
	cw, ch = min(max(cw, 4), 32), min(max(ch, 6), 64)
	return s.W * cw, (s.H-1)*ch - 6, cw, ch
}

func (s *Session) cellKnown() bool {
	return s.pxW > 0 && s.pxH > 0 || s.cellW > 0 && s.cellH > 0
}

// sixelStripHeight splits an image of height h into roughly ten strips, each
// a whole number of six-pixel bands and character rows, so every strip can
// start at a cursor position. Without a known cell size it can't be aligned,
// so the image is sent in one piece.
func sixelStripHeight(h, ch int, known bool) int {
	if !known {
		return h
	}
	a, b := 6, ch
	for b != 0 {
		a, b = b, a%b
	}
	l := 6 * ch / a
	return l * max(1, (h/10+l-1)/l)
}

// busy runs fn in the background, animating the status line until it
// finishes. Returns false if the user cancelled or fn panicked.
func (s *Session) busy(label string, fn func()) bool {
	done := make(chan bool, 1)
	go func() {
		defer func() { done <- recover() == nil }()
		fn()
	}()
	start := time.Now()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for i := 0; ; i++ {
		sec := int(time.Since(start).Seconds())
		s.Bar(s.H, fmt.Sprintf(" %s %c  %d:%02d", label, `|/-\`[i%4], sec/60, sec%60), "Esc=Cancel ", 15, 1)
		s.flush()
		select {
		case ok := <-done:
			return ok
		case <-tick.C:
		}
		for s.keyAvailable() {
			switch k := s.getKey(0); {
			case k == kEsc && time.Now().Before(s.escGuard):
			case k == kEsc, k == 'q', k == 'Q':
				return false
			}
		}
	}
}

// progress shows a transfer bar with bytes sent and time remaining on the
// status line.
type progress struct {
	s           *Session
	label       string
	done, total int
	start, last time.Time
}

func (s *Session) newProgress(label string, total int) *progress {
	return &progress{s: s, label: label, total: max(total, 1), start: time.Now()}
}

func (p *progress) add(n int) { p.done = min(p.done+n, p.total) }

// show redraws the bar; unless forced, at most a few times per second.
func (p *progress) show(force bool) {
	if !force && time.Since(p.last) < 300*time.Millisecond {
		return
	}
	p.last = time.Now()
	const n = 20
	fill := p.done * n / p.total
	bar := strings.Repeat("\xDB", fill) + strings.Repeat("\xB0", n-fill)
	kb := func(n int) string { return fmtNum(int64(n+1023)/1024) + "K" }
	left := fmt.Sprintf(" %s %3d%% %s %s of %s", p.label, p.done*100/p.total, bar, kb(p.done), kb(p.total))
	eta := -1
	if p.s.Baud > 0 {
		eta = (p.total - p.done) * 10 / p.s.Baud
	} else if el := time.Since(p.start); el > time.Second && p.done > 0 {
		eta = int(el.Seconds() * float64(p.total-p.done) / float64(p.done))
	}
	right := "Any key stops "
	if eta >= 0 {
		right = fmt.Sprintf("%d:%02d left  %s", eta/60, eta%60, right)
	}
	p.s.Bar(p.s.H, left, right, 15, 1)
	p.s.flush()
}

// drawSixel renders img as Sixel graphics; returns false if interrupted.
func (s *Session) drawSixel(img image.Image) (bool, string) {
	bw, bh, cw, ch := s.pixelArea()
	b := img.Bounds()
	w, h := fitSize(b.Dx(), b.Dy(), bw, bh)
	step := sixelStripHeight(h, ch, s.cellKnown())
	var strips [][][]byte
	if !s.busy("Preparing image", func() {
		sc := scaleImage(img, w, h)
		p := dither(sc, medianCut(sc, 256))
		for y := 0; y < h; y += step {
			strips = append(strips, sixelEncode(p.SubImage(image.Rect(0, y, w, min(y+step, h))).(*image.Paletted)))
		}
	}) {
		return false, ""
	}
	total := 0
	for _, chunks := range strips {
		for _, c := range chunks {
			total += len(c)
		}
	}
	pr := s.newProgress("Sending image", total)
	col := (bw-w)/2/cw + 1
	for k, chunks := range strips {
		pr.show(true)
		s.GotoXY(col, 1+k*step/ch)
		s.flush()
		for i, c := range chunks {
			if i > 0 && i < len(chunks)-1 && s.keyAvailable() {
				s.rawSend([]byte("\x1b\\"))
				return false, ""
			}
			s.paced(c, false)
			pr.add(len(c))
		}
	}
	return true, fmt.Sprintf("Sixel %dx%d", w, h)
}

// drawBlocks renders img with CP437 half blocks in the 16 ANSI colors, for
// terminals without Sixel support.
func (s *Session) drawBlocks(img image.Image) (bool, string) {
	b := img.Bounds()
	w, h := fitSize(b.Dx(), b.Dy(), s.W, (s.H-1)*2)
	var p *image.Paletted
	if !s.busy("Preparing image", func() { p = orderedCGA(scaleImage(img, w, h)) }) {
		return false, ""
	}
	ox := (s.W-w)/2 + 1
	var rows [][]byte
	total := 0
	for y := 0; y < h; y += 2 {
		s.GotoXY(ox, y/2+1)
		for x := 0; x < w; x++ {
			t := int(p.ColorIndexAt(x, y))
			bot := 0
			if y+1 < h {
				bot = int(p.ColorIndexAt(x, y+1))
			}
			switch {
			case t == bot:
				s.Color(t, 0)
				s.Print("\xDB")
			case bot < 8:
				s.Color(t, bot)
				s.Print("\xDF")
			case t < 8:
				s.Color(bot, t)
				s.Print("\xDC")
			default:
				s.Color(t, bot&7)
				s.Print("\xDF")
			}
		}
		rows = append(rows, s.takeOut())
		total += len(rows[len(rows)-1])
	}
	pr := s.newProgress("Sending image", total)
	for _, r := range rows {
		pr.show(false)
		ok := s.paced(r, true)
		pr.add(len(r))
		if !ok {
			s.ResetAttr()
			return false, ""
		}
	}
	s.ResetAttr()
	return true, fmt.Sprintf("ANSI %dx%d", w, (h+1)/2)
}

func (s *Session) imageView(name, format string, data []byte) {
	s.Cls()
	var img image.Image
	var err error
	if !s.busy("Decoding "+format+" image", func() { img, err = decodeImage(data, format) }) {
		return
	}
	if err == nil && img == nil {
		err = fmt.Errorf("the %s data could not be decoded", format)
	}
	if err != nil {
		s.message(safeText(name), "Cannot display image: "+err.Error())
		return
	}
	s.detectGraphics()
	useSixel := s.sixel
	b := img.Bounds()
	for {
		s.Cls()
		s.flush()
		var ok bool
		var shown string
		if useSixel {
			ok, shown = s.drawSixel(img)
		} else {
			ok, shown = s.drawBlocks(img)
		}
		if !ok {
			s.getKey(0)
			shown = "interrupted"
		}
		for {
			mode := "Sixel"
			if useSixel {
				mode = "ANSI"
			}
			left := fmt.Sprintf(" %s  %dx%d %s  [%s]", safeText(name), b.Dx(), b.Dy(), format, shown)
			right := fmt.Sprintf("M=%s B=Baud:%s R=Redraw I=Info Q=Quit ", mode, baudLabel(s.Baud))
			s.Bar(s.H, left, right, 15, 1)
			s.flush()
			k := s.waitKey()
			switch k {
			case 'm', 'M':
				useSixel = !useSixel
			case 'r', 'R':
			case 'b', 'B':
				s.Baud = nextBaud(s.Baud)
				continue
			case 'i', 'I':
				s.imageInfo(name, format, len(data), b, useSixel)
			case kEnter, kEsc, 'q', 'Q', ' ':
				return
			default:
				continue
			}
			break
		}
	}
}

func (s *Session) imageInfo(name, format string, size int, b image.Rectangle, sixel bool) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "File:     %s\n", safeText(name))
	fmt.Fprintf(&sb, "Format:   %s, %dx%d pixels\n", format, b.Dx(), b.Dy())
	fmt.Fprintf(&sb, "Size:     %s bytes\n", fmtNum(int64(size)))
	bw, bh, cw, ch := s.pixelArea()
	fmt.Fprintf(&sb, "Screen:   %dx%d chars, %dx%d px cells, %dx%d px area\n", s.W, s.H, cw, ch, bw, bh)
	support := "not reported by terminal"
	if s.sixelReported {
		support = "reported by terminal"
	}
	mode := "ANSI blocks"
	if sixel {
		mode = "Sixel"
	}
	fmt.Fprintf(&sb, "Sixel:    %s\nShowing:  %s", support, mode)
	s.message("Image", sb.String())
}
