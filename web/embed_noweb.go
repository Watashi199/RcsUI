//go:build noweb

package web

import (
	"errors"
	"io/fs"
)

// Build : binaire compilé sans le frontend (-tags noweb), pour le développement
// du backend seul (le frontend tourne alors sous `vite dev`).
func Build() (fs.FS, error) { return nil, errors.New("built with -tags noweb") }
