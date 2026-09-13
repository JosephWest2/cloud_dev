package config

import (
	"path/filepath"
	"strings"
)

// CommandPrefix makes copyable recovery commands preserve the selected public
// configuration and AWS scope. It never includes credential material.
func CommandPrefix(path string, c Config) string {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	absolute, err := filepath.Abs(path)
	if err != nil || strings.ContainsAny(absolute+c.Region+c.AWSProfile, "\r\n\x00") {
		return "devbox"
	}
	prefix := "devbox --config " + quote(absolute) + " --region " + quote(c.Region)
	if c.AWSProfile != "" {
		prefix += " --aws-profile " + quote(c.AWSProfile)
	}
	return prefix
}
