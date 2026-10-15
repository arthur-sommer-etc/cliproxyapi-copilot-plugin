# Cached-search capture fixture

These fixtures preserve the original Codex CLI request body and paired local Responses body captured through CPA. The request body keeps its complete tool declarations and original model, prompt, and `web_search.external_web_access: false` value. Machine paths and user, session, window, turn, and response identifiers use portable fixture placeholders; the opaque encrypted response field is also replaced. No headers or authentication fields are included.

The response body contains a captured `response.completed` event with a client `exec_command` function call. The capture does not establish clean stream EOF, and the function call is protocol data only; this test must not execute it. The test derives a local Messages mock from that captured call to exercise both executor paths. These bodies are regression fixtures, not evidence of live provider success.

The response body is stored as a JSON string. Decoding preserves every SSE separator, including the terminal blank line, without changing the captured protocol bytes.
