// Command console serves the operator-console HTML UI on localhost.
//
// The console is a single static HTML file (web/console.html) that
// hits suture-server's HTTP endpoint to demonstrate the request /
// trace / result flow for a tool invocation.
//
// We can't open the HTML directly via file:// because browsers block
// fetch() from filesystem origins. This binary embeds the HTML into
// the Go binary at build time, so deployment is one file with no
// Python or Node dependency.
//
// Usage:
//
//	console                                              # defaults: :9000, talks to :8080/mcp
//	console -port 9090                                   # different console port
//	console -server-url http://localhost:8888/mcp        # different suture-server URL
//	console -version                                     # print version and exit
//
// Environment variables (override flag defaults):
//
//	CONSOLE_PORT          (e.g. 9090)
//	SUTURE_SERVER_URL     (e.g. https://suture.fly.dev/mcp)
//
// In a typical demo flow you run two processes side by side:
//
//	./bin/suture-server   # the MCP server (port 8080)
//	./bin/console         # the UI (port 9000)
//
// Then open http://localhost:9000 in a browser.
package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
)

//go:embed all:assets
var embedded embed.FS

const version = "0.1.0"

func main() {
	port := flag.String("port", defaultPort(), "HTTP port to listen on")
	serverURL := flag.String("server-url", defaultServerURL(), "default suture-server URL the console points at")
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("console %s\n", version)
		return
	}

	// Strip the "assets/" prefix so the embedded files are served
	// from the URL root (i.e., /console.html, not /assets/console.html).
	sub, err := fs.Sub(embedded, "assets")
	if err != nil {
		fmt.Fprintf(os.Stderr, "embed: %v\n", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()

	// /config.json — the HTML fetches this on load to pick up the
	// server URL the operator wants to talk to. This lets you run
	// the console with `-server-url` pointing at any deployed
	// suture-server (Fly, Cloud Run, a different local port, etc.)
	// without rebuilding the binary or editing the embedded HTML.
	mux.HandleFunc("/config.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store, must-revalidate")
		fmt.Fprintf(w, `{"serverURL":%q,"version":%q}`, *serverURL, version)
	})

	// Serve "/" as console.html so users don't have to type the filename.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Disable caching during demos — we don't want stale console
		// HTML when the developer is iterating on it.
		w.Header().Set("Cache-Control", "no-store, must-revalidate")
		if r.URL.Path == "/" {
			r.URL.Path = "/console.html"
		}
		http.FileServer(http.FS(sub)).ServeHTTP(w, r)
	})

	addr := ":" + *port
	url := fmt.Sprintf("http://localhost:%s/", *port)

	fmt.Fprintf(os.Stderr, "console %s ready on %s\n", version, addr)
	fmt.Fprintf(os.Stderr, "open: %s\n", url)
	fmt.Fprintf(os.Stderr, "talks to suture-server at: %s\n\n", *serverURL)

	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(1)
	}
}

func defaultPort() string {
	if p := os.Getenv("CONSOLE_PORT"); p != "" {
		return p
	}
	return "9000"
}

func defaultServerURL() string {
	if u := os.Getenv("SUTURE_SERVER_URL"); u != "" {
		return u
	}
	return "http://localhost:8080/mcp"
}
