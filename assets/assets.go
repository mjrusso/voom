// Package assets provides Voom's embedded assets.
package assets

import _ "embed"

// MacOSIcon is the icon used by MacOS guest apps.
//
//go:embed voom.icns
var MacOSIcon []byte
