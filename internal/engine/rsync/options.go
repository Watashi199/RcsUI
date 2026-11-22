package rsync

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"rcsui/internal/engine"
)

// DeleteMode contrôle les suppressions en mode sync.
type DeleteMode string

const (
	DeleteNone   DeleteMode = ""
	DeleteDuring DeleteMode = "during"
	DeleteAfter  DeleteMode = "after"
	DeleteDelay  DeleteMode = "delay"
)

// Options est la liste blanche typée des options rsync exposées à l'UI.
// Aucun champ libre : chaque champ se traduit en arguments fixes (argv.go).
type Options struct {
	V                int        `json:"v"`
	Delete           DeleteMode `json:"delete,omitempty"`
	MaxDelete        *int       `json:"maxDelete,omitempty"`
	Owner            bool       `json:"owner,omitempty"`
	Group            bool       `json:"group,omitempty"`
	FakeSuper        bool       `json:"fakeSuper,omitempty"`
	Checksum         bool       `json:"checksum,omitempty"`
	Compress         bool       `json:"compress,omitempty"`
	Partial          bool       `json:"partial,omitempty"`
	HardLinks        bool       `json:"hardLinks,omitempty"`
	ACLs             bool       `json:"acls,omitempty"`
	Xattrs           bool       `json:"xattrs,omitempty"`
	Sparse           bool       `json:"sparse,omitempty"`
	NumericIDs       bool       `json:"numericIds,omitempty"`
	Update           bool       `json:"update,omitempty"`
	IgnoreExisting   bool       `json:"ignoreExisting,omitempty"`
	ExistingOnly     bool       `json:"existingOnly,omitempty"`
	OneFileSystem    bool       `json:"oneFileSystem,omitempty"`
	SizeOnly         bool       `json:"sizeOnly,omitempty"`
	DeleteExcluded   bool       `json:"deleteExcluded,omitempty"`
	BwLimitKiB       *int       `json:"bwLimitKiB,omitempty"`
	TimeoutSec       *int       `json:"timeoutSec,omitempty"`
	ContimeoutSec    *int       `json:"contimeoutSec,omitempty"`
	Excludes         []string   `json:"excludes,omitempty"`
	Includes         []string   `json:"includes,omitempty"`
	BackupDir        string     `json:"backupDir,omitempty"`
	LinkDest         string     `json:"linkDest,omitempty"`
	AccurateProgress bool       `json:"accurateProgress,omitempty"`
}

// ExtraWritePaths implémente engine.ExtraWrites : --backup-dir reçoit les
// versions remplacées ou supprimées.
func (o *Options) ExtraWritePaths() []string {
	if o == nil || o.BackupDir == "" {
		return nil
	}
	return []string{o.BackupDir}
}

const maxPatterns = 200

// DecodeOptions décode strictement (champs inconnus refusés) et valide.
func DecodeOptions(raw []byte) (*Options, error) {
	o := &Options{V: 1}
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(o); err != nil {
			return nil, &engine.ValidationError{Fields: map[string]string{"options": err.Error()}}
		}
	}
	if err := o.validate(); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *Options) validate() error {
	ve := &engine.ValidationError{}
	if o.V != 1 {
		ve.Add("options.v", "unsupported options version")
	}
	switch o.Delete {
	case DeleteNone, DeleteDuring, DeleteAfter, DeleteDelay:
	default:
		ve.Add("options.delete", "must be one of during, after, delay")
	}
	posInt := func(field string, v *int, max int) {
		if v != nil && (*v < 0 || *v > max) {
			ve.Add(field, "out of range")
		}
	}
	posInt("options.maxDelete", o.MaxDelete, 1<<30)
	posInt("options.bwLimitKiB", o.BwLimitKiB, 1<<30)
	posInt("options.timeoutSec", o.TimeoutSec, 86400)
	posInt("options.contimeoutSec", o.ContimeoutSec, 3600)
	if o.Update && o.IgnoreExisting {
		ve.Add("options.ignoreExisting", "incompatible with update")
	}
	if o.ExistingOnly && o.IgnoreExisting {
		ve.Add("options.existingOnly", "incompatible with ignoreExisting")
	}
	if len(o.Excludes)+len(o.Includes) > maxPatterns {
		ve.Add("options.excludes", "too many patterns")
	}
	for i, p := range o.Excludes {
		if err := checkPattern(p); err != "" {
			ve.Add("options.excludes."+strconv.Itoa(i), err)
		}
	}
	for i, p := range o.Includes {
		if err := checkPattern(p); err != "" {
			ve.Add("options.includes."+strconv.Itoa(i), err)
		}
	}
	return ve.Err()
}

func checkPattern(p string) string {
	switch {
	case p == "":
		return "empty pattern"
	case len(p) > 1024:
		return "pattern too long"
	case strings.ContainsAny(p, "\x00\n\r"):
		return "pattern contains control characters"
	}
	return ""
}

// Warnings renvoie des avertissements non bloquants (affichés dans l'UI).
func (o *Options) Warnings(nonRoot bool) []string {
	var w []string
	if nonRoot && (o.Owner || o.Group) {
		w = append(w, "owner/group preservation has no effect when the server is not running as root; consider fakeSuper")
	}
	return w
}

// String est utilisé dans les messages d'erreur.
func (d DeleteMode) String() string { return fmt.Sprintf("%q", string(d)) }
