package main

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestIsUnauthorizedError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"401 is fatal", &HTTPError{Status: http.StatusUnauthorized}, true},
		{"403 is fatal", &HTTPError{Status: http.StatusForbidden}, true},
		{"wrapped 401 is fatal", fmt.Errorf("poll: %w", &HTTPError{Status: http.StatusUnauthorized}), true},
		{"404 is not (transient/other)", &HTTPError{Status: http.StatusNotFound}, false},
		{"500 is not (transient)", &HTTPError{Status: http.StatusInternalServerError}, false},
		{"plain network error is not", errors.New("connection refused"), false},
		{"nil is not", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUnauthorizedError(tt.err); got != tt.want {
				t.Errorf("isUnauthorizedError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
