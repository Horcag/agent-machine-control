package ssh

import (
	"strings"
	"unicode/utf8"
)

type terminalParseState uint8

const (
	terminalGround terminalParseState = iota
	terminalEscape
	terminalCSI
	terminalOSC
	terminalOSCEscape
	terminalString
	terminalStringEscape
	terminalDiscard
)

// terminalFilter is a streaming allowlist parser. It preserves valid UTF-8 text
// plus horizontal tab, carriage return, and newline, and strips every terminal
// control sequence. Parser state is constant-sized; only an incomplete UTF-8
// rune may be retained between chunks.
type terminalFilter struct {
	state    terminalParseState
	utf8Tail []byte
}

func (f *terminalFilter) Push(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	data := append(append([]byte(nil), f.utf8Tail...), raw...)
	f.utf8Tail = nil

	var clean strings.Builder
	clean.Grow(len(data))
	for offset := 0; offset < len(data); {
		r, size, complete := nextTerminalRune(data[offset:])
		if !complete {
			f.utf8Tail = append(f.utf8Tail, data[offset:]...)
			break
		}
		if f.state == terminalGround {
			f.consumeGround(&clean, r, data[offset:offset+size])
		} else {
			f.consumeControl(r)
		}
		offset += size
	}
	return clean.String()
}

func (f *terminalFilter) Flush() string {
	var clean string
	if f.state == terminalGround && len(f.utf8Tail) > 0 {
		clean = "\uFFFD"
	}
	f.state = terminalGround
	f.utf8Tail = nil
	return clean
}

func (f *terminalFilter) RetainedBytes() int {
	return len(f.utf8Tail)
}

func nextTerminalRune(data []byte) (rune, int, bool) {
	b := data[0]
	if b < utf8.RuneSelf || (b >= 0x80 && b <= 0x9f) {
		return rune(b), 1, true
	}
	if !utf8.FullRune(data) {
		return 0, 0, false
	}
	r, size := utf8.DecodeRune(data)
	return r, size, true
}

func (f *terminalFilter) consumeGround(clean *strings.Builder, r rune, encoded []byte) {
	switch {
	case r == '\x1b':
		f.state = terminalEscape
	case r >= 0x80 && r <= 0x9f:
		f.startC1(r)
	case r == '\n' || r == '\r' || r == '\t':
		clean.WriteRune(r)
	case r < 0x20 || r == 0x7f:
		return
	case r == utf8.RuneError && len(encoded) == 1:
		clean.WriteRune(utf8.RuneError)
	default:
		clean.Write(encoded)
	}
}

func (f *terminalFilter) consumeControl(r rune) {
	if f.state == terminalDiscard {
		return
	}
	if r >= 0x80 && r <= 0x9f {
		f.consumeC1(r)
		return
	}

	switch f.state {
	case terminalEscape:
		f.consumeEscape(r)
	case terminalCSI:
		f.consumeCSI(r)
	case terminalOSC:
		f.consumeOSC(r)
	case terminalOSCEscape:
		f.consumeStringEscape(r, terminalOSC)
	case terminalString:
		if r == '\x1b' {
			f.state = terminalStringEscape
		}
	case terminalStringEscape:
		f.consumeStringEscape(r, terminalString)
	}
}

func (f *terminalFilter) consumeCSI(r rune) {
	switch {
	case r == '\x1b':
		f.state = terminalEscape
	case r >= 0x40 && r <= 0x7e:
		f.state = terminalGround
	case r < 0x20 || r > 0x3f:
		f.state = terminalDiscard
	}
}

func (f *terminalFilter) consumeOSC(r rune) {
	switch r {
	case '\a':
		f.state = terminalGround
	case '\x1b':
		f.state = terminalOSCEscape
	}
}

func (f *terminalFilter) consumeStringEscape(r rune, stringState terminalParseState) {
	switch r {
	case '\\':
		f.state = terminalGround
	case '\x1b':
		return
	default:
		f.state = stringState
	}
}

func (f *terminalFilter) consumeEscape(r rune) {
	switch r {
	case '[':
		f.state = terminalCSI
	case ']':
		f.state = terminalOSC
	case 'P', 'X', '^', '_':
		f.state = terminalString
	case '\x1b':
		return
	default:
		if r >= 0x30 && r <= 0x7e {
			f.state = terminalGround
		} else if r < 0x20 || r > 0x2f {
			f.state = terminalDiscard
		}
	}
}

func (f *terminalFilter) consumeC1(r rune) {
	if f.state == terminalOSC || f.state == terminalOSCEscape || f.state == terminalString || f.state == terminalStringEscape {
		if r == 0x9c {
			f.state = terminalGround
		}
		return
	}
	if f.state == terminalEscape || f.state == terminalCSI {
		f.state = terminalDiscard
		return
	}
	f.startC1(r)
}

func (f *terminalFilter) startC1(r rune) {
	switch r {
	case 0x90, 0x98, 0x9e, 0x9f:
		f.state = terminalString
	case 0x9b:
		f.state = terminalCSI
	case 0x9d:
		f.state = terminalOSC
	default:
		f.state = terminalGround
	}
}

func filterTerminalOutput(raw []byte) string {
	var filter terminalFilter
	return filter.Push(raw) + filter.Flush()
}
