package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode"

	"rcsui/internal/auth"
	"rcsui/internal/authz"
	"rcsui/internal/engine"
	"rcsui/internal/runner"
	"rcsui/internal/scheduler"
	"rcsui/internal/store"
)

type scheduleIn struct {
	Cron    string `json:"cron"`
	Enabled bool   `json:"enabled"`
}

type jobInput struct {
	Name             string          `json:"name"`
	Engine           engine.Kind     `json:"engine"`
	Mode             engine.Mode     `json:"mode"`
	Source           engine.Endpoint `json:"source"`
	Destination      engine.Endpoint `json:"destination"`
	Options          json.RawMessage `json:"options"`
	Schedule         *scheduleIn     `json:"schedule"`
	RetryInterrupted bool            `json:"retryInterrupted"`
	RetryMax         int             `json:"retryMax"`
	RetryDelaySec    int             `json:"retryDelaySec"` // 0 = défaut (300 s)
	CatchUp          bool            `json:"catchUp"`
	// ExclusiveDest : ne pas démarrer si un autre job actif écrit au même endroit.
	ExclusiveDest bool `json:"exclusiveDestination"`
}

// Bornes des relances automatiques.
const (
	maxRetries        = 10
	defaultRetryDelay = 300
	minRetryDelay     = 10
	maxRetryDelay     = 24 * 3600
)

type jobOut struct {
	store.Job
	Permission store.JobPermission `json:"permission"`
	NextRunAt  *time.Time          `json:"nextRunAt,omitempty"`
	RetryAt    *time.Time          `json:"retryAt,omitempty"` // relance automatique en attente
	LastRun    *store.Run          `json:"lastRun,omitempty"`
	Active     bool                `json:"active"`
}

func validJobName(n string) bool {
	if n == "" || len(n) > 100 {
		return false
	}
	for _, c := range n {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

// checkJob valide l'entrée et l'accès aux ressources référencées (remotes).
func (s *Server) checkJob(r *http.Request, cur *auth.Current, in *jobInput) (store.Job, engine.Preview, error) {
	in.Name = strings.TrimSpace(in.Name)
	ve := &engine.ValidationError{}
	if !validJobName(in.Name) {
		ve.Add("name", "required, 1-100 characters, no control characters")
	}
	j := store.Job{Name: in.Name, Engine: in.Engine, Mode: in.Mode, Source: in.Source, Destination: in.Destination,
		Options: in.Options, RetryInterrupted: in.RetryInterrupted, RetryMax: in.RetryMax, RetryDelaySec: in.RetryDelaySec,
		CatchUp: in.CatchUp, ExclusiveDest: in.ExclusiveDest}
	if j.RetryDelaySec == 0 {
		j.RetryDelaySec = defaultRetryDelay
	}
	if j.RetryMax < 0 || j.RetryMax > maxRetries {
		ve.Add("retryMax", "must be between 0 and 10")
	}
	if j.RetryDelaySec < minRetryDelay || j.RetryDelaySec > maxRetryDelay {
		ve.Add("retryDelaySec", "must be between 10 and 86400 seconds")
	}
	if len(j.Options) == 0 || string(j.Options) == "null" {
		j.Options = json.RawMessage(`{"v":1}`)
	}
	if in.Schedule != nil {
		j.ScheduleCron, j.ScheduleOn = strings.TrimSpace(in.Schedule.Cron), in.Schedule.Enabled
		if j.ScheduleCron != "" {
			if _, err := scheduler.Parse(j.ScheduleCron); err != nil {
				ve.Add("schedule.cron", err.Error())
			}
		} else if j.ScheduleOn {
			ve.Add("schedule.cron", "required when the schedule is enabled")
		}
	}
	for field, ep := range map[string]engine.Endpoint{"source": in.Source, "destination": in.Destination} {
		if err := s.checkEndpointAccess(r, cur, field, ep, ve); err != nil {
			return j, engine.Preview{}, err
		}
	}
	if err := ve.Err(); err != nil {
		return j, engine.Preview{}, err
	}
	spec, eng, err := s.Runner.BuildSpec(r.Context(), runner.SnapshotOf(j), "PREVIEW", false)
	if err != nil {
		return j, engine.Preview{}, err
	}
	pv, err := eng.Preview(spec)
	return j, pv, err
}

// checkEndpointAccess vérifie que l'utilisateur peut utiliser les ressources
// désignées par un endpoint (remote rclone, identité SSH). Les erreurs de
// validation vont dans ve ; l'erreur retournée est une erreur interne.
func (s *Server) checkEndpointAccess(r *http.Request, cur *auth.Current, field string, ep engine.Endpoint, ve *engine.ValidationError) error {
	switch ep.Type {
	case engine.EndpointRemote:
		ok, err := authz.CanUseRemote(r.Context(), s.DB, cur.User, ep.Remote)
		if err != nil {
			return err
		}
		if !ok {
			ve.Add(field+".remote", "you are not allowed to use this remote")
		}
	case engine.EndpointSSH:
		if s.SSH == nil {
			ve.Add(field+".type", "ssh is not available on this server")
			return nil
		}
		if ep.SSH == nil {
			ve.Add(field+".ssh", "required")
			return nil
		}
		ok, err := s.canUseIdentity(r, cur, ep.SSH.Identity)
		if err != nil {
			return err
		}
		if !ok {
			ve.Add(field+".ssh.identity", "you are not allowed to use this ssh identity")
		}
	}
	return nil
}

func (s *Server) jobPerm(r *http.Request, cur *auth.Current, j store.Job) (store.JobPermission, error) {
	return authz.JobPermission(r.Context(), s.DB, cur.User, j)
}

// loadJob charge un job et vérifie la permission ; renvoie false si une réponse a été écrite.
func (s *Server) loadJob(w http.ResponseWriter, r *http.Request, cur *auth.Current, need store.JobPermission) (store.Job, store.JobPermission, bool) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return store.Job{}, "", false
	}
	j, err := s.DB.GetJob(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return j, "", false
	}
	p, err := s.jobPerm(r, cur, j)
	if err != nil {
		s.fail(w, r, err)
		return j, "", false
	}
	if !authz.Allows(p, store.PermView) {
		// Ne pas révéler l'existence d'un job invisible.
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return j, "", false
	}
	if !authz.Allows(p, need) {
		writeError(w, http.StatusForbidden, "forbidden", "insufficient permission on this job")
		return j, "", false
	}
	return j, p, true
}

func (s *Server) decorate(j store.Job, p store.JobPermission, last map[int64]store.Run) jobOut {
	o := jobOut{Job: j, Permission: p, Active: s.Runner.IsJobActive(j.ID)}
	if s.Scheduler != nil {
		o.NextRunAt = s.Scheduler.Next(j.ID)
	}
	o.RetryAt = s.Runner.PendingRetry(j.ID)
	if lr, ok := last[j.ID]; ok {
		o.LastRun = &lr
	}
	return o
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	jobs, err := s.DB.ListJobs(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	vis, err := authz.VisibleJobs(r.Context(), s.DB, cur.User, jobs)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	last, _ := s.DB.LastRunsByJob(r.Context())
	out := []jobOut{}
	for _, j := range jobs {
		p := store.PermEdit
		if vis != nil {
			var ok bool
			if p, ok = vis[j.ID]; !ok {
				continue
			}
		}
		out = append(out, s.decorate(j, p, last))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	j, p, ok := s.loadJob(w, r, cur, store.PermView)
	if !ok {
		return
	}
	last, _ := s.DB.LastRunsByJob(r.Context())
	writeJSON(w, http.StatusOK, s.decorate(j, p, last))
}

func (s *Server) handleValidateJob(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	if !authz.CanCreateJobs(cur.User) {
		writeError(w, http.StatusForbidden, "forbidden", "not allowed")
		return
	}
	var in jobInput
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	_, pv, err := s.checkJob(r, cur, &in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "preview": pv})
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	if !authz.CanCreateJobs(cur.User) {
		writeError(w, http.StatusForbidden, "forbidden", "your role cannot create jobs")
		return
	}
	var in jobInput
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	j, _, err := s.checkJob(r, cur, &in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	uid := cur.User.ID
	j.OwnerID = &uid
	id, err := s.DB.CreateJob(r.Context(), j)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	j, _ = s.DB.GetJob(r.Context(), id)
	if s.Scheduler != nil {
		if err := s.Scheduler.Sync(j); err != nil {
			s.Log.Warn("schedule sync", "job", j.Name, "err", err)
		}
	}
	s.audit(r, cur, "job.create", "job:"+j.Name, map[string]any{"id": id, "engine": j.Engine})
	writeJSON(w, http.StatusCreated, s.decorate(j, store.PermEdit, nil))
}

func (s *Server) handleUpdateJob(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	old, p, ok := s.loadJob(w, r, cur, store.PermEdit)
	if !ok {
		return
	}
	var in jobInput
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	j, _, err := s.checkJob(r, cur, &in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	j.ID, j.OwnerID = old.ID, old.OwnerID
	if err := s.DB.UpdateJob(r.Context(), j); err != nil {
		s.fail(w, r, err)
		return
	}
	j, _ = s.DB.GetJob(r.Context(), j.ID)
	if j.RetryMax == 0 {
		s.Runner.CancelRetry(j.ID)
	}
	if s.Scheduler != nil {
		if err := s.Scheduler.Sync(j); err != nil {
			s.Log.Warn("schedule sync", "job", j.Name, "err", err)
		}
	}
	s.audit(r, cur, "job.update", "job:"+j.Name, map[string]any{"id": j.ID})
	last, _ := s.DB.LastRunsByJob(r.Context())
	writeJSON(w, http.StatusOK, s.decorate(j, p, last))
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	j, _, ok := s.loadJob(w, r, cur, store.PermEdit)
	if !ok {
		return
	}
	if s.Runner.IsJobActive(j.ID) {
		writeError(w, http.StatusConflict, "job_active", "cannot delete a job while it is running")
		return
	}
	if s.Scheduler != nil {
		s.Scheduler.Remove(j.ID)
	}
	s.Runner.CancelRetry(j.ID)
	if err := s.DB.DeleteJob(r.Context(), j.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.audit(r, cur, "job.delete", "job:"+j.Name, map[string]any{"id": j.ID})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRunJob(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	j, p, ok := s.loadJob(w, r, cur, store.PermRun)
	if !ok {
		return
	}
	var in struct {
		DryRun bool `json:"dryRun"`
		// Resync (bisync) : reconstruit les listings de référence. Il peut
		// faire réapparaître des fichiers supprimés d'un côté : réservé aux
		// utilisateurs qui peuvent modifier le job.
		Resync bool `json:"resync"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if in.Resync && !authz.Allows(p, store.PermEdit) {
		writeError(w, http.StatusForbidden, "forbidden", "resync requires the edit permission on this job")
		return
	}
	uid := cur.User.ID
	id, err := s.Runner.SubmitWith(r.Context(), j.ID, store.TriggerManual, &uid,
		runner.RunOptions{DryRun: in.DryRun, Resync: in.Resync})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.audit(r, cur, "run.start", "job:"+j.Name, map[string]any{"run": id, "dryRun": in.DryRun, "resync": in.Resync})
	writeJSON(w, http.StatusAccepted, map[string]string{"runId": id})
}

func (s *Server) handleGetJobGrants(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	j, p, ok := s.loadJob(w, r, cur, store.PermView)
	if !ok {
		return
	}
	if !authz.IsAdmin(cur.User) && p != store.PermEdit {
		writeError(w, http.StatusForbidden, "forbidden", "insufficient permission")
		return
	}
	g, err := s.DB.ListJobGrants(r.Context(), j.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": g})
}

func (s *Server) handlePutJobGrants(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	j, _, ok := s.loadJob(w, r, cur, store.PermView)
	if !ok {
		return
	}
	// Seuls l'admin et le propriétaire du job gèrent ses droits.
	isOwner := j.OwnerID != nil && *j.OwnerID == cur.User.ID
	if !authz.IsAdmin(cur.User) && !isOwner {
		writeError(w, http.StatusForbidden, "forbidden", "only an administrator or the job owner can manage grants")
		return
	}
	var in []store.JobGrant
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	for _, g := range in {
		if g.Permission.Level() == 0 {
			writeError(w, http.StatusBadRequest, "validation", "invalid permission")
			return
		}
		if _, err := s.DB.GetUser(r.Context(), g.UserID); err != nil {
			writeError(w, http.StatusBadRequest, "validation", "unknown user")
			return
		}
	}
	if err := s.DB.SetJobGrants(r.Context(), j.ID, in); err != nil {
		s.fail(w, r, err)
		return
	}
	s.audit(r, cur, "job.grants", "job:"+j.Name, map[string]any{"grants": in})
	writeJSON(w, http.StatusOK, map[string]any{"items": in})
}

func (s *Server) handleSchedulePreview(w http.ResponseWriter, r *http.Request, _ *auth.Current) {
	next, err := scheduler.Preview(r.URL.Query().Get("cron"), 5, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"next": next})
}
