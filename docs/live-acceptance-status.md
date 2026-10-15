# Live acceptance access decision

The four fresh live acceptance cases below are **NOT RUN**.
On 2026-10-13, the Orchestrator explicitly marked the sealed provider-path request **NOT GRANTED**.
No approved task-only Copilot credential or broker is available.
The only credentials on this host belong to the live CLIProxyAPI.
Authority prohibits using, copying or reconfiguring its authentication, configuration or plugins, including moving credentials into task processes.
No new target-provider inference was dispatched for these cases.

## Exact blocked operations

| Fresh acceptance case | Status | Blocked action | Required access |
| --- | --- | --- | --- |
| Claude WebSearch | NOT RUN | Execute original Claude hosted WebSearch on exact `claude-haiku-5.5` Messages and verify genuine execution, meaningful paired results and client completion. | A separate approved task-only Copilot credential with explicit read and refresh authority, plus task-owned loopback ports `19055` and `19056`. |
| GPT exact URL pairing | NOT RUN | Execute exact `gpt-6-luna` Responses search and compare the full cited URL, including its query, with the same search's meaningful results and action sources. | A separate approved task-only Copilot credential with explicit read and refresh authority, plus task-owned loopback ports `19055` and `19056`. |
| Claude-to-GPT PNG semantics | NOT RUN | Dispatch the original Claude Read carrier and unchanged 177-byte red-top/blue-bottom PNG to exact `gpt-6-luna`, preserving hashes and tool pairing before checking the answer. | A separate approved task-only Copilot credential with explicit read and refresh authority, plus task-owned loopback ports `19055` and `19056`. |
| Original Codex interrupt frame | NOT RUN | Execute authenticated `gpt-6-luna` through the genuine original Codex control path and observe `response.interrupt`, cancellation, interrupted terminal and continuation. | A separate approved task-only Copilot credential with explicit read and refresh authority, plus task-owned loopback ports `19055` and `19056`. |

Port `19055` is proposed for the isolated host, and `19056` is proposed for its capture and egress gate.
They are not active or granted runtime resources.
A future authorized execution must recheck both ports and abort on a collision.
It also needs isolated process trees, configuration, authentication and client-state directories.
The credential must be supplied independently of the live CLIProxyAPI assets.
No credential discovery or alternate use of the live service is authorized.

Approved access alone does not resolve the retained provider or client failures.
Each case also needs genuine changed capability, supported profile or original-client control-path evidence.
The denied request proposed at most 18 physical inference dispatches, one catalogue read per exact model and 45 minutes after admission.
These proposed limits grant no execution authority.

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

The independent Sol/high integration decision retains `codex/copilot-v8-pr` as the paired-host v8 candidate.
Maintained-v7 `main` remains separate, and the primary checkout stays on `main`.
Promotion requires a dedicated reviewed migration with matched-host validation and rollback requirements.
No live deployment, upstream integration or release acceptance is claimed.

The private provider-request manifest remains untracked and records NOT GRANTED and NOT RUN.
No authenticated acceptance process, copied credential or provider capture was created for the denied request.
Each owner removes disposable worktrees, feature refs, processes, builds, caches and redundant logs after delivery and preservation checks.
