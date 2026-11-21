// Package events diffuse les événements de runs aux flux SSE, avec un ring
// buffer par flux pour la reprise via Last-Event-ID.
package events

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// Types d'événements.
const (
	TypeRunState    = "run.state"
	TypeRunProgress = "run.progress"
	TypeRunLog      = "run.log"
	TypeRunItem     = "run.item"
)

// Event est un événement diffusé.
type Event struct {
	ID    string // "<flux>:<seq>"
	Type  string
	RunID string
	JobID int64
	Data  any
}

const (
	globalStream = "g"
	globalRing   = 1000
	runRing      = 500
	subBuffer    = 256
	runRetention = 5 * time.Minute
)

type stream struct {
	seq     uint64
	ring    []Event
	subs    map[*Sub]struct{}
	closeAt time.Time // non nul : flux terminé, purgé après rétention
}

// Sub est un abonnement. C est fermé si l'abonné est trop lent ou désabonné.
type Sub struct {
	C      chan Event
	stream string
	closed bool
}

// Broker est le hub pub/sub.
type Broker struct {
	mu      sync.Mutex
	streams map[string]*stream
	now     func() time.Time
}

// NewBroker crée un broker.
func NewBroker() *Broker {
	return &Broker{streams: map[string]*stream{globalStream: {subs: map[*Sub]struct{}{}}}, now: time.Now}
}

func (b *Broker) get(name string) *stream {
	s, ok := b.streams[name]
	if !ok {
		s = &stream{subs: map[*Sub]struct{}{}}
		b.streams[name] = s
	}
	return s
}

func (b *Broker) publishLocked(name string, e Event, size int) {
	s := b.get(name)
	s.seq++
	e.ID = name + ":" + strconv.FormatUint(s.seq, 10)
	s.ring = append(s.ring, e)
	if len(s.ring) > size {
		s.ring = s.ring[len(s.ring)-size:]
	}
	for sub := range s.subs {
		select {
		case sub.C <- e:
		default:
			// Abonné trop lent : déconnecté ; il reprendra via Last-Event-ID.
			b.closeSubLocked(sub)
		}
	}
}

// PublishRun publie un événement sur le flux d'un run, et aussi sur le flux
// global si global est vrai.
func (b *Broker) PublishRun(runID string, jobID int64, typ string, data any, global bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := Event{Type: typ, RunID: runID, JobID: jobID, Data: data}
	b.publishLocked(runID, e, runRing)
	if global {
		b.publishLocked(globalStream, e, globalRing)
	}
	b.gcLocked()
}

// EndRun marque la fin d'un flux de run ; il sera purgé après la rétention.
func (b *Broker) EndRun(runID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.streams[runID]; ok {
		s.closeAt = b.now().Add(runRetention)
	}
}

func (b *Broker) gcLocked() {
	t := b.now()
	for name, s := range b.streams {
		if name != globalStream && !s.closeAt.IsZero() && t.After(s.closeAt) && len(s.subs) == 0 {
			delete(b.streams, name)
		}
	}
}

// Subscribe s'abonne à un flux ("" = global). lastID est la valeur de
// Last-Event-ID (éventuellement vide). Retourne les événements à rejouer, et
// resync=true si lastID est inexploitable (le client doit recharger l'état).
func (b *Broker) Subscribe(runID, lastID string) (sub *Sub, replay []Event, resync bool) {
	name := runID
	if name == "" {
		name = globalStream
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.get(name)
	sub = &Sub{C: make(chan Event, subBuffer), stream: name}
	s.subs[sub] = struct{}{}
	if lastID == "" {
		if runID != "" {
			// Premier abonnement à un run : rejouer ce qui est en mémoire.
			replay = append(replay, s.ring...)
		}
		return sub, replay, false
	}
	stName, seqStr, ok := strings.Cut(lastID, ":")
	last, err := strconv.ParseUint(seqStr, 10, 64)
	if !ok || err != nil || stName != name || last > s.seq {
		return sub, nil, true
	}
	if len(s.ring) > 0 {
		first := s.seq - uint64(len(s.ring)) + 1
		if last+1 < first {
			return sub, nil, true
		}
	}
	for _, e := range s.ring {
		if seqOf(e.ID) > last {
			replay = append(replay, e)
		}
	}
	return sub, replay, false
}

func seqOf(id string) uint64 {
	_, s, _ := strings.Cut(id, ":")
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}

// Unsubscribe retire un abonnement (idempotent).
func (b *Broker) Unsubscribe(sub *Sub) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closeSubLocked(sub)
}

func (b *Broker) closeSubLocked(sub *Sub) {
	if sub.closed {
		return
	}
	sub.closed = true
	if s, ok := b.streams[sub.stream]; ok {
		delete(s.subs, sub)
	}
	close(sub.C)
}

// SubscriberCount retourne le nombre total d'abonnés.
func (b *Broker) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, s := range b.streams {
		n += len(s.subs)
	}
	return n
}
