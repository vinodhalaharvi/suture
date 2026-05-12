# cmd/console — operator console UI

A small Go binary that serves a single-file React demo UI for
`suture-server`. The UI visualizes a real `get_patient_summary`
invocation: HTTP request panel → execution timeline (with overlapping
parallel bars for `weft.Par`) → typed result rendered as a patient card.

## Why this exists

For the hackathon demo video. Watching `curl | jq` doesn't communicate
"this is real infrastructure." A dark observability dashboard showing
the request flow does.

## Why a Go binary instead of `python -m http.server`

So everything ships as Go. No runtime dependency on Python. The HTML is
embedded into the binary via `embed.FS`, so deployment is a single
binary with no separate file to ship.

## Run

In two terminals:

```bash
# Terminal 1
./bin/suture-server          # MCP server on :8080

# Terminal 2
./bin/console                # UI on :9000
```

Open http://localhost:9000 in a browser. The UI hits `localhost:8080/mcp`
by default — change it in the URL field at the top of the page.

## Layout

```
cmd/console/
├── main.go                  # ~50 LOC: embed.FS + http.FileServer
└── assets/
    └── console.html         # ~700 LOC: single-file React, no build step
```

The HTML uses React via CDN (`unpkg.com`) and Babel standalone, so
there's no npm or webpack involved. Edit `assets/console.html` and
rebuild (`go build ./cmd/console`) to ship changes.

## CORS

`suture-server` sends `Access-Control-Allow-Origin: *` on all HTTP
responses (not just OPTIONS preflight), so the console works from any
local origin without a proxy. This was a real bug in the earlier
build — easy to miss because `curl` doesn't enforce CORS.
