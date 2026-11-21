// Spike M0 : vérifie empiriquement les hypothèses du PLAN.md §6 sur librclone.
// go run ./spikes/rclone
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/rclone/rclone/backend/local"
	_ "github.com/rclone/rclone/backend/memory"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/accounting"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/log"
	_ "github.com/rclone/rclone/fs/operations"
	"github.com/rclone/rclone/fs/rc/jobs"
	_ "github.com/rclone/rclone/fs/sync"
	"github.com/rclone/rclone/librclone/librclone"
)

type capture struct {
	inner slog.Handler
	mu    sync.Mutex
	byKey map[string][]string
}

func (c *capture) Enabled(ctx context.Context, l slog.Level) bool { return c.inner.Enabled(ctx, l) }
func (c *capture) WithAttrs(a []slog.Attr) slog.Handler          { return c }
func (c *capture) WithGroup(string) slog.Handler                  { return c }
func (c *capture) Handle(ctx context.Context, r slog.Record) error {
	key := "unattributed"
	if g, ok := accounting.StatsGroupFromContext(ctx); ok {
		key = "group=" + g
	} else if id, ok := jobs.GetJobID(ctx); ok {
		key = fmt.Sprintf("job=%d", id)
	}
	msg := r.Message
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "object" {
			msg = a.Value.String() + ": " + msg
		}
		return true
	})
	c.mu.Lock()
	c.byKey[key] = append(c.byKey[key], r.Level.String()+" "+msg)
	c.mu.Unlock()
	return nil
}

func rpc(method string, in any) map[string]any {
	b, _ := json.Marshal(in)
	out, status := librclone.RPC(method, string(b))
	var m map[string]any
	_ = json.Unmarshal([]byte(out), &m)
	if status != 200 {
		fmt.Printf("  RPC %s -> %d %s\n", method, status, strings.TrimSpace(out))
	}
	return m
}

func main() {
	tmp, _ := os.MkdirTemp("", "rcsui-spike")
	defer os.RemoveAll(tmp)
	cfg := filepath.Join(tmp, "rclone.conf")
	os.WriteFile(cfg, nil, 0o600)
	if err := config.SetConfigPath(cfg); err != nil {
		panic(err)
	}

	librclone.Initialize()
	defer librclone.Finalize()
	capt := &capture{inner: log.Handler, byKey: map[string][]string{}}
	fs.SetLogger(capt)

	fmt.Println("Q1 config path:", rpc("config/paths", map[string]any{})["config"], "(attendu", cfg+")")

	src := filepath.Join(tmp, "src")
	os.MkdirAll(filepath.Join(src, "sub"), 0o755)
	for i := 0; i < 5; i++ {
		os.WriteFile(filepath.Join(src, "sub", fmt.Sprintf("f%d.txt", i)), []byte(strings.Repeat("x", 1000*(i+1))), 0o644)
	}
	dst := filepath.Join(tmp, "dst")

	// Q2 : dry-run via _config.DryRun + _group + _async
	r := rpc("sync/copy", map[string]any{
		"srcFs": src, "dstFs": dst, "_async": true, "_group": "run/dry",
		"_config": map[string]any{"DryRun": true, "LogLevel": "INFO"},
	})
	waitJob(r["jobid"])
	st := rpc("core/stats", map[string]any{"group": "run/dry"})
	_, err := os.Stat(dst)
	fmt.Printf("Q2 dry-run: transfers=%v bytes=%v dst existe=%v\n", st["transfers"], st["bytes"], err == nil)

	// Q3 : vraie copie, stats de groupe, transferred
	r = rpc("sync/copy", map[string]any{
		"srcFs": src, "dstFs": dst, "_async": true, "_group": "run/real",
		"_config": map[string]any{"LogLevel": "INFO"},
	})
	js := waitJob(r["jobid"])
	st = rpc("core/stats", map[string]any{"group": "run/real"})
	tr := rpc("core/transferred", map[string]any{"group": "run/real"})
	fmt.Printf("Q3 copy: success=%v transfers=%v/%v bytes=%v/%v transferred=%d\n",
		js["success"], st["transfers"], st["totalTransfers"], st["bytes"], st["totalBytes"], lenOf(tr["transferred"]))

	// Q4 : job/stop sur un gros transfert ralenti (bwlimit global)
	big := filepath.Join(tmp, "bigsrc")
	os.MkdirAll(big, 0o755)
	os.WriteFile(filepath.Join(big, "big.bin"), make([]byte, 20<<20), 0o644)
	rpc("core/bwlimit", map[string]any{"rate": "1M"})
	r = rpc("sync/copy", map[string]any{"srcFs": big, "dstFs": dst + "2", "_async": true, "_group": "run/stop"})
	time.Sleep(1500 * time.Millisecond)
	st = rpc("core/stats", map[string]any{"group": "run/stop"})
	fmt.Printf("Q4 avant stop: bytes=%v speed=%v transferring=%d\n", st["bytes"], st["speed"], lenOf(st["transferring"]))
	t0 := time.Now()
	rpc("job/stop", map[string]any{"jobid": r["jobid"]})
	js = waitJob(r["jobid"])
	fmt.Printf("Q4 après stop (%v): finished=%v success=%v error=%q\n", time.Since(t0).Round(time.Millisecond), js["finished"], js["success"], js["error"])
	rpc("core/bwlimit", map[string]any{"rate": "off"})

	// Q5 : stats-delete
	rpc("core/stats-delete", map[string]any{"group": "run/real"})
	gl := rpc("core/group-list", map[string]any{})
	fmt.Println("Q5 groupes restants:", gl["groups"])

	// Q6 : remote memory + listremotes + config/get
	rpc("config/create", map[string]any{"name": "mem", "type": "memory", "parameters": map[string]any{}, "opt": map[string]any{"nonInteractive": true, "obscure": true}})
	fmt.Println("Q6 listremotes:", rpc("config/listremotes", map[string]any{})["remotes"])

	// Q7 : fs inline accepté par rc ?
	_, status := librclone.RPC("operations/list", `{"fs":":memory:","remote":""}`)
	fmt.Println("Q7 fs inline ':memory:' accepté par rc ? status =", status, "(=> doit être bloqué par notre code)")

	// Q8 : JobExpireDuration réglable via options/set ?
	o := rpc("options/set", map[string]any{"rc": map[string]any{"JobExpireDuration": "10m"}})
	fmt.Println("Q8 options/set rc.JobExpireDuration:", o)
	if g, ok := rpc("options/get", map[string]any{"blocks": "rc"})["rc"].(map[string]any); ok {
		fmt.Println("   options/get rc.JobExpireDuration:", g["JobExpireDuration"])
	}

	fmt.Println("\nLogs capturés par clé :")
	for k, v := range capt.byKey {
		fmt.Printf("  [%s] %d lignes\n", k, len(v))
		for i, l := range v {
			if i >= 8 {
				fmt.Println("     …")
				break
			}
			fmt.Println("    ", l)
		}
	}
}

func lenOf(v any) int {
	if a, ok := v.([]any); ok {
		return len(a)
	}
	return 0
}

func waitJob(id any) map[string]any {
	for {
		js := rpc("job/status", map[string]any{"jobid": id})
		if js["finished"] == true {
			return js
		}
		time.Sleep(200 * time.Millisecond)
	}
}
