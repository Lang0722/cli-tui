//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package tui

func backspaceSendsDEL() bool { return true }
