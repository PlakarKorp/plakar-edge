package main

import (
	"reflect"
	"testing"
)

func TestParseTags(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty string", "", nil},
		{"only whitespace", "   ", nil},
		{"only commas", ",,,", nil},
		{"single tag", "role=ingest", []string{"role=ingest"}},
		{"multiple tags", "role=ingest,env=prod", []string{"role=ingest", "env=prod"}},
		{"surrounding whitespace", " role=ingest , env=prod ", []string{"role=ingest", "env=prod"}},
		{"trailing comma", "role=ingest,", []string{"role=ingest"}},
		{"doubled comma", "role=ingest,,env=prod", []string{"role=ingest", "env=prod"}},
		{"leading comma", ",role=ingest", []string{"role=ingest"}},
		{"freeform value, no equals sign required", "solo-tag", []string{"solo-tag"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTags(tc.raw)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseTags(%q) = %#v, want %#v", tc.raw, got, tc.want)
			}
		})
	}
}
