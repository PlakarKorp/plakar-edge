package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// enrollRetryDelayForTest shortens the enroll backoff for the duration of a test
// and returns a restore func.
func enrollRetryDelayForTest(t *testing.T, d time.Duration) func() {
	t.Helper()
	orig := enrollRetryDelay
	enrollRetryDelay = d
	return func() { enrollRetryDelay = orig }
}

// The client surfaces a 4xx/5xx as an *HTTPError carrying the status, so the
// enroll loop can tell a definitive rejection from a transient failure.
func TestClientHTTPErrorCarriesStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL).Enroll(context.Background(), EnrollRequest{})
	var he *HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("want *HTTPError, got %T (%v)", err, err)
	}
	if he.Status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", he.Status)
	}
}

// enroll retries on transient failures (here: 503) and succeeds once the control
// plane recovers, without fatal-exiting.
func TestEnrollRetriesThenSucceeds(t *testing.T) {
	origDelay := enrollRetryDelayForTest(t, 10*time.Millisecond)
	defer origDelay()

	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable) // transient
			return
		}
		_ = json.NewEncoder(w).Encode(EnrollResponse{
			EdgeId:    uuid.MustParse("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"),
			Token:     "tok",
			Supported: true,
		})
	}))
	defer srv.Close()

	cfg := &Config{APIURL: srv.URL, StateDir: t.TempDir()}
	st := enroll(context.Background(), cfg, "test", enrollIntoOrganization(NewClient(srv.URL), testOrg, "key", "edge", "host"))
	if st == nil {
		t.Fatal("enroll returned nil (context not canceled, should have succeeded)")
	}
	if st.Token != "tok" {
		t.Errorf("token = %q, want tok", st.Token)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("attempts = %d, want 3 (two transient failures then success)", got)
	}
}

// enroll returns nil (not fatal, not hang) when the context is canceled while it
// is still retrying a transiently-failing control plane.
func TestEnrollReturnsNilOnContextCancel(t *testing.T) {
	origDelay := enrollRetryDelayForTest(t, 50*time.Millisecond)
	defer origDelay()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable) // never recovers
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	cfg := &Config{APIURL: srv.URL, StateDir: t.TempDir()}
	if st := enroll(ctx, cfg, "test", enrollIntoOrganization(NewClient(srv.URL), testOrg, "key", "edge", "host")); st != nil {
		t.Errorf("enroll = %+v, want nil on context cancel", st)
	}
}

// testOrg is the organization the enrollment tests join.
var testOrg = uuid.MustParse("11111111-2222-3333-4444-555555555555")

// The organization reaches the control plane on the wire.
//
// plakman requires it and validates it as a uuid, so an edge that does not send
// it is refused -- and the refusal is a 400 that reads like a bad key rather
// than a missing field.
func TestEnrollSendsTheOrganization(t *testing.T) {
	var got EnrollRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(EnrollResponse{
			EdgeId:    uuid.MustParse("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"),
			Token:     "tok",
			Supported: true,
		})
	}))
	defer srv.Close()

	cfg := &Config{APIURL: srv.URL, StateDir: t.TempDir()}
	if st := enroll(context.Background(), cfg, "test", enrollIntoOrganization(NewClient(srv.URL), testOrg, "key", "edge", "host")); st == nil {
		t.Fatal("enroll returned nil, should have succeeded")
	}
	if got.OrganizationID != testOrg {
		t.Errorf("organization_id = %v, want %v", got.OrganizationID, testOrg)
	}
	if got.EnrollmentKey != "key" {
		t.Errorf("enrollment_key = %q, want key", got.EnrollmentKey)
	}
}

// And it is spelled the way plakman reads it: the field is matched by json tag,
// so a rename on either side is a silently absent organization.
func TestEnrollRequestUsesTheAgreedFieldName(t *testing.T) {
	raw, err := json.Marshal(EnrollRequest{OrganizationID: testOrg})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["organization_id"]; !ok {
		t.Errorf("no organization_id in %s", raw)
	}
}

// A pool enrollment goes to the pool endpoint with the pool's key and names no
// organization: the control plane derives it from the pool.
func TestEnrollIntoPoolSendsTheKeyToThePoolEndpoint(t *testing.T) {
	var path string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(EnrollResponse{
			EdgeId:    uuid.MustParse("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"),
			Token:     "tok",
			Supported: true,
		})
	}))
	defer srv.Close()

	cfg := &Config{APIURL: srv.URL, StateDir: t.TempDir()}
	st := enroll(context.Background(), cfg, "test", enrollIntoPool(NewClient(srv.URL), "pool-key", "edge", "host"))
	if st == nil {
		t.Fatal("enroll returned nil, should have succeeded")
	}
	if st.Token != "tok" {
		t.Errorf("token = %q, want tok", st.Token)
	}
	if path != "/api/v1/edge/pools/enroll" {
		t.Errorf("path = %s, want /api/v1/edge/pools/enroll", path)
	}
	if got["enrollment_key"] != "pool-key" {
		t.Errorf("enrollment_key = %v, want pool-key", got["enrollment_key"])
	}
	if got["name"] != "edge" {
		t.Errorf("name = %v, want edge", got["name"])
	}
	if _, ok := got["organization_id"]; ok {
		t.Errorf("pool enrollment sent an organization_id: %v", got)
	}
}

func TestCheckEnrollFlags(t *testing.T) {
	tests := []struct {
		name              string
		poolKey, key, org string
		wantErr           bool
	}{
		{name: "organization enrollment", key: "k", org: "o"},
		{name: "pool enrollment", poolKey: "p"},
		{name: "nothing", wantErr: false},
		{name: "pool with key", poolKey: "p", key: "k", wantErr: true},
		{name: "pool with organization", poolKey: "p", org: "o", wantErr: true},
		{name: "pool with both", poolKey: "p", key: "k", org: "o", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkEnrollFlags(tt.poolKey, tt.key, tt.org)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkEnrollFlags(%q, %q, %q) = %v, wantErr %v", tt.poolKey, tt.key, tt.org, err, tt.wantErr)
			}
		})
	}
}

func TestPoolEnrollmentKeyOrEnv(t *testing.T) {
	tests := []struct {
		name, flag, env, want string
	}{
		{name: "flag only", flag: "f", want: "f"},
		{name: "env only", env: "e", want: "e"},
		{name: "flag wins over env", flag: "f", env: "e", want: "f"},
		{name: "neither"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PLAKAR_EDGE_POOL_ENROLL_KEY", tt.env)
			if got := poolEnrollmentKeyOrEnv(tt.flag); got != tt.want {
				t.Errorf("poolEnrollmentKeyOrEnv(%q) = %q, want %q", tt.flag, got, tt.want)
			}
		})
	}
}
