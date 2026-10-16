# Captured Copilot catalogue

This fixture preserves the complete actual HTTP 200 catalogue body from an isolated token-exchange plugin startup.
The original response contains fifty-seven rows, including enabled and disabled policies.
The request metadata preserves the public method, URL and non-secret profile headers; credential headers are excluded.
The body SHA256 is `7465e46f7235fd940c0b55e8fef22219472d89b625121ca22c4e726d9bfe5e5d`.

The profile is the accepted plugin's VS Code catalogue profile, not the default Copilot CLI profile.
The pinned host initially returned an empty local model list before asynchronous registration completed.
Its subsequent registration contained nine available models and all three assigned exact targets.
The cached-model regression uses this complete body and synthetic authentication to verify eighteen provider-available models before the host template applies its exclusions and aliases.
It does not establish fresh credentials, current future availability, native HTTP parity or hosted-tool support.
