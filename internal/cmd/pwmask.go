package cmd

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

// maskState accumulates a typed password while deciding what to echo for each
// keystroke. It is the pure core of the masked-input reader, separated out so
// the keystroke handling can be tested without a real terminal.
type maskState struct {
	buf []byte
}

// feed processes one input byte and returns what to echo to the screen, whether
// input is complete (Enter), and whether it was aborted (Ctrl-C).
func (m *maskState) feed(b byte) (echo string, done, abort bool) {
	switch {
	case b == '\r' || b == '\n':
		return "\r\n", true, false
	case b == 3: // Ctrl-C
		return "", false, true
	case b == 127 || b == 8: // DEL / Backspace
		if len(m.buf) > 0 {
			m.buf = m.buf[:len(m.buf)-1]
			return "\b \b", false, false
		}
		return "", false, false
	case b >= 32 && b < 127: // printable
		m.buf = append(m.buf, b)
		return "*", false, false
	default: // other control bytes: ignore
		return "", false, false
	}
}

// readPasswordMasked prints prompt and reads a line from the terminal at fd,
// echoing one '*' per typed character. Backspace erases; Ctrl-C aborts. It puts
// the terminal in raw mode for the duration and restores it after.
func readPasswordMasked(fd int, prompt string) ([]byte, error) {
	fmt.Fprint(os.Stderr, prompt)
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	defer term.Restore(fd, state)

	var m maskState
	buf := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		echo, done, abort := m.feed(buf[0])
		fmt.Fprint(os.Stderr, echo)
		if abort {
			term.Restore(fd, state)
			fmt.Fprintln(os.Stderr)
			return nil, fmt.Errorf("aborted")
		}
		if done {
			return m.buf, nil
		}
	}
}
