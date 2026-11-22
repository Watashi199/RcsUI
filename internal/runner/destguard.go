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
//
// Un chemin SSH relatif (ou en ~/) est résolu par le serveur distant depuis le
// dossier personnel de l'utilisateur, inconnu ici : home porte cet utilisateur
// et path est relatif à ce dossier.
type place struct {
	fs, path string
	home     string // non vide : chemin relatif au dossier personnel de cet utilisateur
}

// writtenPlaces calcule les emplacements écrits par un run : aucun pour un
// dry-run. Les chemins locaux ont leurs symlinks résolus (comme le font les
// moteurs) : appel hors m.mu.
func writtenPlaces(s engine.Spec) []place {
	if s.DryRun {
		return nil
	}
	eps := writtenEndpoints(s.Mode, s.Source, s.Destination)
	if x, ok := s.Options.(engine.ExtraWrites); ok {
		for _, p := range x.ExtraWritePaths() {
			e := s.Destination
			e.Path = p
			eps = append(eps, e)
		}
	}
	out := make([]place, 0, len(eps))
	for _, e := range eps {
		out = append(out, location(e))
	}
	return out
}

// location identifie le système de fichiers d'un endpoint (même machine, même
// remote) et le chemin normalisé dans celui-ci.
func location(e engine.Endpoint) place {
	var fsID, home string
	p := e.Path
	switch e.Type {
	case engine.EndpointSSH:
		if e.SSH != nil {
			port := e.SSH.Port
			if port == 0 {
				port = 22
			}
			fsID = fmt.Sprintf("ssh:%s:%d", strings.ToLower(e.SSH.Host), port)
			if !strings.HasPrefix(p, "/") {
				home = e.SSH.User
				if strings.HasPrefix(p, "~") && !strings.HasPrefix(p, "~/") && p != "~" {
					home = "~" // dossier d'un autre utilisateur : inconnu
				}
				p = strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/")
				// ".." remonte au-dessus du dossier personnel : emplacement inconnu.
				if c := path.Clean(p); c == ".." || strings.HasPrefix(c, "../") {
					home = "~"
				}
			}
		}
	case engine.EndpointRemote:
		fsID = "remote:" + e.Remote
	default:
		fsID = "local"
	}
	if fsID == "local" && p != "" {
		p = paths.Canonical(p)
	}
	p = strings.ReplaceAll(p, `\`, "/")
	p = path.Clean("/" + p)
	return place{fs: fsID, path: p, home: home}
}

// overlaps : même système de fichiers et un chemin inclus dans l'autre. Faute
// de connaître le dossier personnel distant, un chemin SSH relatif chevauche
// tout chemin absolu du même hôte, et tout chemin relatif d'un autre utilisateur.
func (a place) overlaps(b place) bool {
	if a.fs != b.fs {
		return false
	}
	if a.home != b.home || a.home == "~" {
		return true
	}
	return within(a.path, b.path) || within(b.path, a.path)
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
