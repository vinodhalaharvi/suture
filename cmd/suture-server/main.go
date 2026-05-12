// Command suture-server is the MCP server entrypoint.
//
// By default, it listens for HTTP MCP requests on $PORT (or 8080). The
// Prompt Opinion platform calls this endpoint with FHIR context
// attached as HTTP headers per:
//
//	https://docs.promptopinion.ai/fhir-context/mcp-fhir-context
//
// For local debugging, the -stdio flag switches to newline-delimited
// JSON-RPC on stdin/stdout. Note that the stdio transport cannot
// carry HTTP headers, so tools that require FHIR context will not work
// over stdio against the real Prompt Opinion platform — use HTTP.
//
// Usage:
//
//	suture-server                  # HTTP on $PORT (default 8080) at /mcp
//	suture-server -port 9090       # HTTP on :9090
//	suture-server -stdio           # stdio JSON-RPC for local debug
//	suture-server -list            # print registered tools and exit
//	suture-server -version         # print version and exit
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/vinodhalaharvi/suture/internal/fhir"
	"github.com/vinodhalaharvi/suture/internal/mcp"
	"github.com/vinodhalaharvi/suture/pkg/tools"
)

const (
	serverName    = "suture"
	serverVersion = "0.1.0"
)

func main() {
	listFlag := flag.Bool("list", false, "list registered tools and exit")
	versionFlag := flag.Bool("version", false, "print version and exit")
	stdioFlag := flag.Bool("stdio", false, "serve over stdio instead of HTTP (local debug only)")
	portFlag := flag.String("port", defaultPort(), "HTTP port to listen on (HTTP mode)")
	pathFlag := flag.String("path", "/mcp", "HTTP path to serve MCP on")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("%s %s\n", serverName, serverVersion)
		return
	}

	s := mcp.NewServer(serverName, serverVersion)
	registerAll(s)

	if *listFlag {
		for _, t := range s.Tools() {
			fmt.Fprintf(os.Stdout, "%s\t%s\n", t.Name, t.Description)
		}
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		cancel()
	}()

	if *stdioFlag {
		fmt.Fprintf(os.Stderr, "%s %s ready (stdio, local debug only)\n", serverName, serverVersion)
		if err := s.Serve(ctx, os.Stdin, os.Stdout); err != nil {
			fatal(err)
		}
		return
	}

	addr := ":" + *portFlag
	fmt.Fprintf(os.Stderr, "%s %s ready (HTTP) on %s%s\n", serverName, serverVersion, addr, *pathFlag)
	if err := s.ListenAndServe(ctx, addr, *pathFlag); err != nil {
		fatal(err)
	}
}

// registerAll wires every Suture tool into the MCP server and declares
// the SMART scopes the tools collectively require.
func registerAll(s *mcp.Server) {
	c := fhir.NewClient()
	tools.PatientSummaryTool(s, c)
	tools.CHA2DS2VAScTools(s, c)
	tools.ChartReviewTool(s, c)
	tools.PriorAuthAgentTool(s, c)

	// Per https://docs.promptopinion.ai/fhir-context/mcp-fhir-context,
	// we declare the SMART scopes our tools collectively need. Required
	// scopes cannot be skipped by the user.
	s.RequestFHIRScope("patient/Patient.rs", true)
	s.RequestFHIRScope("patient/Condition.rs", true)
	s.RequestFHIRScope("patient/Encounter.rs", false)
	s.RequestFHIRScope("patient/Observation.rs", false)
}

func defaultPort() string {
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	return "8080"
}

func fatal(err error) {
	errBody, _ := json.Marshal(map[string]string{"fatal": err.Error()})
	fmt.Fprintln(os.Stderr, string(errBody))
	os.Exit(1)
}
