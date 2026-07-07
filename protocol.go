package main

import (
	"encoding/json"

	"github.com/google/uuid"
)

// This file duplicates the wire contracts plakar-edge speaks, so the daemon has
// no dependency on plakman-internal packages.
//
// SOURCE OF TRUTH — keep the JSON shapes in sync with the plakman control plane:
//   - edge API:  github.com/PlakarKorp/plakman api/v1/contract/edge.go
//   - the plaklet stdin/stdout protocol: github.com/PlakarKorp/plakman executor
//     (ExecPayload, ExecReply, executor/contract.Configuration)

// ---- Edge <-> control plane API ----

// EdgeProtocolVersion is the wire protocol version this edge speaks. It must
// match plakman's contract.EdgeProtocolVersion for the control plane to dispatch
// work; it is bumped only when the WorkItem/Reply/Configuration or plaklet
// ExecPayload/ExecReply shapes change. Keep in sync with plakman.
const EdgeProtocolVersion = 1

type EnrollRequest struct {
	EnrollmentKey   string     `json:"enrollment_key"`
	Name            string     `json:"name"`
	Hostname        string     `json:"hostname"`
	ProtocolVersion int        `json:"protocol_version"`
	EdgeVersion     string     `json:"edge_version"`
	SystemInfo      SystemInfo `json:"system_info"`
}

type EnrollResponse struct {
	EdgeId uuid.UUID `json:"edge_id"`
	Token  string    `json:"token"`
	// ProtocolVersion is the control plane's protocol; Supported is whether it
	// can dispatch to this edge. When false, the edge enrolls but gets no work
	// until upgraded.
	ProtocolVersion int  `json:"protocol_version"`
	Supported       bool `json:"supported"`
}

// PollRequest is sent on every poll. Besides asking for work it re-reports the
// facts that can change between boots (build version, protocol, hostname, system
// info) so the control plane's view of this edge self-heals after an upgrade,
// without a re-enrollment. Mirrors plakman's contract.EdgePollRequest.
type PollRequest struct {
	ProtocolVersion int        `json:"protocol_version"`
	EdgeVersion     string     `json:"edge_version"`
	Hostname        string     `json:"hostname"`
	SystemInfo      SystemInfo `json:"system_info"`
}

type WorkItem struct {
	WorkId     uuid.UUID         `json:"work_id"`
	Op         string            `json:"op"`
	TaskConfig map[string]string `json:"task_config"`
	Source     *Configuration    `json:"source"`
	Target     *Configuration    `json:"target"`
}

type ReplyType string

const (
	ReplyInfo    ReplyType = "info"
	ReplyWarning ReplyType = "warning"
	ReplyError   ReplyType = "error"
	ReplyReport  ReplyType = "report"
	ReplyState   ReplyType = "state"
	ReplyFailure ReplyType = "failure"
	ReplySuccess ReplyType = "success"
)

type Reply struct {
	Type    ReplyType       `json:"type"`
	Message string          `json:"message,omitempty"`
	Report  json.RawMessage `json:"report,omitempty"`
	State   json.RawMessage `json:"state,omitempty"`
}

// ---- Connector configuration (shared shape between the API and plaklet) ----

type Integration struct {
	Id      uuid.UUID `json:"id"`
	Name    string    `json:"name"`
	Version string    `json:"version"`
}

// ConfigurationField matches executor/contract.ConfigurationField. Provider is
// always nil in the edge PoC (secrets are resolved centrally), but the field is
// kept so the JSON matches what plaklet expects to decode.
type ConfigurationField struct {
	Key      string         `json:"key"`
	Provider *Configuration `json:"provider"`
	Val      string         `json:"val"`
}

type Configuration struct {
	Id          uuid.UUID            `json:"id"`
	Revision    int                  `json:"revision"`
	Type        string               `json:"type"`
	Integration Integration          `json:"integration"`
	Name        string               `json:"name"`
	Fields      []ConfigurationField `json:"fields"`
	Environment string               `json:"environment,omitempty"`
	DataClasses []string             `json:"data_classes,omitempty"`
}

// ---- plaklet stdin/stdout protocol ----

// ExecPayload matches executor.ExecPayload: what plaklet reads from stdin.
type ExecPayload struct {
	Op         string            `json:"op"`
	TaskConfig map[string]string `json:"task_config"`
	Source     *Configuration    `json:"source"`
	Target     *Configuration    `json:"target"`
}

// ExecReply matches executor.ExecReply: what plaklet writes to stdout. Report
// and State are carried as raw JSON so we can forward them verbatim to the
// control plane without linking plakman's reporting package.
//
// The control plane sends connector fields as {key,val}; plaklet expects
// {key,provider,val}. The two are decode-compatible into the single
// Configuration/ConfigurationField shape above: the missing provider decodes to
// nil, and re-encoding for plaklet emits the literal val it needs. So a WorkItem
// Source/Target can be fed straight to plaklet with no remapping.
type ExecReply struct {
	Type    ReplyType       `json:"type"`
	Message string          `json:"message"`
	Report  json.RawMessage `json:"report"`
	State   json.RawMessage `json:"state"`
}
