package interactive

import (
	"bufio"
	"context"
	"io"
)

// KeyEvent is one parsed key press.
type KeyEvent struct {
	// Rune is the single printable rune (when Key == "rune"). Zero otherwise.
	Rune rune
	// Key is one of: "rune", "tab", "shift-tab", "enter", "esc", "ctrl-c",
	// "left", "right", "up", "down".
	Key string
}

// ReadKeys reads bytes from r and emits KeyEvents to out until ctx is done
// or r returns EOF. The caller is responsible for putting the terminal into
// raw mode (e.g. via golang.org/x/term.MakeRaw) before invoking ReadKeys.
//
// Parses a minimal subset of ANSI escape sequences sufficient for arrow
// keys and shift-tab on macOS / Linux / Windows ConPTY. Unknown sequences
// are dropped silently.
func ReadKeys(ctx context.Context, r io.Reader, out chan<- KeyEvent) {
	br := bufio.NewReader(r)
	for {
		if ctx.Err() != nil {
			return
		}
		b, err := br.ReadByte()
		if err != nil {
			return
		}
		ev := KeyEvent{}
		switch b {
		case 0x03:
			ev.Key = "ctrl-c"
		case 0x09:
			ev.Key = "tab"
		case 0x0a, 0x0d:
			ev.Key = "enter"
		case 0x7f, 0x08:
			ev.Key = "backspace"
		case 0x1b:
			// Either a lone ESC or the start of an escape sequence.
			next, err := peekByte(br)
			if err != nil {
				ev.Key = "esc"
				break
			}
			if next != '[' && next != 'O' {
				ev.Key = "esc"
				break
			}
			_, _ = br.ReadByte() // consume '[' or 'O'
			seq, _ := readCSI(br)
			ev = decodeCSI(seq)
		default:
			if b < 0x20 {
				continue
			}
			ev.Key = "rune"
			ev.Rune = rune(b)
		}
		if ev.Key == "" {
			continue
		}
		select {
		case out <- ev:
		case <-ctx.Done():
			return
		}
	}
}

func peekByte(br *bufio.Reader) (byte, error) {
	bs, err := br.Peek(1)
	if err != nil {
		return 0, err
	}
	return bs[0], nil
}

func readCSI(br *bufio.Reader) (string, error) {
	var seq []byte
	for {
		b, err := br.ReadByte()
		if err != nil {
			return string(seq), err
		}
		seq = append(seq, b)
		// Terminator: any byte in @-~ ends a CSI sequence.
		if b >= 0x40 && b <= 0x7e {
			return string(seq), nil
		}
		if len(seq) > 32 {
			return string(seq), nil
		}
	}
}

func decodeCSI(seq string) KeyEvent {
	switch seq {
	case "A":
		return KeyEvent{Key: "up"}
	case "B":
		return KeyEvent{Key: "down"}
	case "C":
		return KeyEvent{Key: "right"}
	case "D":
		return KeyEvent{Key: "left"}
	case "Z":
		return KeyEvent{Key: "shift-tab"}
	}
	return KeyEvent{}
}
