# Route Claude Code through Claude and Copilot subscriptions

This guide deploys CLIProxyAPI with this repository's GitHub Copilot plugin,
authenticates both a Claude subscription and a GitHub Copilot subscription, and
configures Claude Code to use both through one local endpoint.
The Compose deployment uses the official v8.0.23 runtime image with a paired
maintained host binary built from the module replacement selected in `go.mod`.

Every step is an explicit command; no repository setup scripts are involved.

The resulting path is:

```text
Claude Code
    |
    | Anthropic Messages API
    v
paired CLIProxyAPI host
    |-- built-in Claude OAuth ------> Anthropic subscription
    `-- cliproxyapi-copilot plugin -> GitHub Copilot subscription
                                      (OpenAI Responses, Chat Completions,
                                       or Copilot Messages)
```

CCR is not required.
The plugin routes Claude Messages requests through each model's supported Copilot endpoint and translates responses for Claude Code.

## Prerequisites

- Linux on `amd64`
- Docker Engine with Docker Compose v2
- Git
- Claude Code installed and available as `claude`
- An active Claude subscription
- An active GitHub Copilot subscription

The Compose stack binds only to `127.0.0.1:8317`, so it is not reachable from
other machines by default.

This guide creates the repository's complete Compose stack. If CLIProxyAPI is
already deployed, install only the plugin using
[`install-existing-deployment.md`](install-existing-deployment.md), then resume
here at **Authenticate GitHub Copilot**.

## 1. Clone the repository

```bash
cd "$HOME"
git clone https://github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin.git
cd cliproxyapi-copilot-plugin
```

## 2. Generate local secrets

The deployment needs two local secrets:

- A CLIProxyAPI management password
- An API key used by Claude Code

Generate them once into `.runtime/secrets.env` with mode `0600`. The
`.runtime/` directory is ignored by Git.

```bash
mkdir -p .runtime
umask 077

{
  printf 'MANAGEMENT_PASSWORD=%s\n' "$(openssl rand -hex 32)"
  printf 'CLIPROXYAPI_API_KEY=%s\n' "$(openssl rand -hex 32)"
} > .runtime/secrets.env
chmod 600 .runtime/secrets.env
```

Skip this step if `.runtime/secrets.env` already exists. Regenerating it
replaces both secrets, which invalidates the rendered runtime configuration
and any Claude Code sessions using the old API key.

## 3. Render the runtime configuration

CLIProxyAPI does not expand environment variables in `api-keys`, so the inert
`__CLIPROXYAPI_API_KEY__` placeholder in the committed `config/config.yaml`
template must be replaced locally:

```bash
sed "s/__CLIPROXYAPI_API_KEY__/$(sed -n 's/^CLIPROXYAPI_API_KEY=//p' .runtime/secrets.env)/g" \
  config/config.yaml > .runtime/config.yaml
chmod 600 .runtime/config.yaml
```

Re-run this step whenever `config/config.yaml` changes.

## 4. Build the paired plugin and host, then start the stack

Build the plugin shared library and the matching CLIProxyAPI host inside the
pinned Go container:

```bash
make prepare-runtime-host
```

The plugin uses the official CLIProxyAPI v8.0.23 SDK and plugin ABI. Its final
payload-rule callback is supplied by the published maintained CLIProxyAPI
module replacement selected in `go.mod`; the official v8.0.23 server does not
implement that callback. The Compose file keeps the official image as the
runtime base, then mounts the host binary built from that paired module over the
image's server binary. Do not run Compose before `make prepare-runtime-host`, or
the required host binary will be missing. Compose pulls the official base image
when it is absent locally.

Start the stack. The `--env-file` flag passes the management password to the
container; the API key is only read from the mounted `.runtime/config.yaml`:

```bash
docker compose --env-file .runtime/secrets.env up -d
```

Confirm the container is running and the API answers with the generated key:

```bash
docker compose --env-file .runtime/secrets.env ps

API_KEY=$(sed -n 's/^CLIPROXYAPI_API_KEY=//p' .runtime/secrets.env)
curl -fsS -o /dev/null -w 'API health: %{http_code}\n' \
  -H "Authorization: Bearer $API_KEY" \
  http://127.0.0.1:8317/v1/models
```

Expect `API health: 200`. OAuth credentials created in the next steps are
stored in the Docker volume `cliproxyapi_official_copilot_dev_home`.

Open the management center:

```text
http://127.0.0.1:8317/management.html
```

Retrieve the management password when the UI asks for it:

```bash
sed -n 's/^MANAGEMENT_PASSWORD=//p' .runtime/secrets.env
```

## 5. Authenticate GitHub Copilot

In the management center:

1. Open the plugin or authentication section.
2. Start the **Copilot** login.
3. Open the GitHub device-login URL.
4. Enter the displayed device code.
5. Approve access and wait for the management center to report success.

The plugin stores the GitHub OAuth credential through CLIProxyAPI's normal auth
storage. Short-lived Copilot API tokens remain in process memory.

Confirm that the credential exists:

```bash
MGMT=$(sed -n 's/^MANAGEMENT_PASSWORD=//p' .runtime/secrets.env)

curl -fsS \
  -H "Authorization: Bearer $MGMT" \
  http://127.0.0.1:8317/v0/management/auth-files
```

## 6. Authenticate the Claude subscription

Start the built-in **Anthropic/Claude** login from the management center.
CLIProxyAPI uses Anthropic's OAuth flow unchanged.

Anthropic redirects to `localhost:54545`. This Compose stack intentionally does
not publish that port. When the browser reaches the final callback:

1. Copy the complete URL from the browser address bar.
2. Assign it to `REDIRECT_URL`.
3. Submit it to the official manual callback endpoint.

```bash
MGMT=$(sed -n 's/^MANAGEMENT_PASSWORD=//p' .runtime/secrets.env)
REDIRECT_URL='PASTE_THE_COMPLETE_CALLBACK_URL_HERE'

curl -fsS -X POST \
  -H "Authorization: Bearer $MGMT" \
  -H 'Content-Type: application/json' \
  -d "{\"provider\":\"anthropic\",\"redirect_url\":\"$REDIRECT_URL\"}" \
  http://127.0.0.1:8317/v0/management/oauth-callback
```

Return to the management center and wait for the login status to become
successful.

## 7. Verify models and inference

Read the generated API key:

```bash
API_KEY=$(sed -n 's/^CLIPROXYAPI_API_KEY=//p' .runtime/secrets.env)
```

List all models:

```bash
curl -fsS \
  -H "Authorization: Bearer $API_KEY" \
  http://127.0.0.1:8317/v1/models
```

The default catalog reflects the models available to the authenticated Copilot account.
Configured `model_endpoint_overrides` can also register their IDs when they are absent from the upstream catalog.
Known models with a non-enabled Copilot policy remain excluded; upstream availability and capabilities are unverified for override-only IDs.
Check `/v1/models` after authentication for the current model IDs.

Only a valid Claude request whose root `safeguards` is an array with one object
containing exactly `type` and `classifier_context`, with type `dangerous_tool_use`
and nonempty classifier context, receives a static HTTP 400 `safeguards` error
before a Copilot request is sent. Other nonempty or malformed safeguard forms
remain 422 translation errors.
Claude Code releases that recognize this response retry once with the classifier
beta header and server safeguard metadata omitted while local classification stays
active. Other clients should fail closed for automatic tool use.

The Copilot filter excludes selected older model IDs by family and exact name.
Review [GitHub's supported Copilot models](https://github.com/github/docs/blob/main/content/copilot/reference/ai-models/supported-models.md) when updating these exclusions.
These OAuth exclusions affect Copilot only.
Matching IDs from other provider catalogs remain available.
The exact Claude exclusions do not filter `claude-opus-5.5` or `claude-sonnet-5.5`.

The template adds input aliases for selected Claude model IDs.

```yaml
model-alias:
  copilot:
    - name: "claude-opus-5.5"
      alias: "claude-opus-5-5"
      fork: true
    - name: "claude-sonnet-5.5"
      alias: "claude-sonnet-5-5"
      fork: true
    - name: "claude-fable-5.1"
      alias: "claude-fable-5-1"
      fork: true
```

When Copilot exposes an upstream model, the aliases keep its dotted ID and add these client names.
CLIProxyAPI sends the original dotted model IDs to Copilot for aliased requests.
The configuration does not force response model IDs to change.

The template sets the advertised context length to 272000 tokens for both GPT models.

```yaml
settings:
  copilot:
    - name: "gpt-6.1-sol"
      max-context-length: 272000
    - name: "gpt-6-luna"
      max-context-length: 272000
```

The native host setting changes advertised context metadata only.
It does not enforce a billing or usage-cost cap.
Thinking support and other capabilities remain based on Copilot model metadata.
The setting is global and needs no plugin-specific option or auth-file field.

Test Copilot through the Responses API:

```bash
curl -fsS http://127.0.0.1:8317/v1/responses \
  -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-6-luna","input":"Reply exactly: copilot-ok","max_output_tokens":16}'
```

Test Claude through the Messages API:

```bash
curl -fsS http://127.0.0.1:8317/v1/messages \
  -H "x-api-key: $API_KEY" \
  -H 'anthropic-version: 2023-06-01' \
  -H 'Content-Type: application/json' \
  -d '{"model":"claude-sonnet-5-5","max_tokens":16,"messages":[{"role":"user","content":"Reply exactly: claude-ok"}]}'
```

Chat Completions, Responses, and Messages requests can route to Copilot Chat Completions, Responses, or Messages endpoints.
The plugin selects an endpoint from model metadata.
Set `model_endpoint_overrides` when a model needs a fixed route.
Send full caller history to Copilot Chat or Messages because the plugin does not store conversations.
Use `previous_response_id` only with a native Copilot Responses endpoint that supports it.
The plugin stores an authenticated replay carrier with the Copilot credential.
It accepts the carrier only for the same account, model, and endpoint.
The plugin rejects foreign signed or encrypted reasoning on Copilot Chat with HTTP 422.
Send full caller history for cross-format requests because the bridge cannot reconstruct it from `previous_response_id` alone.
The plugin derives `prompt_cache_key` for Copilot Responses requests when a stable session identity exists.
Set `support-prompt-cache-key: false` in the plugin configuration to omit generated keys.
The plugin preserves explicit caller keys.
Copilot's implicit cache lifetime can differ from Codex's.
The plugin enables compaction for listed models on compatible CLIProxyAPI hosts.
This option does not add unavailable models to `/v1/models`.

```yaml
compaction_models:
  - "gpt-6-luna"
  - "gpt-6.1-sol"
  - "mai-code-1.1-flash"
```

## 8. Configure Claude Code globally

Claude Code reads its API key through `apiKeyHelper`, a shell command it runs
at startup. Using a command that reads `.runtime/secrets.env` avoids copying
the key into `~/.claude/settings.json`.

If Claude Code settings already exist, back them up:

```bash
test ! -f "$HOME/.claude/settings.json" ||
  cp "$HOME/.claude/settings.json" "$HOME/.claude/settings.json.backup"
```

Merge the following values into `~/.claude/settings.json`. Replace the
`/home/YOUR_USER` path in `apiKeyHelper` with the actual clone location:

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:8317",
    "ANTHROPIC_MODEL": "gpt-6-luna",
    "ANTHROPIC_DEFAULT_FABLE_MODEL": "claude-fable-5-1",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "gpt-6-luna",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "claude-sonnet-5-5",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "gemini-3.8-flash",
    "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1"
  },
  "apiKeyHelper": "sed -n 's/^CLIPROXYAPI_API_KEY=//p' /home/YOUR_USER/cliproxyapi-copilot-plugin/.runtime/secrets.env"
}
```

Do not leave old `ANTHROPIC_AUTH_TOKEN` or `ANTHROPIC_API_KEY` values in the
settings file or shell environment. They override or conflict with
`apiKeyHelper` and commonly cause `401 Invalid API key`.

Exit every running Claude Code process after changing the settings. Claude Code
caches credentials at process startup.

Start a fresh session:

```bash
claude
```

The configured aliases are:

| Claude Code selection | Routed model | Possible subscription |
| --- | --- | --- |
| Default | `gpt-6-luna` | GitHub Copilot |
| Fable | `claude-fable-5-1` | Claude or GitHub Copilot |
| Opus | `gpt-6-luna` | GitHub Copilot |
| Sonnet | `claude-sonnet-5-5` | Claude or GitHub Copilot |
| Haiku | `gemini-3.8-flash` | GitHub Copilot |

Claude and Copilot can expose the same Fable and Sonnet IDs.
Plain aliases keep the requested names and can route through either provider's registered credentials.
Use a dotted Copilot ID, a unique alias, or a provider prefix to pin a request to one backend.

Select aliases with `/model` or at launch:

```bash
claude --model fable
claude --model opus
claude --model haiku
claude --model sonnet
```

Test the global default non-interactively:

```bash
claude -p 'Reply exactly: global-routing-ok' \
  --max-turns 1 \
  --output-format json
```

## 9. Operations

Check health:

```bash
docker compose --env-file .runtime/secrets.env ps

API_KEY=$(sed -n 's/^CLIPROXYAPI_API_KEY=//p' .runtime/secrets.env)
curl -fsS -o /dev/null -w 'API health: %{http_code}\n' \
  -H "Authorization: Bearer $API_KEY" \
  http://127.0.0.1:8317/v1/models
```

Rebuild after updating the repository:

```bash
git pull
make test
make build
docker restart cliproxyapi-official-copilot-dev
```

If `config/config.yaml` changed, re-render `.runtime/config.yaml` (step 3)
before restarting.

Stop the service without deleting OAuth credentials:

```bash
docker compose --env-file .runtime/secrets.env down
```

Permanently remove this deployment and its credentials:

```bash
docker compose --env-file .runtime/secrets.env down
docker volume rm cliproxyapi_official_copilot_dev_home
rm -rf .runtime build .cache logs
```

## Troubleshooting

### `401 {"error":"Invalid API key"}`

- Exit all existing Claude Code sessions and relaunch.
- Remove stale `ANTHROPIC_AUTH_TOKEN` and `ANTHROPIC_API_KEY` values.
- Confirm the helper command and runtime configuration resolve to the same key:

```bash
sed -n 's/^CLIPROXYAPI_API_KEY=//p' .runtime/secrets.env
sed -n 's/^  - "\([^"]*\)"/\1/p' .runtime/config.yaml
```

Do not paste either value into an issue.

### Copilot models do not appear

Check plugin registration:

```bash
MGMT=$(sed -n 's/^MANAGEMENT_PASSWORD=//p' .runtime/secrets.env)

curl -fsS \
  -H "Authorization: Bearer $MGMT" \
  http://127.0.0.1:8317/v0/management/plugins
```

The `cliproxyapi-copilot` plugin should be registered and enabled. Re-run Copilot
device login if no Copilot auth file exists.

### Claude Code does not show all models

Confirm this setting is present:

```json
"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1"
```

Then restart Claude Code and use `/model` (singular).

### Inspecting full proxy errors

Production defaults disable debug request logging because logs can contain
prompts. For temporary troubleshooting, set these values in
`.runtime/config.yaml`:

```yaml
debug: true
logging-to-file: true
logs-max-total-size-mb: 100
```

Restart the isolated container and inspect `logs/`. Return the settings to
`false` and remove sensitive logs afterward.

## Security notes

- The plugin is a trusted in-process shared library. Review and build it from
  source.
- Keep port `8317` bound to `127.0.0.1` unless TLS, network controls, and
  stronger operational protections are added.
- Never commit `.runtime/`, OAuth files, logs, callback URLs, API keys, or
  management passwords.
- The Docker image and Go dependency are version-pinned, but a tag is not as
  immutable as a digest. Pin the verified image digest for stricter
  supply-chain control.
- Use only accounts and subscriptions you are authorized to access.
