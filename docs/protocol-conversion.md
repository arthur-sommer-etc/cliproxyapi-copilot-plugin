# Protocol conversion and ownership

The bridge accepts Chat Completions, Responses and Messages requests and routes supported content to a Copilot endpoint selected from model metadata or an explicit override.
A route existing does not establish that every field, model or hosted tool can be converted or accepted by Copilot.
The bridge rejects known unrepresentable content rather than silently losing it.

## Formats and endpoints

| Host source format | Accepted aliases | Copilot endpoint |
| --- | --- | --- |
| Responses | Empty format, `responses`, `openai-response`, `openai-responses` | `/responses` |
| Chat Completions | `openai`, `chat`, `chat-completions`, `chat/completions` | `/chat/completions` |
| Messages | `claude`, `anthropic` | `/v1/messages` |

Each source format can be routed to each endpoint, subject to supported content and model capabilities.
The endpoint column identifies the corresponding native format, not a requirement that the source use that endpoint.
An unknown source format returns a structured `unsupported_format` error with HTTP 422.
Overrides select routing.
They do not prove model availability or capability.

## Ownership

CLIProxyAPI owns generic API-family codecs and its translator registry.
The plugin uses those translators and adds guarded routes and Copilot-specific handling for endpoint selection, authentication, inference headers, provider state and replay.
Generic API families beyond these three belong in the proxy's generic translation layer, not in Copilot-specific heuristics.

Media preparation belongs to the original client or proxy where the necessary source and capabilities exist.
The bridge preserves representable prepared media.
It does not invent an original client's file reader, renderer or executable tool registry.
The proxy also owns authentic request and header provenance snapshots and provider-specific beta vocabularies when those are needed to make a routing decision.
A source-format label alone does not authenticate an original client.

External state resolution and complete hosted adapters belong in the proxy's generic protocol layer.
A complete adapter must represent execution ownership, tool calls, results, citations, stream lifecycle and continuation together.
Mapping a request declaration alone does not transfer execution ownership or create missing provider state.
The bridge supports specific completed Claude-compatible search replay shapes, but rejects shapes whose state cannot be preserved.
See [hosted tool acceptance](server-tool-acceptance.md) for supported cases and limits.

## Thinking and continuation

For Copilot Chat responses, including Gemini, the bridge preserves `reasoning_text` and `reasoning_opaque` in an authenticated replay carrier for Messages and Responses clients.
On continuation it checks the credential, model, endpoint and API origin and matches available conversation and tool-call anchors before restoring reasoning state.
New carriers compare tool argument numbers by exact value without floating-point rounding.
Legacy version 1 Copilot Chat replay carriers fail closed because their older hashes can be ambiguous.
Start a fresh conversation without replaying the old carrier.
Numbers with more than 4096 coefficient digits or an exponent outside ±1000000 are matched by their original bytes, so alternate spellings can be rejected.
Tool arguments with duplicate JSON object keys are also matched by their original bytes.
For cross-format Chat reasoning, the bridge rejects ambiguous source requests and Chat response data before conversion.
The carrier is authenticated, not encrypted, and is private conversation data.
It is not a native Claude or Google signature.
Changed or foreign state is rejected rather than silently discarded.
The bridge also preserves supported native Messages and Responses reasoning state on same-format routes.
A separate bounded in-memory Responses replay cache can restore matching reasoning for a scoped continuation.
Missing or ambiguous scope, expiry or capacity limits can prevent recovery.

Send full caller history for Chat, Messages and cross-format continuation.
A cross-format bridge cannot reconstruct that history from `previous_response_id` alone.
Use that reference only with a native Copilot Responses endpoint that supports it.
Tool call IDs and paired results must remain correlated, including parallel calls.

## Headers and beta flags

In both authentication modes, the plugin derives `X-Initiator` from the final translated turn and sets `Copilot-Vision-Request: true` only when the model supports vision and the translated body contains a typed image.
A Messages tool-result turn is an agent continuation.
Direct OAuth still uses its own user agent and omits editor and Copilot Chat integration headers.
Token-exchange refresh retries rebuild the derived headers from the same body.
Direct OAuth invalidates cached authentication on HTTP 401 or 403 and does not retry the request.
Direct OAuth provider acceptance of these derived headers remains unverified.

The plugin forwards repeated `OpenAI-Beta` values in order only for native Responses-to-Responses requests with a nonempty original body whose model exactly matches the selected model.
This applies to JSON, streams and refresh retry.
Cross-format requests, missing original bodies and model transformations do not qualify.
Inbound authorization, cookies and identity or session tokens are not forwarded as provider credentials.
These rules do not establish that a provider accepts any particular beta flag.

## Attachments, errors and numbers

For representable routes, the bridge preserves prepared image and document data, MIME types, URLs, filenames, ordering and supported image detail, including attachments in tool results.
For some unsupported nonempty attachment shapes and image detail covered by its preflight checks, the bridge returns a structured failure before inference instead of dropping them.
It cannot infer file bytes or reconstruct media that the original client never supplied.

Provider errors expose structured code, message, HTTP status and retryability.
Unsupported image detail maps to HTTP 422 `unsupported_image_detail`.
Attachment conversion errors detected by preflight map to HTTP 422 `unsupported_attachment` and identify the source format, destination format, attachment kind and field.
Unknown extra field names are reported as `additional_fields` without exposing the supplied names or values.
Other request conversion failures map to HTTP 422 `translation_error`.
Attachment preflight examines the host payload.
It cannot detect an attachment already removed by the host before this plugin receives that payload.
Response translation failures can return HTTP 502.
Upstream HTTP status is retained, and client-visible errors suppress provider response text except for fixed safe classifications.
Upstream errors mark 408, 429 and 5xx responses as retryable.
This does not promise a successful retry.

Setting the selected model and stream fields preserves the other JSON tokens in the routed body.
The number-preserving object decoder also avoids floating-point conversion on the paths that use it.
For example, the synthetic integer `9007199254740993` must not become `9007199254740992` through floating-point decoding.
Explicit zero temperature is preserved where the destination accepts it.
Invalid or unrepresentable values are rejected.

## Evidence and qualification

Structured offline tests exercise contracts without requiring provider access:

- [Reasoning carriers](../internal/provider/reasoning_carrier_test.go), [Copilot opaque replay](../internal/translate/reasoning_opaque_test.go), [Responses replay](../internal/provider/protocol_state_test.go), [reasoning guards](../internal/translate/reasoning_guard_test.go) and [Chat replay](../internal/translate/chat_responses_replay_test.go) cover scoped state and continuation.
- [Inference headers and beta flags](../internal/provider/native_beta_test.go) and [request parity](../internal/provider/chat_request_parity_test.go) cover outbound headers and routing.
- [Attachment conversion](../internal/translate/attachment_request_test.go) and [attachment routing](../internal/provider/executor_attachment_routing_test.go) cover prepared media and refusal paths.
- [Hosted search round trips](../internal/translate/hosted_search_roundtrip_test.go), [tool preflight](../internal/translate/native_tools_preflight_test.go) and [provider errors](../internal/provider/errors_test.go) cover bounded replay and structured failures.
- [Request parity tests](../internal/translate/chat_request_parity_test.go) cover explicit temperature values, and the carrier tests check a large usage integer without rounding.

Synthetic tests and hosted build or native-host fixture results establish only the tested local behavior.
They do not prove provider acceptance, server-side state reuse or original-client acceptance.
Live provider acceptance is closed as not pursued, and the [live acceptance record](live-acceptance-status.md) holds its retained outcomes.
Supported hosted-tool conversion cases are listed in [hosted tool acceptance](server-tool-acceptance.md).
