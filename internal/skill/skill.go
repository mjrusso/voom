// Package skill provides the agent instructions shipped with Voom.
package skill

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed SKILL.md
var markdown string

// Markdown returns the embedded agent skill.
func Markdown() string {
	return markdown
}

// SHA256 returns the hexadecimal digest of Markdown.
func SHA256() string {
	sum := sha256.Sum256([]byte(markdown))
	return hex.EncodeToString(sum[:])
}
