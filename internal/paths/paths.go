// Package paths restreint l'accès aux chemins locaux à une liste de racines
// autorisées (RCSUI_ALLOWED_ROOTS), en tenant compte des liens symboliques.
package paths

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrOutsideRoots est retournée pour un chemin hors des racines autorisées.
var ErrOutsideRoots = errors.New("path is outside allowed roots")

// ErrInvalid est retournée pour un chemin syntaxiquement refusé.
var ErrInvalid = errors.New("invalid path")

// Resolver valide les chemins locaux.
type Resolver struct {
	roots []string // absolues, nettoyées, symlinks résolus
}

// New construit un Resolver. Les racines inexistantes sont refusées.
func New(roots []string) (*Resolver, error) {
	r := &Resolver{}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if !filepath.IsAbs(root) {
			return nil, fmt.Errorf("allowed root %q must be absolute", root)
		}
		real, err := filepath.EvalSymlinks(filepath.Clean(root))
		if err != nil {
			return nil, fmt.Errorf("allowed root %q: %w", root, err)
		}
		st, err := os.Stat(real)
		if err != nil || !st.IsDir() {
			return nil, fmt.Errorf("allowed root %q is not a directory", root)
		}
		r.roots = append(r.roots, real)
	}
	sort.Strings(r.roots)
	return r, nil
}

// Roots retourne les racines autorisées (résolues).
func (r *Resolver) Roots() []string { return append([]string(nil), r.roots...) }

// CheckSyntax applique les règles syntaxiques communes (sans accès disque).
func CheckSyntax(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("%w: empty", ErrInvalid)
	case strings.ContainsRune(p, 0):
		return fmt.Errorf("%w: NUL byte", ErrInvalid)
	case strings.ContainsAny(p, "\n\r"):
		return fmt.Errorf("%w: newline", ErrInvalid)
	case strings.HasPrefix(p, "-"):
		return fmt.Errorf("%w: leading dash", ErrInvalid)
	case len(p) > 4096:
		return fmt.Errorf("%w: too long", ErrInvalid)
	}
	return nil
}

// Resolve valide p et retourne son chemin réel (symlinks résolus sur la
// partie existante). p peut ne pas exister (destination à créer).
func (r *Resolver) Resolve(p string) (string, error) {
	if err := CheckSyntax(p); err != nil {
		return "", err
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%w: must be absolute", ErrInvalid)
	}
	real, err := resolveExistingPrefix(filepath.Clean(p))
	if err != nil {
		return "", err
	}
	if _, rel := r.rootOf(real); rel == "" {
		return "", ErrOutsideRoots
	}
	return real, nil
}

// resolveExistingPrefix résout les symlinks du plus long préfixe existant de p
// puis y rajoute la partie inexistante.
func resolveExistingPrefix(p string) (string, error) {
	var rest []string
	cur := p
	for {
		_, err := os.Lstat(cur)
		if err == nil {
			real, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", fmt.Errorf("%w: %v", ErrInvalid, err)
			}
			for i := len(rest) - 1; i >= 0; i-- {
				real = filepath.Join(real, rest[i])
			}
			return real, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("%w: no existing ancestor", ErrInvalid)
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}

// rootOf retourne la racine (la plus spécifique) contenant real et le chemin
// relatif ; rel est vide si real n'est sous aucune racine.
func (r *Resolver) rootOf(real string) (root, rel string) {
	for _, rt := range r.roots {
		rr, err := filepath.Rel(rt, real)
		if err != nil || rr == ".." || strings.HasPrefix(rr, ".."+string(filepath.Separator)) || filepath.IsAbs(rr) {
			continue
		}
		if len(rt) > len(root) {
			root, rel = rt, rr
		}
	}
	return root, rel
}

// Entry est une entrée de l'explorateur.
type Entry struct {
	Name    string    `json:"name"`
	Dir     bool      `json:"dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	Symlink bool      `json:"symlink,omitempty"`
}

// Listing est le résultat d'un listage.
type Listing struct {
	Path      string  `json:"path"`
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
}

// List liste un dossier sous une racine autorisée. L'ouverture passe par
// os.Root, qui interdit toute évasion par lien symbolique.
func (r *Resolver) List(p string, max int) (Listing, error) {
	real, err := r.Resolve(p)
	if err != nil {
		return Listing{}, err
	}
	root, rel := r.rootOf(real)
	or, err := os.OpenRoot(root)
	if err != nil {
		return Listing{}, err
	}
	defer or.Close()
	f, err := or.Open(filepath.ToSlash(rel))
	if err != nil {
		return Listing{}, err
	}
	defer f.Close()
	des, err := f.ReadDir(-1)
	if err != nil {
		return Listing{}, err
	}
	sort.Slice(des, func(i, j int) bool {
		if des[i].IsDir() != des[j].IsDir() {
			return des[i].IsDir()
		}
		return strings.ToLower(des[i].Name()) < strings.ToLower(des[j].Name())
	})
	out := Listing{Path: real, Entries: []Entry{}}
	for _, de := range des {
		if max > 0 && len(out.Entries) >= max {
			out.Truncated = true
			break
		}
		e := Entry{Name: strings.ToValidUTF8(de.Name(), "�"), Dir: de.IsDir(), Symlink: de.Type()&fs.ModeSymlink != 0}
		if info, err := de.Info(); err == nil {
			e.Size = info.Size()
			e.ModTime = info.ModTime()
		}
		if e.Symlink {
			// Un lien vers un dossier situé sous les racines est navigable.
			if t, err := r.Resolve(filepath.Join(real, de.Name())); err == nil {
				if st, err := os.Stat(t); err == nil && st.IsDir() {
					e.Dir = true
				}
			}
		}
		out.Entries = append(out.Entries, e)
	}
	return out, nil
}
