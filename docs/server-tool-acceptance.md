# Hosted server tools and destination-native requests

Expanded acceptance is tracked in [issue #5](https://github.com/ririnto/cpa-copilot-bridge/issues/5).
The earlier [live validation](live-copilot-validation.md) remains complete within its documented scope.
Its 18 JSON/SSE inference cells do not establish hosted tools or complete native-client request parity.
Unsupported responses, excluded declarations, client-executed functions, and unrun cases do not satisfy this acceptance.

## Current acceptance (2026-10-11)

The latest matched host and dependency checks are described in [current live validation](live-copilot-validation.md#current-validation-2026-10-11).
The corrected model-registry startup and five native JSON probes completed.
The earlier startup race and NOTRUN snapshot below are historical.

| Native follow-up | Current outcome |
| --- | --- |
| Claude Bash execution SSE | PASS: actual paired execution result, stdout `385`, successful terminal and clean EOF. |
| Claude exact-history continuation | PASS: actual preceding content reaches Messages unchanged and the answer reuses `385` without another tool call. |
| GPT search SSE before repair | FAIL: native response, reasoning and message identities change between phases. |
| GPT search SSE after repair | Identities pass; exact result/citation URL pairing fails because the annotation adds a tracking query absent from result URLs. |
| GPT captured-terminal continuation | PASS: one real Responses request replays the original complete terminal output and ciphertext unchanged and answers without another search. |

## Current conversion support and guards

The completion audit and [denied live-access record](live-acceptance-status.md) separate current source validation from fresh provider acceptance, which is NOT RUN after the sealed request was not granted.

[PR #36](https://github.com/ririnto/cpa-copilot-bridge/pull/36) completes the representable Claude-origin hosted-search round trip into Responses, retaining completed result ownership and history. This is conversion support; it does not change the recorded live Haiku WebSearch refusal or establish that the provider executes that tool.

[PR #41](https://github.com/ririnto/cpa-copilot-bridge/pull/41) preserves strict Chat request semantics and refuses hosted state before dispatch when the destination cannot retain it. Complete hosted search from Chat to Responses remains explicitly unsupported until a lossless destination carrier exists. Claude Bash to Responses is also explicitly unsupported because no lossless equivalent execution/history contract is established.

[PR #44](https://github.com/ririnto/cpa-copilot-bridge/pull/44) preserves standard Responses URL citations in Chat JSON, including exact text, URL/title and composed character positions. A standard streaming annotation carrier is not established. These bridge implementation states do not upgrade any provider-side acceptance result.

The successful GPT continuation uses the original stream's identical public/client completed terminal.
That terminal contains meaningful search results and an exactly matching source citation.
Offline conversion repairs the earlier identities before validating the retained history.
The separate eligibility record marks `native_stream_accepted: false` and `offline_identity_replay: true`.
Neither failed stream verdict is overwritten or counted as a live streaming pass.
The changed-identity stream's distinct citation-pairing failure remains open.
No tracking query or result content is normalized away to make it pass.

Declared native Responses search now holds the complete lifecycle behind the existing 8 MiB barrier.
The terminal owns response and output-index identities, including reasoning and message items.
Earlier events use those identities while terminal bytes, ciphertext, call IDs, results and citations remain unchanged.
Other native Responses streams retain their existing streaming behavior.
Hosted search therefore delays client output until the terminal snapshot arrives.

Declared search also retains the barrier when `tool_choice: auto` produces no search invocation.
The barrier fails closed at its size limit, with no partially repaired frames released.
Matched-host regressions cover terminal-remapped interrupt identities, a late interrupt during the next active turn, and cancellation before buffered completion.
Separate translator regressions cover successful no-invocation release and overflow without emitted frames.
These tests establish bridge behavior with synthetic fixtures; they do not establish original-client control emission or provider capability.

Five complete actual request/response pairs are retained in [portable regression fixtures](../internal/translate/testdata/native-hosted-tool-stream-captures/README.md).
Synthetic lifecycle mutations and native-host tests establish regression behavior separately from live provider capability.
This follow-up made five inference dispatches and five authentication/catalogue startup pairs, including the continuation-only audit stopped before inference.
A later current-stream original Codex revalidation added eight inference dispatches and three authentication/catalogue startup pairs.
The expanded ledger now records 97 inference, 39 authentication and 35 catalogue dispatches.
A separate original Claude URL carrier follow-up added one inference and two authentication/catalogue startup pairs.
The separately counted public PNG preparation fetch is not inference.
These counts are observations, not limits.

The latest original-client attachments pass for Codex-to-Haiku PNG/PDF and Claude-to-GPT PDF.
Claude-to-GPT PNG remains a semantic failure despite preserved bytes and successful transport.
Current-stream original Codex baseline, real child delegation/wait and interruption/same-thread continuation pass.
The original client still emitted zero `response.interrupt` and `response.incomplete` frames.
The same-thread follow-up used a new WebSocket with `previous_response_id: null`; it does not prove Responses-chain continuation.
The current original native-search request includes the actual hosted declaration, but the model reports the tool unavailable without invoking it; this case fails despite HTTP 200 and a completed client turn.
Its earlier PASS remains historical and does not overwrite the current failure.
The original app-server HTTPS-image request is now a captured client rejection: JSON-RPC `-32600`, remote image URLs unsupported.
It reaches neither CPA inference ingress nor Copilot inference, so that Codex carrier does not establish provider URL-reference capability.
A distinct original Claude structured-stdin URL carrier reached exact Haiku native Messages with its image block unchanged.
Copilot returned HTTP 400 `external image URLs are not supported`; the client wire retained the corresponding 400 error.
This is a URL-only profile rejection, since the original Messages URL source has no independent MIME/detail fields.
The first duplicate-path harness 404 made zero inference; the corrected attempt made one, with later automatic retries denied locally before turn interruption.
A complete final CLI result was not observed. The retained provider rejection is not retried unchanged.
The maintained harness corrects the root origin, always applies the child sandbox, disables client retries, streams private output captures and validates joined client lifecycles.
Required synthetic wire tests cover active and late control handling separately.

The remaining citation and PNG answer failures are present in the actual provider bodies before bridge conversion.
The tracked citation appears in action sources but not in meaningful search results; native stream repair preserves its completed terminal unchanged.
The latest original Claude-to-GPT PNG carries identical prepared and public image bytes, and the wrong solid-blue provider answer equals the delivered client text.
The historical Read-cycle capture instead answers both red, while the native GPT capture answers both blue; those are distinct failed attempts.
No bridge-side output correction is justified by these captures.
The original HTTPS harness did have a separate prompt/oracle defect: it asked for left/right colors on a red-top, blue-bottom fixture.
The maintained prompt now uses top/bottom and its regression checks the actual PNG geometry.
The Codex client rejection remains immutable. The separate Claude URL-only profile is now a provider rejection; broader URL/MIME/detail acceptance remains unmet.

## Remaining inputs and owners

Main owns the remaining acceptance in [issue #5](https://github.com/ririnto/cpa-copilot-bridge/issues/5).
Unchanged rejected profiles are retained without another speculative provider retry.

| Remaining acceptance | Owner and next required input |
| --- | --- |
| Cross-format Chat hosted search and Claude Bash-to-Responses | Chat hosted state is refused before dispatch; support requires a lossless destination carrier and genuine result ownership/history. Bash-to-Responses remains unsupported without a lossless execution/history contract. |
| Gemini search, Claude WebSearch/Web Fetch, GPT interpreter | Copilot capability owner must provide changed capability/profile evidence for the exact model and rejected declaration. |
| Image generation, file search, hosted MCP and tool search | Main needs advertised exact-model capability and genuine files, servers or tool registry resources. |
| Original HTTPS image references and remote uploads | Codex rejects remote URLs before inference; genuine Claude URL-only stdin reaches Haiku Messages but Copilot rejects external URLs. Main needs a changed supported exact-model native profile preserving the required carrier fields. Uploads need authentic provider-owned issuance and lookup. |
| Audio, video and office attachments | Main needs a supported original-client preparer and exact-model capability evidence. |
| GPT PNG and tracked citation pairing | Copilot model behavior must produce the correct image content or exactly correlated result citation under a substantively changed case. |
| Original Codex search non-invocation | A distinct retained native GPT `tool_choice: required` provider profile already passes. The original-client owner must provide a genuine Codex control or request that emits a changed supported profile; all current original requests use `auto`. |
| Identity-neutral native HTTP headers | Main needs ownership release for the preserved provider WIP before changing its streaming `Accept` behavior. |
| First-party Copilot session and executable-tool parity | Main needs an explicit authentic identity policy and genuine ACP/tool context unavailable from Claude/Codex inbound headers. |
| Original-client interrupt wire emission | The original Codex client must emit the control frame before live wire acceptance can be established. |
| Original token-limit incomplete continuation | The original-client owner must supply a genuine incomplete response and a supported client path that retains/resumes it; current Codex treats token-limit incomplete as a stream error and exposes no deterministic token-cap input. |

The current catalogue advertises image/PDF support without establishing all other modalities as globally unsupported.
Native Copilot CLI captures use `application/json` for streamed Chat/Responses and `*/*` for Messages.
The plugin's current streamed `Accept: text/event-stream` is a recorded mismatch.
The five existing provider WIP files remain unchanged and excluded from publication.
Full HTTP/2, TLS framing and native first-party identity parity remain unverified.

## Assigned destinations

| Exact model | Destination | Existing hosted-tool evidence |
| --- | --- | --- |
| `gemini-3.8-flash` | Chat | Search returned HTTP 200 with an empty assistant message and `finish_reason: error`. |
| `gpt-6-luna` | Responses | Hosted search completed with actual results, action sources and a matching assistant citation. |
| `claude-haiku-5.5` | Messages | Code execution completed with a paired bash result; WebSearch and Web Fetch were refused. |

The earlier GPT search capture contains only query and citation evidence.
A later isolated probe requested both `web_search_call.results` and `web_search_call.action.sources` and received nonempty search snippets, source URLs and a citation to the same NASA result.
This proves that native JSON search capability for the tested request, without establishing translated streaming or continuation.
Public API documentation describes schemas and does not prove capabilities of an assigned Copilot endpoint.
Existing configured aliases may resolve to the assigned exact upstream model without changing that model.
No model substitution, invented alias, hidden reroute, fabricated result, or invented provider replay token is permitted.

## Acceptance dimensions

Each supported family requires preserved declarations, selection, request options, provider call identity, actual results, and content.
JSON and SSE must retain relevant citations, usage, event ordering, terminal outcomes, and errors.
Continuation must retain genuine history, paired results, replay tokens, and ownership relationships.
Provider-internal tool failures inside successful HTTP responses remain distinct from HTTP-level refusal.
A valid incomplete response must not become a fabricated completed response.

The known conversion gaps include Chat search options, hosted declarations, hosted output, and result-only continuation.
Messages server calls must retain provider execution semantics when translated to Responses.
They must not become executable client function calls.
Responses search must retain meaningful result content and identity when translated to Messages or Chat.
The existing SDK conversion routes require guards against empty synthesized results and invented identifiers.

## Request parity

Actual installed Copilot CLI default-provider captures govern Copilot upstream HTTP headers and body behavior.
BYOK requests and VS Code source alone do not establish that baseline.
Destination-client behavior remains an additional comparison for incoming Codex and Claude requests.
Compare the same assigned provider, exact model, and user task against genuine current native-client baselines.
Assess method, scheme, authority, path/query, header semantics, required values, body schema/content, encoding, and streaming separately.
Preserve ordered instructions, executable tool schemas, selection, parallelism, effort, output limits, structured output, and continuation state.
Provider-native preparation applies by destination even when the incoming client uses another protocol.
Copied destination tool names require actual adapters to tools available in the incoming client.

Random identifiers and timestamps may differ across independent runs.
Session, thread, cache, call, and replay identifiers require equivalent binding, lifetime, and equality relationships.
Instruction content, tool behavior, header presence, effort, limits, and encoding choices cannot be normalized away.
Credentials remain local and excluded from published evidence.
A successful status or matching protocol label alone does not establish parity.

## Reference scope

The pinned CLIProxyAPI translator registry supplies reusable conversion routes.
Its native Claude and Codex executor preparation resides in internal packages unavailable through the external plugin SDK.
Those historical paths provide implementation guidance without authorizing direct Anthropic or ChatGPT credentials on Copilot requests.
See [Claude preparation](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.15/internal/runtime/executor/claude_executor_request.go) and [Codex preparation](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.15/internal/runtime/executor/codex_executor_request.go).

[OpenCodex routing](https://github.com/lidge-jun/opencodex/pull/3866) assigns GPT Responses destinations independently of incoming format.
Its [bearer/origin snapshot change](https://github.com/lidge-jun/opencodex/pull/2841) reinforces credential-bound dispatch.
Its [Responses repair](https://github.com/lidge-jun/opencodex/blob/main/src/server/github-copilot-responses-repair.ts) corroborates provider identifier and completion concerns.
Existing bridge captures remain the authority for its observed Copilot behavior.

VS Code supplies maintained Copilot proxy request construction as provisional implementation guidance.
Any provider header changes derived from this source require reconciliation with actual installed Copilot CLI captures.
Its [Claude proxy](https://github.com/microsoft/vscode/blob/main/src/vs/platform/agentHost/node/claude/claudeProxyService.ts) forwards version, supported beta families, and transformed user-agent semantics.
Its [Copilot service](https://github.com/microsoft/vscode/blob/main/src/vs/platform/agentHost/node/shared/copilotApiService.ts) distinguishes Messages proxy intent from Responses conversation intent.
Its [initiator correction](https://github.com/microsoft/vscode/pull/320343) omits origin classification when the genuine turn origin is unavailable.
Its [Codex proxy](https://github.com/microsoft/vscode/blob/main/src/vs/platform/agentHost/node/codex/codexProxyService.ts) does not justify forwarding every inbound header.

The supplied Codex, ZCode, Copilot CLI, GitHub App, and VS Code repositories were inspected read-only.
Codex [request construction](https://github.com/openai/codex/blob/main/codex-rs/core/src/client.rs) informs current body and stream comparisons.
Its [compression discussion](https://github.com/openai/codex/issues/41662) distinguishes official authenticated-provider compression from custom-provider behavior.
Copilot CLI and App checkouts contain release/documentation material rather than authoritative inference request implementation.
They cannot supply missing wire-level baselines.

The bounded discussion inventory covered CLIProxyAPI issues/PRs 5871, 5661, 5735, 6392, 3837, 4094, and 3670.
It covered OpenCodex issues/PRs 3866, 2841, 1110, 1111, and 6472.
It covered VS Code 320343 and 298683, Codex 41662, Copilot CLI 4594, and Copilot App 2733.
The [unmerged Copilot proposal](https://github.com/router-for-me/CLIProxyAPI/pull/5661) and [unmerged search proposal](https://github.com/router-for-me/CLIProxyAPI/pull/3837) are historical guidance, not shipped support.
The [open forwarding proposal](https://github.com/router-for-me/CLIProxyAPI/pull/4094) does not authorize hidden model rerouting.
The [HTTP continuation discussion](https://github.com/router-for-me/CLIProxyAPI/issues/5735) and [related limitation](https://github.com/router-for-me/CLIProxyAPI/issues/6392) remain relevant host boundaries.

## Attachments

Representative inputs are a valid PNG and a small PDF with verifiable content.
Each attachment follows the original client's preparation, file-reading tools, and follow-up requests.
Native Claude sends inline PDF content, while native GPT and Gemini initially send local tagged-file references.
Forcing an inline PDF would not validate the latter clients' original paths.
Equivalent protocol declarations must retain identical decoded bytes, MIME, reference semantics, and representable metadata.
Native Copilot captures establish which declarations and exact models accept these inputs.
Synthetic converter tests, sanitized captured fixtures, and real provider acceptance are reported separately.
Uploads, remote URLs, office files, audio, and video remain unproven without applicable captures and real calls.
JSON/SSE responses and continuations must retain relevant attachment content, results, citations, and provider-owned references.
A rejection, dropped part, or unrun case keeps the corresponding acceptance open.
The representative cases do not establish a complete attachment matrix.

## Execution accounting

Physical authentication, catalogue and inference requests are counted separately for truthful evidence.
There is no blanket numeric ceiling, phase allocation limit or one-call-per-case restriction.
Use sufficient distinct cases and evidence-based revalidation for the required behavior.
Reuse unchanged accepted evidence without skipping missing acceptance.
Every dispatch needs an explicit prepared case and durable accounting before egress.
Failures retain private bodies and sanitized HTTP metadata and remain distinct from unrun cases.
No automatic model fallback, fabricated result or unbounded retry is permitted.

| Evidence group | Scope |
| --- | --- |
| A | Original Codex and Claude search and completed client delegation. |
| B | Native-endpoint hosted search, fetch and execution capabilities. |
| C | Native PNG/PDF representatives and evidence-supported translated tool continuation. |
| D | Codex incoming Claude and Claude incoming GPT search parity. |
| E | Changed-evidence verification and applicable cross-protocol attachment checks. |
| F | Original GPT/Gemini PDF preparation, viewing, follow-up and cross-client paths. |

The initial capability candidates are Gemini Chat search, GPT Responses source-bearing search, Claude fetch, Claude code execution and GPT code interpreter.
Only genuine successful native execution permits its translated-family claims.
Additional supported families require recorded declarations, actual results and continuation evidence before cross-protocol support is claimed.
Image generation, file search, hosted MCP and tool search remain unknown or unrun without applicable capabilities and real resources.
Client-executed shell, computer and ordinary functions are tracked separately.

The verified retained historical native-host logs contain fifty-six physical inference requests.
They contain twenty-four GPT Responses calls, twenty-two Claude Messages calls and ten Gemini Chat calls.
This is not a complete historical total and excludes authentication, catalogue and other direct captures.
Explicit local catalogue replay must preserve genuine captured bytes and verify credential, origin, client-profile and response-hash bindings.
Replay is recorded separately and does not establish a fresh public response.
Normal SDK discovery and known-disabled policy remain required.
Fresh outcomes, physical counts and unresolved cases must be recorded before expanded acceptance is closed.

## Historical native evidence snapshot

Twelve native inference requests completed before the later isolated plugin startup.
The later corrected startup, five native capability probes and original-client attachment attempts bring observed totals to twenty-seven inference requests, fourteen authentication dispatches and ten catalogue dispatches.
The authentication count includes one accidental credential-free test request that returned HTTP 404.
Subsequent offline tests enforce loopback-only isolation.
Native Claude PNG and PDF content assertions passed, as did native Gemini PNG content.
Native GPT PNG returned HTTP 200 with unchanged input bytes but failed the visual-content assertion.
The initial GPT and Gemini PDF runs prohibited tools and therefore establish only tagged-file preparation.
They do not establish PDF support failure or completion of the original file-reading path.
Two further GPT requests establish the native `view` call, paired PDF result, and completed marker answer.
Three further Gemini requests establish the same path and a completed same-session user-turn continuation.
The native reader returned the original PDF file text without a custom extraction step.
These bounded runs expose the original native `view` tool and preserve native temporary-file preparation.
GPT's extra user turn uses native ACP `session/load` of the retained original session.
Its single text-only request preserves the original `view` call, paired PDF output, and prior assistant answer.
The new user question contains no marker, and the new completed model response returns the marker without another tool call.
The original session state remains unchanged.
Six additional request/SSE fixtures preserve these real call identities, file results, and histories after sanitization.
This evidence does not establish arbitrary PDF interpretation or cross-client attachment parity.
HTTP/1.1 no-replay gate checks passed, without establishing full HTTP/2 or TLS-framing parity.
Actual native profiles differ from the provisional provider-header change, which remains unpublished pending reconciliation.

## Cross-format server-tool guards

Claude native tool declarations must not become ordinary client functions when targeting Responses or Chat.
Only the existing untyped, `custom`, and `function` client declarations enter that conversion path.
Other typed declarations use the existing exclusion disclosure, and forced excluded selections fail before inference.
Native Messages declarations retain their original behavior.
Ordinary client functions with server-tool names retain their client execution semantics.

Incoming Claude `server_tool_use` history cannot use the ordinary function-call adapter.
[PR #15](https://github.com/ririnto/cpa-copilot-bridge/pull/15) adds completed Claude-compatible hosted-search history from Responses to destination-native Messages, preserving tool ownership and complete history for supported shapes.
Completed Claude WebSearch call/result/history input targeting Responses is supported by PR #36 with authentic ownership and opaque replay values. Complete hosted state targeting Chat and Claude Bash targeting Responses remain explicitly rejected.
The request adapter also rejects unsupported Bash history, incomplete search pairing and opaque replay state that cannot be represented without invention.
CPA's built-in response conversion preserves actual paired search results and replay tokens when present.
Offline/native regression checks establish the supported completed-search conversion and zero-dispatch rejection; they do not establish a fresh original-client provider conversion pass or general cross-format hosted-tool parity.
The earlier GPT capture lacks full result entries; the later full-result capture permits testing the actual response shape.

The [feature completion matrix](feature-completion.md) records current implementation receipts and external acceptance limits separately from the dated live evidence in this document.

The retained catalogue replay binds to the native CLI profile rather than the current plugin's token-exchange catalogue profile.
An isolated-host rerun needs complete catalogue bytes and matching credential, origin, profile, and response-hash provenance.
The later isolated startup completed token exchange and public catalogue discovery with HTTP 200.
The complete catalogue advertised fifty-seven entries with all three exact targets enabled and their expected endpoints.
The first local model lookup preceded asynchronous registration by one second and returned an empty list.
The host subsequently registered all three targets and nine models in total.
The first runner stopped before inference, preserving its startup failure and all five candidates as unrun at that time.
The corrected runner below and current acceptance above supersede that readiness status.
TCP listener readiness alone does not establish model-registry readiness.
This failure does not establish provider unavailability or a catalogue parser regression.
The corrected isolated readiness is complete.
Main owns the remaining acceptance listed above.

The corrected runner polls the local registry until the exact targets are registered, checking process health and capture failures within a bounded deadline.
Normal SDK authentication and public catalogue discovery completed before the five native probes.
Each probe made one observed physical inference request; this is an outcome, not a call limit.
No gate denial or capture failure occurred, and the aggregate test failed because three capabilities did not pass.

| Native JSON probe | Observed outcome |
| --- | --- |
| Gemini Chat search | HTTP 200, empty content and `finish_reason: error`; no call, result or successful terminal evidence. |
| GPT Responses search | Completed provider search with eleven nonempty results, matching action sources and a NASA citation. |
| Claude Messages Web Fetch | HTTP 400, `invalid_request_body`, with `rejected tool(s): web_fetch`. |
| Claude Messages code execution | `server_tool_use` named `bash_code_execution`, paired `bash_code_execution_tool_result`, exit code zero, stdout `385`, and `end_turn`. |
| GPT Responses Code Interpreter | HTTP 400, `unsupported_value`, with `tools` identifying the unsupported interpreter. |

Raw public refusals and protocol-shaped local errors are retained separately.
The two successful native probes do not establish cross-format requests, streaming, continuation or default native Copilot CLI HTTP parity.

## Capability evidence checks

A successful capability claim requires the exact model, protocol envelope and successful terminal status.
Incomplete Responses, truncated Messages and unfinished Chat choices cannot pass as completed execution.
A Gemini search requires a correlated nonempty provider result rather than an empty result envelope.
GPT search keeps result content, action sources and assistant citations as separate evidence.
The same completed search call and source URL must bind meaningful result content to the cited answer.
URL-only entries, completed-call status alone and results borrowed from another call cannot establish complete search evidence.
Claude execution recognizes the explicit native bash execution call/result variant with the same call ID, exit code zero and exact stdout.
These offline predicates and synthetic negatives validate acceptance checks rather than supplying missing provider capabilities.
An incomplete private capture stops further dispatch as soon as its retained-body limit is exceeded, before stream completion or capture serialization.
The partial body and truncation marker remain available for failure diagnosis.

## Historical original-client attachment evidence

The first cross-model packet uses Claude Code's built-in `Read` and Codex's original image or executable/view workflow.
Original client bodies are captured before conversion, separately from converted public requests and both response streams.
Claude's PNG read and converted Responses function result contain the original image bytes, with identical decoded SHA256.
GPT nevertheless answered that both halves were red, so this visual case remains failed.
The first PDF read reported `pdftoppm` unavailable in the isolated client environment.
Both Codex cases rejected the empty model catalogue before inference.
These preparation failures and all original bodies remain retained; they do not establish global model unavailability.
Only demonstrated environment or instrumentation changes justify revalidation of these cases.

The preparation retry preserves the earlier failures and uses separately recorded operation cells.
Claude PDF now reaches the maintained renderer, but Read reports Fontconfig and image-output write errors.
Both model responses complete cleanly, including the paired Read result; the final answer has no inspected PDF content.
Codex PNG prepares the original bytes, then receives a local HTTP 422 because explicit image detail cannot be represented by Messages.
Codex PDF receives a local HTTP 422 because its cached-only search declaration sets `external_web_access=false`, which Messages cannot represent.
Neither refusal dispatches provider inference.
The full original client and local error bodies remain retained separately from public provider captures.

A later changed-environment run uses Claude Code 2.1.296 and its original PDF Read page-rendering path.
The source PDF remains unchanged, while Read returns a JPEG page whose decoded bytes match the original follow-up and converted public request.
The source PDF and rendered JPEG hashes are recorded separately.
The first provider response ends with clean EOF.
The second successful terminal is fully forwarded and flushed before captured downstream and outbound contexts report cancellation, establishing semantic completion separately from clean EOF.
Visual inspection of the retained JPEG shows the blue rectangle but no printed code, matching the model's answer.
That content assertion remains failed; it does not establish a model misreading of visible text.

An existing installed Fontconfig configuration resolves the Helvetica substitution, and the original Read now renders the printed code with the blue rectangle.
The changed-environment live case retains Read's genuine `pages: "1-20"` selection on the one-page fixture.
It returns one correlated JPEG, with identical decoded client-prepared and public bytes, followed by the correct `Q7B9` and blue answer.
Both provider streams complete with clean EOF.
The initial validator wrongly requires the literal page selection `1` and therefore records a failure.
That original verdict remains preserved, and a separate independently reviewed body replay passes the genuine range, exact tool arguments, single-page output, hashes and completed-response checks.
No request modification or additional provider call substitutes for this replay.

The checked-in original Claude-to-GPT PNG cycle contains both original Messages requests/local SSE streams and both converted Responses requests/provider SSE streams.
Its converter regression verifies the actual Read declaration, arguments, encoded tool-ID carrier, paired result and unchanged PNG bytes across both hops.
Privacy replacements preserve the carrier encoding and repeated path relationships.
The real wrong-color answer and earlier unclassified read cause remain failed evidence.

Both original PDF Read cycles are also retained as complete Messages and Responses request/SSE bodies.
Their regressions preserve the genuine page selections, tool arguments, encoded IDs and rendered JPEG bytes.
The historical render and validator failures remain separate from the reviewed successful body replay.

The maintained original-client packet is opt-in through `CPA_LIVE_CLIENT_ATTACHMENTS=1`.
It requires explicit isolated CPA binary/plugin, copied authentication, private diagnostics, sandbox and expected installed client versions.
PDF cases additionally require the existing renderer and Fontconfig paths; fixture overrides must match the pinned source bytes.
The offline `TestAttachmentOriginalClaudePDFReadOffline` probe exercises installed Claude Read against a loopback fixture without provider inference.
The independently reviewed `TestAttachmentPacketCorrectedPDFReplay` reads retained evidence and writes a separate verdict without changing the original failure.
Ordinary tests cover strict terminal schemas, ordered content-block lifecycles, successful renderer/result correlation, counted authentication recovery and capture failure retention.

## Historical CPA v8.0.23 Codex follow-up

The earlier cached-only-search 422 is historical. The existing native-tool exclusion mechanism now excludes optional Responses search with `external_web_access=false` when targeting Messages, retaining ordinary functions and disclosing the exclusion. Forced excluded selection still fails before inference. Original request and paired response bodies are retained in a portable regression fixture; they do not establish clean EOF for the historical capture.

The original Codex app-server was also run against the real isolated proxy with a loopback WebSocket recorder. Active interruption and a completed same-thread follow-up answer passed. The client closed the active socket and emitted no `response.interrupt` frame. The required native-host test separately exercises unchanged control-frame forwarding, plugin HTTP cancellation and same-socket continuation using CPA's existing implementation. After plugin forwarder cleanup, a late control frame remains unsupported.

Codex PDF preparation keeps the original renderer and `view_image` tools. The original workspace-write CLI cannot launch nested Seatbelt inside the task-owned outer loopback-only sandbox. An alternate inner danger-full-access profile generates live search despite an explicit cached setting and receives the provider's HTTP 400. The installed CLI has no external-restricted sandbox mode, so the maintained packet retains workspace-write and full PDF completion remains unverified. Operating client settings remain unchanged. High image-detail semantics still have no verified lossless Messages representation.

## Native producer boundary

The pinned SDK exposes original request bodies, headers, metadata and a host HTTP client to executors.
Authentically supplied request information can therefore be preserved without a new plugin ABI.
The schema's source format does not identify an authenticated client runtime.
The SDK does not supply Copilot's executable tool registry, attachment preparation callback or ACP session-loading state.
Different original clients retain their own instructions and available executable tools.
Literal native Copilot body and HTTP fingerprint equality cannot be claimed by copying its tool declarations or inventing lifecycle identifiers.
Applicable interoperability checks retain exact models, authentic identity bindings, original attachment preparation, correlated tool results and continuation.

The actual GPT search result entries have titles, URLs and snippets but no Anthropic encrypted result content.
Its citation lacks an Anthropic encrypted index.
CPA's existing converter cannot invent those replay values; an explicit unsupported result remains necessary where lossless conversion requires them.
Native Claude bash execution likewise has no demonstrated lossless Python Code Interpreter equivalent in the pinned registry.
The complete native capture fixtures preserve these actual fields and outcomes rather than supplying invented cross-format success.
