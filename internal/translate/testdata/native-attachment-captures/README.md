# Native attachment inference captures

These six fixtures originate from forwarded native Copilot CLI 1.0.94-4 inference requests recorded on 2026-10-09 UTC.
Every source inference response was HTTP 200 with a complete, untruncated SSE body.
No authentication or model-catalog captures are included.

The request fixtures retain the original JSON field topology, attachment declarations, MIME types, decoded attachment bytes, options, and content order.
The SSE fixtures retain every captured event in its original order, including reasoning events, ping events, terminal events, and existing done markers.
The PNG and PDF bytes are the canary attachments actually observed in native requests, rather than regenerated or projected protocol requests.

Private filesystem paths and native resource filenames are replaced consistently with paths under `/sanitized/native-attachments/`.
Those paths identify sanitized local references and do not describe an upload or inline conversion.
The Gemini PDF response splits its original resource basename across deltas, so its replacement occupies the first affected delta and the remaining affected filename delta becomes empty without removing either event.
Dynamic provider/client identifiers and signed or encrypted opaque blobs are replaced with explicit placeholders that preserve equality relationships.
The shared persistent GPT safety identifier is replaced consistently with `SANITIZED_SAFETY_IDENTIFIER_001`, preserving its string type and equality across both captures and all response snapshots.
CLI system/developer instructions and tool descriptions are replaced with placeholders while retaining their JSON fields.
Reasoning text is replaced with a consistent placeholder in the first delta and an empty string in remaining deltas, retaining the original delta count and corresponding terminal text.
Capture headers, credentials, private run identifiers, machine identifiers, and raw environment text are excluded.
These sanitized bodies cannot prove transport fingerprint parity, cryptographic validity, or byte parity for redacted text.
They preserve the original attachment byte hashes and application-level structure.

| Fixture | Observed native preparation and result |
| --- | --- |
| `claude-png` | An inline PNG produced the correct red-top and blue-bottom answer. |
| `gemini-png` | An inline PNG produced the correct red-top and blue-bottom answer. |
| `gpt-png` | The identical inline PNG bytes received HTTP 200, but the answer incorrectly described both halves as blue. |
| `claude-pdf` | An inline PDF produced the Q7B9 text canary and blue rectangle answer. |
| `gemini-pdf` | The original request carried local `tagged_files` text and no inline file part, and the intentional no-tools request did not complete local file reading. |
| `gpt-pdf` | The original request carried local `tagged_files` text and no inline file part, and the intentional no-tools request did not complete local file reading. |

The GPT/Gemini PDF cases establish initial native preparation only.
Their no-tools responses neither complete the original file-reader/tool continuation route nor establish provider PDF acceptance or refusal.
They must not be counted as attachment support failures or rewritten into forced inline PDF requests.

The `response.sse` files are the complete sanitized captured responses.
Their final blank lines are retained SSE frame delimiters, including the delimiters after terminal events or done markers, and must not be removed to silence patch whitespace warnings.
The GPT `derived-response.json` files contain the captured `response.completed.response` snapshots.
The Claude and Gemini `derived-response.json` files are offline message assemblies from captured SSE blocks or chunks.
Those assemblies are explicitly derived data and are not independently captured HTTP JSON responses.

The tests replay native requests with only existing model/stream normalization and verify native response fields, order, model, text, and terminal semantics.
Offline response conversion tests use the recorded native request as converter context and verify the recorded answer and terminal after conversion.
They do not establish a live cross-protocol attachment matrix, a complete local file-reader route, upload/reference interoperability, or opaque continuation acceptance.
The original private captures remain the evidence owner’s responsibility and are not changed by these fixtures.
