# Suture — healthcare superpowers and agents on weft + MCP

Suture is a small set of healthcare AI tools built for the Prompt Opinion
**Agents Assemble** hackathon. It demonstrates the SHARP / MCP / A2A
integration story end-to-end, using [weft](https://github.com/vinodhalaharvi/weft)
as the underlying composition algebra.

The thesis: every step of a clinical workflow — a FHIR read, a scoring
rule, an LLM summarization, an agent loop — is a `weft.Arrow[A, B]`.
The same combinators (`Compose`, `Par`, `Pipe3`, `Traverse`, `Apply`)
operate uniformly on every arrow regardless of how it was constructed.
You get hardened cross-cutting behavior (retry, timeout, concurrency,
partial-failure tolerance) for free, and the agent loop comes out as
just another arrow.

## What's in the box

Four Superpowers (Option 1 path) and one Agent (Option 2 path), all
exposed through a single MCP server:

| Tool | Shape | What it shows |
|---|---|---|
| `get_patient_summary` | `Par + Map` | The minimum viable Superpower — two parallel FHIR reads merged into one typed output |
| `calculate_cha2ds2_vasc` | `Pipe3` | Full clinical scoring pipeline: FHIR reads → component extraction → score |
| `get_cha2ds2_vasc_components` | `Compose` | The same building blocks, composed differently — different MCP tool, shared upstream work |
| `summarize_recent_encounters` | `Traverse + Apply` | Bounded-concurrency fan-out across N encounters with `PartialResults` policy |
| `prior_auth_assistant` | `Loop` over the others | Multi-step agent that orchestrates the superpowers behind one MCP tool |

## Architecture

```
Prompt Opinion platform
    │ MCP call_tool + SHARP context (_meta)
    ▼
suture-server (this repo)
    │
    ├─ internal/mcp     JSON-RPC 2.0 over stdio (~200 LOC, no external deps)
    ├─ internal/sharp   SHARP context propagation through context.Context
    ├─ internal/fhir    Typed FHIR R4 reads as weft.Arrow values
    ├─ pkg/agent        LLM tool-use loop as Arrow[Prompt, Response]
    └─ pkg/tools        The five healthcare tools above
            │
            ▼ (composes via weft)
        github.com/vinodhalaharvi/weft
            │
            ▼ (calls)
        FHIR sandbox + Anthropic Claude
```

The architecture diagram in [`docs/architecture.svg`](docs/architecture.svg)
shows how the layers compose and where SHARP context is injected.

## Quick start

```bash
git clone https://github.com/vinodhalaharvi/suture.git
cd suture
go test ./...          # all 50+ tests pass under -race
go build ./...

# Run the MCP server (reads JSON-RPC on stdin, writes on stdout):
./suture-server

# List the registered tools:
./suture-server -list

# Drive a single tool against a real FHIR sandbox:
./demo \
    -tool get_patient_summary \
    -fhir https://hapi.fhir.org/baseR4 \
    -patient 1234567 \
    -token dev

# To use the prior_auth_assistant agent, also set:
export ANTHROPIC_API_KEY=sk-ant-...
./demo \
    -tool prior_auth_assistant \
    -patient 1234567 \
    -request "apixaban for atrial fibrillation"
```

### Talking to the server via raw MCP

```bash
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}
{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' | ./suture-server
```

### Registering with Prompt Opinion

Configure the platform to spawn `suture-server` as a stdio MCP server.
Prompt Opinion's runtime will inject SHARP context (patient ID, FHIR
base URL, bearer token) under the `_meta` field of every `tools/call`
request. See [`internal/sharp/sharp.go`](internal/sharp/sharp.go) for
the exact field names; the contract lives in one file by design — if
Prompt Opinion's SHARP spec uses different keys, this is the only
place that changes.

## SHARP context

Every tool is gated by a small middleware (`runWithSharp` in
[`pkg/tools/patient_summary.go`](pkg/tools/patient_summary.go)) that:

1. Reads `sharp.patient_id`, `sharp.fhir_base`, `sharp.token`, and
   `sharp.practitioner` from the MCP request's `_meta` field.
2. Validates them.
3. Injects a typed `sharp.Context` into `context.Context`.
4. Runs the tool's arrow.

Downstream arrows (FHIR reads, scoring) pull SHARP fields out of
`ctx` rather than threading them through parameters. This keeps the
arrow types clean and means the LLM never sees or controls auth data.

If SHARP context is missing or invalid, the tool returns a clean
`isError: true` MCP result rather than calling FHIR with a bad token.

## Why weft

The repo demonstrates four properties that justify the algebra:

1. **Role-erasure.** `Traverse` doesn't know it contains an LLM call.
   The LLM call doesn't know it's wrapped in retry. Every layer only
   sees its argument's type contract.

2. **Building blocks compose.** `fetchClinicalData`, `extractComponents`,
   and `computeScore` are written once and used to build three different
   MCP tools (`calculate_cha2ds2_vasc`, `get_cha2ds2_vasc_components`,
   and the agent's `calculate_cha2ds2_vasc` binding).

3. **Cross-cutting concerns are decoupled.** `WithRetry`, `WithTimeout`,
   and `WithTap` wrap any arrow without touching its body, and
   `weft.Traverse` provides bounded concurrency with `FailFast` /
   `PartialResults` policies for free.

4. **The agent loop is just an arrow.** `pkg/agent.Loop` returns an
   `Arrow[llm.Prompt, llm.Response]`, which composes with everything
   else in the algebra. You could `Traverse` it across a batch of
   patients, wrap it with `WithTimeout`, or pipe its output into
   another arrow without ceremony.

## Implementation notes

**MCP transport.** The project ships its own ~200-LOC MCP server in
[`internal/mcp`](internal/mcp/server.go) rather than depending on
`mark3labs/mcp-go`. The protocol is just JSON-RPC 2.0 over newline-
delimited stdio; hand-rolling it removes a transitive dependency and
makes the project's surface area auditable in a single file. The
handler interface is intentionally identical in shape to mcp-go's, so
swapping in the upstream library is a one-package change.

**weft v0.1.0 specifically.** The published v0.1.0 of weft does not
expose `llm.Loop`. We implement our own in [`pkg/agent/loop.go`](pkg/agent/loop.go)
in ~100 LOC. This was a happy accident: it makes the "agent loop is
just an arrow" thesis legible without a black-box dependency.

**LLM provider.** Suture uses Anthropic Claude through
[`weft/llm.Claude`](https://github.com/vinodhalaharvi/weft/blob/main/llm/anthropic.go).
Set `ANTHROPIC_API_KEY` to enable the `prior_auth_assistant` tool.
The other four tools work without it.

## Testing

```bash
make test          # all packages
make test-race     # under the race detector (50+ tests, all clean)
make cover         # HTML coverage report
make demo          # build and run the demo against HAPI FHIR
```

Test coverage (current):

| Package | Coverage |
|---|---|
| `internal/sharp` | 96% |
| `pkg/agent` | 94% |
| `internal/mcp` | 91% |
| `pkg/tools` | 79% |
| `internal/fhir` | 64% |

## Layout

```
.
├── cmd/
│   ├── suture-server/   the MCP server binary
│   └── demo/            local CLI for testing tools against a FHIR sandbox
├── internal/
│   ├── mcp/             minimal MCP JSON-RPC server
│   ├── sharp/           SHARP context propagation
│   └── fhir/            typed FHIR R4 client as weft Arrows
├── pkg/
│   ├── agent/           LLM tool-use loop combinator
│   └── tools/           the five healthcare tools (Superpowers + Agent)
├── docs/
│   └── architecture.svg the integration diagram
└── examples/            sample MCP request bodies for each tool
```

## License

MIT.
