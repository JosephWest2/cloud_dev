//go:build !linux

package lifecycle

import "context"

func lockReceipt(context.Context, string) (func(), error) {
	return nil, failure("platform_unsupported", "launch request locking currently supports Linux only")
}
