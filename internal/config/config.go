// Package config lit la configuration depuis l'environnement (PLAN.md §10.2).
// Les secrets acceptent une variante *_FILE (Docker secrets).
package config

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config est la configuration du serveur.
type Config struct {
	Listen          string
	DataDir         string
	AllowedRoots    []string
	MaxConcurrent   int
	AdminUser       string
	AdminPassword   string
	SecretKey       string
	BaseURL         string
	TrustedProxies  []netip.Prefix
	CookieSecure    string
	SSHDir          string
	LogLevel        string
	LogRetention    time.Duration
	SessionIdle     time.Duration
	SessionAbsolute time.Duration
	RsyncBinary     string
	DisableRclone   bool
	// RcloneEncrypt : chiffrer rclone.conf (défaut) ; false le déchiffre au démarrage.
	RcloneEncrypt bool
	MetricsToken  string // vide : /metrics désactivé
	OIDC          OIDC
}

// OIDC regroupe la configuration OpenID Connect (désactivé si Issuer est vide).
type OIDC struct {
	Issuer         string
	ClientID       string
	ClientSecret   string
	Scopes         []string
	RoleClaim      string
	UsernameClaim  string
	AdminGroups    []string
	OperatorGroups []string
	AutoProvision  bool
}

func list(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func loadOIDC(c *Config) error {
	o := OIDC{
		Issuer:         strings.TrimRight(env("RCSUI_OIDC_ISSUER", ""), "/"),
		ClientID:       env("RCSUI_OIDC_CLIENT_ID", ""),
		Scopes:         list(env("RCSUI_OIDC_SCOPES", "openid,profile,email,groups")),
		RoleClaim:      env("RCSUI_OIDC_ROLE_CLAIM", "groups"),
		UsernameClaim:  env("RCSUI_OIDC_USERNAME_CLAIM", "preferred_username"),
		AdminGroups:    list(env("RCSUI_OIDC_ADMIN_GROUPS", "")),
		OperatorGroups: list(env("RCSUI_OIDC_OPERATOR_GROUPS", "")),
	}
	o.AutoProvision, _ = strconv.ParseBool(env("RCSUI_OIDC_AUTO_PROVISION", "false"))
	var err error
	if o.ClientSecret, err = secret("RCSUI_OIDC_CLIENT_SECRET"); err != nil {
		return err
	}
	if o.Issuer != "" {
		if o.ClientID == "" {
			return fmt.Errorf("RCSUI_OIDC_CLIENT_ID is required when RCSUI_OIDC_ISSUER is set")
		}
		if c.BaseURL == "" {
			return fmt.Errorf("RCSUI_BASE_URL is required when OIDC is enabled (redirect URL)")
		}
	}
	c.OIDC = o
	return nil
}

// Chemins dérivés de DataDir.
func (c Config) DBPath() string       { return filepath.Join(c.DataDir, "rcsui.db") }
func (c Config) BackupDir() string    { return filepath.Join(c.DataDir, "backups") }
func (c Config) LogDir() string       { return filepath.Join(c.DataDir, "logs", "runs") }
func (c Config) RcloneConfig() string { return filepath.Join(c.DataDir, "rclone", "rclone.conf") }
func (c Config) RcloneCache() string  { return filepath.Join(c.DataDir, "rclone", "cache") }
func (c Config) SSHStateDir() string  { return filepath.Join(c.DataDir, "ssh") }
func (c Config) RunTmpDir() string    { return filepath.Join(c.DataDir, "run") }

// Load lit l'environnement.
func Load() (Config, error) {
	c := Config{
		Listen:          env("RCSUI_LISTEN", ":8080"),
		DataDir:         env("RCSUI_DATA_DIR", "/data"),
		CookieSecure:    strings.ToLower(env("RCSUI_COOKIE_SECURE", "auto")),
		BaseURL:         strings.TrimRight(env("RCSUI_BASE_URL", ""), "/"),
		SSHDir:          env("RCSUI_SSH_DIR", ""),
		LogLevel:        strings.ToLower(env("RCSUI_LOG_LEVEL", "info")),
		AdminUser:       env("RCSUI_ADMIN_USER", "admin"),
		RsyncBinary:     env("RCSUI_RSYNC_BINARY", "rsync"),
		SessionIdle:     24 * time.Hour,
		SessionAbsolute: 7 * 24 * time.Hour,
	}
	var err error
	if c.AdminPassword, err = secret("RCSUI_ADMIN_PASSWORD"); err != nil {
		return c, err
	}
	if c.SecretKey, err = secret("RCSUI_SECRET_KEY"); err != nil {
		return c, err
	}
	if c.MetricsToken, err = secret("RCSUI_METRICS_TOKEN"); err != nil {
		return c, err
	}
	if c.MetricsToken != "" && len(c.MetricsToken) < 16 {
		return c, fmt.Errorf("RCSUI_METRICS_TOKEN must be at least 16 characters")
	}
	for _, r := range strings.Split(env("RCSUI_ALLOWED_ROOTS", ""), ",") {
		if r = strings.TrimSpace(r); r != "" {
			c.AllowedRoots = append(c.AllowedRoots, r)
		}
	}
	if c.MaxConcurrent, err = intEnv("RCSUI_MAX_CONCURRENT", 2); err != nil {
		return c, err
	}
	if c.MaxConcurrent < 1 || c.MaxConcurrent > 64 {
		return c, fmt.Errorf("RCSUI_MAX_CONCURRENT must be between 1 and 64")
	}
	days, err := intEnv("RCSUI_LOG_RETENTION_DAYS", 90)
	if err != nil {
		return c, err
	}
	c.LogRetention = time.Duration(days) * 24 * time.Hour
	if c.TrustedProxies, err = parseProxies(env("RCSUI_TRUSTED_PROXIES", "")); err != nil {
		return c, err
	}
	switch c.CookieSecure {
	case "auto", "true", "false":
	default:
		return c, fmt.Errorf("RCSUI_COOKIE_SECURE must be auto, true or false")
	}
	c.DisableRclone, _ = strconv.ParseBool(env("RCSUI_DISABLE_RCLONE", "false"))
	if c.RcloneEncrypt, err = strconv.ParseBool(env("RCSUI_RCLONE_CONFIG_ENCRYPT", "true")); err != nil {
		return c, fmt.Errorf("RCSUI_RCLONE_CONFIG_ENCRYPT must be true or false")
	}
	if err := loadOIDC(&c); err != nil {
		return c, err
	}
	return c, nil
}

func parseProxies(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			if a, err := netip.ParseAddr(p); err == nil {
				p = netip.PrefixFrom(a, a.BitLen()).String()
			}
		}
		pref, err := netip.ParsePrefix(p)
		if err != nil {
			return nil, fmt.Errorf("RCSUI_TRUSTED_PROXIES: %w", err)
		}
		out = append(out, pref.Masked())
	}
	return out, nil
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return def
}

func intEnv(k string, def int) (int, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", k, err)
	}
	return n, nil
}

// secret lit K, ou le fichier désigné par K_FILE.
func secret(k string) (string, error) {
	if f := os.Getenv(k + "_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", fmt.Errorf("%s_FILE: %w", k, err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	return os.Getenv(k), nil
}
