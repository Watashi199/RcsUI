package authz

import (
	"context"
	"testing"

	"rcsui/internal/store"
)

type fakeGrants struct {
	jobs    map[[2]int64]store.JobPermission
	remotes map[int64]map[string]bool
}

func (f fakeGrants) GetJobPermission(_ context.Context, j, u int64) (store.JobPermission, error) {
	return f.jobs[[2]int64{j, u}], nil
}
func (f fakeGrants) ListUserJobPermissions(_ context.Context, u int64) (map[int64]store.JobPermission, error) {
	out := map[int64]store.JobPermission{}
	for k, v := range f.jobs {
		if k[1] == u {
			out[k[0]] = v
		}
	}
	return out, nil
}
func (f fakeGrants) ListRemoteGrants(_ context.Context, u int64) (map[string]bool, error) {
	return f.remotes[u], nil
}

// Matrice rôle × grant × permission (PLAN.md §9).
func TestJobMatrix(t *testing.T) {
	owner := int64(2)
	job := store.Job{ID: 10, OwnerID: &owner}
	g := fakeGrants{jobs: map[[2]int64]store.JobPermission{
		{10, 3}: store.PermRun,
		{10, 4}: store.PermEdit, // viewer avec edit : doit être plafonné à view
		{10, 5}: store.PermView,
	}}
	users := map[string]store.User{
		"admin":          {ID: 1, Role: store.RoleAdmin},
		"owner-op":       {ID: 2, Role: store.RoleOperator},
		"op-run":         {ID: 3, Role: store.RoleOperator},
		"viewer-edit":    {ID: 4, Role: store.RoleViewer},
		"viewer-view":    {ID: 5, Role: store.RoleViewer},
		"op-none":        {ID: 6, Role: store.RoleOperator},
		"disabled-admin": {ID: 7, Role: store.RoleAdmin, Disabled: true},
	}
	want := map[string][3]bool{ // view, run, edit
		"admin":          {true, true, true},
		"owner-op":       {true, true, true},
		"op-run":         {true, true, false},
		"viewer-edit":    {true, false, false},
		"viewer-view":    {true, false, false},
		"op-none":        {false, false, false},
		"disabled-admin": {false, false, false},
	}
	needs := []store.JobPermission{store.PermView, store.PermRun, store.PermEdit}
	for name, u := range users {
		have, err := JobPermission(context.Background(), g, u, job)
		if err != nil {
			t.Fatal(err)
		}
		for i, need := range needs {
			if Allows(have, need) != want[name][i] {
				t.Errorf("%s need %s: got %v (have %q)", name, need, !want[name][i], have)
			}
		}
	}
	vis, _ := VisibleJobs(context.Background(), g, users["viewer-edit"], []store.Job{job})
	if vis[10] != store.PermView {
		t.Errorf("visible viewer-edit=%q", vis[10])
	}
	vis, _ = VisibleJobs(context.Background(), g, users["op-none"], []store.Job{job})
	if len(vis) != 0 {
		t.Errorf("op-none sees %v", vis)
	}
	vis, _ = VisibleJobs(context.Background(), g, users["owner-op"], []store.Job{job})
	if vis[10] != store.PermEdit {
		t.Errorf("owner sees %v", vis)
	}
}

func TestRemotes(t *testing.T) {
	g := fakeGrants{remotes: map[int64]map[string]bool{3: {"b2": true}, 5: {"b2": true}}}
	ctx := context.Background()
	cases := []struct {
		u    store.User
		r    string
		want bool
	}{
		{store.User{ID: 1, Role: store.RoleAdmin}, "any", true},
		{store.User{ID: 3, Role: store.RoleOperator}, "b2", true},
		{store.User{ID: 3, Role: store.RoleOperator}, "s3", false},
		{store.User{ID: 5, Role: store.RoleViewer}, "b2", false},
		{store.User{ID: 3, Role: store.RoleOperator, Disabled: true}, "b2", false},
	}
	for _, c := range cases {
		if got, _ := CanUseRemote(ctx, g, c.u, c.r); got != c.want {
			t.Errorf("%+v %s: %v", c.u, c.r, got)
		}
	}
	if CanCreateJobs(store.User{Role: store.RoleViewer}) || !CanCreateJobs(store.User{Role: store.RoleOperator}) {
		t.Error("CanCreateJobs")
	}
}
