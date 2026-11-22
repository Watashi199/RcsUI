package runner

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"rcsui/internal/engine"
	"rcsui/internal/paths"
)

// ErrDestinationBusy : le job demande l'exclusivité de sa destination et un
// autre job actif écrit au même endroit.
var ErrDestinationBusy = errors.New("destination in use by another job")

// DestinationBusyError précise le job qui occupe la destination.
type DestinationBusyError struct{ JobName string }

func (e *DestinationBusyError) Error() string {
	return fmt.Sprintf("destination in use by job %q", e.JobName)
}

func (e *DestinationBusyError) Is(t error) bool { return t == ErrDestinationBusy }

// writtenEndpoints retourne les emplacements modifiés par un run : la
// destination, plus la source pour move (supprimée) et bisync (les deux côtés).
func writtenEndpoints(mode engine.Mode, src, dst engine.Endpoint) []engine.Endpoint {
	if mode == engine.ModeMove || mode == engine.ModeBisync {
		return []engine.Endpoint{dst, src}
	}
	return []engine.Endpoint{dst}
}

// place est un emplacement écrit : système de fichiers et chemin normalisé.
type place struct{ fs, path string }

// writtenPlaces calcule les emplacements écrits par un run. Les chemins locaux
// ont leurs symlinks résolus (comme le font les moteurs) : appel hors m.mu.
func writtenPlaces(mode engine.Mode, src, dst engine.Endpoint) []place {
	var out []place
	for _, e := range writtenEndpoints(mode, src, dst) {
		fsID, p := location(e)
		out = append(out, place{fsID, p})
	}
	return out
}

// location identifie le système de fichiers d'un endpoint (même machine, même
// remote) et le chemin normalisé dans celui-ci.
func location(e engine.Endpoint) (fsID, p string) {
	switch e.Type {
	case engine.EndpointSSH:
		if e.SSH != nil {
			port := e.SSH.Port
			if port == 0 {
				port = 22
			}
			fsID = fmt.Sprintf("ssh:%s:%d", strings.ToLower(e.SSH.Host), port)
		}
	case engine.EndpointRemote:
		fsID = "remote:" + e.Remote
	default:
		fsID = "local"
	}
	p = e.Path
	if fsID == "local" && p != "" {
		p = paths.Canonical(p)
	}
	p = strings.ReplaceAll(p, `\`, "/")
	p = path.Clean("/" + p)
	return fsID, p
}

// overlaps : même système de fichiers et un chemin inclus dans l'autre.
func (a place) overlaps(b place) bool {
	return a.fs == b.fs && (within(a.path, b.path) || within(b.path, a.path))
}

// within indique si p est q ou se trouve sous q.
func within(p, q string) bool {
	return p == q || q == "/" || strings.HasPrefix(p, q+"/")
}

// placesOverlap : deux runs modifient un même emplacement.
func placesOverlap(a, b []place) bool {
	for _, x := range a {
		for _, y := range b {
			if x.overlaps(y) {
				return true
			}
		}
	}
	return false
}

// destinationBusyLocked retourne l'erreur à opposer à un run exclusif si un
// autre job actif (en file ou en cours) écrit au même endroit. m.mu est tenu.
func (m *Manager) destinationBusyLocked(jobID int64, writes []place) error {
	for jid, ar := range m.byJob {
		if jid != jobID && placesOverlap(writes, ar.writes) {
			return &DestinationBusyError{JobName: ar.run.JobName}
		}
	}
	return nil
}
