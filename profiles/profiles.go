// Package profiles ships the default workload contract with the binary.
package profiles

import _ "embed"

//go:embed agent.toml
var Agent []byte
