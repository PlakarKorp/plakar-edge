package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

func newTestStreamer() *logStreamer {
	return &logStreamer{local: io.Discard}
}

func TestLogStreamerHoldsPartialLine(t *testing.T) {
	s := newTestStreamer()
	_, _ = s.Write([]byte("one\ntw"))

	chunk, ok := s.next(false)
	if !ok || chunk != "one\n" {
		t.Fatalf("next = %q, %v, want the complete line", chunk, ok)
	}
	if chunk, ok := s.next(false); ok {
		t.Fatalf("next = %q, want the partial line held", chunk)
	}
	if chunk, ok := s.next(true); !ok || chunk != "tw" {
		t.Fatalf("final next = %q, %v, want the partial line", chunk, ok)
	}
}

func TestLogStreamerSplitsLongLineOnRuneBoundary(t *testing.T) {
	s := newTestStreamer()
	line := strings.Repeat("é", logChunkMax) // 2 bytes each
	_, _ = s.Write([]byte(line))

	var got string
	for {
		chunk, ok := s.next(true)
		if !ok {
			break
		}
		if len(chunk) > logChunkMax || !utf8.ValidString(chunk) {
			t.Fatalf("chunk of %d bytes, valid UTF-8 %v", len(chunk), utf8.ValidString(chunk))
		}
		got += chunk
	}
	if got != line {
		t.Fatal("chunks do not add up to the line")
	}
}

func TestLogStreamerTruncatesPastBufferMax(t *testing.T) {
	s := newTestStreamer()
	_, _ = s.Write([]byte(strings.Repeat("x", logBufferMax-1) + "\n"))
	_, _ = s.Write([]byte("dropped\n"))

	var got string
	for {
		chunk, ok := s.next(true)
		if !ok {
			break
		}
		got += chunk
	}
	if strings.Contains(got, "dropped") || !strings.HasSuffix(got, "[log truncated: the control plane was unreachable]\n") {
		t.Fatalf("log tail = %q", got[max(0, len(got)-80):])
	}
}

func TestLogStreamerMarksLostChunk(t *testing.T) {
	var got []string
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			fail = false
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var rep Reply
		_ = json.NewDecoder(r.Body).Decode(&rep)
		got = append(got, rep.Message)
	}))
	defer srv.Close()

	s := newTestStreamer()
	s.ctx = context.Background()
	s.clt = NewClient(srv.URL)
	s.workID = uuid.New()

	_, _ = s.Write([]byte("lost\n"))
	s.flush(false)
	_, _ = s.Write([]byte("kept\n"))
	s.flush(true)

	want := []string{"kept\n", "[log truncated: the control plane was unreachable]\n"}
	if strings.Join(got, "") != strings.Join(want, "") {
		t.Fatalf("log = %q, want %q", got, want)
	}
}

func TestLogStreamerFlushesPeriodically(t *testing.T) {
	got := make(chan Reply, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rep Reply
		_ = json.NewDecoder(r.Body).Decode(&rep)
		got <- rep
	}))
	defer srv.Close()

	s := newLogStreamer(context.Background(), NewClient(srv.URL), uuid.New(), 10*time.Millisecond)
	defer s.Close()
	s.local = io.Discard
	_, _ = s.Write([]byte("early\n"))

	select {
	case rep := <-got:
		if rep.Type != ReplyLog || rep.Message != "early\n" {
			t.Fatalf("reply = %+v, want the log line", rep)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no log reply before Close")
	}
}
