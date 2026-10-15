# Live acceptance access decision

The four fresh live acceptance cases below are **NOT RUN**.
Live acceptance requires approved access and an isolated test environment.
These records do not authorize use of existing credentials or deployments.

## Exact blocked operations

| Fresh acceptance case | Status | Blocked action | Required access |
| --- | --- | --- | --- |
| Claude WebSearch | NOT RUN | Execute Claude hosted WebSearch on `claude-haiku-5.5` Messages and verify genuine execution, meaningful paired results and client completion. | Approved access in an isolated test environment. |
| GPT exact URL pairing | NOT RUN | Execute `gpt-6-luna` Responses search and compare the full cited URL with the same search's meaningful results and action sources. | Approved access in an isolated test environment. |
| Claude-to-GPT PNG semantics | NOT RUN | Execute the Claude Read image flow with the recorded PNG fixture, preserve hashes and tool pairing, and check the answer. | Approved access in an isolated test environment. |
| Original Codex interrupt frame | NOT RUN | Execute `gpt-6-luna` through the original Codex control path and observe `response.interrupt`, cancellation, interrupted terminal and continuation. | Approved access in an isolated test environment. |

An isolated execution also needs separate process, configuration, authentication and client-state boundaries.

Approved access alone does not resolve the retained provider or client failures.
Each case also needs genuine changed capability, supported profile or original-client control-path evidence.
These acceptance criteria do not authorize provider calls.

## Retained evidence

The following results are **REUSED** historical evidence scoped to their original requests and profiles.
They remain failures or unmet criteria, not fresh live acceptance.

| Case | Retained result |
| --- | --- |
| Claude WebSearch | Copilot returned HTTP 400 `unsupported_value` after the genuine declaration was invoked. |
| GPT exact URL pairing | The cited tracking URL appears in action sources but is absent from the same search's meaningful results. |
| Claude-to-GPT PNG semantics | Image bytes and tool pairing were preserved, but the provider returned an incorrect color answer. |
| Original Codex interrupt frame | A successful `turn/interrupt` RPC and same-thread follow-up emitted zero `response.interrupt` wire frames. |

The [server-tool acceptance record](server-tool-acceptance.md) retains the original outcomes and distinct attempts.
The [live validation record](live-copilot-validation.md) provides their dated scope.
Source checks, synthetic fixtures, byte parity, RPC acknowledgements and socket closure do not establish fresh provider acceptance.
URLs, results, identifiers, replay tokens and interrupt frames must not be invented to satisfy these cases.

## Offline delivery and integration

**ACTUAL** offline delivery includes reviewed [PR #36](https://github.com/ririnto/cpa-copilot-bridge/pull/36) and [PR #41](https://github.com/ririnto/cpa-copilot-bridge/pull/41), with local tests, race checks, vet, matched native-host groups and public CI.
[Issue #43](https://github.com/ririnto/cpa-copilot-bridge/issues/43) separately owns representable standard URL citations in Chat JSON.
[PR #35](https://github.com/ririnto/cpa-copilot-bridge/pull/35) owns the final feature completion matrix.
Those offline results do not remove the four NOT RUN cases.

Promotion requires a dedicated reviewed migration with matched-host validation and rollback requirements.
No live deployment, upstream integration or release acceptance is claimed.

Task evidence and disposable resources should follow their ownership and retention requirements.
