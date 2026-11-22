package rclone

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rclone/rclone/fs/config"
)

// ExportRemotes retourne toutes les sections de la configuration telles que
// stockées (secrets obscurcis par rclone, clés non masquées). Réservé à
// l'export chiffré de la configuration.
func (c *Client) ExportRemotes() map[string]map[string]string {
	c.cfgMu.Lock()
	defer c.cfgMu.Unlock()
	data := config.LoadedData()
	out := map[string]map[string]string{}
	for _, name := range data.GetSectionList() {
		sec := map[string]string{}
		for _, k := range data.GetKeyList(name) {
			if v, ok := data.GetValue(name, k); ok {
				sec[k] = v
			}
		}
		out[name] = sec
	}
	return out
}

// ImportRemotes recrée des remotes exportés, valeurs écrites telles quelles
// (sans passer par l'assistant de configuration : un remote OAuth garde son
// jeton). Les mêmes règles qu'à la création s'appliquent (types et options
// interdits, références vers d'autres remotes ou des chemins locaux). Les
// remotes qui en référencent d'autres sont créés après eux. Retourne les noms
// créés et, par remote refusé, la raison.
func (c *Client) ImportRemotes(remotes map[string]map[string]string, v RemoteValidator) ([]string, map[string]string) {
	pending := make([]string, 0, len(remotes))
	for name := range remotes {
		pending = append(pending, name)
	}
	sort.Strings(pending)
	var created []string
	failed := map[string]string{}
	for len(pending) > 0 {
		var next []string
		progress := false
		for _, name := range pending {
			err := c.importRemote(name, remotes[name], v)
			switch {
			case err == nil:
				created = append(created, name)
				progress = true
			case isUnknownRemote(err):
				next = append(next, name) // dépend d'un remote pas encore créé
			default:
				failed[name] = err.Error()
			}
		}
		if !progress {
			for _, name := range next {
				failed[name] = c.importRemote(name, remotes[name], v).Error()
			}
			break
		}
		pending = next
	}
	return created, failed
}

type unknownRemoteError struct{ error }

func isUnknownRemote(err error) bool {
	_, ok := err.(unknownRemoteError)
	return ok
}

func (c *Client) importRemote(name string, params map[string]string, v RemoteValidator) error {
	if !ValidRemoteName(name) {
		return fmt.Errorf("invalid remote name")
	}
	if _, exists := c.RemoteType(name); exists {
		return fmt.Errorf("remote already exists")
	}
	typ := params["type"]
	anyParams := make(map[string]any, len(params))
	for k, val := range params {
		if k != "type" {
			anyParams[k] = val
		}
	}
	if err := c.validateParams(typ, anyParams, v); err != nil {
		for _, ref := range []string{params["remote"], params["upstreams"]} {
			if ref != "" && dependsOnMissing(c, ref) {
				return unknownRemoteError{err}
			}
		}
		return err
	}
	c.cfgMu.Lock()
	defer c.cfgMu.Unlock()
	data := config.LoadedData()
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	data.SetValue(name, "type", typ)
	for _, k := range keys {
		if k != "type" {
			data.SetValue(name, k, params[k])
		}
	}
	return data.Save()
}

// dependsOnMissing indique si une référence ("remote:chemin", liste pour
// union) vise un remote encore absent.
func dependsOnMissing(c *Client, ref string) bool {
	for _, item := range strings.Fields(ref) {
		for _, sfx := range []string{":ro", ":nc", ":writeback"} {
			item = strings.TrimSuffix(item, sfx)
		}
		name, _, hasColon := strings.Cut(item, ":")
		if !hasColon || strings.HasPrefix(item, "/") || !ValidRemoteName(name) {
			continue
		}
		if _, exists := c.RemoteType(name); !exists {
			return true
		}
	}
	return false
}
