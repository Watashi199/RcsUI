package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"rcsui/internal/auth"
	"rcsui/internal/backup"
	"rcsui/internal/engine"
	"rcsui/internal/engine/rclone"
	"rcsui/internal/notify"
	"rcsui/internal/runner"
	"rcsui/internal/store"
)

// Export / import de la configuration (M11). Le fichier est chiffré par une
// phrase de passe (internal/backup) et contient jobs, remotes rclone,
// identités SSH (clés privées), hôtes approuvés, réglages et notifications.
// Pas de comptes, de droits ni de jetons. L'import n'est possible que sur une
// instance vide (aucun job, remote ni identité) : jamais de fusion.

// configBundle est le contenu (déchiffré) du fichier.
type configBundle struct {
	RcsUIVersion  string                       `json:"rcsuiVersion"`
	ExportedAt    time.Time                    `json:"exportedAt"`
	Jobs          []store.Job                  `json:"jobs"`
	Remotes       map[string]map[string]string `json:"remotes,omitempty"`
	SSHIdentities []bundleIdentity             `json:"sshIdentities,omitempty"`
	KnownHosts    []store.KnownHost            `json:"knownHosts,omitempty"`
	Settings      map[string]string            `json:"settings,omitempty"`
	Notifications *notify.Config               `json:"notifications,omitempty"`
}

type bundleIdentity struct {
	ID         int64  `json:"id"` // id d'origine, référencé par les jobs
	Name       string `json:"name"`
	PrivateKey string `json:"privateKey"` // PEM OpenSSH
}

var exportedSettings = []string{SettingMaxConcurrent, SettingVanishedSuccess, SettingLogRetention}

func (s *Server) handleExportConfig(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	var in struct {
		Passphrase string `json:"passphrase"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := backup.CheckPassphrase(in.Passphrase); err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	ctx := r.Context()
	b := configBundle{RcsUIVersion: s.Info.Version, ExportedAt: time.Now().UTC(), Settings: map[string]string{}}
	jobs, err := s.DB.ListJobs(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, j := range jobs {
		j.OwnerID = nil
		b.Jobs = append(b.Jobs, j)
	}
	if s.Rclone != nil {
		b.Remotes = s.Rclone.ExportRemotes()
	}
	idents, err := s.DB.ListSSHIdentities(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, id := range idents {
		if s.SSH == nil {
			break
		}
		pem, err := s.SSH.PrivatePEM(ctx, id.ID)
		if err != nil {
			s.fail(w, r, fmt.Errorf("ssh identity %q: %w", id.Name, err))
			return
		}
		b.SSHIdentities = append(b.SSHIdentities, bundleIdentity{ID: id.ID, Name: id.Name, PrivateKey: string(pem)})
	}
	if b.KnownHosts, err = s.DB.ListKnownHosts(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	for _, k := range exportedSettings {
		if v, ok, err := s.DB.GetSetting(ctx, k); err == nil && ok {
			b.Settings[k] = v
		}
	}
	if s.Notifier != nil {
		c := s.Notifier.Config()
		b.Notifications = &c
	}
	plain, err := json.Marshal(b)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	file, err := backup.Seal(plain, in.Passphrase)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.audit(r, cur, "config.export", "", map[string]any{"jobs": len(b.Jobs), "remotes": len(b.Remotes),
		"sshIdentities": len(b.SSHIdentities)})
	name := "rcsui-config-" + time.Now().Format("20060102-150405") + ".json"
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(file)
}

// importResult résume un import.
type importResult struct {
	Jobs          int      `json:"jobs"`
	Remotes       int      `json:"remotes"`
	SSHIdentities int      `json:"sshIdentities"`
	KnownHosts    int      `json:"knownHosts"`
	Warnings      []string `json:"warnings"`
}

func (s *Server) instanceEmpty(r *http.Request) (bool, error) {
	jobs, err := s.DB.ListJobs(r.Context())
	if err != nil || len(jobs) > 0 {
		return false, err
	}
	idents, err := s.DB.ListSSHIdentities(r.Context())
	if err != nil || len(idents) > 0 {
		return false, err
	}
	if s.Rclone != nil && len(s.Rclone.ExportRemotes()) > 0 {
		return false, nil
	}
	return true, nil
}

func (s *Server) handleImportConfig(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	var in struct {
		Passphrase string          `json:"passphrase"`
		File       json.RawMessage `json:"file"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if ok, wait := s.Limiter.Allow("config-import:" + strconv.FormatInt(cur.User.ID, 10)); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts")
		return
	}
	empty, err := s.instanceEmpty(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !empty {
		writeError(w, http.StatusConflict, "not_empty", "import requires an instance without jobs, remotes or SSH identities")
		return
	}
	plain, err := backup.Open(in.File, in.Passphrase)
	if err != nil {
		if errors.Is(err, backup.ErrPassphrase) {
			writeError(w, http.StatusBadRequest, "bad_passphrase", err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, "bad_file", err.Error())
		return
	}
	var b configBundle
	if err := json.Unmarshal(plain, &b); err != nil {
		writeError(w, http.StatusBadRequest, "bad_file", "invalid export content")
		return
	}
	res := s.applyImport(r, cur, b)
	s.audit(r, cur, "config.import", "", map[string]any{"jobs": res.Jobs, "remotes": res.Remotes,
		"sshIdentities": res.SSHIdentities, "warnings": len(res.Warnings)})
	writeJSON(w, http.StatusOK, res)
}

// applyImport recrée la configuration, au mieux : un élément refusé (chemin
// hors des racines de cette instance, type de remote interdit...) est signalé
// dans Warnings sans interrompre le reste.
func (s *Server) applyImport(r *http.Request, cur *auth.Current, b configBundle) importResult {
	ctx := r.Context()
	res := importResult{Warnings: []string{}}
	warn := func(format string, a ...any) { res.Warnings = append(res.Warnings, fmt.Sprintf(format, a...)) }

	// Identités SSH : nouveaux ids, rechiffrées avec la clé maître de cette instance.
	idMap := map[int64]int64{}
	for _, id := range b.SSHIdentities {
		if s.SSH == nil {
			warn("ssh identity %q skipped: ssh is not available", id.Name)
			continue
		}
		created, err := s.SSH.Import(ctx, id.Name, id.PrivateKey, "")
		if err != nil {
			warn("ssh identity %q: %v", id.Name, err)
			continue
		}
		idMap[id.ID] = created.ID
		res.SSHIdentities++
	}
	for _, h := range b.KnownHosts {
		h.AddedBy = &cur.User.ID
		if _, err := s.DB.AddKnownHost(ctx, h); err != nil {
			warn("known host %s:%d: %v", h.Host, h.Port, err)
			continue
		}
		res.KnownHosts++
	}
	if s.SSH != nil && res.KnownHosts > 0 {
		if err := s.SSH.WriteKnownHosts(ctx); err != nil {
			warn("known_hosts: %v", err)
		}
	}

	if len(b.Remotes) > 0 {
		if s.Rclone == nil {
			warn("%d remotes skipped: rclone is disabled", len(b.Remotes))
		} else {
			created, failed := s.Rclone.ImportRemotes(b.Remotes, rclone.RemoteValidator{Paths: s.Paths})
			res.Remotes = len(created)
			for name, why := range failed {
				warn("remote %q: %s", name, why)
			}
		}
	}

	for _, j := range b.Jobs {
		if !remapIdentity(&j.Source, idMap) || !remapIdentity(&j.Destination, idMap) {
			warn("job %q: its SSH identity was not imported", j.Name)
		}
		uid := cur.User.ID
		j.ID, j.OwnerID = 0, &uid
		id, err := s.DB.CreateJob(ctx, j)
		if err != nil {
			warn("job %q: %v", j.Name, err)
			continue
		}
		j.ID = id
		res.Jobs++
		// Créé même s'il ne passe pas la validation ici (racines différentes...) :
		// il reste modifiable, et le lancement revalide tout.
		if _, _, err := s.Runner.BuildSpec(ctx, runner.SnapshotOf(j), "IMPORT", false); err != nil {
			warn("job %q needs attention: %v", j.Name, err)
		}
		if s.Scheduler != nil {
			if err := s.Scheduler.Sync(j); err != nil {
				warn("job %q schedule: %v", j.Name, err)
			}
		}
	}

	for _, k := range exportedSettings {
		if v, ok := b.Settings[k]; ok {
			if err := s.DB.SetSetting(ctx, k, v); err != nil {
				warn("setting %s: %v", k, err)
			}
		}
	}
	if v, ok := b.Settings[SettingMaxConcurrent]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			s.Runner.SetMaxConcurrent(n)
		}
	}
	if b.Notifications != nil && s.Notifier != nil {
		if err := s.Notifier.Save(ctx, *b.Notifications); err != nil {
			warn("notifications: %v", err)
		}
	}
	return res
}

// remapIdentity remplace l'id d'identité SSH gérée par celui de cette
// instance ; false si l'identité n'a pas été importée (référence retirée).
func remapIdentity(e *engine.Endpoint, idMap map[int64]int64) bool {
	if e.SSH == nil || e.SSH.Identity.ManagedID == nil {
		return true
	}
	if n, ok := idMap[*e.SSH.Identity.ManagedID]; ok {
		e.SSH.Identity.ManagedID = &n
		return true
	}
	e.SSH.Identity.ManagedID = nil
	return false
}
