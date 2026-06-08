package readline

import (
	"os"
	"unicode/utf8"
)

type key struct {
	r       rune
	special KeySpecial
}

type KeySpecial int

const (
	KeyNone KeySpecial = iota
	KeyEnter
	KeyBackspace
	KeyDelete
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	KeyDeleteFwd
	KeyCtrlA
	KeyCtrlB
	KeyCtrlC
	KeyCtrlD
	KeyCtrlE
	KeyCtrlF
	KeyCtrlH
	KeyCtrlK
	KeyCtrlL
	KeyCtrlN
	KeyCtrlP
	KeyCtrlR
	KeyCtrlU
	KeyCtrlW
	KeyEsc
	KeyAltEnter
)

func (e *LineEditor) readKey() (key, error) {
	buf := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return key{}, err
		}
		if n == 0 {
			continue
		}

		if buf[0] == 0x1b {
			return e.readEscapeSequence()
		}

		if buf[0] < 0x20 || buf[0] == 0x7f {
			// Newline in EOF: check if more data follows (paste detection)
			if (buf[0] == 0x0a || buf[0] == 0x0d) && e.hasMoreData() {
				return key{r: '\n'}, nil
			}
			return key{special: parseCtrlKey(buf[0])}, nil
		}

		if buf[0] >= 0x80 {
			readBuf := []byte{buf[0]}
			for len(readBuf) < 4 {
				r, size := utf8.DecodeRune(readBuf)
				if r != utf8.RuneError || size != 1 {
					return key{r: r}, nil
				}
				n, err := os.Stdin.Read(buf)
				if err != nil || n == 0 {
					return key{r: utf8.RuneError}, nil
				}
				readBuf = append(readBuf, buf[0])
			}
			r, _ := utf8.DecodeRune(readBuf)
			return key{r: r}, nil
		}

		return key{r: rune(buf[0])}, nil
	}
}

func (e *LineEditor) readEscapeSequence() (key, error) {
	buf := make([]byte, 1)
	seq := make([]byte, 0, 8)
	seq = append(seq, 0x1b)

	n, err := os.Stdin.Read(buf)
	if err != nil || n == 0 {
		return key{special: KeyEsc}, nil
	}
	seq = append(seq, buf[0])

	if buf[0] != '[' && buf[0] != 'O' {
		if buf[0] == '\r' || buf[0] == '\n' {
			return key{special: KeyAltEnter}, nil
		}
		return key{special: KeyEsc}, nil
	}

	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			return key{special: KeyEsc}, nil
		}
		seq = append(seq, buf[0])

		if buf[0] >= 0x40 && buf[0] <= 0x7e {
			break
		}
		if len(seq) > 16 {
			return key{special: KeyEsc}, nil
		}
	}

	return parseEscapeSequence(seq), nil
}

func parseEscapeSequence(seq []byte) key {
	if len(seq) < 3 {
		return key{special: KeyEsc}
	}

	if seq[0] == 0x1b {
		if len(seq) == 3 && seq[1] == '[' {
			switch seq[2] {
			case 'A':
				return key{special: KeyUp}
			case 'B':
				return key{special: KeyDown}
			case 'C':
				return key{special: KeyRight}
			case 'D':
				return key{special: KeyLeft}
			case 'H':
				return key{special: KeyHome}
			case 'F':
				return key{special: KeyEnd}
			}
		}

		if len(seq) == 4 && seq[1] == '[' && seq[2] == '1' {
			switch seq[3] {
			case '~':
				return key{special: KeyHome}
			case ';':
				readBuf := make([]byte, 1)
				n, err := os.Stdin.Read(readBuf)
				if err != nil || n == 0 {
					return key{special: KeyEsc}
				}
				n2, err := os.Stdin.Read(readBuf)
				if err != nil || n2 == 0 {
					return key{special: KeyEsc}
				}
				if readBuf[0] == '5' {
					readBuf2 := make([]byte, 1)
					n3, err := os.Stdin.Read(readBuf2)
					if err != nil || n3 == 0 {
						return key{special: KeyEsc}
					}
					if readBuf2[0] == 'D' {
						return key{special: KeyLeft}
					}
					if readBuf2[0] == 'C' {
						return key{special: KeyRight}
					}
				}
				return key{special: KeyEsc}
			}
		}

		if len(seq) == 4 && seq[1] == '[' && seq[2] == '3' {
			if seq[3] == '~' {
				return key{special: KeyDeleteFwd}
			}
		}

		if len(seq) == 4 && seq[1] == '[' && seq[2] == '7' {
			if seq[3] == '~' {
				return key{special: KeyHome}
			}
		}

		if len(seq) == 4 && seq[1] == '[' && seq[2] == '8' {
			if seq[3] == '~' {
				return key{special: KeyEnd}
			}
		}
	}

	return key{special: KeyEsc}
}

func parseCtrlKey(b byte) KeySpecial {
	switch b {
	case 0x01:
		return KeyCtrlA
	case 0x02:
		return KeyCtrlB
	case 0x03:
		return KeyCtrlC
	case 0x04:
		return KeyCtrlD
	case 0x05:
		return KeyCtrlE
	case 0x06:
		return KeyCtrlF
	case 0x08:
		return KeyCtrlH
	case 0x0b:
		return KeyCtrlK
	case 0x0c:
		return KeyCtrlL
	case 0x0e:
		return KeyCtrlN
	case 0x10:
		return KeyCtrlP
	case 0x12:
		return KeyCtrlR
	case 0x15:
		return KeyCtrlU
	case 0x17:
		return KeyCtrlW
	case 0x0a, 0x0d:
		return KeyEnter
	case 0x7f:
		return KeyBackspace
	}
	return KeyNone
}
