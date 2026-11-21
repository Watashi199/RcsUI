// Package authz centralise la politique d'autorisation (PLAN.md §8, risque 10).
//
// Rôles : admin (tout), operator (crée des jobs, agit sur les jobs accordés,
// utilise les remotes accordés), viewer (lecture des jobs accordés).
// Droits par job : view < run < edit. Le créateur d'un job reçoit "edit".
package authz

import (
	"context"

	"rcsui/internal/store"
)

// Grants fournit les droits stockés.
type Grants interface {
	GetJobPermission(ctx context.Context, jobID, userID int64) (store.JobPermission, error)
	ListUserJobPermissions(ctx context.Context, userID int64) (map[int64]store.JobPermission, error)
	ListRemoteGrants(ctx context.Context, userID int64) (map[string]bool, error)
}

// IsAdmin indique si l'utilisateur est administrateur actif.
func IsAdmin(u store.User) bool { return u.Role == store.RoleAdmin && !u.Disabled }

// CanCreateJobs : admin et operator.
func CanCreateJobs(u store.User) bool {
	return !u.Disabled && (u.Role == store.RoleAdmin || u.Role == store.RoleOperator)
}

// capFor plafonne une permission selon le rôle : un viewer ne peut que voir,
// même si un grant "run" ou "edit" lui a été donné par erreur.
func capFor(u store.User, p store.JobPermission) store.JobPermission {
	if u.Disabled {
		return ""
	}
	if u.Role == store.RoleViewer && p.Level() > store.PermView.Level() {
		return store.PermView
	}
	return p
}

func isOwner(u store.User, j store.Job) bool {
	return j.OwnerID != nil && *j.OwnerID == u.ID && u.Role == store.RoleOperator && !u.Disabled
}

// JobPermission retourne la permission effective d'un utilisateur sur un job.
func JobPermission(ctx context.Context, g Grants, u store.User, j store.Job) (store.JobPermission, error) {
	if IsAdmin(u) {
		return store.PermEdit, nil
	}
	if isOwner(u, j) {
		return store.PermEdit, nil
	}
	p, err := g.GetJobPermission(ctx, j.ID, u.ID)
	if err != nil {
		return "", err
	}
	return capFor(u, p), nil
}

// Allows indique si `have` satisfait `need`.
func Allows(have, need store.JobPermission) bool {
	return have != "" && have.Level() >= need.Level()
}

// VisibleJobs retourne, pour un non-admin, les permissions par job visible.
// Pour un admin, retourne nil (= tout est visible).
func VisibleJobs(ctx context.Context, g Grants, u store.User, jobs []store.Job) (map[int64]store.JobPermission, error) {
	if IsAdmin(u) {
		return nil, nil
	}
	perms, err := g.ListUserJobPermissions(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	out := map[int64]store.JobPermission{}
	for _, j := range jobs {
		p := capFor(u, perms[j.ID])
		if isOwner(u, j) {
			p = store.PermEdit
		}
		if p != "" {
			out[j.ID] = p
		}
	}
	return out, nil
}

// CanUseRemote indique si l'utilisateur peut utiliser un remote (jobs, explorateur).
func CanUseRemote(ctx context.Context, g Grants, u store.User, remote string) (bool, error) {
	if IsAdmin(u) {
		return true, nil
	}
	if u.Disabled || u.Role != store.RoleOperator {
		return false, nil
	}
	m, err := g.ListRemoteGrants(ctx, u.ID)
	if err != nil {
		return false, err
	}
	return m[remote], nil
}

// CanBrowseLocal : admin et operator (les racines autorisées restent appliquées).
func CanBrowseLocal(u store.User) bool { return CanCreateJobs(u) }
