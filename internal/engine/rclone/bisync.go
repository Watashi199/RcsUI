package rclone

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"rcsui/internal/engine"
)

// BisyncOptions regroupe les options propres au mode bisync (sync/bisync,
// cmd/bisync/rc.go en v1.76.0). Les valeurs vides prennent le défaut rclone.
type BisyncOptions struct {
	// Résolution des conflits (fichier modifié des deux côtés).
	ConflictResolve string `json:"conflictResolve,omitempty"` // none|newer|older|larger|smaller|path1|path2
	ConflictLoser   string `json:"conflictLoser,omitempty"`   // num|pathname|delete
	ConflictSuffix  string `json:"conflictSuffix,omitempty"`
	// Gagnant lors d'un resync quand un fichier diffère des deux côtés.
	ResyncMode string `json:"resyncMode,omitempty"` // path1|path2|newer|older|larger|smaller
	// Critères de comparaison, liste de size, modtime, checksum.
	Compare string `json:"compare,omitempty"`
	// Garde-fou : abandon si plus de N % des fichiers seraient supprimés d'un côté (rclone : 50).
	MaxDeletePercent *int `json:"maxDeletePercent,omitempty"`
	CheckAccess      bool `json:"checkAccess,omitempty"`
	RemoveEmptyDirs  bool `json:"removeEmptyDirs,omitempty"`
	// Resilient : une erreur non critique n'impose pas de resync au run suivant.
	Resilient bool `json:"resilient,omitempty"`
	// NoRecover désactive --recover (activé par défaut ici : reprise propre
	// après une interruption, sans resync).
	NoRecover bool   `json:"noRecover,omitempty"`
	MaxLock   string `json:"maxLock,omitempty"`
}

var (
	conflictResolveVals = set("none", "newer", "older", "larger", "smaller", "path1", "path2")
	conflictLoserVals   = set("num", "pathname", "delete")
	resyncModeVals      = set("path1", "path2", "newer", "older", "larger", "smaller")
	compareVals         = set("size", "modtime", "checksum")
	suffixRe            = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}(,[A-Za-z0-9._-]{1,32})?$`)
)

func set(v ...string) map[string]bool {
	m := make(map[string]bool, len(v))
	for _, s := range v {
		m[s] = true
	}
	return m
}

func (b *BisyncOptions) validate(ve *engine.ValidationError) {
	if b.ConflictResolve != "" && !conflictResolveVals[b.ConflictResolve] {
		ve.Add("options.bisync.conflictResolve", "invalid value")
	}
	if b.ConflictLoser != "" && !conflictLoserVals[b.ConflictLoser] {
		ve.Add("options.bisync.conflictLoser", "invalid value")
	}
	if b.ConflictSuffix != "" && !suffixRe.MatchString(b.ConflictSuffix) {
		ve.Add("options.bisync.conflictSuffix", "letters, digits, '.', '_' or '-' (one suffix, or two separated by a comma)")
	}
	if b.ResyncMode != "" && !resyncModeVals[b.ResyncMode] {
		ve.Add("options.bisync.resyncMode", "invalid value")
	}
	if b.Compare != "" {
		for _, c := range strings.Split(b.Compare, ",") {
			if !compareVals[c] {
				ve.Add("options.bisync.compare", "comma-separated list of size, modtime, checksum")
				break
			}
		}
	}
	if b.MaxDeletePercent != nil && (*b.MaxDeletePercent < 0 || *b.MaxDeletePercent > 100) {
		ve.Add("options.bisync.maxDeletePercent", "must be between 0 and 100")
	}
	if b.MaxLock != "" && !durationRe.MatchString(b.MaxLock) {
		ve.Add("options.bisync.maxLock", "invalid duration (e.g. 2m)")
	}
}

// bisyncIncompatible liste les options génériques sans sens (ou dangereuses)
// en bisync, avec l'alternative à proposer.
func bisyncIncompatible(o *Options) map[string]string {
	bad := map[string]string{}
	if o.Checksum || o.SizeOnly {
		bad["options.checksum"] = "not used by bisync; set bisync.compare instead"
	}
	if o.MaxDelete != nil {
		bad["options.maxDelete"] = "not used by bisync; set bisync.maxDeletePercent instead"
	}
	if o.IgnoreExisting || o.Update || o.TrackRenames || o.DeleteEmptySrcDirs || o.DeleteExcluded {
		bad["options"] = "ignoreExisting, update, trackRenames, deleteEmptySrcDirs and deleteExcluded are not supported with bisync"
	}
	if o.MaxAge != "" || o.MinAge != "" || o.MaxSize != "" || o.MinSize != "" {
		bad["options.maxAge"] = "age and size filters are not supported with bisync (files leaving the filter would look deleted)"
	}
	return bad
}

// bisyncFilters retourne le contenu du fichier de filtres et son chemin.
// Le nom dérive du contenu : modifier les filtres change le fichier, et
// bisync (qui mémorise le hash du fichier au resync) exige alors un resync
// au lieu d'interpréter les fichiers nouvellement filtrés comme supprimés.
func bisyncFilters(dir string, o *Options) (string, []byte) {
	var b strings.Builder
	b.WriteString("# rcsui: generated, do not edit\n")
	for _, p := range o.Includes {
		b.WriteString("+ " + p + "\n")
	}
	for _, p := range o.Excludes {
		b.WriteString("- " + p + "\n")
	}
	content := []byte(b.String())
	sum := sha256.Sum256(content)
	return filepath.Join(dir, "filters", hex.EncodeToString(sum[:8])+".filter"), content
}

func (e *Engine) buildBisync(s engine.Spec, o *Options, src, dst string, ve *engine.ValidationError) built {
	for f, msg := range bisyncIncompatible(o) {
		ve.Add(f, msg)
	}
	if e.BisyncDir == "" {
		ve.Add("mode", "bisync is not available (no working directory configured)")
	}
	if ve.Err() != nil {
		return built{}
	}
	b := o.Bisync
	if b == nil {
		b = &BisyncOptions{}
	}
	cfg := map[string]any{"DryRun": s.DryRun, "LogLevel": "INFO"}
	if o.Transfers != nil {
		cfg["Transfers"] = *o.Transfers
	}
	if o.Checkers != nil {
		cfg["Checkers"] = *o.Checkers
	}
	filtersPath, filters := bisyncFilters(e.BisyncDir, o)
	in := map[string]any{
		"path1": src, "path2": dst,
		"workdir":            e.BisyncDir,
		"filtersFile":        filtersPath,
		"dryRun":             s.DryRun,
		"resync":             s.Resync,
		"recover":            !b.NoRecover,
		"createEmptySrcDirs": o.CreateEmptySrcDirs,
		"removeEmptyDirs":    b.RemoveEmptyDirs,
		"checkAccess":        b.CheckAccess,
		"resilient":          b.Resilient,
		"_config":            cfg,
	}
	for k, v := range map[string]string{"conflictResolve": b.ConflictResolve, "conflictLoser": b.ConflictLoser,
		"conflictSuffix": b.ConflictSuffix, "resyncMode": b.ResyncMode, "compare": b.Compare, "maxLock": b.MaxLock} {
		if v != "" {
			in[k] = v
		}
	}
	if b.MaxDeletePercent != nil {
		in["maxDelete"] = *b.MaxDeletePercent
	}
	return built{method: "sync/bisync", in: in, files: map[string][]byte{filtersPath: filters}}
}

// writeFileIfChanged écrit content dans p (0600, répertoire 0700) de façon
// atomique, sauf si le fichier a déjà ce contenu. Les fichiers de filtres
// sont nommés d'après leur contenu : la réécriture est donc rare.
func writeFileIfChanged(p string, content []byte) error {
	if cur, err := os.ReadFile(p); err == nil && bytes.Equal(cur, content) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// logLineRe découpe une ligne du log texte rclone : "2006/01/02 15:04:05 LEVEL : message".
var logLineRe = regexp.MustCompile(`^\d{4}/\d\d/\d\d \d\d:\d\d:\d\d(?:\.\d+)? ([A-Z]+)\s*: ?(.*)$`)

// bisyncOutputLines extrait les messages capturés par bisync (champ "output"
// du résultat rc). Ce sont ceux journalisés sans contexte, donc sans groupe
// de stats : ils n'arrivent pas en direct dans le log du run. En pratique,
// les erreurs critiques et leurs consignes (« must run --resync ») ; le récit
// de niveau INFO est filtré par le niveau de log global de rclone.
func bisyncOutputLines(out map[string]any) []engine.LogLine {
	s := outputText(out)
	if s == "" {
		return nil
	}
	var lines []engine.LogLine
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, "\r ")
		if l == "" {
			continue
		}
		ll := engine.LogLine{Stream: "rclone", Level: engine.LevelInfo, Msg: l}
		if m := logLineRe.FindStringSubmatch(l); m != nil {
			ll.Msg = m[2]
			switch m[1] {
			case "ERROR", "CRITICAL", "EMERGENCY", "ALERT":
				ll.Level = engine.LevelError
			case "NOTICE", "WARNING":
				ll.Level = engine.LevelWarn
			case "DEBUG":
				ll.Level = engine.LevelDebug
			}
		}
		lines = append(lines, ll)
	}
	return lines
}

// outputText retourne le champ "output" d'un résultat rc, sans codes ANSI.
func outputText(out map[string]any) string {
	s, _ := out["output"].(string)
	return ansiRe.ReplaceAllString(s, "")
}

// bisyncNeedsResync indique une erreur que seul un resync peut lever (premier
// run, filtres modifiés, listings invalidés par une erreur critique).
func bisyncNeedsResync(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "--resync") || strings.Contains(m, "must run resync") ||
		strings.Contains(m, "cannot find prior path1 or path2 listings")
}
