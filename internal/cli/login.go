package cli

import (
	"context"
	"io"
	"os"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/identity"
)

func prepareCommandLogin(ctx context.Context, path string, overrides config.Overrides, command string, jsonMode bool, terminal io.Writer) context.Context {
	// Proxy stdout is an SSH transport; generated configuration and log streams
	// are also machine interfaces. Never start browser authentication there.
	if jsonMode || !downInputIsTerminal() || command == "proxy" || command == "ssh-config" || command == "logs" {
		return ctx
	}
	c, err := config.LoadCleanup(path, overrides)
	if err != nil {
		return ctx
	}
	if command != "cleanup" {
		c, err = config.Load(path, overrides)
		if err != nil {
			return ctx
		}
		if _, err = config.LoadProfile(c.ProfileFile); err != nil {
			return ctx
		}
	}
	return identity.PrepareLogin(ctx, c, os.Stdin, terminal)
}
