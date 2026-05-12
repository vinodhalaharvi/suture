// Package sharp implements the SHARP context propagation contract.
//
// SHARP (per the Prompt Opinion platform) is the convention by which an
// EHR-authenticated session's healthcare context — patient ID, FHIR
// base URL, FHIR bearer token, practitioner — is carried through MCP
// tool invocations and downstream A2A calls without each tool inventing
// its own auth handshake.
//
// In this implementation we model SHARP as fields in the MCP request's
// `_meta` object (an MCP-spec extension point well-suited for cross-
// cutting metadata). The keys are namespaced under "sharp.". The exact
// wire shape of SHARP is platform-specific; if Prompt Opinion's spec
// uses different keys or a header-based transport, only this file
// changes — the rest of the project reads through Context() and From().
package sharp

import (
	"context"
	"fmt"
)

// Context holds the propagated healthcare session data.
type Context struct {
	PatientID    string
	FHIRBase     string
	Token        string
	Practitioner string
}

// IsZero reports whether c carries no useful context.
func (c Context) IsZero() bool {
	return c.PatientID == "" && c.FHIRBase == "" && c.Token == ""
}

// Validate checks that the minimum required fields are present.
func (c Context) Validate() error {
	if c.PatientID == "" {
		return fmt.Errorf("sharp: patient_id is required")
	}
	if c.FHIRBase == "" {
		return fmt.Errorf("sharp: fhir_base is required")
	}
	if c.Token == "" {
		return fmt.Errorf("sharp: token is required")
	}
	return nil
}

type ctxKey struct{}

// Inject puts a SHARP Context into the Go context.Context for
// downstream Arrow consumers.
func Inject(ctx context.Context, s Context) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}

// From extracts a SHARP Context. The bool is false if no SHARP context
// has been injected.
func From(ctx context.Context) (Context, bool) {
	s, ok := ctx.Value(ctxKey{}).(Context)
	return s, ok
}

// MustFrom panics if no SHARP context is present. Use only in test code
// or in arrows where missing context is a programming error.
func MustFrom(ctx context.Context) Context {
	s, ok := From(ctx)
	if !ok {
		panic("sharp: no context in request")
	}
	return s
}

// FromMeta extracts a Context from MCP request _meta fields. The
// expected keys are:
//
//	sharp.patient_id     (string)
//	sharp.fhir_base      (string)
//	sharp.token          (string, bearer token without "Bearer " prefix)
//	sharp.practitioner   (string, optional)
//
// Missing optional fields are tolerated. Required fields can be
// validated by calling Context.Validate on the result.
func FromMeta(meta map[string]any) Context {
	s := Context{}
	if v, ok := meta["sharp.patient_id"].(string); ok {
		s.PatientID = v
	}
	if v, ok := meta["sharp.fhir_base"].(string); ok {
		s.FHIRBase = v
	}
	if v, ok := meta["sharp.token"].(string); ok {
		s.Token = v
	}
	if v, ok := meta["sharp.practitioner"].(string); ok {
		s.Practitioner = v
	}
	return s
}
