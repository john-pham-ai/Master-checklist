package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
	"golang.org/x/oauth2"
	"google.golang.org/api/idtoken"
)

// dataAPI talks to the apps-platform Data API (https://dataapi.{URL_BASE}),
// which proxies Google/Slack/… APIs using the signed-in user's own OAuth
// connection. Two credentials are needed per call:
//
//   - Authorization: an ID token for the Data API audience, minted by the
//     Cloud Run service account (or DATA_API_AUTH_TOKEN from
//     `apps-platform app forwarder` for local development);
//   - X-Request-Token: the per-request session token the platform's proxy
//     injects into browser requests (or X_REQUEST_TOKEN from the forwarder).
//
// SOCKS_PORT (also from the forwarder) routes calls through the bastion
// tunnel, since dataapi.* only resolves inside the VPC.
type dataAPI struct {
	baseURL string

	mu     sync.Mutex
	client *http.Client
	tokens oauth2.TokenSource
}

func newDataAPI() *dataAPI {
	base := os.Getenv("DATA_API_URL")
	if base == "" && os.Getenv("URL_BASE") != "" {
		base = "https://dataapi." + os.Getenv("URL_BASE")
	}
	return &dataAPI{baseURL: strings.TrimRight(base, "/")}
}

func (d *dataAPI) enabled() bool { return d.baseURL != "" }

// requestToken returns the platform-injected session token for this request.
func (d *dataAPI) requestToken(r *http.Request) string {
	if t := r.Header.Get("X-Request-Token"); t != "" {
		return t
	}
	return os.Getenv("X_REQUEST_TOKEN")
}

func (d *dataAPI) httpClient() (*http.Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.client != nil {
		return d.client, nil
	}
	c := &http.Client{Timeout: 30 * time.Second}
	if port := os.Getenv("SOCKS_PORT"); port != "" {
		dialer, err := proxy.SOCKS5("tcp", "localhost:"+port, nil, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("socks dialer: %w", err)
		}
		c.Transport = &http.Transport{DialContext: dialer.(proxy.ContextDialer).DialContext}
	}
	d.client = c
	return c, nil
}

func (d *dataAPI) authHeader() (string, error) {
	if t := os.Getenv("DATA_API_AUTH_TOKEN"); t != "" {
		return "Bearer " + t, nil
	}
	d.mu.Lock()
	if d.tokens == nil {
		ts, err := idtoken.NewTokenSource(context.Background(), d.baseURL)
		if err != nil {
			d.mu.Unlock()
			return "", fmt.Errorf("idtoken.NewTokenSource: %w", err)
		}
		d.tokens = ts
	}
	ts := d.tokens
	d.mu.Unlock()

	tok, err := ts.Token()
	if err != nil {
		return "", fmt.Errorf("mint id token: %w", err)
	}
	return "Bearer " + tok.AccessToken, nil
}

// dataAPIError is a non-2xx response from the Data API or the upstream API.
type dataAPIError struct {
	Status int
	Body   string
}

func (e *dataAPIError) Error() string {
	return fmt.Sprintf("data api: HTTP %d: %s", e.Status, truncate(e.Body, 300))
}

// needsConnect reports whether the failure most likely means the user has not
// connected the integration for this app (or connected it without the needed
// scope), in which case the browser should run the OAuth popup and retry.
func (e *dataAPIError) needsConnect() bool {
	b := strings.ToLower(e.Body)
	for _, hint := range []string{"connect", "connection", "credential", "nango", "insufficient", "scope", "not authorized", "unauthorized"} {
		if strings.Contains(b, hint) {
			return true
		}
	}
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusPreconditionFailed, http.StatusNotFound:
		return true
	}
	return false
}

// do performs an authenticated Data API request; a JSON response is decoded
// into out when out is non-nil.
func (d *dataAPI) do(ctx context.Context, requestToken, method, path string, body, out interface{}) error {
	if !d.enabled() {
		return fmt.Errorf("data api not configured (URL_BASE is unset)")
	}
	if requestToken == "" {
		return fmt.Errorf("missing X-Request-Token: request did not arrive through the platform proxy")
	}
	client, err := d.httpClient()
	if err != nil {
		return err
	}
	auth, err := d.authHeader()
	if err != nil {
		return err
	}

	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, d.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("X-Request-Token", requestToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &dataAPIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// startOAuth returns the URL the browser must open (in a popup) to connect
// the integration for the current user with the given scopes.
func (d *dataAPI) startOAuth(ctx context.Context, requestToken, integration string, scopes []string) (string, error) {
	q := url.Values{"integration": {integration}}
	if len(scopes) > 0 {
		q.Set("scopes", strings.Join(scopes, ","))
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := d.do(ctx, requestToken, "POST", "/api/data/oauth/start?"+q.Encode(), nil, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", fmt.Errorf("data api returned no oauth url")
	}
	return out.URL, nil
}

const gmailSendScope = "https://www.googleapis.com/auth/gmail.send"

// sendGmail sends an RFC 822 message from the signed-in user's Gmail account.
// Returns the Gmail message ID.
func (d *dataAPI) sendGmail(ctx context.Context, requestToken string, rfc822 []byte) (string, error) {
	payload := map[string]string{"raw": base64.URLEncoding.EncodeToString(rfc822)}
	var out struct {
		ID string `json:"id"`
	}
	if err := d.do(ctx, requestToken, "POST", "/api/data/google-mail/gmail/v1/users/me/messages/send", payload, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
