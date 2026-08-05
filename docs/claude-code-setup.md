# Route Claude Code through Claude and Copilot subscriptions

This guide deploys the official CLIProxyAPI with this repository's GitHub
Copilot plugin, authenticates both a Claude subscription and a GitHub Copilot
subscription, and configures Claude Code to use both through one local endpoint.

The resulting path is:

```text
Claude Code
    |
    | Anthropic Messages API
    v
official CLIProxyAPI
    |-- built-in Claude OAuth ------> Anthropic subscription
    `-- cliproxyapi-copilot plugin -> GitHub Copilot subscription
                                      (OpenAI Responses or Chat Completions)
```

CCR is not required. The plugin translates Claude Messages requests to the
Copilot endpoint supported by each model and translates responses back to
Claude Code.

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

## 1. Clone and bootstrap

```bash
cd "$HOME"
git clone https://github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin.git
cd cliproxyapi-copilot-plugin

scripts/bootstrap.sh
make build
scripts/up.sh
scripts/status.sh
```

Bootstrap generates two local secrets:

- A CLIProxyAPI management password
- An API key used by Claude Code

They are stored in `.runtime/secrets.env` with mode `0600` and are ignored by
Git. OAuth credentials are stored in the Docker volume
`cliproxyapi_official_copilot_dev_home`.

Open the management center:

```text
http://127.0.0.1:8317/management.html
```

Retrieve the management password when the UI asks for it:

```bash
sed -n 's/^MANAGEMENT_PASSWORD=//p' .runtime/secrets.env
```

## 2. Authenticate GitHub Copilot

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

## 3. Authenticate the Claude subscription

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

## 4. Verify models and inference

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

The list should include models from both subscriptions, including:

- `gpt-5.6-sol`
- `gpt-5.6-terra`
- `claude-fable-5`
- `claude-opus-5`
- `claude-sonnet-5`

The included configuration excludes `claude-*` from the Copilot plugin's
catalog. This prevents duplicate Claude model IDs from being scheduled through
Copilot instead of CLIProxyAPI's native Claude subscription provider.

Test Copilot through the Responses API:

```bash
curl -fsS http://127.0.0.1:8317/v1/responses \
  -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.6-sol","input":"Reply exactly: copilot-ok","max_output_tokens":16}'
```

Test Claude through the Messages API:

```bash
curl -fsS http://127.0.0.1:8317/v1/messages \
  -H "x-api-key: $API_KEY" \
  -H 'anthropic-version: 2023-06-01' \
  -H 'Content-Type: application/json' \
  -d '{"model":"claude-sonnet-5","max_tokens":16,"messages":[{"role":"user","content":"Reply exactly: claude-ok"}]}'
```

## 5. Configure Claude Code globally

The repository includes `scripts/api-key-helper.sh`, which reads the generated
API key without copying it into `~/.claude/settings.json`.

Make sure it is executable:

```bash
chmod 755 "$HOME/cliproxyapi-copilot-plugin/scripts/api-key-helper.sh"
```

If Claude Code settings already exist, back them up:

```bash
test ! -f "$HOME/.claude/settings.json" ||
  cp "$HOME/.claude/settings.json" "$HOME/.claude/settings.json.backup"
```

Merge the following values into `~/.claude/settings.json`. Replace the helper
path if the repository was cloned elsewhere:

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:8317",
    "ANTHROPIC_MODEL": "gpt-5.6-sol",
    "ANTHROPIC_DEFAULT_FABLE_MODEL": "claude-fable-5",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "gpt-5.6-sol",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "claude-sonnet-5",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "gpt-5.6-terra",
    "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1"
  },
  "apiKeyHelper": "/home/YOUR_USER/cliproxyapi-copilot-plugin/scripts/api-key-helper.sh",
  "model": "gpt-5.6-sol"
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

| Claude Code selection | Routed model | Subscription |
| --- | --- | --- |
| Default | `gpt-5.6-sol` | GitHub Copilot |
| Opus | `gpt-5.6-sol` | GitHub Copilot |
| Haiku | `gpt-5.6-terra` | GitHub Copilot |
| Sonnet | `claude-sonnet-5` | Claude |
| Fable | `claude-fable-5` | Claude |

Select aliases with `/model` or at launch:

```bash
claude --model opus
claude --model haiku
claude --model sonnet
claude --model fable
```

Test the global default non-interactively:

```bash
claude -p 'Reply exactly: global-routing-ok' \
  --max-turns 1 \
  --output-format json
```

## 6. Operations

Check health:

```bash
scripts/status.sh
```

Rebuild after updating the repository:

```bash
git pull
make test
make build
docker restart cliproxyapi-official-copilot-dev
scripts/status.sh
```

Stop the service without deleting OAuth credentials:

```bash
scripts/down.sh
```

Permanently remove this deployment and its credentials:

```bash
scripts/down.sh
docker volume rm cliproxyapi_official_copilot_dev_home
rm -rf .runtime build .cache logs
```

## Troubleshooting

### `401 {"error":"Invalid API key"}`

- Exit all existing Claude Code sessions and relaunch.
- Remove stale `ANTHROPIC_AUTH_TOKEN` and `ANTHROPIC_API_KEY` values.
- Confirm the helper and runtime configuration resolve to the same key:

```bash
scripts/api-key-helper.sh
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
