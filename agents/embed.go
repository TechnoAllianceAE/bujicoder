// Package agentdata embeds agent YAML definitions into the binary.
package agentdata

import "embed"

//go:embed *.yaml
var FS embed.FS
