package fhircontext

import (
	"context"
	"net/http"
	"testing"

	"github.com/vinodhalaharvi/suture/internal/mcp"
)

func TestInjectFrom(t *testing.T) {
	in := Context{
		FHIRBase:  "https://x/r4",
		Token:     "t",
		PatientID: "p1",
	}
	ctx := Inject(context.Background(), in)
	out, ok := From(ctx)
	if !ok {
		t.Fatal("From returned !ok")
	}
	if out != in {
		t.Errorf("roundtrip mismatch: %+v vs %+v", out, in)
	}
}

func TestFromEmpty(t *testing.T) {
	if _, ok := From(context.Background()); ok {
		t.Error("expected !ok on empty context")
	}
}

func TestMustFromPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic")
		}
	}()
	MustFrom(context.Background())
}

func TestRequirePatient(t *testing.T) {
	cases := []struct {
		name    string
		c       Context
		wantErr bool
	}{
		{"complete", Context{PatientID: "p", FHIRBase: "u", Token: "t"}, false},
		{"no token (token is optional)", Context{PatientID: "p", FHIRBase: "u"}, false},
		{"missing patient", Context{FHIRBase: "u", Token: "t"}, true},
		{"missing base", Context{PatientID: "p", Token: "t"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.c.RequirePatient()
			if tc.wantErr && err == nil {
				t.Error("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestFromHTTP_AllHeaders(t *testing.T) {
	headers := mcp.HTTPHeaders{
		http.CanonicalHeaderKey("X-FHIR-Server-URL"):    "https://fhir.x",
		http.CanonicalHeaderKey("X-FHIR-Access-Token"):  "tok",
		http.CanonicalHeaderKey("X-Patient-ID"):         "p1",
		http.CanonicalHeaderKey("X-FHIR-Refresh-Token"): "refresh",
		http.CanonicalHeaderKey("X-FHIR-Refresh-Url"):   "https://refresh.x",
	}
	ctx := mcp.WithHTTPHeaders(context.Background(), headers)
	c := FromHTTP(ctx)
	if c.FHIRBase != "https://fhir.x" {
		t.Errorf("FHIRBase: %s", c.FHIRBase)
	}
	if c.Token != "tok" {
		t.Errorf("Token: %s", c.Token)
	}
	if c.PatientID != "p1" {
		t.Errorf("PatientID: %s", c.PatientID)
	}
	if c.RefreshToken != "refresh" {
		t.Errorf("RefreshToken: %s", c.RefreshToken)
	}
	if c.RefreshURL != "https://refresh.x" {
		t.Errorf("RefreshURL: %s", c.RefreshURL)
	}
}

func TestFromHTTP_SystemLevel(t *testing.T) {
	// System-level call: URL only, no patient.
	headers := mcp.HTTPHeaders{
		http.CanonicalHeaderKey("X-FHIR-Server-URL"): "https://fhir.x",
	}
	ctx := mcp.WithHTTPHeaders(context.Background(), headers)
	c := FromHTTP(ctx)
	if c.HasPatient() {
		t.Error("expected no patient")
	}
	if c.FHIRBase != "https://fhir.x" {
		t.Errorf("FHIRBase: %s", c.FHIRBase)
	}
	if err := c.RequirePatient(); err == nil {
		t.Error("RequirePatient should reject system-level context")
	}
}

func TestFromHTTP_NoContext(t *testing.T) {
	// No HTTP headers in ctx at all (e.g. stdio transport).
	c := FromHTTP(context.Background())
	if !c.IsZero() {
		t.Errorf("expected zero context, got %+v", c)
	}
}

func TestHasPatient(t *testing.T) {
	if (Context{}).HasPatient() {
		t.Error("empty should not have patient")
	}
	if !(Context{PatientID: "p"}).HasPatient() {
		t.Error("with patient should have patient")
	}
}

func TestIsZero(t *testing.T) {
	if !(Context{}).IsZero() {
		t.Error("empty should be zero")
	}
	if (Context{FHIRBase: "x"}).IsZero() {
		t.Error("with FHIRBase should not be zero")
	}
}
