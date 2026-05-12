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
//	console               # serve on :9000, open browser to /
//	console -port 9090    # different port
//	console -version      # print version and exit
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
	fmt.Fprintf(os.Stderr, "(make sure suture-server is also running, default :8080)\n\n")

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
