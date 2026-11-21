package rclone

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"rcsui/internal/engine"
	"rcsui/internal/paths"
)

// remoteNameRe : pas de ':' ni de ',' (qui ouvriraient une connection string),
// pas de premier caractère spécial (':' = backend à la volée).
var remoteNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_. -]{0,63}$`)

// ValidRemoteName indique si un nom de remote est acceptable.
func ValidRemoteName(n string) bool {
	return remoteNameRe.MatchString(n) && !strings.HasSuffix(n, " ") && !strings.HasSuffix(n, ".")
}

// CleanRemotePath nettoie un chemin relatif à un remote et refuse toute
// remontée ("..") et les caractères de contrôle.
func CleanRemotePath(p string) (string, error) {
	if strings.ContainsAny(p, "\x00\n\r") {
		return "", fmt.Errorf("path contains control characters")
	}
	if len(p) > 4096 {
		return "", fmt.Errorf("path too long")
	}
	p = strings.ReplaceAll(p, "\\", "/")
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", fmt.Errorf("path must not contain '..'")
		}
	}
	c := path.Clean("/" + p)
	return strings.TrimPrefix(c, "/"), nil
}

// RemoteLookup fournit les remotes existants et leur type.
type RemoteLookup interface {
	RemoteType(name string) (string, bool)
}

// BuildFs construit la chaîne fs rclone à partir d'un endpoint typé. Jamais de
// chaîne brute venant du client : les backends à la volée (":s3,...:") et les
// blobs JSON sont donc impossibles par construction.
func BuildFs(ep engine.Endpoint, r *paths.Resolver, remotes RemoteLookup) (string, error) {
	switch ep.Type {
	case engine.EndpointLocal:
		p, err := r.Resolve(ep.Path)
		if err != nil {
			return "", err
		}
		return p, nil
	case engine.EndpointRemote:
		if !ValidRemoteName(ep.Remote) {
			return "", fmt.Errorf("invalid remote name")
		}
		typ, ok := remotes.RemoteType(ep.Remote)
		if !ok {
			return "", fmt.Errorf("unknown remote %q", ep.Remote)
		}
		if typ == "local" {
			// Un remote "local" contournerait les racines autorisées.
			return "", fmt.Errorf("remotes of type local are not allowed; use a local endpoint")
		}
		p, err := CleanRemotePath(ep.Path)
		if err != nil {
			return "", err
		}
		return ep.Remote + ":" + p, nil
	default:
		return "", fmt.Errorf("endpoint type %q: %w", ep.Type, engine.ErrUnsupported)
	}
}
