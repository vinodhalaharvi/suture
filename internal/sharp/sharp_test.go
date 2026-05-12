package sharp

import (
	"context"
	"testing"
)

func TestInjectFrom(t *testing.T) {
	in := Context{
		PatientID:    "p1",
		FHIRBase:     "https://fhir.example/r4",
		Token:        "tok",
		Practitioner: "dr1",
	}
	ctx := Inject(context.Background(), in)

	out, ok := From(ctx)
	if !ok {
		t.Fatal("From returned !ok")
	}
	if out != in {
		t.Errorf("roundtrip mismatch: got %+v, want %+v", out, in)
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

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		c       Context
		wantErr bool
	}{
		{"complete", Context{PatientID: "p", FHIRBase: "u", Token: "t"}, false},
		{"missing patient", Context{FHIRBase: "u", Token: "t"}, true},
		{"missing base", Context{PatientID: "p", Token: "t"}, true},
		{"missing token", Context{PatientID: "p", FHIRBase: "u"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.c.Validate()
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestFromMeta(t *testing.T) {
	m := map[string]any{
		"sharp.patient_id":   "p42",
		"sharp.fhir_base":    "https://x/r4",
		"sharp.token":        "abc",
		"sharp.practitioner": "dr",
		"unrelated":          "ignore",
	}
	got := FromMeta(m)
	want := Context{PatientID: "p42", FHIRBase: "https://x/r4", Token: "abc", Practitioner: "dr"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestFromMetaEmpty(t *testing.T) {
	got := FromMeta(nil)
	if !got.IsZero() {
		t.Errorf("expected zero context, got %+v", got)
	}
}

func TestIsZero(t *testing.T) {
	if !(Context{}).IsZero() {
		t.Error("empty should be zero")
	}
	if (Context{PatientID: "p"}).IsZero() {
		t.Error("with patient should not be zero")
	}
}
