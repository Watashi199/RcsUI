// Package rclone intègre rclone comme bibliothèque (librclone). Toutes les
// opérations passent par un wrapper RPC typé : aucun nom de méthode RPC ne
// provient jamais du client HTTP (l'accès à l'API rc équivaut à un shell).
package rclone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	rclog "github.com/rclone/rclone/fs/log"
	"github.com/rclone/rclone/librclone/librclone"
)

// RPCError est une erreur renvoyée par un appel rc.
type RPCError struct {
	Method string
	Status int
	Msg    string
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("rclone %s: %s (status %d)", e.Method, e.Msg, e.Status)
}

// Client est le wrapper typé autour de librclone.RPC.
type Client struct {
	cfgMu sync.Mutex // sérialise les écritures de config
	logs  *logRouter
}

var (
	initOnce sync.Once
	initErr  error
	router   = newLogRouter()
)

// Init initialise librclone (une seule fois par processus). configPath doit
// être défini AVANT Initialize : RCLONE_CONFIG posé après le démarrage du
// process est ignoré (constaté au spike M0).
func Init(configPath, cacheDir string) (*Client, error) {
	initOnce.Do(func() {
		if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
			initErr = err
			return
		}
		if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(configPath, nil, 0o600); err != nil {
				initErr = err
				return
			}
		}
		if cacheDir != "" {
			_ = os.MkdirAll(cacheDir, 0o700)
			_ = config.SetCacheDir(cacheDir)
		}
		if err := config.SetConfigPath(configPath); err != nil {
			initErr = err
			return
		}
		librclone.Initialize()
		// Logs routés vers le run via le groupe de stats porté par le ctx.
		fs.SetLogger(&logHandler{inner: rclog.Handler, router: router})
	})
	if initErr != nil {
		return nil, initErr
	}
	c := &Client{logs: router}
	// Garder les jobs terminés 10 min (défaut 1 min) pour lire leur statut final.
	// Niveau global INFO : bisync journalise son récit sans contexte (fs.Infof),
	// filtré sinon avant d'atteindre logHandler. logHandler jette les messages
	// sans run sous NOTICE, le log serveur garde donc son niveau habituel.
	if _, err := c.call("options/set", map[string]any{
		"rc":   map[string]any{"JobExpireDuration": "10m"},
		"main": map[string]any{"LogLevel": "INFO"},
	}); err != nil {
		return nil, err
	}
	return c, nil
}

// Finalize libère la bibliothèque.
func Finalize() { librclone.Finalize() }

func (c *Client) call(method string, in any) (map[string]any, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	out, status := librclone.RPC(method, string(b))
	var m map[string]any
	if out != "" {
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			return nil, fmt.Errorf("rclone %s: bad response: %w", method, err)
		}
	}
	if status != 200 {
		msg, _ := m["error"].(string)
		if msg == "" {
			msg = out
		}
		return m, &RPCError{Method: method, Status: status, Msg: Redact(msg)}
	}
	return m, nil
}

// callInto décode la réponse dans out.
func (c *Client) callInto(method string, in any, out any) error {
	m, err := c.call(method, in)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(m)
	return json.Unmarshal(b, out)
}

// JobStatus est la réponse de job/status.
type JobStatus struct {
	ID       int64          `json:"id"`
	Finished bool           `json:"finished"`
	Success  bool           `json:"success"`
	Error    string         `json:"error"`
	Output   map[string]any `json:"output"`
}

// Stats est la réponse de core/stats.
type Stats struct {
	Bytes          int64          `json:"bytes"`
	TotalBytes     int64          `json:"totalBytes"`
	Transfers      int64          `json:"transfers"`
	TotalTransfers int64          `json:"totalTransfers"`
	Checks         int64          `json:"checks"`
	TotalChecks    int64          `json:"totalChecks"`
	Deletes        int64          `json:"deletes"`
	Errors         int64          `json:"errors"`
	FatalError     bool           `json:"fatalError"`
	LastError      string         `json:"lastError"`
	Speed          float64        `json:"speed"`
	ETA            *float64       `json:"eta"`
	Transferring   []Transferring `json:"transferring"`
}

// Transferring est un transfert en cours.
type Transferring struct {
	Name       string  `json:"name"`
	Size       int64   `json:"size"`
	Bytes      int64   `json:"bytes"`
	Percentage float64 `json:"percentage"`
}

func (c *Client) startAsync(method string, in map[string]any) (int64, error) {
	in["_async"] = true
	var r struct {
		JobID int64 `json:"jobid"`
	}
	if err := c.callInto(method, in, &r); err != nil {
		return 0, err
	}
	return r.JobID, nil
}

// JobStatus interroge job/status.
func (c *Client) JobStatus(id int64) (JobStatus, error) {
	var s JobStatus
	err := c.callInto("job/status", map[string]any{"jobid": id}, &s)
	return s, err
}

// JobStop annule un job.
func (c *Client) JobStop(id int64) error {
	_, err := c.call("job/stop", map[string]any{"jobid": id})
	return err
}

// Stats interroge core/stats pour un groupe.
func (c *Client) Stats(group string) (Stats, error) {
	var s Stats
	err := c.callInto("core/stats", map[string]any{"group": group}, &s)
	return s, err
}

// StatsDelete supprime un groupe de stats (sinon fuite mémoire).
func (c *Client) StatsDelete(group string) {
	_, _ = c.call("core/stats-delete", map[string]any{"group": group})
}

var uiSeq struct {
	sync.Mutex
	n int64
}

// runAsync exécute un appel en asynchrone et attend son résultat avec un délai
// maximal (un appel synchrone de librclone ne serait pas annulable).
func (c *Client) runAsync(ctx context.Context, method string, in map[string]any, timeout time.Duration) (map[string]any, error) {
	uiSeq.Lock()
	uiSeq.n++
	group := fmt.Sprintf("ui/%d", uiSeq.n)
	uiSeq.Unlock()
	in["_group"] = group
	defer c.StatsDelete(group)
	id, err := c.startAsync(method, in)
	if err != nil {
		return nil, err
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		st, err := c.JobStatus(id)
		if err != nil {
			return nil, err
		}
		if st.Finished {
			if !st.Success {
				return nil, &RPCError{Method: method, Status: 500, Msg: Redact(st.Error)}
			}
			return st.Output, nil
		}
		select {
		case <-ctx.Done():
			_ = c.JobStop(id)
			return nil, ctx.Err()
		case <-deadline.C:
			_ = c.JobStop(id)
			return nil, fmt.Errorf("rclone %s: timeout after %s", method, timeout)
		case <-tick.C:
		}
	}
}

// Version retourne la version de rclone.
func (c *Client) Version() string {
	m, err := c.call("core/version", map[string]any{})
	if err != nil {
		return ""
	}
	v, _ := m["version"].(string)
	return v
}
