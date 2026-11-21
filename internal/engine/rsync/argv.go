package rsync

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"rcsui/internal/engine"
)

var (
	hostRe = regexp.MustCompile(`^(?:[A-Za-z0-9][A-Za-z0-9.-]{0,252}|\[[0-9A-Fa-f:.]+\])$`)
	userRe = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)
)

// SSHConfig fournit au builder ce qu'il faut pour construire l'option -e.
// Tous les chemins sont générés par l'application (jamais saisis par l'utilisateur).
type SSHConfig struct {
	// KeyFile est le chemin de la clé privée déchiffrée pour ce run (mode géré).
	KeyFile string
	// KnownHostsFile est le fichier known_hosts géré par l'application.
	KnownHostsFile string
	// Mounted : utiliser la configuration ~/.ssh montée au lieu d'une clé gérée.
	Mounted bool
}

// ResolvedPaths contient les chemins locaux déjà validés par paths.Resolver.
type ResolvedPaths struct {
	Source, Destination string // chemins locaux réels, "" pour un endpoint SSH
	BackupDir, LinkDest string
}

// BuildArgv construit l'argv complet (sans "rsync" en tête). Il suppose que
// les chemins locaux ont été résolus et les options validées.
func BuildArgv(s engine.Spec, o *Options, rp ResolvedPaths, ssh *SSHConfig) ([]string, error) {
	a := []string{"-rlptD"}
	flag := func(cond bool, f string) {
		if cond {
			a = append(a, f)
		}
	}
	flag(o.Owner, "-o")
	flag(o.Group, "-g")
	flag(o.FakeSuper, "--fake-super")
	flag(o.Checksum, "-c")
	flag(o.Compress, "-z")
	flag(o.Partial, "--partial")
	flag(o.HardLinks, "-H")
	flag(o.ACLs, "-A")
	flag(o.Xattrs, "-X")
	flag(o.Sparse, "-S")
	flag(o.NumericIDs, "--numeric-ids")
	flag(o.Update, "-u")
	flag(o.IgnoreExisting, "--ignore-existing")
	flag(o.ExistingOnly, "--existing")
	flag(o.OneFileSystem, "-x")
	flag(o.SizeOnly, "--size-only")
	flag(o.AccurateProgress, "--no-inc-recursive")

	if s.Mode == engine.ModeSync {
		mode := o.Delete
		if mode == DeleteNone {
			mode = DeleteAfter
		}
		a = append(a, "--delete-"+string(mode))
		flag(o.DeleteExcluded, "--delete-excluded")
		if o.MaxDelete != nil {
			a = append(a, "--max-delete="+strconv.Itoa(*o.MaxDelete))
		}
	} else if s.Mode != engine.ModeCopy {
		return nil, fmt.Errorf("mode %q: %w", s.Mode, engine.ErrUnsupported)
	}
	intOpt := func(name string, v *int) {
		if v != nil && *v > 0 {
			a = append(a, name+"="+strconv.Itoa(*v))
		}
	}
	intOpt("--bwlimit", o.BwLimitKiB)
	intOpt("--timeout", o.TimeoutSec)
	intOpt("--contimeout", o.ContimeoutSec)
	// Les includes doivent précéder les excludes pour être effectifs.
	for _, p := range o.Includes {
		a = append(a, "--include="+p)
	}
	for _, p := range o.Excludes {
		a = append(a, "--exclude="+p)
	}
	if rp.BackupDir != "" {
		a = append(a, "--backup", "--backup-dir="+rp.BackupDir)
	}
	if rp.LinkDest != "" {
		a = append(a, "--link-dest="+rp.LinkDest)
	}
	a = append(a,
		"--info=progress2,stats2",
		"--no-human-readable",
		"--outbuf=L",
		"--out-format="+OutFormat,
	)
	if s.DryRun {
		a = append(a, "--dry-run")
	}

	src, err := endpointArg(s.Source, rp.Source, true)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	dst, err := endpointArg(s.Destination, rp.Destination, false)
	if err != nil {
		return nil, fmt.Errorf("destination: %w", err)
	}
	if s.Source.Type == engine.EndpointSSH && s.Destination.Type == engine.EndpointSSH {
		return nil, fmt.Errorf("remote to remote: %w", engine.ErrUnsupported)
	}
	if sshT := sshTarget(s); sshT != nil {
		if ssh == nil {
			return nil, fmt.Errorf("ssh configuration missing")
		}
		e, err := SSHCommand(sshT, ssh)
		if err != nil {
			return nil, err
		}
		a = append(a, "-e", e)
	}
	a = append(a, "--", src, dst)
	return a, nil
}

func sshTarget(s engine.Spec) *engine.SSHTarget {
	if s.Source.Type == engine.EndpointSSH {
		return s.Source.SSH
	}
	if s.Destination.Type == engine.EndpointSSH {
		return s.Destination.SSH
	}
	return nil
}

func endpointArg(e engine.Endpoint, resolved string, isSource bool) (string, error) {
	switch e.Type {
	case engine.EndpointLocal:
		if resolved == "" {
			return "", fmt.Errorf("unresolved local path")
		}
		p := strings.TrimRight(resolved, "/")
		if p == "" {
			p = "/"
		}
		// Sémantique rsync du slash final : "dir/" copie le contenu, "dir" copie le dossier.
		if isSource && e.ContentsOnly && p != "/" {
			p += "/"
		}
		if !isSource {
			p += "/"
		}
		return p, nil
	case engine.EndpointSSH:
		t := e.SSH
		if t == nil {
			return "", fmt.Errorf("ssh target missing")
		}
		if err := ValidateSSHTarget(t); err != nil {
			return "", err
		}
		if err := checkRemotePath(e.Path); err != nil {
			return "", err
		}
		p := e.Path
		if isSource && e.ContentsOnly && !strings.HasSuffix(p, "/") {
			p += "/"
		}
		return t.User + "@" + t.Host + ":" + p, nil
	default:
		return "", fmt.Errorf("endpoint type %q: %w", e.Type, engine.ErrUnsupported)
	}
}

func checkRemotePath(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("empty remote path")
	case strings.HasPrefix(p, "-"):
		return fmt.Errorf("remote path must not start with '-'")
	case strings.ContainsAny(p, "\x00\n\r"):
		return fmt.Errorf("remote path contains control characters")
	case len(p) > 4096:
		return fmt.Errorf("remote path too long")
	}
	return nil
}

// ValidateSSHTarget vérifie hôte, utilisateur et port.
func ValidateSSHTarget(t *engine.SSHTarget) error {
	if !hostRe.MatchString(t.Host) || strings.HasPrefix(t.Host, "-") {
		return fmt.Errorf("invalid ssh host")
	}
	if !userRe.MatchString(t.User) {
		return fmt.Errorf("invalid ssh user")
	}
	if t.Port < 1 || t.Port > 65535 {
		return fmt.Errorf("invalid ssh port")
	}
	if (t.Identity.ManagedID == nil) == !t.Identity.Mounted {
		return fmt.Errorf("exactly one of identity.managed or identity.mounted is required")
	}
	return nil
}

var safeFileRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// SSHCommand construit la valeur de -e. ATTENTION : rsync redécoupe cette
// chaîne sur les espaces ; elle ne contient donc que des constantes, un entier
// et des chemins générés par l'application (vérifiés par safeFileRe).
func SSHCommand(t *engine.SSHTarget, c *SSHConfig) (string, error) {
	base := []string{"ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=15"}
	if c.Mounted {
		if !t.Identity.Mounted {
			return "", fmt.Errorf("identity mismatch")
		}
	} else {
		if !safeFileRe.MatchString(c.KeyFile) || !safeFileRe.MatchString(c.KnownHostsFile) {
			return "", fmt.Errorf("unsafe ssh file path")
		}
		base = append(base, "-F", "/dev/null", "-i", c.KeyFile, "-o", "IdentitiesOnly=yes",
			"-o", "UserKnownHostsFile="+c.KnownHostsFile)
	}
	base = append(base, "-p", strconv.Itoa(t.Port))
	return strings.Join(base, " "), nil
}
