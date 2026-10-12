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

func TestEdgeTags(t *testing.T) {
	cases := []struct {
		name  string
		flag  string
		extra string
		want  []string
	}{
		{"neither", "", "", nil},
		{"flag only", "role=ingest", "", []string{"role=ingest"}},
		{"extra only", "", "postgresql-18", []string{"postgresql-18"}},
		{"extra adds to flag", "role=ingest,env=prod", "postgresql-18", []string{"role=ingest", "env=prod", "postgresql-18"}},
		{"duplicates dropped", "role=ingest,postgresql-18", "postgresql-18, role=ingest", []string{"role=ingest", "postgresql-18"}},
		{"extra with stray commas", "role=ingest", " ,postgresql-18,", []string{"role=ingest", "postgresql-18"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := edgeTags(tc.flag, tc.extra)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("edgeTags(%q, %q) = %#v, want %#v", tc.flag, tc.extra, got, tc.want)
			}
		})
	}
}
