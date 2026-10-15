# Feature completion and acceptance

This matrix covers the README, all four setup/acceptance documents, the v8 candidate, [upstream PR #6](https://github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/pull/6), and [issue #5](https://github.com/ririnto/cpa-copilot-bridge/issues/5).
There is no separate roadmap file; the public promises and issue #5 supply the acceptance scope.
The audit ran on 2026-10-12 and 2026-10-13.

ACTUAL means a check executed during this completion audit.
REUSED means retained evidence for an unchanged request, profile or source path; it never means a fresh live run.
NOT RUN identifies a specific missing execution.
DONE is scoped to the stated acceptance criterion.
EXTERNAL LIMIT requires the named owner/input; an explicit rejection is not a supported-capability pass.
Historical failures and private complete request/response records remain retained.

## Integration Decision

An independent Sol/high assessment selected continued v8 delivery on the existing candidate branch and upstream PR, with maintained v7 fixes delivered through ordinary PRs to the owned fork's main.
The [rationale is recorded on issue #5](https://github.com/ririnto/cpa-copilot-bridge/issues/5#issuecomment-6126441495).
The primary checkout stays on main.
The public fork does not gain upstream merge authority.

The candidate requires the paired maintained host selected by go.mod.
The official v8.0.26 callback dispatcher still lacks host.payload.finalize and the paired reserved-schema/interrupt behavior; [host PR #6500](https://github.com/router-for-me/CLIProxyAPI/pull/6500) remains open.
Removing the replacement or promoting this as a standalone official-host upgrade would break the documented contract.
A future promotion requires a dedicated migration PR, exact combined-head validation, host/configuration requirements and rollback, preserving current-main fixes.
Source/package validation does not erase external provider or original-client limits.

## Core Features

| Feature and exact criterion | State | ACTUAL | REUSED | NOT RUN / exact limit |
| --- | --- | --- | --- | --- |
| ABI v1/schema 6 registration: auth, models, executor and restricted HTTP register and load in the matched host | DONE | Built C-shared plugin; task-owned authless sandbox host loaded it and returned models HTTP 200; required native-host groups passed | Existing SDK contract tests | Installation into a live deployment is excluded; operator owns deployment |
| Existing-deployment coexistence: use a separate plugin/config/auth directory without replacing other providers | DONE for documented isolated setup | Isolated startup/teardown with empty task auth and loopback-only network | Native host tests and reviewed install/rollback guide | Real existing deployment modification is prohibited by task scope |
| Device login: reserve bounded capacity, retain approved token through temporary verification failure, activate only verified auth | DONE | Lifecycle/race tests, concurrency, saturation, expiry and shutdown checks; PRs #18/#24 | Earlier authorized device-flow evidence | New live OAuth needs an explicitly supplied scoped broker/auth copy |
| Account verification rate limits: honor 403/429 timing, increase waits and expire safely without another exchange | DONE | 403/429 timing/fallback/backoff/recovery regressions; provider race/vet; PRs #24/#26 | Reviewed lifecycle design | No new live rate-limit request is needed to establish local retry behavior |
| Credential refresh and continuity migration: preserve rotation/deadlines and account identity across restart/failure | DONE within supported contract | Native OAuth/restart and provider regressions | Retained real refresh/startup evidence | Different account/profile behavior requires its owner |
| Token exchange: short-lived token from the configured GitHub endpoint, correct headers and expiry refresh | DONE | Provider and matched-host tests | Successful authorized token-exchange/catalogue startups | Fresh authentication NOT RUN: provider request NOT GRANTED; no approved task-only credential/broker |
| Direct OAuth: use account-issued API origin, no v2-token fallback; invalidate 401/403 without speculative retry | DONE implementation; EXTERNAL LIMIT for exact-model live profile | Provider direct-OAuth/expiry/invalidation tests | Historical direct-OAuth profile lacked target models and rejected inference | Copilot owner must supply a changed enabled profile/catalogue |
| Authenticated model discovery: enabled chat models, catalogue endpoints and metadata; disabled policies remain disabled | DONE for recorded account | Native registry/model-policy tests | Catalogue and exact-model registry evidence | An override cannot prove availability; new accounts require their own catalogue |
| Aliases, exclusions and context settings: preserve actual upstream IDs and apply host-native policy/settings | DONE | Required native groups for exclusions/context and route tests | Exact requested-model live bodies | Context metadata is not a provider billing/token-limit claim |
| Nine Chat/Responses/Messages route combinations, JSON and SSE, with exact requested models | DONE for recorded matrix | Synthetic native round-trip and translation checks | All 18 retained cells for Gemini Flash, GPT Luna and Claude Haiku | Current-head live refresh NOT RUN: no approved task-only credential/broker; live credentials are forbidden. Other models need their own evidence |
| Plugin Responses WebSocket routing, native replay and configured compaction | DONE within the paired-host contract | Required matched-host WebSocket group passed normal and compaction/replay cases | Retained original-client WebSocket captures | Original incomplete-resume and response-chain continuation require the separate client/host inputs below |
| Text, ordered system content, function schemas/selection/parallelism/results and correlated IDs | DONE for supported representations | Translator/provider/native JSON/SSE and zero-dispatch tests; PRs #7/#15/#19 | Original-client/schema captures and exact serial-tool profile | Unsupported typed tools/replay state fail explicitly before inference |
| Destination request fidelity: SDK envelope errors, complete Chat tool history, Responses strict structured output, native Chat search options or pre-dispatch rejection of unrepresentable cross-format search, and explicit Messages temperature | DONE for listed request semantics | [PR #41](https://github.com/ririnto/cpa-copilot-bridge/pull/41) implements strict Chat request parity and refuses hosted state when the destination cannot preserve it | Existing supported-field fixtures | Combined Go/Python/race/vet and 14 native-host groups pass with the issue #43 projection; fresh provider execution is not claimed |
| Claude stopping controls: preserve ordered stops on Chat/native Messages; reject unsupported Responses controls | DONE | JSON/SSE preservation/rejection tests and race/vet; PR #19 | Unchanged route coverage | Native Responses has no representable stop field |
| Local Claude token estimates: O200k text/tool schema contribution, aliases counted once and original request preferred | DONE as an estimate | Schema-contribution regression and provider race/vet; PR #17 | Existing tokenizer coverage | Estimates are not provider token accounting |
| Native reasoning and authenticated replay: preserve opaque state, bind account/model/endpoint and reject foreign carriers | DONE within documented representations | Native/replay/cross-turn regressions and strict rejection tests | Actual opaque history and continuation captures | Full caller history is required for cross-format/Chat/Messages; previous_response_id alone is insufficient |
| Prompt-cache keys: preserve explicit key; derive stable Responses key when configured, disable generation when requested | DONE | Provider/translation/native regressions | Stable-session captured requests | Copilot cache lifetime equivalence is not promised |
| Configured Responses compaction and host compaction routes | DONE implementation; EXTERNAL LIMIT for live enabled-profile acceptance | Required native compaction groups | Recorded disabled-profile rejection | Exact enabled model/profile and genuine compaction result required from Copilot owner |
| Restricted provider HTTP: scope origins/auth and prevent arbitrary forwarding | DONE | Provider/security tests | Existing route-contract coverage | Arbitrary external forwarding is outside the contract |
| Standard Responses URL citations project into Chat JSON with exact text, URL/title and character positions | DONE for JSON | PR #44 preserves exact query URLs/title/text and composed Unicode character ranges, including whitespace/boundaries; retained-capture regression, full Go/Python/race/vet and 14 native groups pass | Existing native URL-citation capture | Standard Chat streaming annotation carrier is not established; complete hosted-state/history and Claude replay annotations still reject |
| Chat upstream error finish reasons remain failures across destination JSON/SSE | DONE | PRs #39/#42 merged; corrected destination-caller context also passes race/vet with the published parity implementation. Retained-response and synthetic partial-output/sticky-failure regressions, translator/provider race/vet, 14 native groups, post-opening Sol/high review and public CI passed | Original HTTP 200 empty/error capture | No fresh Gemini hosted-search success is claimed |
| SSE framing, ordering, usage, errors and completed/incomplete terminals | DONE within supported transports | Chunk/terminal/overflow/cancellation/native regression groups; PR #20 fixed the recorded full-race timeout | Retained real JSON/SSE/incomplete bodies | WebSocket incomplete-resume support remains a host/client limit |
| Search stream identity reconciliation: terminal owns IDs, exact bytes/state preserved, bounded fail-closed barrier | DONE implementation | Native and translator remapped-ID/no-invocation/overflow/interrupt cases | Real identity-repair and distinct continuation evidence | Exact citation correctness is a separate failed provider criterion |
| Active/late interruption and cancellation without affecting another turn | DONE wire implementation; EXTERNAL LIMIT for original-client emission | Required native control/cancellation/same-socket tests | Original RPC interruption and same-thread follow-up | Codex 0.162.1 emitted zero response.interrupt frames; a genuine changed original-client control/version is required |
| Original-client token-limit incomplete continuation | EXTERNAL LIMIT | Synthetic native incomplete handling | Retained incomplete provider response and client stream-error verdict | Original client exposes no deterministic token-cap/resume path; changed client/host behavior required |
| Account token/catalogue caches: expired entries pruned and retained count bounded under churn | DONE | Churn/expiry/forced-refresh-failure regressions, provider race/vet, independent Sol/high review and public CI; PRs #32/#33 merged | Existing in-flight/generation tests | No background cache goroutine is required |
| Client harness resource lifecycle: bound waits, terminate/reap groups and close captures/relays | DONE | PRs #34/#40 merged; duplicate post-reap group signaling (Darwin EPERM) was reproduced and fixed; ten focused race repeats and the full combined-head race suite pass. Darwin race checks cover inner RPC/turn deadlines, notification floods and outer timeout after descendant creation; Sol/high review and both public CI lanes passed | Existing sandbox/capture guards | Live original-client execution still needs the separate capability/authentication grant |
| Go/HTTP dependency security and production/CI toolchain validation | DONE for scanned dependencies | Default-branch vulnerability failure remediated by PR #21; fresh candidate scan found zero reachable/imported-package vulnerabilities; race/vet/tidy/module/build pass | Unchanged candidate patched dependency scan | Unimported openpgp module advisory has no fixed release; no reachable package was found |

## Hosted Tools And Attachments

| Feature and exact criterion | State | ACTUAL | REUSED | NOT RUN / exact limit |
| --- | --- | --- | --- | --- |
| Ordinary client functions and original Claude/Codex delegated children | DONE for recorded clients | Native schema/tool tests | Original baseline, spawn/wait/handback and genuine delegation passes | New client versions require their own original-client evidence |
| Completed Claude-compatible hosted-search history from Responses to destination-native Messages | DONE for supported replay shapes | PR #15 offline/native exact-history and zero-dispatch rejection tests in combined source | Original captured search structure | No fresh original-client provider conversion pass is claimed; missing opaque replay values remain rejected. The completed Claude-origin reverse direction is supported by PR #36 |
| Cross-format Chat hosted search retains mandatory retrieval, complete results/citations/lifecycle and native replay history | EXTERNAL LIMIT | Request-parity audit confirmed the pinned SDK/Chat representation cannot retain native hosted state; zero-dispatch refusal and exact native Chat preservation regressions | Native Responses history is the supported lossless path | Protocol/SDK owner must supply a lossless authenticated Chat response/history carrier and meaningful original-client execution/continuation; permission alone cannot add it |
| Claude-origin completed hosted-search round trip through Responses with authentic destination ownership/history | DONE for the representable completed-search carrier | [PR #36](https://github.com/ririnto/cpa-copilot-bridge/pull/36) adds the completed Claude-origin search round trip and guards while preserving destination ownership/history | Recorded original structures and earlier supported replay direction | Combined Go/Python/race/vet and 14 native-host groups pass. Live WebSearch remains NOT RUN after denied access; Bash-to-Responses and unrepresentable opaque state remain unsupported |
| Native Claude Bash execution and exact-history continuation | DONE for recorded profile | Capture/native acceptance predicates | Real paired stdout 385, exit zero, terminal/EOF and continuation without repeated execution | Cross-format Bash-to-interpreter equivalence lacks a lossless destination contract |
| Native GPT required-tool search JSON with meaningful results/sources/citation | DONE for distinct recorded profile | Strict result/source/citation predicates | Real required-tool profile passed | It does not promote the failed automatic original-client case |
| Native GPT hosted-search SSE exact citation/result URL pairing | EXTERNAL LIMIT; fresh access NOT GRANTED | Causal capture/oracle regression | Tracking query already appears in provider citation/sources and is absent from meaningful results | Copilot owner must supply changed exact-model behavior or a substantively changed supported case; preserve URL including query |
| Original Codex automatic native search actually invokes the provider tool | EXTERNAL LIMIT; fresh access NOT GRANTED | Authentic declaration/non-invocation guards | Current auto profile completed without invoking; earlier pass stays historical | Changed genuine original-client supported control/profile required; a proxy-invented required selection does not qualify |
| Claude WebSearch actually executes and returns meaningful paired results | EXTERNAL LIMIT; fresh access NOT GRANTED | Refusal/success predicates | Genuine declaration was invoked; Copilot returned HTTP 400 unsupported_value | Changed exact-Haiku capability/profile from Copilot owner |
| Gemini native search returns correlated nonempty content and successful terminal | EXTERNAL LIMIT; capability request sent | Strict empty/error rejection predicates | Recorded HTTP 200 empty content/finish_reason error | Changed exact-Gemini supported search profile |
| Claude Web Fetch and GPT Code Interpreter execute genuine hosted tools | EXTERNAL LIMIT; capability request sent | Protocol-shaped rejection/acceptance tests | Recorded rejected web_fetch and unsupported interpreter | Exact-model advertised support and genuine tool context from Copilot owner |
| Original Codex-to-Haiku PNG and PDF; original Claude-to-GPT PDF | DONE for retained preparation/cases | Fixture pixel/PDF/read-cycle and correlation checks | Original source/prepared/public bytes, genuine tools and successful content assertions | Other client/model/file cases are not established by these examples |
| Original GPT/Gemini PDF preparation, viewing, follow-up and additional client/model file pairs | EXTERNAL LIMIT; capability request sent | Existing strict file/preparation/correlation predicates | Earlier tool-prohibited probes prove tagged preparation only; native GPT viewing evidence is separately retained | Genuine original preparer/view/follow-up cases and exact-model file capability are required |
| Original Claude-to-GPT unchanged PNG gives correct red-top/blue-bottom answer | EXTERNAL LIMIT; fresh access NOT GRANTED | Byte/correlation/oracle checks | Identical 177-byte PNG and public/client text, but wrong provider answer | Changed exact-GPT vision behavior or a supported profile preserving the carrier |
| Image detail selects an advertised same-model capable route or rejects before dispatch | DONE implementation | Chat-fallback/forced-route 422 regressions | Original image-detail request evidence | Lossless high detail on unsupported Messages is not invented |
| Original remote HTTPS image carrier preserves URL/detail and reaches a supported provider route | EXTERNAL LIMIT; fresh access NOT GRANTED | Carrier/root-origin/sandbox/refusal predicates | Codex rejects before inference; original Haiku URL-only Messages reaches Copilot and is refused | Genuine changed original-client/provider profile; no URL-to-inline substitution |
| Image generation and provider-owned uploads/lookup | EXTERNAL LIMIT; capability request sent | Explicit unsupported/capability guards | None establishes genuine issuance | Advertised exact-model capability, original preparer and authentic provider-issued resource required |
| File search and hosted MCP/tool search | EXTERNAL LIMIT; capability request sent | Typed-tool ownership/selection guards | No genuine supported resource/registry evidence | Real files/server/tool registry plus exact-model support |
| Audio, video and office attachments | EXTERNAL LIMIT; capability request sent | Strict unsupported-content guards | Representative image/PDF support does not establish these modalities | Original preparer and exact-model MIME/content capability evidence |
| Native first-party HTTP/session/executable-tool identity parity | EXTERNAL LIMIT; ownership/resource request sent | Origin/redaction/authenticity guards | Native captures and preserved header mismatch; provider WIP remains unpublished | Owner release plus genuine identity/ACP/tool context; SDK inbound headers cannot prove native runtime identity |
| HTTP/2 and TLS framing equality | EXTERNAL LIMIT | HTTP/1.1 no-replay/isolated-loopback checks | Native fingerprint comparison baseline | Authentic native transport harness/profile required; no equality claim from ordinary HTTP bodies |

## Delivery And Operations

| Feature and exact criterion | State | ACTUAL | REUSED | NOT RUN / exact limit |
| --- | --- | --- | --- | --- |
| Native host source override validates local checkout and mounts it read-only, defaults to selected module | DONE | Relative/absolute/path-space/invalid-path fake-container regressions; PR #22 | Matched selected-module host | Local Docker is not used without an Orchestrator lease |
| Linux package, ZIP/checksum/architecture and exact paired-host metadata | DONE for candidate artifact | Public CI, downloaded package SHA256/ZIP/ELF64 AMD64 and both root entries verified | Existing host-requirements checks | Inspection did not install or publish a release |
| Release/integration helpers have purpose-owned locations and all consumers move together | DONE | Full Go/Python/vet and native relocated-runner validation, independent Sol/high review and public CI; PRs #31/#33 merged | Existing helper behavior | No generic compatibility wrapper remains |
| Public CI retains tests, production packaging and required native groups | DONE for published reviewed heads | Both push/PR lanes on completed audit PRs passed | Earlier unaffected CI | Release workflow and publication are NOT RUN |
| Versioned standalone official-host release / upstream merge | EXTERNAL LIMIT | Paired-host package/readiness checked | Existing upstream plugin/host PRs | Upstream maintainer controls merge; official callback contract is absent. Candidate must remain explicitly paired-host |
| Docker deployment and existing live installation | EXTERNAL LIMIT; sealed lease request sent | Authless task-owned host/plugin startup and teardown; pinned images and isolated deployment proposal prepared without Docker access | Reviewed isolation/backup/rollback instructions | Live service/config/plugins/auth are forbidden; shared Docker requires an exclusive Orchestrator lease and enforceable resource guard |
| Frontend components and Storybook | Not applicable | No frontend component project is present | Go plugin architecture | No frontend/story framework is introduced |

## Sealed Acceptance Request

Main prepared separate host/plugin/config/auth-copy/client-state/capture directories, built matched artifacts, verified authless startup over an exclusive loopback port, denied external network, and reaped the host.
No live CLIProxyAPI files or Copilot credentials were accessed, and no new live Copilot acceptance inference ran.

The sealed four-cell provider request was sent, then the translator thread recorded the direct human decision NOT GRANTED.
[The denied-access record](live-acceptance-status.md) names each blocked inference/control operation and the required separate task-only credential with explicit read/refresh authority.
Only live-service credentials were reported available, and their use/copy/reconfiguration is forbidden.
No approved task-only credential or broker was supplied; all fresh provider cells and the current 18-cell refresh are NOT RUN, with zero new provider dispatches.
No repeated approval request or alternate credential discovery is authorized.
The prior WebSearch HTTP 400, citation/result URL mismatch, wrong PNG answer and zero original interruption frames remain REUSED failures for their exact requests.

Clients would use task-owned state and isolated host/gate ports, bounded cell/phase deadlines, complete private capture, physical dispatch accounting and teardown.
The live service/config/plugins/auth, global saved credentials/Keychain, global client configuration, foreign WIP and shared Docker remain forbidden resources.
Separate capability requests identify direct OAuth, enabled compaction, genuine URL/upload carriers, incomplete/resume controls, native identity/transport comparison and remaining original PDF/client pairs.
Permission alone cannot supply a missing protocol carrier, correct a provider answer or make an original client emit a missing frame.
The [official Codex release API](https://api.github.com/repos/openai/codex/releases/latest) identified 0.162.1 as the latest stable release at audit time; no newer stable interruption fix was available.

The initial Docker reservation request asks for exclusive runtime ownership, daemon identity, a quiet receipt and physical resource enforcement.
The reviewed Docker execution request supplies exact build/native/probe/cleanup commands and a controller that passed independent Sol/high review and offline deadline checks.
The earlier 153-file snapshot at 9957e66 is historical. The current proposal was resealed from all 407 tracked Git blobs at f897ef9, including current source and executable modes; native seeds/selector and the current CI artifact were verified.
Independent Sol/high review found the current proposal conditionally clear and the controller/resource contract unchanged; offline validation passed. This establishes proposal/source provenance, not Docker execution.
It remains unarmed until the Orchestrator supplies a bound exclusive lease, enforced resource/storage budgets and independent cancellation/cleanup guard.
The current sealed request was sent to the Orchestrator. Execution against another source requires resealing; earlier requests cannot authorize an unsealed replacement.
The proposal uses task-specific names, empty authentication, loopback publishing, an isolated internal network, pinned images and bounded execution/cleanup.
No Docker command or API access has occurred during this audit.
Provider access is explicitly denied. The Docker lease and additional capability/resource contracts remain external inputs; elapsed time supplies neither permission nor capability evidence.

## Audit Evidence

The combined source reviewed at f2b8ef3, merged by [PR #44](https://github.com/ririnto/cpa-copilot-bridge/pull/44), passes the full Go suite, nine Python checks, full race suite, go vet and module verification.
The current Darwin C-shared plugin and paired host pass all 14 required native groups with zero skips.
Both public CI test/package lanes passed; the downloaded Linux package checksum, two ZIP root entries, ELF64 AMD64 architecture and paired-host metadata were checked.
The selected dependency graph is unchanged from the audit vulnerability scan: zero called/imported vulnerable packages; the unimported module-only advisory remains disclosed.
Task-owned current-source authless startup returned models HTTP 200 with empty auth and external network denied; teardown reaped the host.
No target-provider inference, local Docker execution, release workflow or live deployment ran.

Independent documentation and source audits ran in separate worktrees.
Post-opening code/approval reviews use Sol/high; publication exposure scans are separate.
The initial full-race capture timeout and OAuth review findings remain recorded and were fixed without weakening deadlines or oracles.
Fresh synthetic/native checks and retained genuine live results remain distinct.
Completion does not mean that all external capability rows passed.
