# Live Copilot compatibility validation

## Current validation (2026-10-16)

The dependency and original-client checks use CLIProxyAPI SDK v8.0.30 with the same matched-host `replace` from `go.mod` and Go 1.27.2.
Claude Code 2.1.300 and Codex CLI 0.162.1 ran directly in isolated task-owned homes against the matched host.
The runs used a minimal task-private enabled copy of the Copilot auth record; the original auth record stayed read-only and untouched.
Refresh activity, when it occurred, was isolated to the copy.
All captures, ledgers and rejected bodies remain private; only verdicts and classifications appear here.

All 18 protocol matrix cells passed across JSON and SSE with the task-local enabled profile, Gemini first.

| Model | Chat | Responses | Messages |
| --- | --- | --- | --- |
| `gemini-3.8-flash` | JSON and SSE: PASS | JSON and SSE: PASS | JSON and SSE: PASS |
| `gpt-6-luna` | JSON and SSE: PASS | JSON and SSE: PASS | JSON and SSE: PASS |
| `claude-haiku-5.5` | JSON and SSE: PASS | JSON and SSE: PASS | JSON and SSE: PASS |

| Client | Baseline | Web search | Delegation |
| --- | --- | --- | --- |
| Claude Code 2.1.300 | PASS | FAIL: Copilot returned HTTP 400 `unsupported_value` for the web search tool; unchanged external limit | Real Agent subdelegation: PASS |
| Codex CLI 0.162.1 | PASS | PASS with a genuine native `web_search` invocation and meaningful results | Real child spawn, wait and handback: PASS, including attributed-answer phrasings and encrypted handback carriers |

The original-client attachment packet added seven cross-client image and PDF cells with Claude Code 2.1.300 and Codex CLI 0.162.1 for `gemini-3.8-flash`, `gpt-6-luna` and `claude-haiku-5.5`.
Its harness initially misclassified two things and both were corrected with retained evidence: Messages-style `tool_use` stream terminals were not recognized as ingress completion confirmations, and replay-time prior-user fingerprinting stopped at image-bearing user turns while seal-time fingerprinting skipped them.
The replay fix lets sealed reasoning restore onto the SDK-coalesced assistant turn after an image-bearing user message, verified by an offline regression that fails with the old asymmetric fingerprint.
The upstream rejection evidence recorder now retains a redacted, header-allowlisted private record of non-2xx Copilot inference responses when explicitly enabled; client-visible errors still never include provider response text.
The attachment qualification remains incomplete. The retained earlier packet has one ACTUAL pass for `codex-claude-png`; its source and toolchain predate the subsequent fixes, so this is REUSED evidence for the current candidate, not a fresh final-candidate pass.
The later bounded attempts returned HTTP 429 with `error.code: gate_dispatch_denied`. Source inspection identifies this response as a **local test-harness refusal**, emitted by `liveServerToolGate.serveHTTP` when `reserve` denies a dispatch. The persistent ledger already froze the affected cell after the earlier failure; its physical inference count did not increase during these attempts. The requests did not establish a new Copilot inference response.
Consequently these local refusals establish neither Copilot quota exhaustion, provider throttling, nor a provider routing/protocol error. No provider reset time is known. The CLI's `unrecognized_model` diagnostic is not proof of an unsupported model either.
One changed-evidence qualification packet then used distinct attempt keys without clearing any frozen historical entries, in Gemini, Haiku and Luna order. It made six physical inference dispatches across five cells; both Luna cells were blocked before inference after the harness lost readiness. The local-denial attempts must not be counted as physical provider dispatches.
That packet used the previously built plugin, not the final review-fixed artifact. The matched-host suite exposed the stale artifact by observing `validated` where the corrected mandatory-tool test expected `required`. Rebuilding the plugin before rerunning the native suite resolved all six leaf failures; the fresh native suite passed. At that stage, no additional live packet had run against the final artifact.
The changed packet re-passed Codex-to-Haiku PNG. Both Claude-to-Gemini image/PDF cells produced real Read calls and results but failed final completion and public carrier/terminal proofs. Codex-to-Gemini PNG returned the correct image answer with high detail preserved but failed the required public-terminal proof, so it remains FAILED, not accepted. Codex-to-Haiku PDF hit local HTTP 400 `attachment_request_unprepared` on a route the harness did not admit. These are unresolved qualification-harness failures, not established external provider blockers.
In that earlier packet, `codex-gemini-pdf` was NOT RUN because its harness cell did not yet exist.
The earlier missing combinations and original-format multi-turn reasoning acceptance remained incomplete.
All earlier failed verdicts remain retained without rewriting them after later harness changes.
The PDF preflight again requires the exact previously verified rendered-page SHA-256. The attempted pixel-density substitute was removed after review because it could accept unreadable glyph substitutes and misclassify unsigned color differences. JPEG carrier checks remain separate from printed-code and final-answer acceptance.

### Final-artifact attachment packet

The harness now defines all twelve original-client, exact-model and media combinations.
Offline checks cover actual ingress and public-gate HTTP boundaries, persistent budgets, readiness failure, and original-format tool/reasoning replay.
Review corrections require every tool-turn reasoning carrier at its own call/name/argument anchor.
They also bind actual Claude PDF media to the verified JPEG hash and Codex PDF media to an exact-source offline PNG render.
Native Responses must preserve image detail, assistant item/call identity, canonical arguments and paired results.

Before traffic, the rebuilt plugin passed the matched native-host suite with 220 PASS markers and zero failures.
The installed original clients and maintained renderer passed their offline preflights.
One final-artifact packet then persisted twelve ordered cells, one attempt per cell, a per-cell cap of four and a total cap of 48 physical inference dispatches.
Frozen historical counts remained unchanged.
The order was Gemini, Haiku, then Luna.

| Cell | Actual packet result | Physical inference dispatches |
| --- | --- | --- |
| `claude-gemini-png` | FAILED: original-client capture readiness was lost. | 1 |
| `claude-gemini-pdf` | NOT RUN: packet stopped. | 0 |
| `codex-gemini-png` | NOT RUN: packet stopped. | 0 |
| `codex-gemini-pdf` | NOT RUN: packet stopped. | 0 |
| `claude-claude-png` | NOT RUN: packet stopped. | 0 |
| `claude-claude-pdf` | NOT RUN: packet stopped. | 0 |
| `codex-claude-png` | NOT RUN: packet stopped. | 0 |
| `codex-claude-pdf` | NOT RUN: packet stopped. | 0 |
| `claude-gpt-png` | NOT RUN: packet stopped. | 0 |
| `claude-gpt-pdf` | NOT RUN: packet stopped. | 0 |
| `codex-gpt-png` | NOT RUN: packet stopped. | 0 |
| `codex-gpt-pdf` | NOT RUN: packet stopped. | 0 |

The provider returned a valid Chat tool-call stream for the first cell.
The translated Messages stream included an empty `thinking` block with a nonempty signature carrier.
The ingress validator rejected that representation and denied the next local client request before another provider dispatch.
Systemic readiness stopping prevented requests for all eleven remaining cells.
The packet made exactly one physical inference dispatch, increasing the retained ledger total from 13 to 14.
This is a local qualification failure, not evidence of provider quota exhaustion or throttling.

A subsequent offline regression reproduced the signature-only carrier failure through the actual ingress HTTP boundary.
The validator now accepts that carrier alongside a meaningful tool/text block, while rejecting empty unsigned thinking.
That correction passed offline tests and race checks, but received no additional live qualification.
No packet restart, cell retry, ledger clearing or second final-artifact packet occurred.
Final original-client attachment and multi-turn reasoning acceptance remains incomplete.
Earlier protocol and client passes remain historical evidence rather than acceptance of these twelve cells.

### Follow-up owned-boundary qualification

The follow-up census found that the former Codex Haiku PDF route could switch from native Messages preparation to Chat after signed reasoning was issued.
Native Messages signatures are not Chat-owned carriers, so rejecting that switch is correct.
This cell now selects a separate task-owned Chat-configured host before bootstrap and keeps that route throughout the conversation.
Claude Haiku cells retain native Messages.
The public gate admits exact-source Haiku PDF Chat preparation without permitting the former mixed route.

Actual installed-client replay now composes original ingress, the matched native host, production translation, the public gate, synthetic loopback provider responses and the unchanged full acceptance predicate.
It also exercises source-derived PDF rendering and native tool execution rather than substituting client event fixtures.
The replay exposed two additional corrections.
Claude Read continuations now correlate encoded tool identity and canonical arguments against both original and public captures before recognizing the result.
Claude-to-Responses requests now restore an omitted native reasoning item ID using the existing exact-ciphertext, unique-owner, same-scope cache.
Explicit IDs, foreign ciphertext and ambiguous ownership are not rewritten.
Both buffered and streamed execution use that existing restoration capability.
The reasoning observer remains strict.

Codex retains its native read-only sandbox for PNG and workspace-write sandbox for PDF without a nested external Seatbelt wrapper.
The owned offline host and Claude client use the existing loopback Seatbelt wrapper.
Codex uses isolated client configuration and the synthetic task-proxy route; these checks do not establish universal OS-level network denial for its parent process.
A separate read-only HTTP regression forwards the exact retained failed ingress transaction without sending its private carriers to a synthetic provider.

The final owned offline twelve-cell run passed ten cells against the full acceptance predicate.
Both `codex-gpt-png` and `codex-gpt-pdf` remain FAILED because original and public native Responses image detail is omitted rather than explicitly `high`.
Their tools, final answers, source-bound media and reasoning replay complete, but those successes do not override the required detail assertion.
The installed Codex CLI 0.162.1 embeds `gpt-6-luna` metadata with `use_responses_lite=true`.
Inspected Codex source removes image detail from both initial image content and tool-output images for that model mode.
No supported inspected configuration override preserves explicit high detail without replacing the required catalog or model.
This is an unmet installed-client prerequisite, not a reason to manufacture metadata, inject detail after original capture or weaken acceptance.

The rebuilt plugin passed the matched native-host suite with 220 PASS markers and zero failures.
No new live packet was armed or dispatched during this follow-up.
The historical physical inference ledger remains 14, and all earlier failed and NOT RUN records remain unchanged.
Final twelve-cell acceptance remains incomplete.

## Historical validation (2026-10-11)

The latest dependency and original-client checks use CLIProxyAPI SDK v8.0.30.
The matched host used `replace github.com/router-for-me/CLIProxyAPI/v8 => github.com/ririnto/CLIProxyAPI/v8 v8.0.0-20261010040012-c48c64443575` from `go.mod` and Go 1.27.2.
Claude Code 2.1.296 and Codex CLI 0.162.1 ran against that matched host.
The original Codex cases were revalidated after the declared-search stream identity barrier changed their Responses lifecycle.
The stable installed 0.162.1 executable ran directly in isolated client homes.
The current clean plugin was built into a separate artifact directory and loaded there in place, without copying or installing a plugin.
The existing operating proxy, installed plugin, original credentials and saved configuration were preserved.

All 18 protocol matrix calls passed across JSON and SSE.
Each of the three models passed Chat, Responses, and Messages in both formats.

| Model | Chat | Responses | Messages |
| --- | --- | --- | --- |
| `gemini-3.8-flash` | JSON and SSE: PASS | JSON and SSE: PASS | JSON and SSE: PASS |
| `gpt-6-luna` | JSON and SSE: PASS | JSON and SSE: PASS | JSON and SSE: PASS |
| `claude-haiku-5.5` | JSON and SSE: PASS | JSON and SSE: PASS | JSON and SSE: PASS |

| Client | Baseline | Web search | Delegation |
| --- | --- | --- | --- |
| Claude Code 2.1.296 | PASS | Invoked; Copilot returned HTTP 400 `unsupported_value` | Real Agent subdelegation: PASS |
| Codex CLI 0.162.1 | PASS after stream repair | FAIL: declaration present, model reported native search unavailable without invoking it | Real child, wait, and handback: PASS after stream repair |

The earlier original Codex native-search PASS remains historical evidence.
The current failed search request contains the genuine native `web_search` declaration with external access enabled.
HTTP 200, a completed turn and a refusal sentence do not establish tool execution.
The failed request and complete response remain retained; no identical retry was made.

The Codex result followed preservation of the reserved Responses schema's `collaboration.wait_agent.timeout_ms` number type through CPA.
The plugin did not rewrite that schema or invent a delegated result.

The current original Codex app-server revalidation completed `turn/interrupt` and a same-thread follow-up across two actual GPT-6-Luna medium Responses requests.
The original client sent zero `response.interrupt` frames to CPA during that run.
The RPC result does not prove that Codex emitted that wire frame.
The same-thread follow-up used a new WebSocket with `previous_response_id: null`.
It establishes app-server thread continuation, not a Responses-chain continuation.
Synthetic native-control regressions remain separate evidence.
The relay records application messages but cannot expose WebSocket masking bits or fragmentation boundaries.

The original 177-byte Codex CLI PNG request to Haiku retained its SHA-256 and `detail: high` through the advertised Chat route.
Haiku returned the correct red-and-blue answer.
The original Claude Code PDF request to GPT-6-Luna preserved attachment bytes and tool-result correlation, then returned the correct `Q7B9` blue answer.
The original Claude Code PNG request to GPT-6-Luna preserved bytes and transport but received an incorrect blue answer.
That PNG-to-GPT case remains a semantic failure despite successful transport.
Do not substitute another model to make it pass.

Image-detail routing uses the model catalog and preserves the original request semantics.
If Messages cannot represent image detail, the plugin selects Chat before dispatch when the same model advertises compatibility.
It keeps the original image bytes and detail unchanged.
If no advertised route can preserve the detail, or the caller explicitly forces Messages, the plugin returns HTTP 422 `unsupported_image_detail`.
It does not downgrade the detail to `auto` or substitute a model.

The latest original Codex PDF run completed with three HTTP 200 responses, exit code 0, and the correct `Q7B9` blue answer, independently verified against the full response bodies.
Offline replay of the exact captures accepted the case after checking the carrier pair and actual mixed Messages-to-Chat endpoint sequence.
The audit records three physical inference requests and zero provider calls during replay.
It preserves the original `accepted: false` result unchanged and writes the audited `accepted: true` result separately.

Native Claude hosted Bash SSE and exact-history continuation now pass with actual paired stdout `385` and no repeated tool execution.
Native GPT hosted search exposed changing response, reasoning and message identities.
The plugin now buffers declared search from the first event and binds earlier identities to the unchanged terminal snapshot.
An actual rerun passed identity checks but retained an exact result/citation URL-pairing failure.
A separate real continuation reused the original captured terminal output and ciphertext unchanged and passed without another search.
The original failed native streams remain failed; offline repair and authentic terminal eligibility are recorded separately.
The original app-server HTTPS-image input was also tested with the already-published 177-byte PNG.
Codex rejected `turn/start` with JSON-RPC error `-32600`: `remote image URLs are not supported; use an inline data URL instead`.
The rejection happened before any inference request reached CPA; it made zero provider inference calls.
It does not establish whether the exact Copilot model accepts remote image references.
The test preserves the original URL, `detail: high`, exact model and medium effort, without replacing the URL with inline bytes.
The maintained driver and oracle now ask for the fixture's red top and blue bottom.
The earlier left/right prompt was inconsistent with the actual PNG; a regression decodes its pixels and rejects wrong colors or orientation.
That harness correction does not change the retained client rejection or establish a provider URL-reference pass.

A separate original Claude Code 2.1.296 structured-stdin case sent the same HTTPS URL to exact `claude-haiku-5.5` on native `/v1/messages`.
The original and public image blocks were identical; the driver neither fetched the image nor substituted inline bytes.
Copilot returned HTTP 400 with `external image URLs are not supported`, and the original client received the corresponding HTTP 400 error.
This is a captured provider rejection for the URL-only Messages profile, distinct from Codex's client rejection.
Claude's URL source cannot express independent MIME or image-detail values, so this case does not satisfy that broader carrier requirement.
The first harness attempt used a duplicated `/v1/v1/messages` path and returned CPA 404 with zero provider inference; it remains separate from the corrected native-origin attempt.
The corrected attempt made one provider inference; automatic subsequent client retries were denied locally, then turn interruption ended the run before a final CLI result was retained.
The maintained harness now uses the Anthropic root origin, an unconditional loopback child sandbox, process-scoped `CLAUDE_CODE_MAX_RETRIES=0`, and streamed private stdout/stderr capture.
It joins client request/response records by request ID and requires native Messages model, usage and terminal lifecycle before comparing text and actual fixture colors.
No unchanged provider rejection was rerun after these harness-only corrections.

The retained native GPT search profile with `tool_choice: required` already proves provider search and exact result/source/citation correlation for that distinct attempt.
Current original Codex requests use `auto`; no supported original Codex control producing `required` was found.
Its current automatic non-invocation remains a separate failure, pending a genuine changed client profile.

This revalidation added eight inference dispatches and three authentication/catalogue startup pairs.
The expanded physical ledger is 97 inference, 39 authentication and 35 catalogue dispatches, including failures.
The Claude URL carrier follow-up added one inference and two authentication/catalogue startup pairs.
One public fixture preparation fetch is counted separately.
The unchanged 18-cell matrix and existing attachment evidence remain valid because their requests do not activate the declared hosted-search barrier.
See [hosted-tool current acceptance and remaining inputs](server-tool-acceptance.md#current-acceptance-2026-10-11).

To repeat the offline audit without writing an audit record, point the two private replay-directory variables at the retained capture directories.

```sh
CPA_ATTACHMENT_CHAT_TRANSITION_REPLAY_GATE_DIR=/path/to/private-gate-directory \
  CPA_ATTACHMENT_CHAT_TRANSITION_REPLAY_CASE_DIR=/path/to/private-case-directory \
  go test ./integration -run '^TestAttachmentPacketHistoricalCodexPDFChatTransitionReplay$' -count=1 -v
```

Set `CPA_ATTACHMENT_CHAT_TRANSITION_WRITE_AUDIT=1` only to create a new separate audit result; the exclusive write refuses to overwrite an existing file.

Validate each model through all three client APIs on a separate, loopback-only CPA host.
Use existing Copilot credentials through the local credential resolver, without changing the operating proxy or global client settings.

| Model | Native Copilot endpoint | Incoming client APIs |
| --- | --- | --- |
| `gemini-3.8-flash` | `/chat/completions` | Chat, Responses, Messages |
| `gpt-6-luna` | `/responses` | Chat, Responses, Messages |
| `claude-haiku-5.5` | `/v1/messages` | Chat, Responses, Messages |

The incoming API selects the client's schema.
The model's configured native endpoint selects the provider schema.
The plugin converts requests and responses between these schemas through the existing translation layer.
An explicit endpoint override can test an exact model missing from discovery, but does not prove that the account supports it.

## Existing CPA conversion

The plugin uses CLIProxyAPI's `sdk/translator/builtin.Registry()` for request, response, and stream conversion.
The pinned SDK does not supply every required conversion pair.
The existing adapters fill those pairs and preserve Copilot reasoning carriers and tool identifiers.
Live validation uses that production path without a separate converter.

Client function tools, including web search and delegated-task functions, retain their names and input schemas through conversion.
Provider-owned native tools have a different execution contract.
The same endpoint representability check drives both optional native-tool filtering and translation preflight, so the selected endpoint cannot silently discard a declaration that preflight would accept.
Malformed declarations remain on the validation path.
The filter reports excluded optional tools through response metadata and headers.
Forced unsupported tools return an error instead of fabricated tool output.
Ordinary Claude system text remains supported.
The plugin preserves Copilot's structured `model_not_supported` rejection using a fixed message recognized by CPA.
CPA then applies its existing model-specific cooldown while other models retain the credential.

## Acceptance evidence

A successful matrix cell requires the exact upstream request model, a supported response identity, the client's schema, and assistant text.
Record HTTP failures separately from successful conversion.
Do not substitute another model to make a cell pass.

Claude Code and Codex client checks must use isolated settings and the same task-owned proxy.
Record web search invocation and completed results separately.
Record subagent invocation and completed delegated work separately.
A prompt requesting a tool does not establish that the tool ran.
Synthetic tests establish translation behavior; they do not establish live provider availability or completed client tools.

Remove temporary authentication files, client settings, and task processes after validation.
Retain requested debug evidence outside the repository, including failed requests.
Publish only sanitized summaries.

## Run the opt-in checks

Prepare the platform-specific plugin and CPA v8 host as described by the existing native-host build instructions.
Set `CPA_LIVE_COPILOT_AUTH_FILE` to an existing CPA Copilot authentication file to use that credential.
The checks copy its storage into a temporary host and leave the source file unchanged.
File credentials use `token_exchange` by default.
Without a file, the checks use the Copilot CLI Keychain credential on macOS with `direct_oauth`.
They require explicit environment flags and do not run during ordinary tests or CI.

```sh
CPA_BINARY=/path/to/prepared/cli-proxy-api \
  CPA_LIVE_COPILOT_AUTH_FILE=/path/to/copilot-auth.json \
  CPA_LIVE_COPILOT_DEBUG_DIR=/path/to/private-debug-directory \
  CPA_LIVE_COPILOT_MATRIX=1 \
  go test ./integration -run '^TestLiveCopilotProtocolMatrix$' -count=1 -v

CPA_BINARY=/path/to/prepared/cli-proxy-api \
  CPA_LIVE_COPILOT_AUTH_FILE=/path/to/copilot-auth.json \
  CPA_LIVE_COPILOT_DEBUG_DIR=/path/to/private-debug-directory \
  CPA_LIVE_COPILOT_CLIENTS=1 \
  go test ./integration -run '^TestLiveCLIClients$' -count=1 -v

CPA_BINARY=/path/to/prepared/cli-proxy-api \
  CPA_LIVE_COPILOT_AUTH_FILE=/path/to/copilot-auth.json \
  CPA_LIVE_COPILOT_DEBUG_DIR=/path/to/private-debug-directory \
  CPA_LIVE_COPILOT_INCOMPLETE=1 \
  go test ./integration -run '^TestLiveCopilotIncompleteResponses$' -count=1 -v
```

The matrix pins the three native routes listed above, including when discovery advertises multiple endpoints.
It runs nine non-streaming cells and streams only cells whose baseline passes.
The CLI checks allow web search and delegation, then require completed tool events rather than requested tool names.
Each CLI's failed baseline prevents subsequent tool requests for that client.
Debug evidence includes original client and upstream request/response bodies and CLI output.
The checks exclude authentication material and retain evidence in private directories even when validation fails.
Regression fixtures preserve captured JSON fields and stream events, replacing only private or changing values.

### Latest dependency and original-client reruns

Prepare the matched CPA host and plugin, then set these portable environment variables.
Run the commands from the plugin repository root.
The host binary must be built with the versioned replacement shown above and the Go 1.27.2 toolchain used for these results.

```sh
export CPA_BINARY=/path/to/prepared/cli-proxy-api
export CPA_LIVE_COPILOT_AUTH_FILE=/path/to/copilot-auth.json
export CPA_LIVE_COPILOT_AUTH_MODE=token_exchange
export CPA_LIVE_COPILOT_DEBUG_DIR=/path/to/private-debug-directory
```

These common settings provide the prepared CPA binary, copied `token_exchange` auth, and private diagnostics directory required by all three opt-in checks below.

Run the latest captured dependency matrix with all nine model/API pairs in JSON and SSE.

```sh
CPA_LIVE_DEPENDENCY_MATRIX=1 \
  go test ./integration -run '^TestLiveDependencyProtocolMatrix$' -count=1 -v
```

Run only the affected Codex baseline, native search, and real delegation checks after a host schema fix.
The captured-client test resolves `claude` and `codex` through `PATH`; use Claude Code 2.1.296 and Codex CLI 0.162.1 to reproduce the recorded results.

```sh
CPA_LIVE_CAPTURED_CLIENTS=1 \
  CPA_LIVE_CAPTURED_CLIENTS_ATTEMPT=fix-native-reserved-schema \
  CPA_LIVE_CAPTURED_CLIENTS_CASES=codex \
  go test ./integration -run '^TestLiveCapturedCLIClients$' -count=1 -v
```

The captured-client attempt defaults to `latest-dependencies` and also accepts `fix-native-reserved-schema`.
The client selector defaults to both clients when unset and accepts `claude`, `codex`, or `claude,codex` in either client order.
Invalid, duplicate, empty, and malformed selections fail before the host or catalog starts.
Selecting `codex` reruns only Codex and leaves the prior Claude result untouched.

The original Codex interrupt test requires the verified Codex 0.162.1 app-server inputs as private files.
Copy the real client binary, exact model catalog, loopback-restricted sandbox profile, and app-server driver into private locations.
Place the driver's real `codex_interrupt_probe.py` helper beside it with mode 0600.
The test does not generate, fabricate, or bundle an app-server driver.
The driver and helper must have mode 0600, and the existing Python `websockets` dependency must be installed.

```sh
CPA_LIVE_ORIGINAL_INTERRUPT=1 \
  CPA_LIVE_ORIGINAL_INTERRUPT_ATTEMPT=fix-relay-origin \
  CPA_LIVE_CODEX_EXPECTED_VERSION=0.162.1 \
  CPA_LIVE_CODEX_BINARY=/path/to/private/codex-binary \
  CPA_LIVE_CODEX_CATALOG=/path/to/private/codex-model-catalog.json \
  CPA_LIVE_CODEX_SANDBOX=/path/to/private/loopback-sandbox-profile \
  CPA_LIVE_CODEX_INTERRUPT_DRIVER=/path/to/private/codex_interrupt_live_driver.py \
  go test ./integration -run '^TestLiveOriginalCodexInterrupt$' -count=1 -v
```

The interrupt attempt defaults to `latest-dependencies` and also accepts `fix-relay-origin`.
Each attempt creates a fresh captured host and claim ledger.
Keep debug directories outside the repository and publish only sanitized summaries.

## Historical fresh CPA authentication result (2026-10-09)

The follow-up validation on 2026-10-09 used a completed CPA Copilot OAuth file with `token_exchange`.
Discovery returned 59 models, including all three exact target IDs and their assigned native endpoints.

| Model | Chat | Responses | Messages |
| --- | --- | --- | --- |
| `gemini-3.8-flash` | PASS | PASS | PASS |
| `gpt-6-luna` | PASS | PASS | PASS |
| `claude-haiku-5.5` | PASS | PASS | PASS |

Both non-streaming and streaming responses passed for these nine combinations: 18 successful requests.
The retained CPA logs confirm all 18 exact outbound model names and native endpoint selections.
Unknown-model rejection and disabled compaction also passed.

Copilot reports native Claude replies as `claude-haiku-5-5` after receiving `claude-haiku-5.5` in the request.
The matrix accepts this observed response spelling for that model only, while preserving the exact request ID.
The plugin leaves the native Claude model field unchanged.

The native Gemini Chat response omitted the `object` field in JSON and streaming chunks.
The plugin now supplies the Chat protocol discriminator while preserving the remaining response fields.
The stream validator accepts the observed final usage-only Chat chunk with empty `choices`.
The Messages fixture uses a 512-token budget because the earlier 64-token budget truncated Gemini's answer.

Claude Code 2.1.294 and Codex CLI 0.161.0 both passed their baseline and completed delegated child work through the proxy.
Claude ran its Agent tool and returned the child answer.
Codex's V2 delegation records its spawn in a persisted rollout, while stdout leaves the wait event's child state empty.
The acceptance check verifies the actual parent spawn/result, linked child session, completed child turn, successful wait, and received child handback.
It rejects assistant claims without those events.

Claude invoked WebSearch, but Copilot rejected the native tool with HTTP 400:

```json
{"error":{"message":"The use of the web search tool is not supported.","code":"unsupported_value"}}
```

The plugin preserves this known rejection as a structured client error without exposing arbitrary upstream error text.
This is an invoked but unsupported search, not a completed search.

| Client | Baseline | Web search | Subagent |
| --- | --- | --- | --- |
| Claude Code 2.1.294 | PASS | Invoked; unsupported HTTP 400 | PASS |
| Codex CLI 0.161.0 | PASS | PASS | PASS |

The final affected search rerun passed Codex's strict started/completed ID correlation and returned a source URL.
Both client baselines passed again with the rebuilt plugin.
The aggregate search command exited with status 1 because Claude's unsupported native search remains a failed acceptance case.

Codex's isolated profile copies its official model catalogue entry and changes only `use_responses_lite` and `tool_mode` to enable the full Responses API and direct tools.
The temporary client home supplies the catalogue through the supported `model_catalog_json` setting.
The isolated Claude invocation enables WebSearch and Agent explicitly.

Native Copilot hosted search changes its opaque item ID between stream phases and in the completed snapshot.
That earlier repair buffered from the first hosted search event until a valid terminal snapshot, bounded at 8 MiB.
The current repair above starts at the first response event for declared native search and reconciles all output-index identities.
It retains the final snapshot and replay data, and uses its native ID for earlier search lifecycle events.
Later text and tool events wait behind this buffer.
Valid `response.incomplete` snapshots retain their partial output, reason, usage, and actual search status.
Missing terminal snapshots or inconsistent search lifecycle data return an error.

## Review regression checks

The follow-up review identified five cases covered by regression tests:

- OAuth rotation followed by a failed account lookup retains the new token pair in pending storage. The host retries verification after restart; only a verified account can activate the pair, and the previous refresh preference is restored afterward.
- Legacy replay migration retains the old credential fingerprint and account/auth binding when endpoint discovery is temporarily unavailable. Existing capsule authentication must prove the original endpoint; endpoint, account, or auth-ID changes cannot replay the artifact.
- Native incomplete Responses streams retain the terminal event and partial output without caching a successful replay. Chat and Messages map supported token-limit/content-filter reasons through their existing finalization paths. The native host test covers all three client schemas.
- Native tool declarations that the pinned SDK cannot represent use the existing exclusion metadata and headers. Forced excluded tools fail explicitly; supported function, custom, namespace, Claude web search, and local Chat shell declarations retain their existing mappings.
- Reasoning-only assistant carriers and mixed text/tool turns preserve reasoning before visible output at the original conversation position, using the existing SDK conversion path and tool identifiers.

The affected live token-limit probe returned `response.incomplete` with `max_output_tokens`, 24 input tokens, and 16 output tokens, all used for reasoning.
Its four captured request/response bodies retain the original fields and five stream events in sanitized regression fixtures.
HTTP/SSE native Responses, Chat, and Messages partial termination is covered; incomplete tool arguments that cannot be represented by the destination fail explicitly.

Regression fixtures include the corresponding client request, upstream request, upstream response, and client response bodies.
Private values use placeholders while the protocol fields and events remain intact.
CPA's formatted SSE logs required reassembly at transport chunk boundaries; one lost instruction space was restored from the paired client response.
Fixture notices record this reconstruction. Original diagnostics remain private and unchanged.

## Historical CPA v8.0.23 compatibility follow-up

This earlier follow-up predates the current attachment and late-interrupt fixes above.
At that point, the host and SDK were pinned to v8.0.23. Existing CPA handling now cancels an active plugin HTTP stream on `response.interrupt`, emits `response.incomplete` with reason `interrupted`, and permits the next request on the same client WebSocket. Native Codex WebSocket sessions forward the original interrupt body unchanged and retain continuation identity. These paths have required synthetic native-host regressions. After the completed plugin HTTP forwarder has exited, a late interrupt still returns HTTP 400; the native Codex completed-session handling does not extend to the plugin executor.

Codex Responses requests can declare optional `web_search` with `external_web_access: false`. When the selected endpoint is Claude Messages, the existing native-tool exclusion mechanism now removes this unrepresentable declaration and discloses it in `X-Copilot-Excluded-Native-Tools` and response metadata. Ordinary function tools remain intact. Forced selection of the excluded tool fails before inference. This does not implement cached-only search or change it to live web search.

Original Codex 0.161.0 PNG and PDF requests were rerun with full private body captures. PNG still fails before inference because its `detail: high` has no verified lossless Claude Messages mapping. The PDF initial request now reaches the provider and returns an actual rendering function call; The existing strict cancellation proof now permits continuation after a fully forwarded function-call terminal while retaining the read-cancellation record. A subsequent original renderer attempt failed because nested Seatbelt could not launch shell commands. An alternate isolated `danger-full-access` attempt avoided nesting but changed the generated search declaration to `external_web_access=true`, even with an explicit cached-search setting; Copilot refused it with HTTP 400. Those failures and every physical call remain retained. The maintained packet keeps the original `workspace-write` profile and renderer/view tools. The installed CLI rejects an external-sandbox mode; the distinct app-server policy was not substituted for the original PDF CLI route. Full PDF rendering and attachment completion remain unverified. An isolated original app-server run through the real proxy also confirms that `turn/interrupt` ends an active turn and permits another turn in the same thread, but this client version closes its WebSocket without emitting `response.interrupt`. Synthetic wire-control coverage must not be presented as an original-client interrupt frame.

Cross-format image detail that Claude Messages cannot preserve returns a local HTTP 422 with code `unsupported_image_detail` and includes the destination endpoint and requested detail. The request retains its original image bytes; the adapter does not downgrade the detail to `auto`.

## Historical WebSocket interrupt limitation

The exact Codex `response.interrupt` frame is rejected by CPA before it reaches the plugin.
Native diagnostics reproduce this for both plugin normalization and active upstream WebSocket duplex processing.
The examined CPA v8.0.15 and v8.0.20 handlers do not implement this control message, and the plugin executor has no downstream WebSocket control hook.
The error is therefore not resolved by this plugin change.
The pinned host's Responses WebSocket forwarder also treats `response.incomplete` as an unfinished stream: it forwards the event, logs a completion error, and closes the connection instead of preserving continuation.
Incomplete response support in this validation applies to HTTP/SSE; the plugin does not fabricate a `response.completed` event to satisfy the host's WebSocket completion check.

The isolated Codex profile uses supported settings to select HTTP transport and disable immediate interruption:

```toml
[features]
instant_interrupt = false

[model_providers.copilot_proxy]
supports_websockets = false
```

Use the actual custom provider name when applying the transport setting.
No operating proxy or global client configuration was changed during validation.

## Historical earlier Keychain result

The macOS validation on 2026-10-09 used the authenticated Copilot CLI account with `direct_oauth`.
Discovery returned eight model entries and none of the three exact target IDs.
The isolated host registered all three explicit native-route overrides before the measured matrix started.

| Model | Chat | Responses | Messages |
| --- | --- | --- | --- |
| `gemini-3.8-flash` | FAIL: HTTP 400 | FAIL: HTTP 400 | FAIL: HTTP 400 |
| `gpt-6-luna` | FAIL: HTTP 400 | FAIL: HTTP 400 | FAIL: HTTP 400 |
| `claude-haiku-5.5` | FAIL: HTTP 400 | FAIL: HTTP 400 | FAIL: HTTP 400 |

These nine measured client requests returned the public `invalid_request_error` type.
No baseline produced a successful assistant response, so streaming follow-ups were NOTRUN.
Unknown-model rejection passed, and disabled compaction returned HTTP 422.

Three bounded provider-service diagnostic requests identified `model` as the rejected parameter for each native route.
After request/response body inspection was authorized, three direct native requests returned this same body:

```json
{
  "error": {
    "message": "The requested model is not supported.",
    "code": "model_not_supported",
    "param": "model",
    "type": "invalid_request_error"
  }
}
```

These native requests reject the exact model IDs under the tested credential profile.
They do not establish global model availability.
No aliases or retries were used for these six diagnostic requests.

| Client | Baseline model | Baseline | Web search | Subagent |
| --- | --- | --- | --- | --- |
| Claude Code 2.1.294 | `claude-haiku-5.5` | FAIL: HTTP 400 | NOTRUN | NOTRUN |
| Codex CLI 0.161.0 | `gpt-6-luna` | FAIL: HTTP 400 | NOTRUN | NOTRUN |

Each CLI completed one bounded baseline run and exited with status 1.
Neither emitted a completed assistant answer or a tool call.
Web-search and delegation were not exercised under this earlier credential profile; fresh CPA authentication results are recorded above.
The failed live suites retain their failing exit status.

Ordinary Go tests and the CI-selected synthetic native-host suite passed locally.
A native cooldown regression confirms that a repeated unsupported-model request makes only one upstream call and another model remains usable.
A broader supplemental native suite exposed an existing `TestNativeHostResponsesWebsocketCompactionReplay` failure involving instruction preservation.
That supplemental failure remains outside this validation unit.
Temporary client homes, authentication files, host configuration, and host processes were removed after the live checks.

The follow-up recorded 11 additional inference, six authentication and six catalogue dispatches, bringing expanded totals to 38, 20 and 16. One alternate-profile retry occurred before the intended cached-setting edit matched the generated config; its repeated 400 and a separate private correction record are retained. These are measured counts, not limits.

## Loading an existing live artifact in place

Set `CPA_LIVE_EXISTING_PLUGIN_DIR` to an absolute, flat directory containing the existing regular `cliproxyapi-copilot` platform library.
The live host loads this directory directly and does not stage, copy or install that artifact.
If `CPA_LIVE_PLUGIN_PATH` is also supplied, it must identify the same artifact.
Unexpected native libraries, symlinks and nested directories are rejected before host startup.
A directly built artifact directory may also contain the generated C header.

`native-stream-revalidation` is a distinct original CLI/interrupt attempt so frozen historical outcomes are preserved.
The original CLI runner has no inference deadline; cleanup still stops its owned process group.
The HTTPS-image case is opt-in with `CPA_LIVE_ORIGINAL_HTTPS_IMAGE=1` and requires a private original app-server driver, declared installed version and prepared HTTPS source.
[The maintained original driver](../integration/original_codex_image.py) uses only the installed app-server JSON-RPC interface.
Copy this source into the owned private execution directory with mode 0600 and set `CPA_LIVE_CODEX_IMAGE_DRIVER` to that file.
The driver reads the declared original binary, catalogue and loopback sandbox from the existing live environment variables.
Its acceptance checks the actual RPC input and answer, original POST route, unchanged URL/detail/model/effort, complete public and client lifecycles, terminal text/usage, and exact image colors.
Client rejection, missing ingress and transport or semantic failure remain failures.

The original Claude URL-only case is opt-in with `CPA_LIVE_ORIGINAL_CLAUDE_HTTPS_IMAGE=1` and the existing packet environment.
Set `CPA_LIVE_CLAUDE_BINARY`, `CPA_LIVE_CLAUDE_EXPECTED_VERSION`, and `CPA_LIVE_IMAGE_URL` to the existing original client and prepared public source.
Run `go test ./integration -run '^TestLiveOriginalClaudeHTTPSImage$' -count=1 -v -timeout=0`.
The child environment omits the retry watchdog and sets its retry count to zero; proxy retries remain independently governed by the existing host configuration.
The test supplies a genuine newline-delimited user message on stdin and disables client tools, so neither the driver nor a Read tool prepares a substitute image.
A rejected URL is retained as a failed profile, never an image pass.
