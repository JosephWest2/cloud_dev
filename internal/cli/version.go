package cli

// Version is set by release builds with -ldflags -X. Direct go builds retain
// the development default; this is the same value in text and JSON output.
var Version = "0.1.0-dev"
