// Command suture-server is the MCP server entrypoint.
//
// It registers all of Suture's healthcare tools and serves them over
// stdio (MCP's default transport for desktop clients and platforms
// that spawn the server as a subprocess).
//
// Usage:
//
//	suture-server                  # serve over stdio
//	suture-server -list            # print registered tools and exit
//	suture-server -version         # print version and exit
//
// SHARP context (patient ID, FHIR base URL, bearer token) is expected
// on every tools/call request as fields under the `_meta` object,
// namespaced with `sharp.`. See internal/sharp for the wire shape.
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

	// MCP server reads JSON-RPC messages line-by-line from stdin and
	// writes responses to stdout. stderr is reserved for logs.
	fmt.Fprintf(os.Stderr, "%s %s ready (stdio)\n", serverName, serverVersion)
	if err := s.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		// Use a JSON-RPC-shaped error on stderr so Prompt Opinion's
		// process supervisor has something machine-readable.
		errBody, _ := json.Marshal(map[string]string{"fatal": err.Error()})
		fmt.Fprintln(os.Stderr, string(errBody))
		os.Exit(1)
	}
}

// registerAll wires every Suture tool into the MCP server. Adding a
// new superpower means adding a single line here.
func registerAll(s *mcp.Server) {
	c := fhir.NewClient()
	tools.PatientSummaryTool(s, c)
	tools.CHA2DS2VAScTools(s, c)
	tools.ChartReviewTool(s, c)
	tools.PriorAuthAgentTool(s, c)
}
