package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"os"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	// logChunkMax bounds the size of one log reply.
	logChunkMax = 64 << 10
	// logBufferMax bounds what is held while the control plane is unreachable.
	// Past it, output is dropped: the job must not stall or eat the host's
	// memory over its log.
	logBufferMax = 4 << 20
)

// logStreamer is plaklet's stderr. It copies the output to the edge's own
// stderr and ships it to the control plane as ReplyLog replies, which become
// the job's output log.
type logStreamer struct {
	ctx    context.Context
	clt    *Client
	workID uuid.UUID
	local  io.Writer

	mu        sync.Mutex
	buf       bytes.Buffer
	truncated bool

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

func newLogStreamer(ctx context.Context, clt *Client, workID uuid.UUID, interval time.Duration) *logStreamer {
	s := &logStreamer{
		ctx:    ctx,
		clt:    clt,
		workID: workID,
		local:  os.Stderr,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go s.run(interval)
	return s
}

func (s *logStreamer) Write(p []byte) (int, error) {
	_, _ = s.local.Write(p)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buf.Len()+len(p) > logBufferMax {
		s.truncated = true
		return len(p), nil
	}
	s.buf.Write(p)
	return len(p), nil
}

func (s *logStreamer) run(interval time.Duration) {
	defer close(s.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.flush(false)
		}
	}
}

// Close stops the periodic flush and sends what is left. Call it once plaklet
// has exited, so that no output is written after it, and before the terminal
// reply: the control plane drops replies for a finished work item.
func (s *logStreamer) Close() {
	s.once.Do(func() {
		close(s.stop)
		<-s.done
		s.flush(true)
	})
}

// flush sends the buffered output. A periodic flush only sends complete lines,
// so a chunk never ends inside a line or a UTF-8 sequence unless a single line
// outgrows logChunkMax.
func (s *logStreamer) flush(final bool) {
	for {
		chunk, ok := s.next(final)
		if !ok {
			return
		}
		if err := s.clt.Reply(s.ctx, s.workID, Reply{Type: ReplyLog, Message: chunk}); err != nil {
			// The chunk is gone: mark the log so the gap is not silent.
			s.mu.Lock()
			s.truncated = true
			s.mu.Unlock()
			log.Printf("work %s: failed to forward log: %v", s.workID, err)
			return
		}
	}
}

func (s *logStreamer) next(final bool) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.buf.Len() == 0 {
		if final && s.truncated {
			s.truncated = false
			return "[log truncated: the control plane was unreachable]\n", true
		}
		return "", false
	}

	b := s.buf.Bytes()
	n := min(len(b), logChunkMax)
	if !final || n < len(b) {
		if i := bytes.LastIndexByte(b[:n], '\n'); i >= 0 {
			n = i + 1
		} else if n < logChunkMax {
			return "", false
		} else {
			n = runeBoundary(b, n)
		}
	}

	chunk := string(b[:n])
	s.buf.Next(n)
	return chunk, true
}

// runeBoundary backs n off so that b[:n] does not end inside a UTF-8 sequence.
func runeBoundary(b []byte, n int) int {
	for i := n - 1; i >= 0 && i >= n-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:n]) {
				return i
			}
			return n
		}
	}
	return n
}
