//go:build !linux

package cli

func downInputIsTerminal() bool { return false }
