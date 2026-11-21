package rclone

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"rcsui/internal/paths"
)

// ProviderOption est la forme simplifiée d'une option de backend exposée à l'UI.
type ProviderOption struct {
	Name      string    `json:"name"`
	Help      string    `json:"help"`
	Type      string    `json:"type"`
	Default   any       `json:"default,omitempty"`
	Required  bool      `json:"required"`
	Advanced  bool      `json:"advanced"`
	Exclusive bool      `json:"exclusive"`
	Secret    bool      `json:"secret"`
	Provider  string    `json:"provider,omitempty"`
	Examples  []Example `json:"examples,omitempty"`
}

// Example est une valeur proposée pour une option.
type Example struct {
	Value    string `json:"value"`
	Help     string `json:"help"`
	Provider string `json:"provider,omitempty"`
}

// Provider est un backend rclone (type de remote).
type Provider struct {
	Type        string           `json:"type"`
	Description string           `json:"description"`
	Options     []ProviderOption `json:"options"`
}

type rawProvider struct {
	Name        string `json:"Name"`
	Description string `json:"Description"`
	Options     []struct {
		Name       string `json:"Name"`
		Help       string `json:"Help"`
		Provider   string `json:"Provider"`
		Default    any    `json:"Default"`
		Type       string `json:"Type"`
		Hide       int    `json:"Hide"`
		Required   bool   `json:"Required"`
		IsPassword bool   `json:"IsPassword"`
		Advanced   bool   `json:"Advanced"`
		Exclusive  bool   `json:"Exclusive"`
		Sensitive  bool   `json:"Sensitive"`
		Examples   []struct {
			Value    string `json:"Value"`
			Help     string `json:"Help"`
			Provider string `json:"Provider"`
		} `json:"Examples"`
	} `json:"Options"`
}

// Types de backends refusés : "local" contournerait les racines autorisées.
var deniedTypes = map[string]bool{"local": true}

// IsDeniedOption indique si une option permet l'exécution de commandes et doit
// être refusée (même pour un administrateur). Cf. PLAN.md §6.4.
func IsDeniedOption(backend, name string) bool {
	n := strings.ToLower(name)
	if strings.HasSuffix(n, "_command") || strings.HasSuffix(n, "_cmd") {
		return true
	}
	if backend == "sftp" && (n == "ssh" || n == "server_command") {
		return true
	}
	return false
}

type providerCache struct {
	once  sync.Once
	list  []Provider
	byTyp map[string]Provider
	err   error
}

var providers providerCache

// Providers retourne le schéma simplifié de tous les backends (mis en cache).
func (c *Client) Providers() ([]Provider, error) {
	providers.once.Do(func() {
		var raw struct {
			Providers []rawProvider `json:"providers"`
		}
		if err := c.callInto("config/providers", map[string]any{}, &raw); err != nil {
			providers.err = err
			return
		}
		providers.byTyp = map[string]Provider{}
		for _, rp := range raw.Providers {
			if deniedTypes[rp.Name] {
				continue
			}
			p := Provider{Type: rp.Name, Description: rp.Description}
			for _, o := range rp.Options {
				if o.Hide != 0 || IsDeniedOption(rp.Name, o.Name) {
					continue
				}
				po := ProviderOption{Name: o.Name, Help: o.Help, Type: o.Type, Required: o.Required,
					Advanced: o.Advanced, Exclusive: o.Exclusive, Secret: o.IsPassword || o.Sensitive, Provider: o.Provider}
				if !po.Secret {
					po.Default = o.Default
				}
				for _, e := range o.Examples {
					po.Examples = append(po.Examples, Example{Value: e.Value, Help: e.Help, Provider: e.Provider})
				}
				p.Options = append(p.Options, po)
			}
			providers.list = append(providers.list, p)
			providers.byTyp[p.Type] = p
		}
		sort.Slice(providers.list, func(i, j int) bool { return providers.list[i].Type < providers.list[j].Type })
	})
	return providers.list, providers.err
}

func (c *Client) provider(typ string) (Provider, bool) {
	if _, err := c.Providers(); err != nil {
		return Provider{}, false
	}
	p, ok := providers.byTyp[typ]
	return p, ok
}

// RemoteSummary est un élément de liste de remotes.
type RemoteSummary struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// ListRemotes retourne les remotes configurés.
func (c *Client) ListRemotes() ([]RemoteSummary, error) {
	var r struct {
		Remotes []string `json:"remotes"`
	}
	if err := c.callInto("config/listremotes", map[string]any{}, &r); err != nil {
		return nil, err
	}
	out := make([]RemoteSummary, 0, len(r.Remotes))
	for _, n := range r.Remotes {
		typ, _ := c.RemoteType(n)
		out = append(out, RemoteSummary{Name: n, Type: typ})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// RemoteType implémente RemoteLookup.
func (c *Client) RemoteType(name string) (string, bool) {
	m, err := c.call("config/get", map[string]any{"name": name})
	if err != nil || len(m) == 0 {
		return "", false
	}
	t, _ := m["type"].(string)
	return t, t != ""
}

// SecretValue remplace une valeur sensible dans les réponses.
type SecretValue struct {
	Secret bool `json:"secret"`
	Set    bool `json:"set"`
}

// RemoteDetail est un remote avec ses paramètres masqués.
type RemoteDetail struct {
	Name   string         `json:"name"`
	Type   string         `json:"type"`
	Params map[string]any `json:"params"`
}

// GetRemote retourne un remote avec masquage fail-closed : toute clé secrète
// OU inconnue du schéma du provider est masquée.
func (c *Client) GetRemote(name string) (RemoteDetail, error) {
	m, err := c.call("config/get", map[string]any{"name": name})
	if err != nil {
		return RemoteDetail{}, err
	}
	if len(m) == 0 {
		return RemoteDetail{}, fmt.Errorf("remote %q not found", name)
	}
	typ, _ := m["type"].(string)
	return RemoteDetail{Name: name, Type: typ, Params: MaskParams(typ, m, c.secretSet(typ))}, nil
}

// secretSet retourne l'ensemble des options non secrètes connues (le reste est masqué).
func (c *Client) secretSet(typ string) map[string]bool {
	p, ok := c.provider(typ)
	if !ok {
		return nil
	}
	public := map[string]bool{}
	for _, o := range p.Options {
		if !o.Secret {
			public[o.Name] = true
		}
	}
	return public
}

// MaskParams masque tout paramètre qui n'est pas explicitement public.
func MaskParams(typ string, raw map[string]any, public map[string]bool) map[string]any {
	out := map[string]any{}
	for k, v := range raw {
		if k == "type" {
			continue
		}
		if public[k] {
			out[k] = v
			continue
		}
		out[k] = SecretValue{Secret: true, Set: v != nil && fmt.Sprint(v) != ""}
	}
	return out
}

// ConfigResult est la réponse de config/create et config/update.
type ConfigResult struct {
	Done     bool           `json:"done"`
	State    string         `json:"state,omitempty"`
	Question map[string]any `json:"question,omitempty"`
	Error    string         `json:"error,omitempty"`
}

// RemoteValidator valide les cibles locales d'un remote (alias, union...).
type RemoteValidator struct {
	Paths *paths.Resolver
}

func (c *Client) validateParams(typ string, params map[string]any, v RemoteValidator) error {
	if deniedTypes[typ] {
		return fmt.Errorf("backend type %q is not allowed", typ)
	}
	if _, ok := c.provider(typ); !ok {
		return fmt.Errorf("unknown backend type %q", typ)
	}
	for k, val := range params {
		if IsDeniedOption(typ, k) {
			return fmt.Errorf("option %q is not allowed", k)
		}
		s, isStr := val.(string)
		if !isStr {
			continue
		}
		if strings.ContainsAny(s, "\x00\n\r") {
			return fmt.Errorf("option %q contains control characters", k)
		}
		// Options qui désignent un autre remote ou un chemin local (alias, crypt, union, ...).
		if k == "remote" || k == "upstreams" {
			for _, item := range strings.Fields(s) {
				if err := c.validateUpstream(item, v); err != nil {
					return fmt.Errorf("option %q: %w", k, err)
				}
			}
		}
	}
	return nil
}

// validateUpstream vérifie une référence "remote:chemin" ou un chemin local.
func (c *Client) validateUpstream(item string, v RemoteValidator) error {
	if strings.HasPrefix(item, ":") {
		return fmt.Errorf("on-the-fly backends are not allowed")
	}
	// union : suffixes ":ro", ":nc", ":writeback"
	for _, sfx := range []string{":ro", ":nc", ":writeback"} {
		item = strings.TrimSuffix(item, sfx)
	}
	name, _, hasColon := strings.Cut(item, ":")
	if hasColon && !strings.HasPrefix(item, "/") {
		if !ValidRemoteName(name) {
			return fmt.Errorf("invalid remote reference")
		}
		t, ok := c.RemoteType(name)
		if !ok {
			return fmt.Errorf("unknown remote %q", name)
		}
		if deniedTypes[t] {
			return fmt.Errorf("remote %q has a forbidden type", name)
		}
		return nil
	}
	if v.Paths == nil {
		return fmt.Errorf("local paths are not allowed")
	}
	_, err := v.Paths.Resolve(item)
	return err
}

// CreateRemote crée un remote (mode non interactif ; peut renvoyer une question).
func (c *Client) CreateRemote(name, typ string, params map[string]any, v RemoteValidator) (ConfigResult, error) {
	if !ValidRemoteName(name) {
		return ConfigResult{}, fmt.Errorf("invalid remote name")
	}
	if _, exists := c.RemoteType(name); exists {
		return ConfigResult{}, fmt.Errorf("remote %q already exists", name)
	}
	if err := c.validateParams(typ, params, v); err != nil {
		return ConfigResult{}, err
	}
	c.cfgMu.Lock()
	defer c.cfgMu.Unlock()
	m, err := c.call("config/create", map[string]any{"name": name, "type": typ, "parameters": params,
		"opt": map[string]any{"obscure": true, "nonInteractive": true}})
	if err != nil {
		return ConfigResult{}, err
	}
	return toConfigResult(m), nil
}

// UpdateRemote met à jour les seuls paramètres fournis ; les secrets absents
// (ou {"keep":true}) restent inchangés.
func (c *Client) UpdateRemote(name string, params map[string]any, v RemoteValidator) (ConfigResult, error) {
	typ, ok := c.RemoteType(name)
	if !ok {
		return ConfigResult{}, fmt.Errorf("remote %q not found", name)
	}
	clean := map[string]any{}
	for k, val := range params {
		if mm, ok := val.(map[string]any); ok && (mm["keep"] == true || mm["secret"] == true) {
			continue
		}
		clean[k] = val
	}
	if err := c.validateParams(typ, clean, v); err != nil {
		return ConfigResult{}, err
	}
	if len(clean) == 0 {
		return ConfigResult{Done: true}, nil
	}
	c.cfgMu.Lock()
	defer c.cfgMu.Unlock()
	m, err := c.call("config/update", map[string]any{"name": name, "parameters": clean,
		"opt": map[string]any{"obscure": true, "nonInteractive": true}})
	if err != nil {
		return ConfigResult{}, err
	}
	return toConfigResult(m), nil
}

// ContinueRemote poursuit la machine à états de configuration (OAuth, choix...).
func (c *Client) ContinueRemote(name, state string, result any) (ConfigResult, error) {
	if _, ok := c.RemoteType(name); !ok {
		return ConfigResult{}, fmt.Errorf("remote %q not found", name)
	}
	c.cfgMu.Lock()
	defer c.cfgMu.Unlock()
	m, err := c.call("config/update", map[string]any{"name": name, "parameters": map[string]any{},
		"opt": map[string]any{"obscure": true, "nonInteractive": true, "continue": true, "state": state, "result": result}})
	if err != nil {
		return ConfigResult{}, err
	}
	return toConfigResult(m), nil
}

func toConfigResult(m map[string]any) ConfigResult {
	state, _ := m["State"].(string)
	errMsg, _ := m["Error"].(string)
	q, _ := m["Option"].(map[string]any)
	if q != nil {
		// Ne jamais renvoyer une valeur par défaut secrète.
		if pw, _ := q["IsPassword"].(bool); pw {
			delete(q, "Default")
			delete(q, "Value")
		}
	}
	return ConfigResult{Done: state == "" && q == nil, State: state, Question: q, Error: Redact(errMsg)}
}

// DeleteRemote supprime un remote.
func (c *Client) DeleteRemote(name string) error {
	if _, ok := c.RemoteType(name); !ok {
		return fmt.Errorf("remote %q not found", name)
	}
	c.cfgMu.Lock()
	defer c.cfgMu.Unlock()
	_, err := c.call("config/delete", map[string]any{"name": name})
	return err
}

// TestResult est le résultat d'un test de remote.
type TestResult struct {
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latencyMs"`
	Error     string `json:"error,omitempty"`
}

// TestRemote liste la racine du remote (dossiers seulement) avec un timeout.
func (c *Client) TestRemote(ctx context.Context, name string) TestResult {
	if !ValidRemoteName(name) {
		return TestResult{Error: "invalid remote name"}
	}
	t0 := time.Now()
	_, err := c.runAsync(ctx, "operations/list", map[string]any{"fs": name + ":", "remote": "",
		"opt": map[string]any{"dirsOnly": true, "noModTime": true, "noMimeType": true}}, 15*time.Second)
	if err != nil {
		return TestResult{Error: Redact(err.Error()), LatencyMs: time.Since(t0).Milliseconds()}
	}
	return TestResult{OK: true, LatencyMs: time.Since(t0).Milliseconds()}
}

// ListEntry est une entrée de l'explorateur de remote.
type ListEntry struct {
	Name    string    `json:"name"`
	Dir     bool      `json:"dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

// ListRemoteDir liste un dossier d'un remote.
func (c *Client) ListRemoteDir(ctx context.Context, name, dir string, max int) ([]ListEntry, bool, error) {
	if !ValidRemoteName(name) {
		return nil, false, fmt.Errorf("invalid remote name")
	}
	if t, ok := c.RemoteType(name); !ok || deniedTypes[t] {
		return nil, false, fmt.Errorf("unknown remote %q", name)
	}
	p, err := CleanRemotePath(dir)
	if err != nil {
		return nil, false, err
	}
	out, err := c.runAsync(ctx, "operations/list", map[string]any{"fs": name + ":", "remote": p,
		"opt": map[string]any{"noMimeType": true}}, 30*time.Second)
	if err != nil {
		return nil, false, err
	}
	list, _ := out["list"].([]any)
	entries := make([]ListEntry, 0, len(list))
	for _, raw := range list {
		m, _ := raw.(map[string]any)
		e := ListEntry{}
		e.Name, _ = m["Name"].(string)
		e.Dir, _ = m["IsDir"].(bool)
		if s, ok := m["Size"].(float64); ok && s > 0 {
			e.Size = int64(s)
		}
		if mt, ok := m["ModTime"].(string); ok {
			e.ModTime, _ = time.Parse(time.RFC3339Nano, mt)
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Dir != entries[j].Dir {
			return entries[i].Dir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	truncated := false
	if max > 0 && len(entries) > max {
		entries, truncated = entries[:max], true
	}
	return entries, truncated, nil
}
