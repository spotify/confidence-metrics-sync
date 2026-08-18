package confidence

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient wires a Client against fake IAM + metrics servers.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *atomic.Int64) {
	t.Helper()
	var tokenRequests atomic.Int64

	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenRequests.Add(1)
		if r.URL.Path != "/v1/oauth/token" {
			t.Errorf("unexpected IAM path %s", r.URL.Path)
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] != "client_credentials" || body["client_id"] != "test-id" {
			t.Errorf("unexpected token request body: %v", body)
		}
		// expiresIn is string-encoded, as the real IAM returns it.
		fmt.Fprint(w, `{"accessToken": "test-token", "expiresIn": "3600"}`)
	}))
	t.Cleanup(iam.Close)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("missing/wrong Authorization header: %q", got)
		}
		handler(w, r)
	}))
	t.Cleanup(api.Close)

	c, err := NewClient(Config{
		ClientID: "test-id", ClientSecret: "test-secret",
		MetricsURL: api.URL, IAMURL: iam.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, &tokenRequests
}

func TestTokenFetchedOnceAndReused(t *testing.T) {
	c, tokenRequests := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"metrics": []}`)
	})
	for range 3 {
		if _, err := c.ListMetrics(context.Background(), ""); err != nil {
			t.Fatal(err)
		}
	}
	if n := tokenRequests.Load(); n != 1 {
		t.Fatalf("expected 1 token request, got %d", n)
	}
}

func TestListMetricsPaginatesAndFilters(t *testing.T) {
	var pages atomic.Int64
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/metrics" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("filter"); got != `source.reference:"repo"` {
			t.Errorf("unexpected filter %q", got)
		}
		if pages.Add(1) == 1 {
			fmt.Fprint(w, `{"metrics": [{"name": "metrics/a", "displayName": "A"}], "nextPageToken": "t2"}`)
			return
		}
		if got := r.URL.Query().Get("pageToken"); got != "t2" {
			t.Errorf("expected pageToken t2, got %q", got)
		}
		fmt.Fprint(w, `{"metrics": [{"name": "metrics/b", "displayName": "B"}]}`)
	})

	metrics, err := c.ListMetrics(context.Background(), `source.reference:"repo"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 2 || metrics[0].Name != "metrics/a" || metrics[1].Name != "metrics/b" {
		t.Fatalf("unexpected metrics: %+v", metrics)
	}
}

func TestBatchGetFactTablesUsesExactNames(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/factTables:batchGet" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		got := r.URL.Query()["names"]
		if len(got) != 2 || got[0] != "factTables/a" || got[1] != "factTables/b" {
			t.Errorf("unexpected names query: %v", got)
		}
		fmt.Fprint(w, `{"factTables":[{"name":"factTables/a"},{"name":"factTables/b"}]}`)
	})

	factTables, err := c.BatchGetFactTables(context.Background(), []string{"factTables/a", "factTables/b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(factTables) != 2 || factTables[0].Name != "factTables/a" || factTables[1].Name != "factTables/b" {
		t.Fatalf("unexpected fact tables: %+v", factTables)
	}
}

func TestUpdateMetricSendsMaskAndAllowMissing(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/v1/metrics/xyz" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("updateMask") != "description,measurementConfig" || q.Get("allowMissing") != "true" {
			t.Errorf("unexpected query: %v", q)
		}
		var body Metric
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Source == nil || body.Source.Type != "REPOSITORY" {
			t.Errorf("expected repository source, got %+v", body.Source)
		}
		fmt.Fprint(w, `{"name": "metrics/xyz"}`)
	})

	m := &Metric{Name: "metrics/xyz", Source: &MetricSource{Type: "REPOSITORY", Reference: "repo"}}
	if _, err := c.UpdateMetric(context.Background(), m, []string{"description", "measurementConfig"}, true); err != nil {
		t.Fatal(err)
	}
}

func TestAPIErrorParsing(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"code": 5, "message": "metric not found", "status": "NOT_FOUND"}`)
	})
	err := c.DeleteMetric(context.Background(), "metrics/gone")
	if !IsNotFound(err) {
		t.Fatalf("expected NotFound APIError, got %v", err)
	}
	apiErr := err.(*APIError)
	if apiErr.Message != "metric not found" || apiErr.Status != "NOT_FOUND" {
		t.Fatalf("unexpected parse: %+v", apiErr)
	}
}

func TestErrorsNeverContainSecret(t *testing.T) {
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error": "bad credentials"}`)
	}))
	defer iam.Close()

	c, err := NewClient(Config{
		ClientID: "test-id", ClientSecret: "super-secret-value",
		MetricsURL: "http://unused.invalid", IAMURL: iam.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ListMetrics(context.Background(), "")
	if err == nil {
		t.Fatal("expected auth error")
	}
	if msg := err.Error(); strings.Contains(msg, "super-secret-value") {
		t.Fatalf("error message leaks the client secret: %s", msg)
	} else if !strings.Contains(msg, "test-id") {
		t.Fatalf("error message should name the client id: %s", msg)
	}
}

// shortBackoff makes retries immediate for tests and restores the real
// schedule afterwards.
func shortBackoff(t *testing.T) {
	t.Helper()
	saved := retryBackoff
	retryBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { retryBackoff = saved })
}

func TestApplyMetricsSyncRetriesTransient503(t *testing.T) {
	shortBackoff(t)
	var calls atomic.Int64
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			// Bare 503 with empty body, as the edge produces at deadline
			// (Linear C4S-1382).
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"outcomes": [{"name": "metrics/m1", "action": "UNCHANGED"}]}`)
	})

	resp, err := c.ApplyMetricsSync(context.Background(), &ApplyMetricsSyncRequest{Reference: "r"})
	if err != nil {
		t.Fatalf("expected retries to succeed, got %v", err)
	}
	if len(resp.Outcomes) != 1 || calls.Load() != 3 {
		t.Fatalf("expected success on 3rd call, got %d calls, %+v", calls.Load(), resp)
	}
}

func TestTransient503GivesUpAfterAllAttempts(t *testing.T) {
	shortBackoff(t)
	var calls atomic.Int64
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	_, err := c.ApplyMetricsSync(context.Background(), &ApplyMetricsSyncRequest{Reference: "r"})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if want := int64(len(retryBackoff) + 1); calls.Load() != want {
		t.Fatalf("expected %d attempts, got %d", want, calls.Load())
	}
}

func TestNonTransientErrorIsNotRetried(t *testing.T) {
	shortBackoff(t)
	var calls atomic.Int64
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"code":3,"message":"bad","status":"INVALID_ARGUMENT"}`)
	})

	_, err := c.ApplyMetricsSync(context.Background(), &ApplyMetricsSyncRequest{Reference: "r"})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("400 must fail on first attempt, got %d calls, err=%v", calls.Load(), err)
	}
}

func TestNonIdempotentCreateIsNotRetried(t *testing.T) {
	shortBackoff(t)
	var calls atomic.Int64
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	// A 503 after a committed create would double-create on retry — creates
	// must stay single-shot.
	_, err := c.CreateMetric(context.Background(), &Metric{DisplayName: "m"})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("create must not retry, got %d calls, err=%v", calls.Load(), err)
	}
}

// identityClient wires a Client whose IAM server serves both the token
// endpoint and /v1/identities. Returns the client and the filters observed.
func identityClient(t *testing.T, identities func(filter string) string) (*Client, *[]string) {
	t.Helper()
	var filters []string

	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth/token" {
			fmt.Fprint(w, `{"accessToken": "test-token", "expiresIn": "3600"}`)
			return
		}
		if r.URL.Path != "/v1/identities" {
			t.Errorf("unexpected IAM path %s", r.URL.Path)
			return
		}
		filter := r.URL.Query().Get("filter")
		filters = append(filters, filter)
		fmt.Fprint(w, identities(filter))
	}))
	t.Cleanup(iam.Close)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("identity lookups must not hit the metrics API: %s", r.URL.Path)
	}))
	t.Cleanup(api.Close)

	c, err := NewClient(Config{
		ClientID: "test-id", ClientSecret: "test-secret",
		MetricsURL: api.URL, IAMURL: iam.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, &filters
}

// identitiesFor answers a name-filter query with the names it asked for that
// exist in account, mimicking ListIdentities.
func identitiesFor(account map[string]bool) func(string) string {
	return func(filter string) string {
		var out []string
		for _, name := range quotedNames(filter) {
			if account[name] {
				out = append(out, fmt.Sprintf(`{"name": %q, "displayName": "Team"}`, name))
			}
		}
		return `{"identities": [` + strings.Join(out, ",") + `]}`
	}
}

// quotedNames pulls every "quoted" token out of a filter string.
func quotedNames(filter string) []string {
	var names []string
	rest := filter
	for {
		_, after, ok := strings.Cut(rest, `"`)
		if !ok {
			return names
		}
		name, tail, ok := strings.Cut(after, `"`)
		if !ok {
			return names
		}
		names = append(names, name)
		rest = tail
	}
}

func TestLookupIdentities(t *testing.T) {
	many := make([]string, 0, identityBatchSize+3)
	for i := 0; i < identityBatchSize+3; i++ {
		many = append(many, fmt.Sprintf("identities/id%d", i))
	}

	cases := []struct {
		name        string
		exists      []string
		request     []string
		wantFound   []string
		wantFilters []string // nil: only the request count is checked
	}{{
		name:        "one name uses the plain filter form",
		exists:      []string{"identities/abc123"},
		request:     []string{"identities/abc123"},
		wantFound:   []string{"identities/abc123"},
		wantFilters: []string{`name:"identities/abc123"`},
	}, {
		name:      "names that do not exist are simply absent",
		exists:    []string{"identities/real"},
		request:   []string{"identities/real", "identities/nope"},
		wantFound: []string{"identities/real"},
	}, {
		name:        "several names share one OR query",
		exists:      []string{"identities/a", "identities/b", "identities/c"},
		request:     []string{"identities/a", "identities/b", "identities/c"},
		wantFound:   []string{"identities/a", "identities/b", "identities/c"},
		wantFilters: []string{`name:("identities/a" OR "identities/b" OR "identities/c")`},
	}, {
		name:      "past the batch size the names are chunked",
		exists:    many,
		request:   many,
		wantFound: many,
		// One request per chunk: no query grows unbounded, and no batch can
		// spill past a page.
		wantFilters: nil,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := map[string]bool{}
			for _, n := range tc.exists {
				account[n] = true
			}
			c, filters := identityClient(t, identitiesFor(account))

			found, err := c.LookupIdentities(context.Background(), tc.request)
			if err != nil {
				t.Fatal(err)
			}
			if len(found) != len(tc.wantFound) {
				t.Errorf("found %d identities, want %d", len(found), len(tc.wantFound))
			}
			for _, want := range tc.wantFound {
				if _, ok := found[want]; !ok {
					t.Errorf("%q missing from the result", want)
				}
			}
			if tc.wantFilters != nil {
				if len(*filters) != len(tc.wantFilters) {
					t.Fatalf("sent %d requests, want %d: %q", len(*filters), len(tc.wantFilters), *filters)
				}
				for i, want := range tc.wantFilters {
					if (*filters)[i] != want {
						t.Errorf("filter[%d] =\n%q\nwant\n%q", i, (*filters)[i], want)
					}
				}
			} else {
				wantRequests := (len(tc.request) + identityBatchSize - 1) / identityBatchSize
				if len(*filters) != wantRequests {
					t.Errorf("sent %d requests for %d names, want %d", len(*filters), len(tc.request), wantRequests)
				}
			}
		})
	}
}

// Field decoding, including the deactivated flag the owner check keys on:
// per-identity, and from a BATCHED response, where a mixed page must not
// smear one identity's flag onto another.
func TestLookupIdentitiesDecodesFields(t *testing.T) {
	c, _ := identityClient(t, func(string) string {
		return `{"identities": [
			{"name": "identities/abc123", "displayName": "Growth Team", "email": "growth@example.com", "deactivated": true},
			{"name": "identities/def456", "displayName": "Still Here", "email": "here@example.com"}
		]}`
	})

	found, err := c.LookupIdentities(context.Background(),
		[]string{"identities/abc123", "identities/def456"})
	if err != nil {
		t.Fatal(err)
	}
	gone := found["identities/abc123"]
	if gone.DisplayName != "Growth Team" || gone.Email != "growth@example.com" || !gone.Deactivated {
		t.Errorf("deactivated identity not decoded: %+v", gone)
	}
	if active := found["identities/def456"]; active.Deactivated {
		t.Errorf("active identity marked deactivated: %+v", active)
	}
}

// The Lucene filter can match more loosely than an exact resource name, so a
// near-match must not count as existence.
func TestLookupIdentitiesRequireExactNames(t *testing.T) {
	c, _ := identityClient(t, func(string) string {
		return `{"identities": [{"name": "identities/abc123456", "displayName": "Someone Else"}]}`
	})

	found, err := c.LookupIdentities(context.Background(), []string{"identities/abc123"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("a different resource name was accepted as a match: %v", found)
	}
}

// A batch could in principle be paginated; follow the token rather than
// silently reporting the tail as missing.
func TestLookupIdentitiesFollowsPagination(t *testing.T) {
	calls := 0
	c, _ := identityClient(t, func(string) string {
		calls++
		if calls == 1 {
			return `{"identities": [{"name": "identities/a"}], "nextPageToken": "tok2"}`
		}
		return `{"identities": [{"name": "identities/b"}]}`
	})

	found, err := c.LookupIdentities(context.Background(), []string{"identities/a", "identities/b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Errorf("pagination dropped identities: %v", found)
	}
}
