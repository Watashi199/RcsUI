package rclone

import (
	"testing"
)

func TestExportImportRemotes(t *testing.T) {
	c := client(t)
	t.Cleanup(func() {
		for _, n := range []string{"xbase", "xalias"} {
			_ = c.DeleteRemote(n)
		}
	})

	if exp := c.ExportRemotes(); exp["mem"]["type"] != "memory" {
		t.Fatalf("export: %v", exp)
	}

	// L'alias référence xbase : créé après lui malgré l'ordre alphabétique.
	created, failed := c.ImportRemotes(map[string]map[string]string{
		"xalias":  {"type": "alias", "remote": "xbase:sub"},
		"xbase":   {"type": "memory", "description": "obscured-secret-kept-as-is"},
		"xlocal":  {"type": "local"},
		"xbroken": {"type": "alias", "remote": "missing:"},
		"mem":     {"type": "memory"},
	}, RemoteValidator{})
	if len(created) != 2 || created[0] != "xbase" || created[1] != "xalias" {
		t.Fatalf("created %v failed %v", created, failed)
	}
	for _, n := range []string{"xlocal", "xbroken", "mem"} {
		if failed[n] == "" {
			t.Errorf("%s accepted: %v", n, failed)
		}
	}
	exp := c.ExportRemotes()
	if exp["xalias"]["remote"] != "xbase:sub" || exp["xbase"]["description"] != "obscured-secret-kept-as-is" {
		t.Errorf("values not stored as-is: %v %v", exp["xalias"], exp["xbase"])
	}
}
