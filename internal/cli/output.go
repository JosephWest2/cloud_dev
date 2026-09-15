package cli

import "io"

// Treat a truncated write as failure even when a writer omits its required
// error. Lifecycle emitters must then preserve recovery identities on stderr.
type completeOutput struct{ io.Writer }

func (w completeOutput) Write(data []byte) (int, error) {
	n, err := w.Writer.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return n, err
}

func expiryText(value string) string {
	if value == "" {
		return "null"
	}
	return value
}
