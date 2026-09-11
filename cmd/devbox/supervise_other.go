//go:build !linux

package main

import (
	"fmt"
	"os"
)

func supervise([]string) int {
	fmt.Fprintln(os.Stderr, "devbox currently supports Linux only")
	return 1
}
