# Feature completion and acceptance

This matrix covers the README, all four setup and acceptance documents, and the v8 candidate.
There is no separate roadmap file.
The public promises and the criteria below define the acceptance scope.

State values:

- `DONE` means a check executed for the stated criterion.
- `EXTERNAL LIMIT` names an input the project does not own.
- `Not applicable` marks a criterion outside the project's architecture.

Provider acceptance, original-client acceptance and live deployment are closed as not pursued.
The [live acceptance record](live-acceptance-status.md) holds their retained outcomes.

## Integration Decision

An independent assessment selected continued v8 delivery on the existing candidate branch and upstream PR.
Maintained v7 fixes remain a separate delivery scope.
The candidate does not authorize upstream merge or deployment.

The candidate requires the paired maintained host selected by go.mod.
The official v8.0.26 callback dispatcher lacks host.payload.finalize and the paired reserved-schema/interrupt behavior.
[Host PR #6500](https://github.com/router-for-me/CLIProxyAPI/pull/6500) was closed without merging.
Removing the replacement or promoting this as a standalone official-host upgrade would break the documented contract.
A future promotion requires a dedicated migration PR, exact combined-head validation, host/configuration requirements and rollback, preserving current-main fixes.
Source/package validation does not erase external provider or original-client limits.

## Core Features

| Feature and exact criterion | State | Executed evidence |
| --- | --- | --- |
| ABI v1/schema 6 registration. Auth, models, executor and restricted HTTP register and load in the matched host | DONE | Built C-shared plugin. Task-owned authless sandbox host loaded it and returned models HTTP 200. Required native-host groups passed |
| Existing-deployment coexistence. Use a separate plugin/config/auth directory without replacing other providers | DONE | Isolated startup/teardown with empty task auth and loopback-only network |
| Device login. Reserve bounded capacity, retain approved token through temporary verification failure, activate only verified auth | DONE | Lifecycle/race tests, concurrency, saturation, expiry and shutdown checks |
| Account verification rate limits. Honor 403/429 timing, increase waits and expire safely without another exchange | DONE | 403/429 timing/fallback/backoff/recovery regressions. Provider race/vet |
| Credential refresh and continuity migration. Preserve rotation/deadlines and account identity across restart/failure | DONE | Native OAuth/restart and provider regressions |
| Token exchange. Short-lived token from the configured GitHub endpoint, correct headers and expiry refresh | DONE | Provider and matched-host tests. Authorized task-private Copilot-copy token exchange and catalogue startup on 2026-10-16 |
| Copilot inference headers. Derive `X-Initiator` from the last user turn and `Copilot-Vision-Request` from model vision support plus outbound image presence, identical on token-exchange dispatch and bearer-refresh retry | DONE | Derivation tests cover both auth modes and token-exchange refresh stability |
| Direct OAuth. Use account-issued API origin, no v2-token fallback. Invalidate 401/403 without speculative retry | DONE | Provider direct-OAuth/expiry/invalidation tests |
| Authenticated model discovery. Enabled chat models, catalogue endpoints and metadata. Disabled policies remain disabled | DONE | Native registry/model-policy tests |
| Aliases, exclusions and context settings. Preserve actual upstream IDs and apply host-native policy/settings | DONE | Required native groups for exclusions/context and route tests |
| Nine Chat/Responses/Messages route combinations, JSON and SSE, with exact requested models | DONE | Synthetic native round-trip and translation checks. All 18 cells passed in the authorized 2026-10-16 run, Gemini first |
| Plugin Responses WebSocket routing, native replay and configured compaction | DONE | Required matched-host WebSocket group passed normal and compaction/replay cases |
| Text, ordered system content, function schemas/selection/parallelism/results and correlated IDs | DONE | Translator/provider/native JSON/SSE and zero-dispatch tests. Gemini-3-family Chat requests with tools translate omitted or automatic tool selection to `validated`, preserving explicit `none`, `required` and pinned functions |
| Destination request fidelity. SDK envelope errors, complete Chat tool history, Responses strict structured output, native Chat search options or pre-dispatch rejection of unrepresentable cross-format search, and explicit Messages temperature | DONE | The adapter implements strict Chat request parity and refuses hosted state when the destination cannot preserve it |
| Claude stopping controls. Preserve ordered stops on Chat/native Messages. Reject unsupported Responses controls | DONE | JSON/SSE preservation/rejection tests and race/vet |
| Local Claude token estimates. O200k text/tool schema contribution, aliases counted once and original request preferred | DONE | Schema-contribution regression and provider race/vet |
| Native reasoning and authenticated replay. Preserve opaque state, bind account/model/endpoint and reject foreign carriers | DONE | Native/replay/cross-turn regressions and strict rejection tests. Split Responses assistant text and function-call items coalesce back onto one Chat assistant turn with the sealed carrier restored, and replay preserves multimodal prior-user fingerprints and repeated assistant-turn positions |
| Prompt-cache keys. Preserve explicit key. Derive stable Responses key when configured, disable generation when requested | DONE | Provider/translation/native regressions |
| Configured Responses compaction and host compaction routes | DONE | Required native compaction groups |
| Restricted provider HTTP. Scope origins/auth and prevent arbitrary forwarding | DONE | Provider/security tests |
| Standard Responses URL citations project into Chat JSON with exact text, URL/title and character positions | DONE | The adapter preserves exact query URLs/title/text and composed Unicode character ranges, including whitespace/boundaries. Retained-capture regression, full Go/Python/race/vet and 14 native groups pass |
| Chat upstream error finish reasons remain failures across destination JSON/SSE | DONE | Corrected destination-caller context also passes race/vet with the published parity implementation. Retained-response and synthetic partial-output/sticky-failure regressions, translator/provider race/vet, 14 native groups, post-opening Sol/high review and public CI passed |
| SSE framing, ordering, usage, errors and completed/incomplete terminals | DONE | Chunk/terminal/overflow/cancellation/native regression groups. The recorded full-race timeout is fixed |
| Search stream identity reconciliation. Terminal owns IDs, exact bytes/state preserved, bounded fail-closed barrier | DONE | Native and translator remapped-ID/no-invocation/overflow/interrupt cases |
| Active/late interruption and cancellation without affecting another turn | DONE | Required native control/cancellation/same-socket tests |
| Original-client token-limit incomplete continuation | EXTERNAL LIMIT | Synthetic native incomplete handling |
| Account token/catalogue caches. Expired entries pruned and retained count bounded under churn | DONE | Churn/expiry/forced-refresh-failure regressions, provider race/vet, independent Sol/high review and public CI |
| Client harness resource lifecycle. Bound waits, terminate/reap groups and close captures/relays | DONE | Duplicate post-reap group signaling (Darwin EPERM) was reproduced and fixed. Ten focused race repeats and the full combined-head race suite pass. Darwin race checks cover inner RPC/turn deadlines, notification floods and outer timeout after descendant creation. Sol/high review and both public CI lanes passed |
| Go/HTTP dependency security and production/CI toolchain validation | DONE | Default-branch vulnerability failure remediated. Fresh candidate scan found zero reachable/imported-package vulnerabilities. Race/vet/tidy/module/build pass |

## Hosted Tools And Attachments

| Feature and exact criterion | State | Executed evidence |
| --- | --- | --- |
| Ordinary client functions and original Claude/Codex delegated children | DONE | Native schema/tool tests |
| Completed Claude-compatible hosted-search history from Responses to destination-native Messages | DONE | Offline/native exact-history and zero-dispatch rejection tests in combined source |
| Cross-format Chat hosted search retains mandatory retrieval, complete results/citations/lifecycle and native replay history | EXTERNAL LIMIT | Request-parity audit confirmed the pinned SDK/Chat representation cannot retain native hosted state. Zero-dispatch refusal and exact native Chat preservation regressions |
| Claude-origin completed hosted-search round trip through Responses with authentic destination ownership/history | DONE | The adapter supports the completed Claude-origin search round trip and guards while preserving destination ownership/history |
| Native Claude Bash execution and exact-history continuation | DONE | Capture/native acceptance predicates |
| Native GPT required-tool search JSON with meaningful results/sources/citation | DONE | Strict result/source/citation predicates |
| Native GPT hosted-search SSE exact citation/result URL pairing | EXTERNAL LIMIT | Causal capture/oracle regression |
| Original Codex automatic native search actually invokes the provider tool | EXTERNAL LIMIT | Authentic declaration/non-invocation guards |
| Claude WebSearch actually executes and returns meaningful paired results | EXTERNAL LIMIT | Refusal/success predicates |
| Gemini native search returns correlated nonempty content and successful terminal | EXTERNAL LIMIT | Strict empty/error rejection predicates |
| Claude Web Fetch and GPT Code Interpreter execute genuine hosted tools | EXTERNAL LIMIT | Protocol-shaped rejection/acceptance tests |
| Original Codex-to-Haiku PNG and PDF. Original Claude-to-GPT PDF | DONE | Fixture pixel/PDF/read-cycle and correlation checks. The 2026-10-16 cross-client rerun re-passed Codex-to-Haiku PNG with `detail: high` on the advertised Chat route |
| Original GPT/Gemini PDF preparation, viewing, follow-up and additional client/model file pairs | EXTERNAL LIMIT | Exact verified PDF-page hash and JPEG carrier checks. Corrected replay and ingress regressions |
| Original Claude-to-GPT unchanged PNG gives correct red-top/blue-bottom answer | EXTERNAL LIMIT | Byte/correlation/oracle checks |
| Image detail selects an advertised same-model capable route or rejects before dispatch | DONE | Chat-fallback/forced-route 422 regressions |
| Original remote HTTPS image carrier preserves URL/detail and reaches a supported provider route | EXTERNAL LIMIT | Carrier/root-origin/sandbox/refusal predicates |
| Image generation and provider-owned uploads/lookup | EXTERNAL LIMIT | Explicit unsupported/capability guards |
| File search and hosted MCP/tool search | EXTERNAL LIMIT | Typed-tool ownership/selection guards |
| Audio, video and office attachments | EXTERNAL LIMIT | Strict unsupported-content guards |
| Native first-party HTTP/session/executable-tool identity parity | EXTERNAL LIMIT | Origin/redaction/authenticity guards |
| HTTP/2 and TLS framing equality | EXTERNAL LIMIT | HTTP/1.1 no-replay/isolated-loopback checks |

## Delivery And Operations

| Feature and exact criterion | State | Executed evidence |
| --- | --- | --- |
| Native host source override validates local checkout and mounts it read-only, defaults to selected module | DONE | Relative/absolute/path-space/invalid-path fake-container regressions |
| Linux package, ZIP/checksum/architecture and exact paired-host metadata | DONE | Public CI, downloaded package SHA256/ZIP/ELF64 AMD64 and both root entries verified |
| Release/integration helpers have purpose-owned locations and all consumers move together | DONE | Full Go/Python/vet and native relocated-runner validation, independent Sol/high review and public CI |
| Public CI retains tests, production packaging and required native groups | DONE | Both push/PR lanes on completed audit PRs passed |
| Versioned standalone official-host release / upstream merge | EXTERNAL LIMIT | Paired-host package/readiness checked |
| Docker deployment and existing live installation | EXTERNAL LIMIT | Authless task-owned host/plugin startup and teardown over an isolated loopback port |
| Frontend components and Storybook | Not applicable | The project is a Go plugin with no frontend component |

## Audit Evidence

The combined source passed the full Go suite, the Python checks, the full race suite, `go vet` and module verification.
The current Darwin C-shared plugin and paired host pass all 14 required native groups with zero skips.
Both public CI test and package lanes passed.
The audit vulnerability scan found zero called or imported vulnerable packages.
Task-owned current-source authless startup returned models HTTP 200 with empty auth and external network denied.
