package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
	"github.com/JosephWest2/cloud_dev/internal/logs"
)

func emitLogs(r logs.Result, jsonMode, streamMode bool, stdout, stderr io.Writer) int {
	if jsonMode {
		if json.NewEncoder(stdout).Encode(r) != nil {
			fmt.Fprintln(stderr, "devbox: cannot write retrieval result; retain the command ID")
			return 1
		}
		if !r.OK {
			fmt.Fprintf(stderr, "devbox: %s: %s\n", r.Outcome, r.Message)
		}
		for _, warning := range r.Warnings {
			fmt.Fprintf(stderr, "devbox: %s; retain command_id=%s for recovery\n", warning, r.CommandID)
		}
		return r.ExitCode
	}
	metadata := stdout
	if streamMode {
		metadata = stderr
	}
	if code := emitExecution(r.Result, false, metadata, stderr); code != r.ExitCode {
		return code
	}
	if _, err := fmt.Fprintf(metadata, "encoding=%s verification=%s\n", r.Encoding, r.Verification); err != nil {
		return 1
	}
	if r.Streams == nil {
		return r.ExitCode
	}
	for _, item := range []struct {
		name   string
		stream execprotocol.Stream
	}{
		{"stdout", r.Streams.Stdout}, {"stderr", r.Streams.Stderr},
	} {
		var download *logs.Download
		if r.Downloads != nil {
			if item.name == "stdout" {
				download = r.Downloads.Stdout
			} else {
				download = r.Downloads.Stderr
			}
		}
		verification := "not_downloaded"
		if download != nil {
			verification = download.Verification
		}
		if _, err := fmt.Fprintf(metadata, "%s bytes=%d sha256=%s upload=%s verification=%s", item.name, item.stream.Bytes, item.stream.SHA256, item.stream.Upload, verification); err != nil {
			return 1
		}
		if download != nil {
			if _, err := fmt.Fprintf(metadata, " bytes_written=%d destination=%s", download.BytesWritten, download.Destination); err != nil {
				return 1
			}
			if download.File != "" {
				// Quote explicit user-selected paths so embedded terminal controls
				// cannot alter status lines. Workload bytes are never rendered here.
				if _, err := fmt.Fprintf(metadata, " file=%q", download.File); err != nil {
					return 1
				}
			}
		}
		if _, err := fmt.Fprintln(metadata); err != nil {
			return 1
		}
	}
	return r.ExitCode
}
