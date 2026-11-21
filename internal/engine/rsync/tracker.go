package rsync

import (
	"strings"

	"rcsui/internal/engine"
)

// Tracker agrège les lignes stdout parsées en une engine.Progress.
// Il n'est pas thread-safe : l'appelant (la goroutine de lecture) le protège.
type Tracker struct {
	p     engine.Progress
	stats map[string]int64
}

// NewTracker crée un tracker en phase de scan.
func NewTracker() *Tracker {
	return &Tracker{p: engine.Progress{Phase: engine.PhaseScanning}, stats: map[string]int64{}}
}

// Apply intègre une ligne ; retourne true si la progression a changé.
func (t *Tracker) Apply(l Line) bool {
	switch l.Kind {
	case LineProgress:
		pr := l.Progress
		t.p.Phase = engine.PhaseTransferring
		t.p.BytesDone = pr.Bytes
		if pr.Percent > 0 && pr.Percent < 100 {
			// Estimation : rsync calcule le pourcentage sur le total connu à cet instant.
			est := pr.Bytes * 100 / int64(pr.Percent)
			if est > t.p.BytesTotal {
				t.p.BytesTotal = est
			}
		}
		if t.p.BytesTotal < t.p.BytesDone {
			t.p.BytesTotal = t.p.BytesDone
		}
		t.p.SpeedBps = pr.SpeedBps
		eta := int64(pr.ETA.Seconds())
		t.p.ETASec = &eta
		if pr.HasChk {
			t.p.FilesDone = pr.Xfr
			t.p.FilesTotal = pr.Total
			t.p.TotalsExact = pr.Exact
		}
		return true
	case LineItem:
		if l.Item.Op == OpDelete {
			t.p.Deletes++
			return true
		}
	case LineStat:
		t.stats[l.Stat.Key] = l.Stat.Value
		t.p.Phase = engine.PhaseFinalizing
	}
	return false
}

// AddError incrémente le compteur d'erreurs (lignes stderr).
func (t *Tracker) AddError() { t.p.Errors++ }

// Progress retourne l'instantané courant.
func (t *Tracker) Progress() engine.Progress { return t.p }

// Final retourne la progression finale, en privilégiant les statistiques
// rsync (--info=stats2), plus fiables que la dernière ligne de progression.
func (t *Tracker) Final() engine.Progress {
	p := t.p
	p.ETASec = nil
	p.Current = nil
	if v, ok := t.stats["Number of regular files transferred"]; ok {
		p.FilesDone = v
		p.FilesTotal = v
		p.TotalsExact = true
	}
	if v, ok := t.stats["Total transferred file size"]; ok {
		p.BytesDone = v
		p.BytesTotal = v
	}
	for k, v := range t.stats {
		if strings.HasPrefix(k, "Number of deleted files") {
			p.Deletes = v
		}
	}
	return p
}

// Stats retourne les statistiques brutes collectées.
func (t *Tracker) Stats() map[string]int64 { return t.stats }

// ToEngineItem convertit un item rsync en item commun.
func ToEngineItem(it Item) engine.Item {
	op := engine.ItemOther
	switch it.Op {
	case OpTransfer, OpHardlink:
		op = engine.ItemTransfer
	case OpCreate:
		op = engine.ItemCreate
	case OpDelete:
		op = engine.ItemDelete
	case OpAttrs:
		op = engine.ItemAttrs
	}
	return engine.Item{Op: op, Kind: it.Type, Path: it.Path, Size: it.Size, Flags: it.Flags}
}

// StderrLevel déduit le niveau d'une ligne stderr rsync.
func StderrLevel(s string) engine.LogLevel {
	switch {
	case strings.HasPrefix(s, "rsync error:"):
		return engine.LevelError
	case strings.Contains(s, "file has vanished"):
		return engine.LevelWarn
	case strings.HasPrefix(s, "rsync:") || strings.HasPrefix(s, "rsync warning:"):
		return engine.LevelWarn
	default:
		return engine.LevelWarn
	}
}
