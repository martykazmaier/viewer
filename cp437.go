package main

import (
	"strings"
	"unicode/utf8"
)

const cp437High = "ÇüéâäàåçêëèïîìÄÅÉæÆôöòûùÿÖÜ¢£¥₧ƒáíóúñÑªº¿⌐¬½¼¡«»░▒▓│┤╡╢╖╕╣║╗╝╜╛┐└┴┬├─┼╞╟╚╔╩╦╠═╬╧╨╤╥╙╘╒╓╫╪┘┌█▄▌▐▀αßΓπΣσµτΦΘΩδ∞φε∩≡±≥≤⌠⌡÷≈°∙·√ⁿ²■\u00a0"

var (
	runeToCP437 = map[rune]byte{}
	cp437ToRune [256]rune
	foldRunes   = map[rune]string{
		'À': "A", 'Á': "A", 'Â': "A", 'Ã': "A", 'È': "E", 'Ê': "E", 'Ë': "E",
		'Ì': "I", 'Í': "I", 'Î': "I", 'Ï': "I", 'Ò': "O", 'Ó': "O", 'Ô': "O",
		'Õ': "O", 'Ø': "O", 'Ù': "U", 'Ú': "U", 'Û': "U", 'Ý': "Y", 'ã': "a",
		'õ': "o", 'ø': "o", 'ý': "y", 'Š': "S", 'š': "s", 'Ž': "Z", 'ž': "z",
		'Č': "C", 'č': "c", 'Ł': "L", 'ł': "l", 'Œ': "OE", 'œ': "oe",
		'‘': "'", '’': "'", '‚': ",", '“': "\"", '”': "\"", '„': "\"",
		'–': "-", '—': "-", '…': "...", '•': "\xF9", 'β': "\xE1", 'μ': "\xE6",
		'™': "TM", '©': "(C)", '®': "(R)", '×': "x",
	}
)

func init() {
	for i := 0; i < 128; i++ {
		cp437ToRune[i] = rune(i)
	}
	i := 0x80
	for _, r := range cp437High {
		runeToCP437[r] = byte(i)
		cp437ToRune[i] = r
		i++
	}
	if i != 0x100 {
		panic("cp437 table must have 128 entries")
	}
}

// utf8ToCP437 converts UTF-8 text to CP437 bytes for display on BBS terminals.
func utf8ToCP437(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x80 {
			b.WriteByte(byte(r))
		} else if c, ok := runeToCP437[r]; ok {
			b.WriteByte(c)
		} else if f, ok := foldRunes[r]; ok {
			b.WriteString(f)
		} else {
			b.WriteByte('?')
		}
	}
	return b.String()
}

// latin1ToCP437 converts ISO-8859-1 bytes (used by gzip headers) to CP437.
func latin1ToCP437(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		r := rune(s[i])
		if r < 0x80 {
			b.WriteByte(byte(r))
		} else if c, ok := runeToCP437[r]; ok {
			b.WriteByte(c)
		} else {
			b.WriteByte('?')
		}
	}
	return b.String()
}

// looksUTF8 reports whether s is valid UTF-8 containing at least one multi-byte sequence.
func looksUTF8(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return true
		}
	}
	return false
}

// textName converts an archive name to CP437, decoding it as UTF-8 when flagged or detected.
func textName(s string, isUTF8 bool) string {
	if isUTF8 || looksUTF8(s) {
		return utf8ToCP437(s)
	}
	return s
}

// safeText replaces bytes that terminals treat as control codes.
func safeText(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c < 32 || c == 127 {
			b[i] = '?'
		}
	}
	return string(b)
}
