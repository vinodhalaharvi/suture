# Example MCP requests

These show the exact wire shape Prompt Opinion uses, so you can verify
your server independently with `curl`. Replace `localhost:8080` with
your deployed URL.

---

## 1. Initialize (no FHIR context needed)

```bash
curl -X POST http://localhost:8080/mcp \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}'
```

Expected response includes:

```json
{
  "result": {
    "capabilities": {
      "extensions": {
        "ai.promptopinion/fhir-context": {
          "scopes": [
            {"name": "patient/Patient.rs", "required": true},
            {"name": "patient/Condition.rs", "required": true},
            {"name": "patient/Encounter.rs"},
            {"name": "patient/Observation.rs"}
          ]
        }
      },
      "tools": {}
    },
    "protocolVersion": "2024-11-05",
    "serverInfo": {"name": "suture", "version": "0.1.0"}
  }
}
```

## 2. List tools

```bash
curl -X POST http://localhost:8080/mcp \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
```

## 3. Get patient summary (requires FHIR context headers)

```bash
curl -X POST http://localhost:8080/mcp \
    -H "Content-Type: application/json" \
    -H "X-FHIR-Server-URL: https://hapi.fhir.org/baseR4" \
    -H "X-FHIR-Access-Token: your-smart-bearer-token" \
    -H "X-Patient-ID: 1234567" \
    -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_patient_summary","arguments":{}}}'
```

## 4. Calculate CHA2DS2-VASc score

```bash
curl -X POST http://localhost:8080/mcp \
    -H "Content-Type: application/json" \
    -H "X-FHIR-Server-URL: https://hapi.fhir.org/baseR4" \
    -H "X-Patient-ID: 1234567" \
    -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"calculate_cha2ds2_vasc","arguments":{}}}'
```

## 5. Summarize recent encounters

```bash
curl -X POST http://localhost:8080/mcp \
    -H "Content-Type: application/json" \
    -H "X-FHIR-Server-URL: https://hapi.fhir.org/baseR4" \
    -H "X-Patient-ID: 1234567" \
    -d '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"summarize_recent_encounters","arguments":{"limit":8}}}'
```

## 6. Prior auth assistant (multi-step agent, needs ANTHROPIC_API_KEY)

```bash
curl -X POST http://localhost:8080/mcp \
    -H "Content-Type: application/json" \
    -H "X-FHIR-Server-URL: https://hapi.fhir.org/baseR4" \
    -H "X-FHIR-Access-Token: your-smart-bearer-token" \
    -H "X-Patient-ID: 1234567" \
    -d '{
      "jsonrpc":"2.0","id":6,"method":"tools/call",
      "params":{
        "name":"prior_auth_assistant",
        "arguments":{"request":"Drafting prior authorization for apixaban 5mg BID for non-valvular atrial fibrillation."}
      }
    }'
```

## Token-optional behavior

Some FHIR sandboxes don't require authorization. In that case, omit
the `X-FHIR-Access-Token` header — Suture will skip the
`Authorization: Bearer ...` header on outgoing FHIR requests:

```bash
curl -X POST http://localhost:8080/mcp \
    -H "Content-Type: application/json" \
    -H "X-FHIR-Server-URL: https://hapi.fhir.org/baseR4" \
    -H "X-Patient-ID: 1234567" \
    -d '{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"get_patient_summary","arguments":{}}}'
```

## Health check

For deploy platforms that need a liveness probe:

```bash
curl http://localhost:8080/healthz
# → "ok"
```
