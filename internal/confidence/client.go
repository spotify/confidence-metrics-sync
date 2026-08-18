package confidence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Client is a typed client for the Confidence metrics service. It also
// reaches IAM for the few identity reads the CLI needs (owner validation).
type Client struct {
	baseURL string
	iamURL  string
	http    *http.Client
}

// APIError is a non-2xx response from the API.
type APIError struct {
	StatusCode int
	Status     string // gRPC status name when present, e.g. "NOT_FOUND"
	Message    string
}

func (e *APIError) Error() string {
	if e.Status != "" {
		return fmt.Sprintf("API error %d (%s): %s", e.StatusCode, e.Status, e.Message)
	}
	return fmt.Sprintf("API error %d: %s", e.StatusCode, e.Message)
}

// IsNotFound reports whether err is a 404 APIError.
func IsNotFound(err error) bool {
	apiErr, ok := err.(*APIError)
	return ok && apiErr.StatusCode == http.StatusNotFound
}

// retryBackoff is the wait between retries of transient gateway errors.
// Package variable so tests can shorten it. Sized for ApplyMetricsSync at
// large-account scale, which sits at the edge deadline and 503s
// intermittently (Linear C4S-1382): four attempts lift a coin-flip
// success rate above 90%.
var retryBackoff = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}

// isTransient reports whether the status is a gateway/overload error
// where the request may simply be retried.
func isTransient(status int) bool {
	return status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout
}

// doRetry wraps do with backoff on transient gateway errors. ONLY for
// idempotent calls: GETs, and ApplyMetricsSync — a reconciliation whose
// re-run converges (a retried apply that already committed plans
// UNCHANGED). Non-idempotent writes (Create/Update/Delete) must use do
// directly: a 503 after a committed create would double-create on retry.
func (c *Client) doRetry(ctx context.Context, method, path string, query url.Values, body, out any) error {
	return c.doRetryAt(ctx, c.baseURL, method, path, query, body, out)
}

func (c *Client) doRetryAt(ctx context.Context, base, method, path string, query url.Values, body, out any) error {
	var err error
	for attempt := 0; ; attempt++ {
		err = c.doAt(ctx, base, method, path, query, body, out)
		var apiErr *APIError
		if err == nil || attempt == len(retryBackoff) ||
			!errors.As(err, &apiErr) || !isTransient(apiErr.StatusCode) {
			return err
		}
		fmt.Fprintf(os.Stderr, "transient %d from %s, retrying in %s (attempt %d/%d)\n",
			apiErr.StatusCode, path, retryBackoff[attempt], attempt+2, len(retryBackoff)+1)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(retryBackoff[attempt]):
		}
	}
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	return c.doAt(ctx, c.baseURL, method, path, query, body, out)
}

func (c *Client) doAt(ctx context.Context, base, method, path string, query url.Values, body, out any) error {
	u := base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseAPIError(resp.StatusCode, data)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// parseAPIError handles both transcoding error shapes:
// {"code":5,"message":"...","status":"NOT_FOUND"} and {"error":{...}}.
func parseAPIError(statusCode int, body []byte) *APIError {
	var direct struct {
		Message string `json:"message"`
		Status  string `json:"status"`
		Error   *struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	apiErr := &APIError{StatusCode: statusCode, Message: string(body)}
	if err := json.Unmarshal(body, &direct); err == nil {
		switch {
		case direct.Error != nil && direct.Error.Message != "":
			apiErr.Message, apiErr.Status = direct.Error.Message, direct.Error.Status
		case direct.Message != "":
			apiErr.Message, apiErr.Status = direct.Message, direct.Status
		}
	}
	return apiErr
}

// listPages fetches all pages of a list endpoint.
func listPages[T any](
	ctx context.Context, c *Client, path, filter string,
	extract func(body []byte) ([]T, string, error),
) ([]T, error) {
	var all []T
	pageToken := ""
	for {
		q := url.Values{}
		q.Set("pageSize", "100") // API maximum; larger values are INVALID_ARGUMENT
		if filter != "" {
			q.Set("filter", filter)
		}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		var raw json.RawMessage
		if err := c.doRetry(ctx, http.MethodGet, path, q, nil, &raw); err != nil {
			return nil, err
		}
		items, next, err := extract(raw)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if next == "" {
			return all, nil
		}
		pageToken = next
	}
}

// --- Metrics ---

// ListMetrics returns all metrics matching the Lucene filter ("" = all).
func (c *Client) ListMetrics(ctx context.Context, filter string) ([]Metric, error) {
	return listPages(ctx, c, "/v1/metrics", filter, func(body []byte) ([]Metric, string, error) {
		var page struct {
			Metrics       []Metric `json:"metrics"`
			NextPageToken string   `json:"nextPageToken"`
		}
		err := json.Unmarshal(body, &page)
		return page.Metrics, page.NextPageToken, err
	})
}

// CreateMetric creates a metric.
func (c *Client) CreateMetric(ctx context.Context, m *Metric) (*Metric, error) {
	var out Metric
	if err := c.do(ctx, http.MethodPost, "/v1/metrics", nil, m, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateMetric patches the metric named m.Name. Empty updateMask patches all
// set fields; allowMissing upserts.
func (c *Client) UpdateMetric(ctx context.Context, m *Metric, updateMask []string, allowMissing bool) (*Metric, error) {
	q := url.Values{}
	if len(updateMask) > 0 {
		q.Set("updateMask", joinMask(updateMask))
	}
	if allowMissing {
		q.Set("allowMissing", "true")
	}
	var out Metric
	if err := c.do(ctx, http.MethodPatch, "/v1/"+m.Name, q, m, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteMetric soft-deletes a metric by resource name ("metrics/xyz").
func (c *Client) DeleteMetric(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/"+name, nil, nil, nil)
}

// ValidateMetric runs server-side validation without creating anything.
func (c *Client) ValidateMetric(ctx context.Context, m *Metric) (*ValidateMetricResponse, error) {
	var out ValidateMetricResponse
	body := map[string]any{"metric": m}
	if err := c.do(ctx, http.MethodPost, "/v1/metrics:validateMetric", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ApplyMetricsSync is the single write door for repo-managed resources.
// With DryRun it returns the exact plan a real apply would execute.
func (c *Client) ApplyMetricsSync(ctx context.Context, req *ApplyMetricsSyncRequest) (*ApplyMetricsSyncResponse, error) {
	var out ApplyMetricsSyncResponse
	if err := c.doRetry(ctx, http.MethodPost, "/v1/metrics:applyMetricsSync", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- Fact tables ---

// GetFactTable fetches one fact table by resource name ("factTables/xyz").
// Used to poll CREATING/UPDATING tables to ACTIVE or FAILED after apply.
func (c *Client) GetFactTable(ctx context.Context, name string) (*FactTable, error) {
	var out FactTable
	if err := c.doRetry(ctx, http.MethodGet, "/v1/"+name, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// BatchGetFactTables fetches fact tables by resource name. The API accepts at
// most 100 names per request, so larger inputs are split transparently.
func (c *Client) BatchGetFactTables(ctx context.Context, names []string) ([]FactTable, error) {
	var factTables []FactTable
	for start := 0; start < len(names); start += 100 {
		end := min(start+100, len(names))
		q := url.Values{}
		for _, name := range names[start:end] {
			q.Add("names", name)
		}
		var page struct {
			FactTables []FactTable `json:"factTables"`
		}
		if err := c.doRetry(ctx, http.MethodGet, "/v1/factTables:batchGet", q, nil, &page); err != nil {
			return nil, err
		}
		factTables = append(factTables, page.FactTables...)
	}
	return factTables, nil
}

// ListFactTables returns all fact tables matching the filter ("" = all).
func (c *Client) ListFactTables(ctx context.Context, filter string) ([]FactTable, error) {
	return listPages(ctx, c, "/v1/factTables", filter, func(body []byte) ([]FactTable, string, error) {
		var page struct {
			FactTables    []FactTable `json:"factTables"`
			NextPageToken string      `json:"nextPageToken"`
		}
		err := json.Unmarshal(body, &page)
		return page.FactTables, page.NextPageToken, err
	})
}

// CreateFactTable creates a fact table (starts in CREATING state).
func (c *Client) CreateFactTable(ctx context.Context, ft *FactTable) (*FactTable, error) {
	var out FactTable
	if err := c.do(ctx, http.MethodPost, "/v1/factTables", nil, ft, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateFactTable patches the fact table named ft.Name.
func (c *Client) UpdateFactTable(ctx context.Context, ft *FactTable, updateMask []string, allowMissing bool) (*FactTable, error) {
	q := url.Values{}
	if len(updateMask) > 0 {
		q.Set("updateMask", joinMask(updateMask))
	}
	if allowMissing {
		q.Set("allowMissing", "true")
	}
	var out FactTable
	if err := c.do(ctx, http.MethodPatch, "/v1/"+ft.Name, q, ft, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteFactTable soft-deletes a fact table by resource name.
func (c *Client) DeleteFactTable(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/"+name, nil, nil, nil)
}

// ValidateFactTable runs server-side validation including a warehouse schema
// check, without creating anything.
func (c *Client) ValidateFactTable(ctx context.Context, ft *FactTable) (*ValidateFactTableResponse, error) {
	var out ValidateFactTableResponse
	body := map[string]any{"factTable": ft}
	if err := c.do(ctx, http.MethodPost, "/v1/factTables:validateFactTable", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- Measurements ---

// ListMeasurements returns all measurements matching the filter ("" = all).
func (c *Client) ListMeasurements(ctx context.Context, filter string) ([]Measurement, error) {
	return listPages(ctx, c, "/v1/measurements", filter, func(body []byte) ([]Measurement, string, error) {
		var page struct {
			Measurements  []Measurement `json:"measurements"`
			NextPageToken string        `json:"nextPageToken"`
		}
		err := json.Unmarshal(body, &page)
		return page.Measurements, page.NextPageToken, err
	})
}

// CreateMeasurement creates a measurement.
func (c *Client) CreateMeasurement(ctx context.Context, m *Measurement) (*Measurement, error) {
	var out Measurement
	if err := c.do(ctx, http.MethodPost, "/v1/measurements", nil, m, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateMeasurement patches the measurement named m.Name.
func (c *Client) UpdateMeasurement(ctx context.Context, m *Measurement, updateMask []string, allowMissing bool) (*Measurement, error) {
	q := url.Values{}
	if len(updateMask) > 0 {
		q.Set("updateMask", joinMask(updateMask))
	}
	if allowMissing {
		q.Set("allowMissing", "true")
	}
	var out Measurement
	if err := c.do(ctx, http.MethodPatch, "/v1/"+m.Name, q, m, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteMeasurement soft-deletes a measurement by resource name.
func (c *Client) DeleteMeasurement(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/"+name, nil, nil, nil)
}

// --- Entities ---

// ListEntities returns all entities in the account.
func (c *Client) ListEntities(ctx context.Context) ([]Entity, error) {
	return listPages(ctx, c, "/v1/entities", "", func(body []byte) ([]Entity, string, error) {
		var page struct {
			Entities      []Entity `json:"entities"`
			NextPageToken string   `json:"nextPageToken"`
		}
		err := json.Unmarshal(body, &page)
		return page.Entities, page.NextPageToken, err
	})
}

func joinMask(fields []string) string {
	out := ""
	for i, f := range fields {
		if i > 0 {
			out += ","
		}
		out += f
	}
	return out
}

// --- Identities (IAM) ---

// identityBatchSize caps names per ListIdentities query. Well under the
// pageSize below, so a batch can never spill into a second page.
const identityBatchSize = 50

// LookupIdentities resolves identity resource names ("identities/abc123") in
// batches, returning only those that exist, keyed by name. A name absent
// from the result does not exist in the account.
//
// This goes through ListIdentities rather than GetIdentity on purpose.
// GetIdentity requires the can_read relation on the identity and answers
// PERMISSION_DENIED — not NOT_FOUND — for a name that does not exist
// (verified live 2026-07-29), so it cannot tell "missing" from "not allowed
// to look". ListIdentities carries no relation requirement and simply omits
// names that do not exist.
func (c *Client) LookupIdentities(ctx context.Context, names []string) (map[string]Identity, error) {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	found := make(map[string]Identity, len(names))

	for start := 0; start < len(names); start += identityBatchSize {
		end := min(start+identityBatchSize, len(names))
		if err := c.lookupIdentityBatch(ctx, names[start:end], want, found); err != nil {
			return nil, err
		}
	}
	return found, nil
}

func (c *Client) lookupIdentityBatch(ctx context.Context, batch []string, want map[string]bool, found map[string]Identity) error {
	q := url.Values{}
	q.Set("filter", identityNameFilter(batch))
	q.Set("pageSize", "100")

	for {
		var page struct {
			Identities    []Identity `json:"identities"`
			NextPageToken string     `json:"nextPageToken"`
		}
		if err := c.doRetryAt(ctx, c.iamURL, http.MethodGet, "/v1/identities", q, nil, &page); err != nil {
			return fmt.Errorf("looking up owners: %w", err)
		}
		for _, id := range page.Identities {
			// The filter is a Lucene query, so it can match more loosely than
			// an exact resource name — only take back what was asked for.
			if want[id.Name] {
				found[id.Name] = id
			}
		}
		if page.NextPageToken == "" {
			return nil
		}
		q.Set("pageToken", page.NextPageToken)
	}
}

// identityNameFilter builds the Lucene query matching exactly these names.
func identityNameFilter(names []string) string {
	if len(names) == 1 {
		return "name:" + strconv.Quote(names[0])
	}
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		quoted = append(quoted, strconv.Quote(n))
	}
	return "name:(" + strings.Join(quoted, " OR ") + ")"
}

// FriendlyLookupResult holds all IAM candidates returned for one friendly
// owner query (a display name or email, not a resource name).
type FriendlyLookupResult struct {
	Query      string
	Candidates []Identity
}

// LookupIdentitiesByFriendlyName resolves friendly owner references (display
// names or emails) against IAM, scoped to users and groups. Each distinct
// query is one ListIdentities call; duplicates are resolved once.
//
// Returns one FriendlyLookupResult per input query, in input order, with
// duplicates sharing the same result.
func (c *Client) LookupIdentitiesByFriendlyName(ctx context.Context, queries []string) ([]FriendlyLookupResult, error) {
	// Deduplicate: resolve each distinct query once.
	type entry struct {
		result FriendlyLookupResult
		err    error
	}
	cache := map[string]*entry{}
	for _, q := range queries {
		if _, ok := cache[q]; !ok {
			r, err := c.lookupFriendly(ctx, q)
			cache[q] = &entry{r, err}
		}
	}
	// Reassemble in input order.
	out := make([]FriendlyLookupResult, len(queries))
	for i, q := range queries {
		e := cache[q]
		if e.err != nil {
			return nil, e.err
		}
		out[i] = e.result
	}
	return out, nil
}

func (c *Client) lookupFriendly(ctx context.Context, query string) (FriendlyLookupResult, error) {
	var field string
	if strings.Contains(query, "@") {
		field = "email"
	} else {
		field = "displayName"
	}
	filter := field + ":" + strconv.Quote(query)

	q := url.Values{}
	q.Set("filter", filter)
	q.Set("pageSize", "100")

	var candidates []Identity
	for {
		var page struct {
			Identities    []Identity `json:"identities"`
			NextPageToken string     `json:"nextPageToken"`
		}
		if err := c.doRetryAt(ctx, c.iamURL, http.MethodGet, "/v1/identities", q, nil, &page); err != nil {
			return FriendlyLookupResult{}, fmt.Errorf("resolving owner %q: %w", query, err)
		}
		candidates = append(candidates, page.Identities...)
		if page.NextPageToken == "" {
			break
		}
		q.Set("pageToken", page.NextPageToken)
	}
	return FriendlyLookupResult{Query: query, Candidates: candidates}, nil
}
