package confidence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// tokenTransport exchanges client credentials for a bearer token at the IAM
// service and attaches it to every request, refreshing before expiry.
// Ported from terraform-provider-confidence's jwtTransport.
//
// Error messages from this transport never include the client secret or the
// token — only the client ID.
type tokenTransport struct {
	mu           sync.Mutex
	clientID     string
	clientSecret string
	iamBaseURL   string
	base         http.RoundTripper

	token       string
	tokenExpiry time.Time
}

// refreshSkew renews the token slightly before its expiry to avoid using a
// token that expires mid-request.
const refreshSkew = 30 * time.Second

func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	if t.token == "" || time.Now().After(t.tokenExpiry.Add(-refreshSkew)) {
		if err := t.refreshToken(req.Context()); err != nil {
			t.mu.Unlock()
			return nil, fmt.Errorf("authenticating client %q: %w", t.clientID, err)
		}
	}
	token := t.token
	t.mu.Unlock()

	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(req)
}

func (t *tokenTransport) refreshToken(ctx context.Context) error {
	body, err := json.Marshal(map[string]string{
		"grant_type":    "client_credentials",
		"client_id":     t.clientID,
		"client_secret": t.clientSecret,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, t.iamBaseURL+"/v1/oauth/token", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	// Base transport directly: the token endpoint must not recurse into this
	// transport.
	resp, err := (&http.Client{Transport: t.base}).Do(req)
	if err != nil {
		return fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token request returned HTTP %d", resp.StatusCode)
	}

	// expiresIn is a string-encoded int64 in the IAM response.
	var tokenResp struct {
		AccessToken string `json:"accessToken"`
		ExpiresIn   int64  `json:"expiresIn,string"`
	}
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return fmt.Errorf("decoding token response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return fmt.Errorf("token response contained no access token")
	}

	t.token = tokenResp.AccessToken
	t.tokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	return nil
}
