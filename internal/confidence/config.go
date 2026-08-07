// Package confidence is a typed HTTP client for the Confidence management
// APIs (metrics service). It speaks the transcoded JSON/REST surface —
// protojson conventions: camelCase field names, enums as strings, durations
// as "86400s" strings.
package confidence

import (
	"fmt"
	"net/http"
	"os"
)

const (
	defaultMetricsURL = "https://metrics.confidence.dev"
	defaultIAMURL     = "https://iam.confidence.dev"

	// EnvClientID and friends are the canonical configuration environment
	// variables, shared with terraform-provider-confidence.
	EnvClientID     = "CONFIDENCE_CLIENT_ID"
	EnvClientSecret = "CONFIDENCE_CLIENT_SECRET"
	EnvMetricsURL   = "CONFIDENCE_METRICS_URL"
	EnvIAMURL       = "CONFIDENCE_IAM_URL"
)

// Config carries credentials and endpoints. Secrets are read from the
// environment only — never from flags or files.
type Config struct {
	ClientID     string
	ClientSecret string
	MetricsURL   string
	IAMURL       string
}

// ConfigFromEnv resolves configuration from the environment. The boolean
// reports whether credentials are present (callers degrade gracefully when
// they are not).
func ConfigFromEnv() (Config, bool) {
	cfg := Config{
		ClientID:     os.Getenv(EnvClientID),
		ClientSecret: os.Getenv(EnvClientSecret),
		MetricsURL:   envOr(EnvMetricsURL, defaultMetricsURL),
		IAMURL:       envOr(EnvIAMURL, defaultIAMURL),
	}
	return cfg, cfg.ClientID != "" && cfg.ClientSecret != ""
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// NewClient builds a Client authenticating with OAuth2 client credentials.
func NewClient(cfg Config) (*Client, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, fmt.Errorf("missing credentials: set %s and %s", EnvClientID, EnvClientSecret)
	}
	return &Client{
		baseURL: cfg.MetricsURL,
		iamURL:  cfg.IAMURL,
		http: &http.Client{
			Transport: &tokenTransport{
				clientID:     cfg.ClientID,
				clientSecret: cfg.ClientSecret,
				iamBaseURL:   cfg.IAMURL,
				base:         http.DefaultTransport,
			},
		},
	}, nil
}
