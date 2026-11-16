//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package tui

import (
	"os"

	"golang.org/x/sys/unix"
)

// backspaceSendsDEL reports whether the terminal's Backspace key sends DEL
// (0x7f), judged by the tty's erase character, which terminals such as
// gnome-terminal set to match. Then a 0x08 byte is Ctrl+Backspace, not
// the Backspace key.
func backspaceSendsDEL() bool {
	t, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), ioctlGetTermios)
	if err != nil {
		return true // most terminals send DEL
	}
	return t.Cc[unix.VERASE] != 0x08
}
