package main

import (
	"github.com/PlakarKorp/plaklet"
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
const EdgeProtocolVersion = 2

type EnrollRequest struct {
	// OrganizationID is the organization this edge joins. Named rather than
	// derived from the key: an enrollment key belongs to one organization, so
	// the control plane could look it up by key -- but that would put the
	// secret in a SQL predicate and lose the constant-time comparison, leaking
	// key length and prefix through timing. An id is not a secret.
	OrganizationID  uuid.UUID  `json:"organization_id"`
	EnrollmentKey   string     `json:"enrollment_key"`
	Name            string     `json:"name"`
	Hostname        string     `json:"hostname"`
	ProtocolVersion int        `json:"protocol_version"`
	EdgeVersion     string     `json:"edge_version"`
	SystemInfo      SystemInfo `json:"system_info"`
}

// PoolEnrollRequest enrolls into an edge pool. It names no organization: pool
// keys are stored hashed, so the control plane looks the pool up by the key's
// hash and the edge joins that pool's organization.
type PoolEnrollRequest struct {
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
// info, tags) so the control plane's view of this edge self-heals after an
// upgrade, a host change, or a retag, without a re-enrollment. Mirrors
// plakman's contract.EdgePollRequest. Tags is additive to the wire protocol:
// an older plakman that doesn't know about it simply ignores the field.
type PollRequest struct {
	ProtocolVersion int        `json:"protocol_version"`
	EdgeVersion     string     `json:"edge_version"`
	Hostname        string     `json:"hostname"`
	SystemInfo      SystemInfo `json:"system_info"`
	Tags            []string   `json:"tags,omitempty"`
	Slots           int        `json:"slots,omitempty"`
}

type WorkItem struct {
	WorkId     uuid.UUID              `json:"work_id"`
	Op         string                 `json:"op"`
	TaskConfig map[string]string      `json:"task_config"`
	Source     *plaklet.Configuration `json:"source"`
	Target     *plaklet.Configuration `json:"target"`
}
