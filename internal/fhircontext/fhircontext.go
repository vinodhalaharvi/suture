// Package fhircontext implements Prompt Opinion's FHIR context
// propagation contract for MCP servers.
//
// Prompt Opinion forwards FHIR context to MCP tools via HTTP headers
// on every tools/call request. See:
//
//	https://docs.promptopinion.ai/fhir-context/mcp-fhir-context
//
// Headers sent:
//
//	X-FHIR-Server-URL    — the FHIR server endpoint (always present)
//	X-FHIR-Access-Token  — SMART-on-FHIR bearer token (optional)
//	X-Patient-ID         — current patient (absent => system-level call)
//	X-FHIR-Refresh-Token — only when offline_access is granted
//	X-FHIR-Refresh-Url   — only when offline_access is granted
//
// Tool handlers read these via fhircontext.From(ctx) after the HTTP
// transport has stashed them in context.Context. The same code path
// works whether the context came from real HTTP headers or from a test
// that injected them directly.
package fhircontext

import (
	"context"
	"fmt"
	"net/http"

	"github.com/vinodhalaharvi/suture/internal/mcp"
)

// Context holds the FHIR context fields propagated from the platform.
type Context struct {
	// FHIRBase is the URL of the FHIR server. Always present.
	FHIRBase string

	// Token is the SMART-on-FHIR access token. May be empty if the
	// configured FHIR server doesn't require authorization.
	Token string

	// PatientID is the current patient in context. Empty means the
	// server is invoked at a system level (no specific patient).
	PatientID string

	// RefreshToken / RefreshURL are populated only when the server
	// requested and was granted offline_access. Tool code shouldn't
	// touch these directly; a future Refresh helper will use them.
	RefreshToken string
	RefreshURL   string
}

// IsZero reports whether c carries no useful context. A server-level
// MCP call where the FHIR server requires no token would have only
// FHIRBase set — that's still non-zero context.
func (c Context) IsZero() bool {
	return c.FHIRBase == "" && c.Token == "" && c.PatientID == ""
}

// HasPatient reports whether a patient ID is in context. If a tool
// requires a patient, it should check this and return a clean error
// when false instead of making a system-level FHIR query that returns
// the wrong data.
func (c Context) HasPatient() bool {
	return c.PatientID != ""
}

// RequirePatient returns an error if no patient is in context.
// Convenience wrapper around HasPatient for tool handlers.
func (c Context) RequirePatient() error {
	if c.PatientID == "" {
		return fmt.Errorf("fhircontext: no patient in context (this tool requires a patient)")
	}
	if c.FHIRBase == "" {
		return fmt.Errorf("fhircontext: missing FHIR server URL")
	}
	return nil
}

type ctxKey struct{}

// Inject puts a Context into the Go context.Context for downstream
// Arrow consumers.
func Inject(ctx context.Context, c Context) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// From extracts a Context. The bool is false if no FHIR context has
// been injected.
func From(ctx context.Context) (Context, bool) {
	c, ok := ctx.Value(ctxKey{}).(Context)
	return c, ok
}

// MustFrom panics if no FHIR context is present. Use in test code or
// in arrows where missing context is a programming error.
func MustFrom(ctx context.Context) Context {
	c, ok := From(ctx)
	if !ok {
		panic("fhircontext: no context in request")
	}
	return c
}

// FromHTTP extracts a Context from HTTP headers that the MCP HTTP
// transport stashed in context.Context. This is what tool middleware
// calls at the boundary between transport and tool code.
//
// Unknown or missing headers are tolerated — the caller is expected to
// validate (with RequirePatient or similar) before using the context.
func FromHTTP(ctx context.Context) Context {
	h := mcp.HTTPHeadersFrom(ctx)
	return Context{
		FHIRBase:     h[http.CanonicalHeaderKey("X-FHIR-Server-URL")],
		Token:        h[http.CanonicalHeaderKey("X-FHIR-Access-Token")],
		PatientID:    h[http.CanonicalHeaderKey("X-Patient-ID")],
		RefreshToken: h[http.CanonicalHeaderKey("X-FHIR-Refresh-Token")],
		RefreshURL:   h[http.CanonicalHeaderKey("X-FHIR-Refresh-Url")],
	}
}
