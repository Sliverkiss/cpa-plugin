# G14 · host.http.do evaluation (2026-07-23)

## Conclusion

**Do not migrate now.** Keep process-level `net/http` for WorkBuddy upstream
calls. Revisit only if CPA request-monitoring for plugin-originated HTTP
becomes a hard requirement.

## Current path

- Auth, models, chat, billing: shared `http.Client` / short-lived login client
  in `main.go` / `management.go`
- Host RPC used for: `host.auth.list/get/save`, `host.stream.emit/close`
- Usage: in-process `usage.PublishRecord` (same binary as CPA)

## host.http.do pros

- Host can apply global proxy / TLS / logging policy uniformly
- Better correlation in CPA request monitor for upstream hops

## host.http.do cons

- C ABI bridge + JSON envelope per hop; more failure modes
- Login flow needs cookie jar — host HTTP may not preserve jar semantics
  without extra design
- Billing + models + chat volume would amplify RPC overhead
- Live risk: regressions in OAuth poll / multi-account isolation

## Recommendation

| Area | Action |
|---|---|
| Auth OAuth | Stay on plugin `net/http` + cookie jar |
| Chat executor | Stay on plugin `net/http` |
| Billing | Stay on plugin `net/http` |
| Auth store | Keep `host.auth.*` |
| Streaming | Keep `host.stream.*` |

Optional future PoC: single non-auth `host.http.do` probe (e.g. models GET)
behind a feature flag — not scheduled.
