package api

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"rcsui/internal/engine"
	"rcsui/internal/notify"
	"rcsui/internal/secrets"
	"rcsui/internal/store"
)

func TestConfigExportImport(t *testing.T) {
	ctx := context.Background()
	pass := "a long export passphrase"

	// Instance d'origine : une identité SSH, un job SSH qui la référence, un job
	// local, des réglages et des notifications.
	src := newHarness(t)
	withSSH(t, src)
	admin := setupAdmin(t, src)
	code, out := admin.do("POST", "/api/v1/ssh/identities", map[string]any{"name": "nas", "generate": true})
	if code != 201 {
		t.Fatalf("identity: %d %v", code, out)
	}
	identID := int64(out["id"].(float64))
	idents, _ := src.db.ListSSHIdentities(ctx)
	srcPub := idents[0].PublicKey
	if _, err := src.db.CreateJob(ctx, store.Job{Name: "to-nas", Engine: engine.KindRsync, Mode: engine.ModeCopy,
		Source: engine.Endpoint{Type: engine.EndpointLocal, Path: filepath.Join(src.root, "a")},
		Destination: engine.Endpoint{Type: engine.EndpointSSH, Path: "/backup",
			SSH: &engine.SSHTarget{Host: "nas.lan", Port: 22, User: "bk", Identity: engine.SSHIdentity{ManagedID: &identID}}},
		Options: json.RawMessage(`{"v":1}`), ScheduleCron: "0 3 * * *", ScheduleOn: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := src.db.AddKnownHost(ctx, store.KnownHost{Host: "nas.lan", Port: 22, KeyType: "ssh-ed25519",
		PublicKey: "AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl", Fingerprint: "SHA256:x"}); err != nil {
		t.Fatal(err)
	}
	if code, _ := admin.do("PUT", "/api/v1/settings", map[string]any{"maxConcurrent": 3, "rsyncVanishedIsSuccess": false, "logRetentionDays": 30}); code != 200 {
		t.Fatalf("settings: %d", code)
	}
	box, _ := secrets.New(make([]byte, 32))
	src.server.Notifier = &notify.Notifier{DB: src.db, Box: box}
	_ = src.server.Notifier.Load(ctx)
	defer src.server.Notifier.Stop(ctx)
	_ = src.server.Notifier.Save(ctx, notify.Config{On: notify.ModeFailures, NtfyURL: "https://ntfy.example/t", NtfyToken: "tk-secret"})

	if code, _ := admin.do("POST", "/api/v1/config/export", map[string]any{"passphrase": "short"}); code != 400 {
		t.Errorf("short passphrase: %d", code)
	}
	code, file := admin.do("POST", "/api/v1/config/export", map[string]any{"passphrase": pass})
	raw := mustJSON(file)
	if code != 200 || file["format"] != "rcsui-config" || strings.Contains(raw, "to-nas") || strings.Contains(raw, "tk-secret") ||
		strings.Contains(raw, "OPENSSH PRIVATE KEY") {
		t.Fatalf("export: %d %s", code, raw)
	}
	// Réservé à une session : pas de jeton d'API, même admin.
	tok := newToken(t, admin, map[string]any{"name": "ci"})
	if code, _ := bearer(t, src, tok, "POST", "/api/v1/config/export", map[string]any{"passphrase": pass}); code != 403 {
		t.Errorf("export with a token: %d", code)
	}

	// Instance neuve, autre clé maître.
	dst := newHarness(t)
	withSSH(t, dst)
	dst.server.SSH.Box, _ = secrets.New(bytes.Repeat([]byte{9}, 32))
	dstBox, _ := secrets.New(bytes.Repeat([]byte{7}, 32))
	dst.server.Notifier = &notify.Notifier{DB: dst.db, Box: dstBox}
	_ = dst.server.Notifier.Load(ctx)
	defer dst.server.Notifier.Stop(ctx)
	dAdmin := setupAdmin(t, dst)
	if code, out := dAdmin.do("POST", "/api/v1/config/import", map[string]any{"passphrase": "wrong passphrase!!", "file": file}); code != 400 || errCode(out) != "bad_passphrase" {
		t.Fatalf("wrong passphrase: %d %v", code, out)
	}
	code, out = dAdmin.do("POST", "/api/v1/config/import", map[string]any{"passphrase": pass, "file": file})
	if code != 200 || out["jobs"] != float64(1) || out["sshIdentities"] != float64(1) || out["knownHosts"] != float64(1) {
		t.Fatalf("import: %d %v", code, out)
	}
	jobs, _ := dst.db.ListJobs(ctx)
	newIdents, _ := dst.db.ListSSHIdentities(ctx)
	if len(jobs) != 1 || len(newIdents) != 1 || newIdents[0].PublicKey != srcPub {
		t.Fatalf("imported jobs=%+v identities=%+v", jobs, newIdents)
	}
	j := jobs[0]
	if j.Destination.SSH == nil || *j.Destination.SSH.Identity.ManagedID != newIdents[0].ID || !j.ScheduleOn || j.OwnerID == nil {
		t.Errorf("job not remapped: %+v", j)
	}
	// Clé privée rechiffrée avec la clé maître de la nouvelle instance.
	if _, err := dst.server.SSH.PrivatePEM(ctx, newIdents[0].ID); err != nil {
		t.Errorf("identity not readable with the new master key: %v", err)
	}
	if v, _, _ := dst.db.GetSetting(ctx, SettingMaxConcurrent); v != "3" || dst.server.Runner.MaxConcurrent() != 3 {
		t.Errorf("max concurrent not imported: %q %d", v, dst.server.Runner.MaxConcurrent())
	}
	if c := dst.server.Notifier.Config(); c.NtfyToken != "tk-secret" || c.On != notify.ModeFailures {
		t.Errorf("notifications not imported: %+v", c)
	}
	// Jamais de fusion : une instance non vide refuse l'import.
	if code, out := dAdmin.do("POST", "/api/v1/config/import", map[string]any{"passphrase": pass, "file": file}); code != 409 || errCode(out) != "not_empty" {
		t.Errorf("second import: %d %v", code, out)
	}
	// Réservé aux administrateurs.
	op, _ := addUser(t, dst, dAdmin, "op", store.RoleOperator)
	if code, _ := op.do("POST", "/api/v1/config/export", map[string]any{"passphrase": pass}); code != 403 {
		t.Errorf("operator export: %d", code)
	}
	// Audit sans secret.
	entries, _ := dst.db.ListAudit(ctx, 0, 50)
	if a := mustJSON(entries); !strings.Contains(a, "config.import") || strings.Contains(a, pass) {
		t.Errorf("audit: %s", a)
	}
}
