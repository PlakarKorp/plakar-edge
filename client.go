package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Client talks to the plakman edge API. It is deliberately tiny and stdlib-only.
type Client struct {
	baseURL string
	token   string // per-edge bearer token; empty until enrolled
	http    *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{},
	}
}

func (c *Client) SetToken(token string) { c.token = token }

// Enroll trades the enrollment key for a per-edge identity + token.
func (c *Client) Enroll(ctx context.Context, req EnrollRequest) (*EnrollResponse, error) {
	var resp EnrollResponse
	if _, err := c.doStatus(ctx, http.MethodPost, "/api/v1/edge/enroll", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Poll long-polls for the next work item. Returns (nil, nil) on 204 (no work).
// The control plane blocks the request server-side; we set a client timeout a
// little longer than the expected server hold.
func (c *Client) Poll(ctx context.Context, hold time.Duration) (*WorkItem, error) {
	ctx, cancel := context.WithTimeout(ctx, hold+30*time.Second)
	defer cancel()

	var item WorkItem
	status, err := c.doStatus(ctx, http.MethodPost, "/api/v1/edge/poll", nil, &item)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	return &item, nil
}

// Reply posts one reply for a work item back to the control plane.
func (c *Client) Reply(ctx context.Context, workID uuid.UUID, reply Reply) error {
	path := fmt.Sprintf("/api/v1/edge/%s/reply", workID)
	_, err := c.doStatus(ctx, http.MethodPost, path, reply, nil)
	return err
}

func (c *Client) doStatus(ctx context.Context, method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}

	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
			return resp.StatusCode, fmt.Errorf("decode %s response: %w", path, err)
		}
	}
	return resp.StatusCode, nil
}
