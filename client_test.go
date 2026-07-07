package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewClientTrimsTrailingSlash(t *testing.T) {
	c := NewClient("http://example.com/")
	if c.baseURL != "http://example.com" {
		t.Fatalf("baseURL = %q, want %q", c.baseURL, "http://example.com")
	}
}

func TestSetToken(t *testing.T) {
	c := NewClient("http://example.com")
	if c.token != "" {
		t.Fatalf("token should start empty, got %q", c.token)
	}
	c.SetToken("abc")
	if c.token != "abc" {
		t.Fatalf("token = %q, want %q", c.token, "abc")
	}
}

func TestEnrollSuccess(t *testing.T) {
	wantID := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/edge/enroll" {
			t.Errorf("path = %s, want /api/v1/edge/enroll", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q, want application/json", ct)
		}
		var req EnrollRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.EnrollmentKey != "secret" || req.Name != "edge1" || req.Hostname != "host1" {
			t.Errorf("unexpected request: %+v", req)
		}
		resp := EnrollResponse{EdgeId: wantID, Token: "tok123"}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	resp, err := c.Enroll(context.Background(), EnrollRequest{
		EnrollmentKey: "secret",
		Name:          "edge1",
		Hostname:      "host1",
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if resp.EdgeId != wantID {
		t.Errorf("EdgeId = %s, want %s", resp.EdgeId, wantID)
	}
	if resp.Token != "tok123" {
		t.Errorf("Token = %q, want %q", resp.Token, "tok123")
	}
}

func TestEnrollErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("bad enrollment key"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.Enroll(context.Background(), EnrollRequest{EnrollmentKey: "wrong"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestPollNoContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/edge/poll" {
			t.Errorf("path = %s, want /api/v1/edge/poll", r.URL.Path)
		}
		var got PollRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode poll body: %v", err)
		}
		if got.EdgeVersion != "v9.9.9" || got.Hostname != "edge-host" {
			t.Errorf("poll body = %+v, want EdgeVersion=v9.9.9 Hostname=edge-host", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	item, err := c.Poll(context.Background(), time.Millisecond, PollRequest{
		EdgeVersion: "v9.9.9",
		Hostname:    "edge-host",
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if item != nil {
		t.Fatalf("item = %+v, want nil", item)
	}
}

func TestPollWithWork(t *testing.T) {
	workID := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item := WorkItem{WorkId: workID, Op: "backup", TaskConfig: map[string]string{"k": "v"}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(item)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	item, err := c.Poll(context.Background(), time.Millisecond, PollRequest{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if item == nil {
		t.Fatal("item = nil, want work item")
	}
	if item.WorkId != workID || item.Op != "backup" {
		t.Errorf("item = %+v, want WorkId=%s Op=backup", item, workID)
	}
}

func TestPollSendsBearerToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok123" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer tok123")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	c.SetToken("tok123")
	if _, err := c.Poll(context.Background(), time.Millisecond, PollRequest{}); err != nil {
		t.Fatalf("Poll: %v", err)
	}
}

func TestReplySuccess(t *testing.T) {
	workID := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v1/edge/" + workID.String() + "/reply"
		if r.URL.Path != wantPath {
			t.Errorf("path = %s, want %s", r.URL.Path, wantPath)
		}
		var reply Reply
		if err := json.NewDecoder(r.Body).Decode(&reply); err != nil {
			t.Fatalf("decode reply: %v", err)
		}
		if reply.Type != ReplySuccess || reply.Message != "done" {
			t.Errorf("reply = %+v", reply)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	err := c.Reply(context.Background(), workID, Reply{Type: ReplySuccess, Message: "done"})
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
}

func TestReplyErrorIncludesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	err := c.Reply(context.Background(), uuid.New(), Reply{Type: ReplyFailure})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := err.Error(); !contains(got, "boom") {
		t.Errorf("error = %q, want it to contain %q", got, "boom")
	}
}

func TestDoStatusNoRequestBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "" {
			t.Errorf("Content-Type = %q, want empty for nil body", ct)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	status, err := c.doStatus(context.Background(), http.MethodPost, "/x", nil, nil)
	if err != nil {
		t.Fatalf("doStatus: %v", err)
	}
	if status != http.StatusNoContent {
		t.Errorf("status = %d, want %d", status, http.StatusNoContent)
	}
}

func TestDoStatusDecodesEmptyBodyWithoutError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	var out WorkItem
	_, err := c.doStatus(context.Background(), http.MethodGet, "/x", nil, &out)
	if err != nil {
		t.Fatalf("doStatus with empty 200 body should not error, got: %v", err)
	}
}

func TestDoStatusContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.doStatus(ctx, http.MethodPost, "/x", nil, nil)
	if err == nil {
		t.Fatal("expected error for canceled context, got nil")
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
