package auth

import (
	"sync"
	"time"
)

// Limiter est un token bucket par clé (IP, nom d'utilisateur) pour les
// tentatives de connexion : Burst essais, puis un essai par Every.
type Limiter struct {
	Burst int
	Every time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter crée un limiteur : 5 essais, puis 1 toutes les 12 s (5/min).
func NewLimiter() *Limiter {
	return &Limiter{Burst: 5, Every: 12 * time.Second, buckets: map[string]*bucket{}, now: time.Now}
}

// Allow consomme un jeton pour chaque clé ; refuse si l'une est épuisée.
// Retourne aussi le délai conseillé avant un nouvel essai.
func (l *Limiter) Allow(keys ...string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var wait time.Duration
	ok := true
	bs := make([]*bucket, 0, len(keys))
	for _, k := range keys {
		b := l.buckets[k]
		if b == nil {
			b = &bucket{tokens: float64(l.Burst), last: now}
			l.buckets[k] = b
		}
		b.tokens += now.Sub(b.last).Seconds() / l.Every.Seconds()
		if b.tokens > float64(l.Burst) {
			b.tokens = float64(l.Burst)
		}
		b.last = now
		if b.tokens < 1 {
			ok = false
			if w := time.Duration((1 - b.tokens) * float64(l.Every)); w > wait {
				wait = w
			}
		}
		bs = append(bs, b)
	}
	if ok {
		for _, b := range bs {
			b.tokens--
		}
	}
	if len(l.buckets) > 10000 {
		l.gc(now)
	}
	return ok, wait
}

// Reset efface une clé (après une connexion réussie).
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.buckets, key)
	l.mu.Unlock()
}

func (l *Limiter) gc(now time.Time) {
	full := time.Duration(l.Burst) * l.Every
	for k, b := range l.buckets {
		if now.Sub(b.last) > full {
			delete(l.buckets, k)
		}
	}
}
