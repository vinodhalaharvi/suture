// Command demo runs Suture's tools against a SHARP context supplied on
// the command line. Useful for local development and recording the
// hackathon demo video without setting up the full Prompt Opinion
// platform first.
//
// Example with the public HAPI FHIR sandbox:
//
//	demo \
//	    -tool get_patient_summary \
//	    -fhir https://hapi.fhir.org/baseR4 \
//	    -patient 1234567 \
//	    -token dev
//
// (the public HAPI sandbox doesn't enforce tokens but the field is
// required so we have a uniform interface.)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/vinodhalaharvi/suture/internal/fhir"
	"github.com/vinodhalaharvi/suture/internal/sharp"
	"github.com/vinodhalaharvi/suture/pkg/tools"
)

func main() {
	tool := flag.String("tool", "get_patient_summary", "tool to invoke")
	patient := flag.String("patient", "", "FHIR patient ID")
	fhirBase := flag.String("fhir", "https://hapi.fhir.org/baseR4", "FHIR base URL")
	token := flag.String("token", "dev", "bearer token")
	limit := flag.Int("limit", 5, "limit (for chart review)")
	request := flag.String("request", "", "free-text request (for prior_auth_assistant)")
	flag.Parse()

	if *patient == "" {
		fmt.Fprintln(os.Stderr, "error: -patient is required")
		os.Exit(2)
	}

	ctx := sharp.Inject(context.Background(), sharp.Context{
		PatientID: *patient,
		FHIRBase:  *fhirBase,
		Token:     *token,
	})

	c := fhir.NewClient()
	var (
		out any
		err error
	)
	switch *tool {
	case "get_patient_summary":
		out, err = tools.PatientSummaryArrow(c)(ctx, tools.PatientSummaryIn{})
	case "calculate_cha2ds2_vasc":
		out, err = tools.CalculateScoreArrow(c)(ctx, tools.CHA2DS2VAScIn{})
	case "get_cha2ds2_vasc_components":
		out, err = tools.GetComponentsArrow(c)(ctx, tools.CHA2DS2VAScIn{})
	case "summarize_recent_encounters":
		out, err = tools.ChartReviewArrow(c)(ctx, tools.ChartReviewIn{Limit: *limit})
	case "prior_auth_assistant":
		if *request == "" {
			fmt.Fprintln(os.Stderr, "error: -request is required for prior_auth_assistant")
			os.Exit(2)
		}
		out, err = tools.PriorAuthAgentArrow(c)(ctx, tools.PriorAuthIn{Request: *request})
	default:
		fmt.Fprintf(os.Stderr, "unknown tool: %s\n", *tool)
		fmt.Fprintln(os.Stderr, "available: get_patient_summary, calculate_cha2ds2_vasc, get_cha2ds2_vasc_components, summarize_recent_encounters, prior_auth_assistant")
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "tool failed: %v\n", err)
		os.Exit(1)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
