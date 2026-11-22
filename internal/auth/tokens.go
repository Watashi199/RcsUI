package auth

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

// APITokenPrefix distingue les jetons d'API (repérables par les scanners de secrets).
const APITokenPrefix = "rcs_"

// NewAPIToken génère un jeton d'API : valeur brute (montrée une seule fois),
// empreinte stockée et préfixe affichable.
func NewAPIToken() (raw string, hash []byte, prefix string) {
	raw = APITokenPrefix + RandomToken(32)
	return raw, hashToken(raw), raw[:len(APITokenPrefix)+6]
}

// ErrBadToken : jeton d'API absent, inconnu, expiré ou inutilisable.
var ErrBadToken = errors.New("invalid API token")

// BearerToken retourne le jeton de l'en-tête Authorization ("" s'il n'y en a pas).
func BearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	scheme, tok, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", true
	}
	return strings.TrimSpace(tok), true
}

// LookupAPIToken valide un jeton d'API. Le rôle de l'utilisateur est plafonné
// par celui du jeton ; un compte désactivé ou devant changer son mot de passe
// ne peut pas utiliser ses jetons.
func (s *Sessions) LookupAPIToken(r *http.Request, raw string) (*Current, error) {
	if !strings.HasPrefix(raw, APITokenPrefix) || len(raw) > 128 {
		return nil, ErrBadToken
	}
	t, u, err := s.DB.GetAPITokenByHash(r.Context(), hashToken(raw))
	if err != nil {
		return nil, ErrBadToken
	}
	now := time.Now()
	if (t.ExpiresAt != nil && now.After(*t.ExpiresAt)) || u.Disabled || u.MustChangePW {
		return nil, ErrBadToken
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) > touchInterval {
		_ = s.DB.TouchAPIToken(r.Context(), t.ID, now)
	}
	u.Role = u.Role.Cap(t.Role)
	return &Current{User: u, APIToken: &t}, nil
}
