// Package skills embeds the renfe agent skill so the CLI can install it.
package skills

import "embed"

// FS holds the skill folder "renfe" (SKILL.md, agents/, scripts/).
//
//go:embed renfe
var FS embed.FS
