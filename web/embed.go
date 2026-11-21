//go:build !noweb

// Package web embarque le build statique SvelteKit (adapter-static) dans le binaire.
package web

import (
	"embed"
	"io/fs"
)

// "all:" est indispensable : SvelteKit produit un dossier _app/, que go:embed
// ignorerait sinon (fichiers commençant par '_' ou '.').
//
//go:embed all:build
var files embed.FS

// Build retourne le système de fichiers du build (racine = build/).
func Build() (fs.FS, error) { return fs.Sub(files, "build") }
