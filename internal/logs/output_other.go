//go:build !linux

package logs

import (
	"context"
	"io"
)

// The executable rejects non-Linux platforms before invoking the CLI.
func prepareOutput(_ context.Context, dst io.Writer) (io.Writer, func(), error) {
	return dst, func() {}, nil
}
