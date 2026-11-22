package runner

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"rcsui/internal/engine"
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
	p = strings.ReplaceAll(e.Path, `\`, "/")
	p = path.Clean("/" + p)
	return fsID, p
}

// endpointsOverlap : même système de fichiers et un chemin inclus dans l'autre.
func endpointsOverlap(a, b engine.Endpoint) bool {
	fa, pa := location(a)
	fb, pb := location(b)
	if fa != fb {
		return false
	}
	return within(pa, pb) || within(pb, pa)
}

// within indique si p est q ou se trouve sous q.
func within(p, q string) bool {
	return p == q || q == "/" || strings.HasPrefix(p, q+"/")
}

// writesOverlap : deux runs modifient un même emplacement.
func writesOverlap(a, b engine.Spec) bool {
	for _, x := range writtenEndpoints(a.Mode, a.Source, a.Destination) {
		for _, y := range writtenEndpoints(b.Mode, b.Source, b.Destination) {
			if endpointsOverlap(x, y) {
				return true
			}
		}
	}
	return false
}

// destinationBusyLocked retourne l'erreur à opposer à un run exclusif si un
// autre job actif (en file ou en cours) écrit au même endroit. m.mu est tenu.
func (m *Manager) destinationBusyLocked(jobID int64, spec engine.Spec) error {
	for jid, ar := range m.byJob {
		if jid != jobID && writesOverlap(spec, ar.spec) {
			return &DestinationBusyError{JobName: ar.run.JobName}
		}
	}
	return nil
}
