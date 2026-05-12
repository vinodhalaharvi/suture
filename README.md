# Suture — healthcare superpowers and agents on weft + MCP

Suture is a set of healthcare AI tools built for the Prompt Opinion
**Agents Assemble** hackathon. It demonstrates a working integration
with Prompt Opinion's published FHIR context spec, with the underlying
tools composed using the [weft](https://github.com/vinodhalaharvi/weft)
algebra.

The thesis: every step of a clinical workflow — a FHIR read, a scoring
rule, an LLM summarization, an agent loop — is a `weft.Arrow[A, B]`.
The same combinators (`Compose`, `Par`, `Pipe3`, `Traverse`, `Apply`)
operate uniformly on every arrow regardless of how it was constructed.

## What's in the box

Five healthcare MCP tools, exposed over HTTP at `/mcp`:

| Tool | Shape | What it shows |
|---|---|---|
| `get_patient_summary` | `Par + Map` | Two parallel FHIR reads merged into one typed output |
| `calculate_cha2ds2_vasc` | `Pipe3` | FHIR reads → component extraction → scoring rules |
| `get_cha2ds2_vasc_components` | `Compose` | Same building blocks composed differently |
| `summarize_recent_encounters` | `Traverse + Apply` | Bounded-concurrency fan-out with `PartialResults` |
| `prior_auth_assistant` | `Loop` over the others | Multi-step agent that orchestrates the superpowers |

## Integration with Prompt Opinion

Suture implements [Prompt Opinion's FHIR context spec for MCP servers](https://docs.promptopinion.ai/fhir-context/mcp-fhir-context):

1. **Transport.** HTTP POST to `/mcp` carrying JSON-RPC 2.0 messages.
2. **Capability declaration.** The `initialize` response includes
   `capabilities.extensions["ai.promptopinion/fhir-context"]` with the
   SMART-on-FHIR scopes Suture's tools require:
   - `patient/Patient.rs` (required)
   - `patient/Condition.rs` (required)
   - `patient/Encounter.rs` (optional)
   - `patient/Observation.rs` (optional)
3. **Context propagation.** Every `tools/call` request carries the FHIR
   context as HTTP headers:
   - `X-FHIR-Server-URL` — the FHIR base URL
   - `X-FHIR-Access-Token` — SMART bearer token (optional)
   - `X-Patient-ID` — current patient
4. **Token handling.** The token is passed verbatim as a `Bearer`
   header on every FHIR request. If no token is supplied, no
   `Authorization` header is sent — per the spec, some FHIR servers
   don't require auth.

## Quick start

```bash
git clone https://github.com/vinodhalaharvi/suture.git
cd suture
go test ./...           # all tests pass, race-clean
go build ./...

# Run the MCP server (HTTP on :8080):
./suture-server

# Or pick a different port:
PORT=9090 ./suture-server

# List the registered tools:
./suture-server -list

# Call a tool directly via HTTP (mimics what Prompt Opinion does):
curl -X POST http://localhost:8080/mcp \
    -H "Content-Type: application/json" \
    -H "X-FHIR-Server-URL: https://hapi.fhir.org/baseR4" \
    -H "X-Patient-ID: 12345" \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_patient_summary","arguments":{}}}'
```

### Local CLI demo (no Prompt Opinion required)

```bash
./demo \
    -tool get_patient_summary \
    -fhir https://hapi.fhir.org/baseR4 \
    -patient 1234567

# To use the prior_auth_assistant agent:
export ANTHROPIC_API_KEY=sk-ant-...
./demo \
    -tool prior_auth_assistant \
    -patient 1234567 \
    -request "apixaban for atrial fibrillation"
```

### Registering with Prompt Opinion

1. Deploy `suture-server` somewhere reachable (Fly.io, Cloud Run, Railway, etc.).
2. In the Prompt Opinion web app, go to `Configuration → MCP Servers`.
3. Paste your deployed URL (e.g., `https://suture.fly.dev/mcp`).
4. Click `Continue`. The platform sends `initialize` and reads the
   FHIR context extension. You'll be shown the SMART scopes the server
   requests; authorize them.
5. The platform now invokes Suture's tools with the FHIR headers
   attached. The full integration test in
   `cmd/suture-server/main_test.go` simulates this exact flow against
   a faithful HTTP test client.

## Configuration via environment variables

```bash
PORT=8080                          # HTTP port (default 8080)
ANTHROPIC_API_KEY=sk-ant-...       # required only for prior_auth_assistant
```

That's the complete config surface for production.

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│  Prompt Opinion platform                                     │
│  (HTTP POST to /mcp with FHIR context headers)               │
└─────────────────────────┬────────────────────────────────────┘
                          │
┌─────────────────────────▼────────────────────────────────────┐
│  cmd/suture-server                                           │
│  HTTP listener → JSON-RPC dispatch                           │
└─────────────────────────┬────────────────────────────────────┘
                          │
┌─────────────────────────▼────────────────────────────────────┐
│  internal/mcp                                                │
│  Stashes HTTP headers into context.Context                   │
│  Declares ai.promptopinion/fhir-context extension            │
└─────────────────────────┬────────────────────────────────────┘
                          │
┌─────────────────────────▼────────────────────────────────────┐
│  internal/fhircontext                                        │
│  Reads X-FHIR-Server-URL / X-FHIR-Access-Token / X-Patient-ID│
│  Injects typed fhircontext.Context for downstream arrows     │
└─────────────────────────┬────────────────────────────────────┘
                          │
┌─────────────────────────▼────────────────────────────────────┐
│  internal/fhir + pkg/tools + pkg/agent                       │
│  weft.Arrow values composed via Pipe3, Par, Traverse, …      │
└──────────────────────────────────────────────────────────────┘
```

The integration-specific code (HTTP transport + header extraction) is
isolated in `internal/mcp/http.go` and `internal/fhircontext`. The FHIR
client, tool arrows, agent loop, and tests below that line are
platform-agnostic.

## Test suite

```bash
make test          # all packages
make test-race     # under the race detector
make cover         # HTML coverage report
```

The headline test is in `cmd/suture-server/main_test.go`: it spins up
a real HTTP MCP server, simulates Prompt Opinion's exact calling
pattern (POST `/mcp` with `X-FHIR-Server-URL`, `X-FHIR-Access-Token`,
`X-Patient-ID` headers), and verifies the full stack — header
extraction → FHIR context propagation → weft arrow → authenticated
FHIR call → typed output. If that test passes, the integration is real.

## Layout

```
.
├── cmd/
│   ├── suture-server/        the MCP server binary
│   └── demo/                 local CLI for testing tools against a FHIR sandbox
├── internal/
│   ├── mcp/                  HTTP + stdio MCP transport, capability extensions
│   ├── fhircontext/          Prompt Opinion FHIR context propagation
│   └── fhir/                 typed FHIR R4 client as weft Arrows
├── pkg/
│   ├── agent/                LLM tool-use loop combinator
│   └── tools/                the five healthcare tools
├── docs/
│   └── architecture.svg      integration diagram
└── examples/                 sample MCP request bodies
```

## License

MIT.
