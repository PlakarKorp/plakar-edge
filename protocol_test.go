package main

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

// TestConfigurationFieldDecodeCompatible verifies the claim documented in
// protocol.go: the control plane sends {key,val} pairs (no provider), and
// that decodes cleanly into ConfigurationField, which plaklet expects to
// receive back as {key,provider,val}.
func TestConfigurationFieldDecodeCompatible(t *testing.T) {
	controlPlaneJSON := `{"key":"bucket","val":"my-bucket"}`

	var f ConfigurationField
	if err := json.Unmarshal([]byte(controlPlaneJSON), &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if f.Key != "bucket" || f.Val != "my-bucket" || f.Provider != nil {
		t.Fatalf("got %+v", f)
	}

	reencoded, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundtrip map[string]any
	if err := json.Unmarshal(reencoded, &roundtrip); err != nil {
		t.Fatalf("unmarshal roundtrip: %v", err)
	}
	if roundtrip["key"] != "bucket" || roundtrip["val"] != "my-bucket" {
		t.Errorf("roundtrip = %+v", roundtrip)
	}
	if _, ok := roundtrip["provider"]; !ok {
		t.Errorf("re-encoded field should still carry a provider key (nil), got %+v", roundtrip)
	}
}

func TestWorkItemToExecPayload(t *testing.T) {
	cfgID := uuid.New()
	item := WorkItem{
		WorkId:     uuid.New(),
		Op:         "backup",
		TaskConfig: map[string]string{"path": "/etc"},
		Source: &Configuration{
			Id:   cfgID,
			Type: "fs",
			Fields: []ConfigurationField{
				{Key: "root", Val: "/data"},
			},
		},
	}

	payload := ExecPayload{
		Op:         item.Op,
		TaskConfig: item.TaskConfig,
		Source:     item.Source,
		Target:     item.Target,
	}

	buf, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ExecPayload
	if err := json.Unmarshal(buf, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Op != "backup" || decoded.TaskConfig["path"] != "/etc" {
		t.Errorf("decoded = %+v", decoded)
	}
	if decoded.Source == nil || decoded.Source.Id != cfgID || decoded.Source.Fields[0].Val != "/data" {
		t.Errorf("decoded.Source = %+v", decoded.Source)
	}
	if decoded.Target != nil {
		t.Errorf("decoded.Target = %+v, want nil", decoded.Target)
	}
}

func TestExecReplyPassesReportAndStateVerbatim(t *testing.T) {
	raw := `{"type":"report","message":"","report":{"bytes":123,"files":4},"state":{"cursor":"abc"}}`

	var r ExecReply
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.Type != ReplyReport {
		t.Errorf("Type = %q, want %q", r.Type, ReplyReport)
	}

	// Report/State must survive as raw JSON, unparsed, so they can be
	// forwarded to the control plane without linking plakman's report types.
	var report map[string]any
	if err := json.Unmarshal(r.Report, &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if report["bytes"] != float64(123) || report["files"] != float64(4) {
		t.Errorf("report = %+v", report)
	}

	var state map[string]any
	if err := json.Unmarshal(r.State, &state); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	if state["cursor"] != "abc" {
		t.Errorf("state = %+v", state)
	}
}

func TestReplyOmitsEmptyOptionalFields(t *testing.T) {
	buf, err := json.Marshal(Reply{Type: ReplyInfo, Message: "hello"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(buf, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw["report"]; ok {
		t.Errorf("report should be omitted when empty, got %+v", raw)
	}
	if _, ok := raw["state"]; ok {
		t.Errorf("state should be omitted when empty, got %+v", raw)
	}
}

func TestPollRequestOmitsEmptyTags(t *testing.T) {
	buf, err := json.Marshal(PollRequest{EdgeVersion: "v1.0.0"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(buf, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw["tags"]; ok {
		t.Errorf("tags should be omitted when nil, got %+v", raw)
	}
}

func TestPollRequestIncludesTagsWhenSet(t *testing.T) {
	buf, err := json.Marshal(PollRequest{Tags: []string{"role=ingest", "env=prod"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got PollRequest
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := []string{"role=ingest", "env=prod"}
	if len(got.Tags) != len(want) || got.Tags[0] != want[0] || got.Tags[1] != want[1] {
		t.Errorf("Tags round-trip = %+v, want %+v", got.Tags, want)
	}
}

func TestReplyTypeConstants(t *testing.T) {
	types := map[ReplyType]string{
		ReplyInfo:    "info",
		ReplyWarning: "warning",
		ReplyError:   "error",
		ReplyReport:  "report",
		ReplyState:   "state",
		ReplyFailure: "failure",
		ReplySuccess: "success",
	}
	for typ, want := range types {
		if string(typ) != want {
			t.Errorf("%v != %q", typ, want)
		}
	}
}
