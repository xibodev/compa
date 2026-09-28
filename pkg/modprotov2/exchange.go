package modprotov2

import "encoding/json"

// Request is one v2 invocation.
//
// NO NEW AUTHORITY MECHANISM. Roots, grants and binaries are unchanged from v1
// in mechanism: they are enforced, tested, and §8 states them normatively.
// Changing a working authority model while changing everything else would be
// the wrong risk, so v2 adds exactly two things -- the contract pin and the
// approval record.
type Request struct {
	Protocol        string `json:"protocol"`
	ContractVersion string `json:"contract_version"`

	Capability string          `json:"capability"`
	RequestID  string          `json:"request_id"`
	Input      json.RawMessage `json:"input"`

	Roots    map[string]Root   `json:"roots"`
	Grants   Grants            `json:"grants"`
	Binaries map[string]string `json:"binaries"`

	DeadlineMS     int `json:"deadline_ms"`
	MaxOutputBytes int `json:"max_output_bytes"`

	// Approval states WHETHER approval was obtained and FOR WHAT (§6).
	//
	// ABSENT MEANS NONE WAS OBTAINED, and a module MUST NOT infer approval
	// from the field being missing. A pointer rather than a value type so that
	// absence is representable at all: a zero-valued struct with Granted false
	// says "asked and refused", which is a different fact from "never asked".
	Approval *ApprovalRecord `json:"approval,omitempty"`
}

// ApprovalRecord is the host's statement about an approval it obtained.
type ApprovalRecord struct {
	Granted bool   `json:"granted"`
	Reason  string `json:"reason"`

	// GrantedBy is never "model". An approval minted by a model is stripped by
	// the host before this point: the agent cannot approve spending on the
	// operator's behalf, and a field that could carry such a claim would make
	// the gate bypassable by the thing it gates.
	GrantedBy string `json:"granted_by"`
}

type Root struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
}

type Grants struct {
	Network       []string `json:"network"`
	Credentials   []string `json:"credentials"`
	PaidProviders []string `json:"paid_providers"`
	Publish       bool     `json:"publish"`
	Subprocess    []string `json:"subprocess"`
}

// Envelope is one completed v2 invocation.
type Envelope struct {
	Protocol        string `json:"protocol"`
	ContractVersion string `json:"contract_version"`

	Module    string `json:"module"`
	Operation string `json:"operation"`
	RequestID string `json:"request_id"`

	OK       bool            `json:"ok"`
	Result   json.RawMessage `json:"result,omitempty"`
	Warnings []string        `json:"warnings"`

	Execution *Execution `json:"execution,omitempty"`
	Error     *Error     `json:"error,omitempty"`
}

// Execution is what ACTUALLY happened, reported separately from what was
// declared -- which is the point of reporting it, since the host compares.
type Execution struct {
	Network        bool   `json:"network"`
	ExternalWrites bool   `json:"external_writes"`
	Provider       string `json:"provider"`

	// Charged is what happened, distinct from may_charge which is what was
	// declared possible.
	Charged bool `json:"charged"`

	// EstimatedCost and ActualCost are POINTERS on purpose. null means
	// genuinely UNKNOWN; 0 means genuinely free. Collapsing them would let an
	// unpriced provider call render as free and slip past a cost gate.
	EstimatedCost *float64 `json:"estimated_cost"`
	ActualCost    *float64 `json:"actual_cost"`

	// Resolution is requirements as evaluated at runtime, three-valued (§5).
	Resolution []Resolution `json:"resolution"`

	Artifacts []Artifact `json:"artifacts"`
}

// Error carries a REASON and a REMEDY as separate fields.
//
// THE ONE v1 SHAPE THAT CHANGES. v1 has Message only. §8 requires both, and
// the justification is concrete rather than stylistic: a refusal that says only
// what is wrong sends the reader to source, and that is precisely how two
// sibling lanes ended up inferring guarantees nobody had written. The remedy is
// where a reader forms an expectation, so it has to close the wrong ones
// explicitly.
type Error struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
	Remedy string `json:"remedy"`

	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}
