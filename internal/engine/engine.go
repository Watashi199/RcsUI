// Package engine définit l'abstraction commune aux moteurs de transfert
// (rsync, rclone). Le runner ne manipule que ces types ; le frontend ne voit
// que leurs projections JSON.
package engine

import (
	"context"
	"errors"
	"sort"
	"time"
)

// Kind identifie un moteur.
type Kind string

const (
	KindRsync  Kind = "rsync"
	KindRclone Kind = "rclone"
)

// Mode est la sémantique du transfert.
type Mode string

const (
	ModeCopy Mode = "copy" // ajoute/met à jour, ne supprime rien
	ModeSync Mode = "sync" // miroir : supprime à la destination ce qui n'existe plus à la source
	ModeMove Mode = "move" // rclone uniquement en v1
	// ModeBisync : synchronisation bidirectionnelle (rclone uniquement).
	// Source = Path1, Destination = Path2.
	ModeBisync Mode = "bisync"
)

// EndpointType est le type d'une source ou destination.
type EndpointType string

const (
	EndpointLocal  EndpointType = "local"
	EndpointRemote EndpointType = "remote" // remote rclone
	EndpointSSH    EndpointType = "ssh"    // rsync over SSH
)

// SSHIdentity choisit la clé utilisée pour une cible SSH.
type SSHIdentity struct {
	ManagedID *int64 `json:"managed,omitempty"` // identité gérée par l'app
	Mounted   bool   `json:"mounted,omitempty"` // ~/.ssh monté
}

// SSHTarget décrit un hôte distant pour rsync over SSH.
type SSHTarget struct {
	Host     string      `json:"host"`
	Port     int         `json:"port"`
	User     string      `json:"user"`
	Identity SSHIdentity `json:"identity"`
}

// Endpoint est une source ou une destination, sous forme typée (jamais de
// chaîne brute venant du client).
type Endpoint struct {
	Type         EndpointType `json:"type"`
	Path         string       `json:"path"`
	ContentsOnly bool         `json:"contentsOnly,omitempty"` // rsync : copier le contenu du dossier (slash final)
	Remote       string       `json:"remote,omitempty"`
	SSH          *SSHTarget   `json:"ssh,omitempty"`
}

// Spec est tout ce qu'il faut pour lancer une exécution.
type Spec struct {
	RunID       string
	Kind        Kind
	Mode        Mode
	Source      Endpoint
	Destination Endpoint
	DryRun      bool
	// Resync (bisync uniquement) : reconstruit les listings de référence à
	// partir de l'union des deux côtés. Requis au premier run et après une
	// erreur critique ; ne supprime jamais rien.
	Resync bool
	// Options propres au moteur, produites par Engine.DecodeOptions.
	Options any
}

// Preview est la représentation affichable (sans secrets) de ce qui sera exécuté.
type Preview struct {
	Argv []string       `json:"argv,omitempty"`
	RPC  string         `json:"rpc,omitempty"`
	In   map[string]any `json:"input,omitempty"`
}

// Phase de progression.
type Phase string

const (
	PhaseScanning     Phase = "scanning"
	PhaseTransferring Phase = "transferring"
	PhaseFinalizing   Phase = "finalizing"
)

// FileProgress est la progression d'un fichier en cours.
type FileProgress struct {
	Name    string  `json:"name"`
	Size    int64   `json:"size"`
	Percent float64 `json:"percent"`
}

// Progress est un instantané de progression, commun aux moteurs.
type Progress struct {
	Phase       Phase          `json:"phase"`
	BytesDone   int64          `json:"bytesDone"`
	BytesTotal  int64          `json:"bytesTotal"` // 0 = inconnu
	FilesDone   int64          `json:"filesDone"`
	FilesTotal  int64          `json:"filesTotal"` // 0 = inconnu
	TotalsExact bool           `json:"totalsExact"`
	Deletes     int64          `json:"deletes"`
	Errors      int64          `json:"errors"`
	SpeedBps    float64        `json:"speedBps"`
	ETASec      *int64         `json:"etaSec,omitempty"`
	Current     []FileProgress `json:"current,omitempty"`
}

// Percent retourne le pourcentage d'avancement en octets, ou -1 si inconnu.
func (p Progress) Percent() float64 {
	if p.BytesTotal <= 0 {
		return -1
	}
	v := float64(p.BytesDone) / float64(p.BytesTotal) * 100
	if v > 100 {
		v = 100
	}
	return v
}

// LogLevel d'une ligne de log.
type LogLevel string

const (
	LevelDebug LogLevel = "debug"
	LevelInfo  LogLevel = "info"
	LevelWarn  LogLevel = "warn"
	LevelError LogLevel = "error"
)

// LogLine est une ligne de log d'exécution.
type LogLine struct {
	Time   time.Time `json:"t"`
	Stream string    `json:"s"` // stdout | stderr | rclone | system
	Level  LogLevel  `json:"lvl"`
	Msg    string    `json:"msg"`
}

// ItemOp est l'opération sur un fichier (commune aux moteurs).
type ItemOp string

const (
	ItemTransfer ItemOp = "transfer"
	ItemCreate   ItemOp = "create"
	ItemDelete   ItemOp = "delete"
	ItemAttrs    ItemOp = "attrs"
	ItemOther    ItemOp = "other"
)

// Item est un changement de fichier (prévu en dry-run, effectué sinon).
type Item struct {
	Time  time.Time `json:"t"`
	Op    ItemOp    `json:"op"`
	Kind  string    `json:"kind,omitempty"` // f, d, L...
	Path  string    `json:"path"`
	Size  int64     `json:"size"`
	Flags string    `json:"flags,omitempty"` // rsync : %i brut
	Error string    `json:"error,omitempty"`
}

// Sink reçoit les événements d'une exécution. Les implémentations ne doivent
// pas bloquer.
type Sink interface {
	OnProgress(Progress)
	OnLog(LogLine)
	OnItem(Item)
}

// Outcome est l'issue terminale d'une exécution.
type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomePartial   Outcome = "partial"
	OutcomeFailed    Outcome = "failed"
	OutcomeCancelled Outcome = "cancelled"
)

// Result est le résultat d'une exécution terminée.
type Result struct {
	Outcome   Outcome
	ExitCode  *int
	Retryable bool
	Err       error
	Final     Progress
}

// Execution est une exécution en cours.
type Execution interface {
	Progress() Progress
	// Cancel est idempotent et non bloquant.
	Cancel(reason string)
	Done() <-chan struct{}
	// Result n'est valide qu'après fermeture de Done().
	Result() Result
	// PID retourne le pid du processus (rsync) ou 0.
	PID() int
	// ExternalID retourne l'identifiant côté moteur (jobid rclone) ou 0.
	ExternalID() int64
}

// Engine est un moteur de transfert.
type Engine interface {
	Kind() Kind
	// DecodeOptions décode et valide les options propres au moteur (JSON du job).
	DecodeOptions(raw []byte) (any, error)
	// Validate vérifie la spec (racines, remotes, options). Appelé à la
	// sauvegarde ET au lancement.
	Validate(ctx context.Context, s Spec) error
	// Preview décrit ce qui sera exécuté, sans secrets.
	Preview(s Spec) (Preview, error)
	// Start lance l'exécution et rend la main immédiatement.
	Start(ctx context.Context, s Spec, sink Sink) (Execution, error)
}

// ValidationError regroupe des erreurs par champ ; l'API la renvoie en 400.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "invalid spec"
	}
	return "invalid " + keys[0] + ": " + e.Fields[keys[0]]
}

// Add ajoute une erreur de champ (la première erreur d'un champ est conservée).
func (e *ValidationError) Add(field, msg string) {
	if e.Fields == nil {
		e.Fields = map[string]string{}
	}
	if _, ok := e.Fields[field]; !ok {
		e.Fields[field] = msg
	}
}

// Err retourne e si des erreurs ont été ajoutées, nil sinon.
func (e *ValidationError) Err() error {
	if len(e.Fields) == 0 {
		return nil
	}
	return e
}

// ErrUnsupported indique une combinaison non supportée par le moteur.
var ErrUnsupported = errors.New("unsupported by this engine")
