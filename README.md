# MMFP Govee

MMFP Govee Webhook listener and General Govee tool — turn GoSlimStratum (GSS) and GSS Miners (GSSM)
notification webhooks into Govee light effects. Flash the ceiling fan light green/gold five times
when a block is found, hold a lamp red while a miner is offline, switch it back when it recovers.

An MMFP Solutions product. Open source under GPLv3 (see [LICENSE](LICENSE)).

## How it works

```
GSS / GSSM ── generic webhook (POST /hook, X-MMFP-Token) ──▶ MMFP Govee ──▶ Govee cloud API ──▶ lights
```

Two listeners:

| Listener | Port | Purpose |
|----------|------|---------|
| Web app | 3008 | Devices, event mappings, tokens, activity, settings |
| Webhook | 8787 | Receives GSS / GSSM generic-webhook events |

The webhook endpoint routes on the event type **in the body** (`event_type` from GSSM, `type` from
GSS), so one URL serves every event a channel sends. Effects run asynchronously — the webhook is
acknowledged immediately, effects are serialized per device, and a per-mapping cooldown collapses
event storms.

## Quick start

1. **Run it** (Docker):

   ```bash
   docker run -d -p 3008:3008 -p 8787:8787 -v ./config:/app/config \
     ghcr.io/mmfpsolutions/mmfp-govee:latest
   ```

   Or locally: `go build ./cmd/mmfp-govee && ./mmfp-govee` (config is created on first run).

2. **Add your Govee API key** — open `http://localhost:3008`, you'll land on Settings. Get a key in
   the Govee Home app (Profile → Settings → Apply for API Key). The key is stored encrypted at rest.

3. **Create a token** on the Tokens page — the dialog shows the secret once, with copy-paste GSS/GSSM
   setup values.

4. **Point GSS or GSSM at it** — add a *generic* webhook channel:
   - **GSSM** (its webhook form only takes a URL): `http://<this-machine>:8787/hook?token=<your secret>`
   - Callers that support custom headers: URL `http://<this-machine>:8787/hook` +
     header `X-MMFP-Token: <your secret>` (keeps the token out of URLs and logs).
     GSSM's backend supports a `headers` map too, but only via hand-editing notifications.json.

   Use the sender's Test button — it returns 200 and shows up on the Activity page. Note the Test
   buttons emit **fixed** events (GSS: `startup`, GSSM: `test`), not your mapped events — they prove
   the wiring, not the mapping. To preview a mapping's effect, use its own Test button in MMFP
   Govee, or force the event: `curl "http://<host>:8787/hook/<event>?token=<secret>"`.

5. **Create a mapping** on the Mappings page: pick the token, the event(s) (e.g. `block_found`),
   the device(s), and an effect (presets included: Celebrate, Alert, All clear, Off). Hit its Test
   button to see the lights fire.

## Webhook endpoint

```
POST /hook              event type read from the JSON body (GSSM event_type / GSS type)
POST /hook/{event}      forced-event mode (body optional)
GET  /hook/{event}      same, curl-friendly
```

Auth: `X-MMFP-Token` header or `?token=` query param. Responses: `202` queued / no mapping,
`200` test events, `400` no event type, `403` bad token.

## Configuration

`config/config.json` — see [config.example.json](config/config.example.json). Managed from the UI;
ports require a restart. Secrets (`goveeApiKey`, token secrets) are encrypted at rest automatically
(`ENC:` prefix); plaintext values are migrated on first load.

**Optional login:** authentication is off by default. To enable it, set
`"disableAuthentication": false` and create in the config directory:

- `access.json` — `{"<username>": "<sha256-hex-of-password>"}`
- `jsonWebTokenKey.json` — `{"jsonWebTokenKey": "<random-secret>", "expiresIn": "1h"}`

**Quiet hours:** suppress effects inside a local-time window (webhooks are still logged).

## Build

```bash
./build-local.sh        # compiles Tailwind CSS + builds the Docker image (no cache)
go test ./...           # unit tests
```

## Events

Any event type the sender emits can be mapped. Known types are offered in the mapping editor:

- **GSS:** `block_found`, `block_matured`, `block_orphaned`, `payment_*`, `node_*`,
  `miner_connect`, `miner_disconnect`, `best_share`, `notable_share`, `rejected_shares`,
  `network_difficulty_*`, `startup`, `shutdown`, `cleanup`
- **GSSM:** `miner_offline`, `miner_online`, `miner_failover`, `miner_zero_hashrate`,
  `miner_temp_high`, `miner_temp_normal`, `pool_offline`, `pool_online`, `node_offline`,
  `node_online`, `system_startup`, `system_shutdown`, `test`
