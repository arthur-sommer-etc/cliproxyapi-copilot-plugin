# Complete native hosted-tool stream captures

These five fixtures come from actual Copilot inference bodies on the assigned native endpoints.
They preserve every request field, response field, SSE event, array entry and value type.
Only nonempty opaque identity, ciphertext, signature, obfuscation, cache and safety string values are replaced with portable markers.
Public search results, URLs, snippets, numeric usage, tool arguments, stdout and errors remain unchanged.
Markers preserve equality relationships across the coupled original GPT terminal and its continuation.
They are not valid credentials or provider replay tokens.

| Capture | Actual source outcome |
| --- | --- |
| `gpt-search-stream-before-repair` | HTTP 200 search with correlated results, but changing response and item identities failed native lifecycle validation. |
| `gpt-search-stream-after-identity-repair` | The client identity repair passed, but a tracked citation URL failed exact result pairing. |
| `gpt-search-captured-history-continuation` | A real request reused the original actual terminal output and ciphertext and completed without another search. |
| `claude-execution-stream` | Actual Bash execution returned a paired result with exit zero, stdout `385` and successful clean completion. |
| `claude-execution-continuation` | A real request replayed the preceding actual content and answered from `385` without another tool call. |

The Responses SSE fixtures contain complete raw provider streams before plugin conversion.
SSE bodies are stored as JSON strings in `response.json`, preserving all framing bytes without trailing-whitespace exceptions.
The fixture digest covers the stored JSON file; the sanitized-source digest covers the decoded SSE bytes.
The after-repair capture still contains the provider's mutable identities.
The native converter regression validates the separately observed client repair against that source.
Claude's upstream `[DONE]` sentinel remains present.
Client-side framing and negative lifecycle mutations are separate offline tests.

Each metadata file records original private body hashes, sanitized fixture hashes and original structural hashes.
The shape files contain complete field/type structures and array lengths.
Tests check source-derived structures, fixture hashes, unchanged terminal bytes and actual continuation ownership.
Original failed live verdicts remain preserved separately from successful offline repair and real continuation outcomes.
No synthetic provider success is used to fill a missing live result.
