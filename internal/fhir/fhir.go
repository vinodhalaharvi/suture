// Package fhir provides minimal FHIR R4 client arrows. Each public
// arrow reads SHARP context from ctx, so it can be composed with any
// other weft.Arrow without parameter threading.
//
// We deliberately model resources as raw map[string]any rather than
// typed FHIR structs. Hand-writing types for every FHIR resource is
// not the value the project is demonstrating; the arrow algebra is.
// A real production version would use a generated client.
package fhir

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/vinodhalaharvi/suture/internal/sharp"
	"github.com/vinodhalaharvi/weft/weft"
)

// Client wraps an http.Client with FHIR-aware request shaping.
type Client struct {
	HTTP *http.Client
}

// NewClient returns a Client with a sensible default timeout.
func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// Resource is a loosely-typed FHIR resource.
type Resource map[string]any

// Bundle is a FHIR search result.
type Bundle struct {
	ResourceType string   `json:"resourceType"`
	Type         string   `json:"type"`
	Total        int      `json:"total"`
	Entry        []Entry  `json:"entry"`
	Raw          Resource `json:"-"`
}

type Entry struct {
	Resource Resource `json:"resource"`
}

// Resources returns the resource maps for each entry.
func (b Bundle) Resources() []Resource {
	out := make([]Resource, 0, len(b.Entry))
	for _, e := range b.Entry {
		out = append(out, e.Resource)
	}
	return out
}

// get issues an authenticated GET against the SHARP-configured FHIR base.
func (c *Client) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	return c.GetRaw(ctx, path, query)
}

// GetRaw is the public form of get — used by ad-hoc tool arrows that
// need to hit endpoints we haven't typed yet (e.g. /Encounter).
func (c *Client) GetRaw(ctx context.Context, path string, query url.Values) ([]byte, error) {
	s, ok := sharp.From(ctx)
	if !ok {
		return nil, fmt.Errorf("fhir: no SHARP context")
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}

	u := s.FHIRBase + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/fhir+json")
	req.Header.Set("Authorization", "Bearer "+s.Token)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fhir: %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("fhir: %s: status %d: %s", path, resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// === Arrows: typed building blocks ===

// ReadPatient is an Arrow[struct{}, Resource] that fetches the patient
// identified by SHARP context.
func (c *Client) ReadPatient() weft.Arrow[struct{}, Resource] {
	return func(ctx context.Context, _ struct{}) (Resource, error) {
		s, _ := sharp.From(ctx)
		body, err := c.get(ctx, "/Patient/"+s.PatientID, nil)
		if err != nil {
			return nil, err
		}
		var r Resource
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		return r, nil
	}
}

// SearchConditions is an Arrow[ConditionFilter, Bundle] that searches
// active conditions for the SHARP patient.
type ConditionFilter struct {
	ClinicalStatus string // "active", "resolved", ""
}

func (c *Client) SearchConditions() weft.Arrow[ConditionFilter, Bundle] {
	return func(ctx context.Context, f ConditionFilter) (Bundle, error) {
		s, _ := sharp.From(ctx)
		q := url.Values{}
		q.Set("patient", s.PatientID)
		if f.ClinicalStatus != "" {
			q.Set("clinical-status", f.ClinicalStatus)
		}
		body, err := c.get(ctx, "/Condition", q)
		if err != nil {
			return Bundle{}, err
		}
		var b Bundle
		if err := json.Unmarshal(body, &b); err != nil {
			return Bundle{}, err
		}
		return b, nil
	}
}

// SearchObservations is an Arrow[ObservationFilter, Bundle].
type ObservationFilter struct {
	Code  string // LOINC, e.g. "718-7" for hemoglobin
	Limit int
}

func (c *Client) SearchObservations() weft.Arrow[ObservationFilter, Bundle] {
	return func(ctx context.Context, f ObservationFilter) (Bundle, error) {
		s, _ := sharp.From(ctx)
		q := url.Values{}
		q.Set("patient", s.PatientID)
		if f.Code != "" {
			q.Set("code", f.Code)
		}
		if f.Limit > 0 {
			q.Set("_count", fmt.Sprintf("%d", f.Limit))
		}
		q.Set("_sort", "-date")
		body, err := c.get(ctx, "/Observation", q)
		if err != nil {
			return Bundle{}, err
		}
		var b Bundle
		if err := json.Unmarshal(body, &b); err != nil {
			return Bundle{}, err
		}
		return b, nil
	}
}

// === Pure helpers on Resource ===

// PatientName returns the first available human-readable name.
func PatientName(p Resource) string {
	names, ok := p["name"].([]any)
	if !ok || len(names) == 0 {
		return ""
	}
	n, ok := names[0].(map[string]any)
	if !ok {
		return ""
	}
	if text, ok := n["text"].(string); ok && text != "" {
		return text
	}
	// fall back to family + given
	family, _ := n["family"].(string)
	givenArr, _ := n["given"].([]any)
	given := ""
	if len(givenArr) > 0 {
		given, _ = givenArr[0].(string)
	}
	if given != "" || family != "" {
		return given + " " + family
	}
	return ""
}

// PatientBirthDate returns ISO-8601 birthDate or "".
func PatientBirthDate(p Resource) string {
	v, _ := p["birthDate"].(string)
	return v
}

// PatientGender returns "male"/"female"/"other"/"unknown" or "".
func PatientGender(p Resource) string {
	v, _ := p["gender"].(string)
	return v
}

// PatientAge returns the age in years computed from birthDate, or 0.
func PatientAge(p Resource) int {
	bd := PatientBirthDate(p)
	if bd == "" {
		return 0
	}
	t, err := time.Parse("2006-01-02", bd)
	if err != nil {
		return 0
	}
	now := time.Now()
	years := now.Year() - t.Year()
	if now.YearDay() < t.YearDay() {
		years--
	}
	return years
}

// ConditionDisplay returns the human-readable description of a Condition.
func ConditionDisplay(c Resource) string {
	code, _ := c["code"].(map[string]any)
	if code == nil {
		return ""
	}
	if text, ok := code["text"].(string); ok && text != "" {
		return text
	}
	codings, _ := code["coding"].([]any)
	if len(codings) > 0 {
		if first, ok := codings[0].(map[string]any); ok {
			if d, ok := first["display"].(string); ok {
				return d
			}
		}
	}
	return ""
}

// ConditionHasICD10Prefix reports whether c is coded with any ICD-10
// code starting with the given prefix (e.g. "I50" for heart failure).
func ConditionHasICD10Prefix(c Resource, prefix string) bool {
	code, _ := c["code"].(map[string]any)
	if code == nil {
		return false
	}
	codings, _ := code["coding"].([]any)
	for _, coding := range codings {
		m, ok := coding.(map[string]any)
		if !ok {
			continue
		}
		system, _ := m["system"].(string)
		// Recognize both common URL forms for ICD-10.
		if system != "http://hl7.org/fhir/sid/icd-10-cm" && system != "http://hl7.org/fhir/sid/icd-10" {
			continue
		}
		code, _ := m["code"].(string)
		if len(code) >= len(prefix) && code[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
