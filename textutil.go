package main

import (
	"strconv"
	"strings"
)

// fit truncates or pads s to exactly w bytes.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if len(s) > w {
		return s[:w]
	}
	return s + strings.Repeat(" ", w-len(s))
}

func pad(s string, w int) string { return fit(s, w) }

func rjust(s string, w int) string {
	if len(s) >= w {
		return s[len(s)-w:]
	}
	return strings.Repeat(" ", w-len(s)) + s
}

// fitTail keeps the end of a long name (the file name) visible.
func fitTail(s string, w int) string {
	if len(s) <= w {
		return fit(s, w)
	}
	if w <= 2 {
		return s[len(s)-w:]
	}
	return ".." + s[len(s)-(w-2):]
}

func fmtNum(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func fmtShort(n int64) string {
	switch {
	case n < 10_000_000:
		return fmtNum(n)
	case n < 10_000<<20:
		return fmtNum(n>>20) + "M"
	default:
		return fmtNum(n>>30) + "G"
	}
}

// wrapText hard-wraps text to width, preferring breaks at spaces and path separators.
func wrapText(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		for len(para) > width {
			cut := strings.LastIndexAny(para[:width+1], " /\\")
			if cut < width/2 {
				cut = width
			} else if para[cut] != ' ' {
				cut++
			}
			out = append(out, strings.TrimRight(para[:cut], " "))
			para = strings.TrimLeft(para[cut:], " ")
		}
		out = append(out, para)
	}
	return out
}
