# Live acceptance record

Live provider acceptance is closed as not pursued by owner decision.
No further live acceptance request is open.
This record does not authorize provider calls or use of existing credentials and deployments.

## Closed acceptance cases

| Acceptance case | Status | Basis |
| --- | --- | --- |
| Claude WebSearch execution | Closed, not pursued | Copilot returned HTTP 400 `unsupported_value` for the genuine declaration. |
| GPT exact URL pairing | Closed, not pursued | The cited tracking URL is absent from the same search's meaningful results. |
| Claude-to-GPT PNG semantics | Closed, not pursued | Transport preserved the bytes, but the provider returned an incorrect color answer. |
| Original Codex interrupt frame | Closed, not pursued | A successful `turn/interrupt` RPC emitted zero `response.interrupt` frames. |

The retained results above come from earlier isolated runs.
They stay recorded as the outcome of those requests and are not re-verified here.

## What remains available

Offline and matched-host checks stay in place for every future change:

- Go and Python suites, race checks and `go vet`.
- The matched native-host integration groups.
- The public CI test and package lanes.

These checks validate source and packaging behavior.
They do not claim provider acceptance, original-client acceptance or live deployment.

## Promotion

Promotion to a standalone official-host release needs a dedicated reviewed migration with matched-host validation and rollback requirements.
No live deployment, upstream integration or release acceptance is claimed.
