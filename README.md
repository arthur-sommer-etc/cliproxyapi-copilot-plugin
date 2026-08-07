# CLIProxyAPI GitHub Copilot plugin

Licensed under the [MIT License](LICENSE).

For an end-to-end deployment and Claude Code configuration walkthrough, see
[`docs/claude-code-setup.md`](docs/claude-code-setup.md).

To add only the plugin to an existing CLIProxyAPI installation, see
[`docs/install-existing-deployment.md`](docs/install-existing-deployment.md).

Initial, self-owned GitHub Copilot subscription provider for the official
`router-for-me/CLIProxyAPI` v7.2.118 plugin ABI. The repository also defines a
strictly isolated Docker deployment that retains CLIProxyAPI's built-in Claude
subscription OAuth support.

This stack uses only:

- container/project: `cliproxyapi-official-copilot-dev`
- host address: `127.0.0.1:8317`
- auth volume: `cliproxyapi_official_copilot_dev_home`
- repository-local config and plugin bind mounts
- image: `eceasy/cli-proxy-api:7.2.118`

It does not map ports 3458 or 54545 on the host.

Docker Hub currently publishes this release as `v7.2.118` rather than the
unprefixed tag required by this deployment. The setup guide documents pulling
the official `v7.2.118` image and creating a local equivalent tag when the
unprefixed image is absent. Compose remains pinned to
`eceasy/cli-proxy-api:7.2.118`.

## Architecture

`cmd/cliproxyapi-copilot` implements ABI version 1 and registration schema 2 using
the official `sdk/pluginabi` and `sdk/pluginapi` contracts. It registers:

- `AuthProvider`: GitHub device-code OAuth and host-owned credential storage
- `ModelProvider`: authenticated discovery from the Copilot `/models` endpoint
- `ProviderExecutor`: non-streaming, SSE streaming, and restricted provider HTTP

The provider packages are intentionally separated:

- `internal/provider`: OAuth, storage, Copilot token exchange/cache, models,
  endpoint selection, and execution
- `internal/translate`: official translator SDK integration plus the missing
  Claude Messages ↔ OpenAI Responses bridge
- `internal/transport`: host HTTP/stream callback abstraction
- `internal/sse`: chunk-safe SSE framing
- `internal/redact`: bounded, token-redacting error text

Claude input is accepted directly. Chat- or Messages-only Copilot models use
official built-in translators. Responses-only models use the custom Claude
bridge; `gpt-5.6-sol` and `gpt-5.6-terra` are always routed to `/responses`.
Claude token-count requests are estimated locally with the same O200k tokenizer
approach used by CLIProxyAPI for translated Claude requests.
Copilot model prefixes can be excluded from discovery to avoid collisions with
native providers; the included dual-subscription deployment excludes
`claude-*` so native Claude OAuth always owns those model IDs.

## Authentication and token handling

The default GitHub OAuth client ID is `Iv1.b507a08c87ecfe98`, the public client
identifier used by established Copilot device-flow clients. It is configurable
and is not a secret. No client secret is embedded or required.

The device flow uses:

- `https://github.com/login/device/code`
- `https://github.com/login/oauth/access_token`
- `https://api.github.com/user`

The GitHub access/refresh material is returned through CLIProxyAPI's
`AuthProvider` storage contract and is persisted only in the isolated auth
volume. The short-lived token obtained from
`https://api.github.com/copilot_internal/v2/token` is cached only in process
memory, refreshed before expiry, and never deliberately logged.

Copilot rejects unrecognized `Copilot-Integration-Id` values. Model discovery
and inference therefore use the recognized VS Code Copilot integration headers
(`vscode-chat` / `copilot-chat`) while authentication and credential storage
remain implemented by this plugin.

## Build and test

Requirements: Docker with Compose v2 and a local Go 1.26 toolchain for `make
test`. The production plugin build runs inside `golang:1.26-bookworm`, matching
the Debian Bookworm runtime used by the official image.

```sh
make test
make build
```

The loader artifact is:

```text
build/plugins/linux/amd64/cliproxyapi-copilot.so
```

`make build-local` exists for development, but a binary built on a newer host
glibc may not load in the Bookworm container.

## Existing CLIProxyAPI deployment

The plugin can be installed without using this repository's Compose stack.
Build `cliproxyapi-copilot.so`, place it under the deployment's configured
plugin directory, merge the `cliproxyapi-copilot` entry into
`plugins.configs`, and restart CLIProxyAPI. Native and Docker instructions,
including the complete configuration block, are in
[`docs/install-existing-deployment.md`](docs/install-existing-deployment.md).

## CI and releases

Every push and pull request runs the Go tests and builds a production-compatible
Linux `amd64` marketplace package. Pushes do not publish releases.

To publish a marketplace-compatible release, create and push a dotted numeric
version tag:

```sh
git tag v0.3.1
git push origin v0.3.1
```

The release workflow builds with the tag version embedded in plugin metadata
and publishes:

```text
cliproxyapi-copilot_0.3.1_linux_amd64.zip
checksums.txt
```

The ZIP contains only `cliproxyapi-copilot.so` at its root, matching the
official CLIProxyAPI Plugins Store requirements.

## Isolated deployment

There are no setup scripts; every step is an explicit documented command. The
complete walkthrough is in [`docs/claude-code-setup.md`](docs/claude-code-setup.md).
In short:

```sh
mkdir -p .runtime
umask 077
{
  printf 'MANAGEMENT_PASSWORD=%s\n' "$(openssl rand -hex 32)"
  printf 'CLIPROXYAPI_API_KEY=%s\n' "$(openssl rand -hex 32)"
} > .runtime/secrets.env
chmod 600 .runtime/secrets.env

sed "s/__CLIPROXYAPI_API_KEY__/$(sed -n 's/^CLIPROXYAPI_API_KEY=//p' .runtime/secrets.env)/g" \
  config/config.yaml > .runtime/config.yaml
chmod 600 .runtime/config.yaml

make build
docker compose --env-file .runtime/secrets.env up -d
```

This generates `.runtime/secrets.env` and `.runtime/config.yaml` with mode
0600. CLIProxyAPI does not expand environment variables in `api-keys`, so the
inert `__CLIPROXYAPI_API_KEY__` template is replaced locally. The management
key is passed through the officially supported `MANAGEMENT_PASSWORD`
environment variable. Generated files are ignored by Git.

The service binds `0.0.0.0` only inside its container. Docker publishes it only
on host loopback. `remote-management.allow-remote` is therefore enabled inside
the container because Docker bridge traffic is not seen as container-local;
the host port binding remains the external security boundary.

Open the management UI at:

```text
http://127.0.0.1:8317/management.html
```

Read a generated secret only when needed:

```sh
sed -n 's/^MANAGEMENT_PASSWORD=//p' .runtime/secrets.env
sed -n 's/^CLIPROXYAPI_API_KEY=//p' .runtime/secrets.env
```

### GitHub Copilot device login

Use the management UI's Copilot login action. It calls the plugin endpoint
`/v0/management/copilot-auth-url`; open the returned GitHub URL, approve the
displayed device code, and let the UI poll until the credential is saved.

Equivalent API flow:

```sh
MGMT=$(sed -n 's/^MANAGEMENT_PASSWORD=//p' .runtime/secrets.env)
curl -H "Authorization: Bearer $MGMT" \
  http://127.0.0.1:8317/v0/management/copilot-auth-url
# Open the returned URL. Then poll no faster than every five seconds:
curl -H "Authorization: Bearer $MGMT" \
  "http://127.0.0.1:8317/v0/management/get-auth-status?state=RETURNED_STATE"
```

No OAuth command runs during setup or container startup.

### Built-in Claude subscription login

CLIProxyAPI's native Anthropic provider is unchanged and uses the same isolated
auth volume. Start it from the management UI or
`/v0/management/anthropic-auth-url`.

Anthropic's fixed redirect is `localhost:54545`. This stack intentionally does
not map that host port. **Do not complete this flow in a browser on the current
host if port 54545 belongs to the existing deployment.** To keep that deployment
untouched, use a separate workstation/browser environment where localhost:54545
is unused, copy the final redirect URL after authorization, and submit it to the
new stack's official manual callback endpoint:

```sh
curl -X POST \
  -H "Authorization: Bearer $MGMT" \
  -H 'Content-Type: application/json' \
  -d '{"provider":"anthropic","redirect_url":"PASTE_FINAL_REDIRECT_URL"}' \
  http://127.0.0.1:8317/v0/management/oauth-callback
```

Then poll `/v0/management/get-auth-status?state=RETURNED_STATE`. This avoids
copying or reusing any existing Claude credential.

## Models and requests

After authenticating Copilot:

```sh
API_KEY=$(sed -n 's/^CLIPROXYAPI_API_KEY=//p' .runtime/secrets.env)
curl -H "Authorization: Bearer $API_KEY" \
  http://127.0.0.1:8317/v1/models
```

Responses request:

```sh
curl http://127.0.0.1:8317/v1/responses \
  -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.6-sol","input":"Reply with ok."}'
```

Claude Messages request:

```sh
curl http://127.0.0.1:8317/v1/messages \
  -H "x-api-key: $API_KEY" \
  -H 'anthropic-version: 2023-06-01' \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.6-terra","max_tokens":32,"messages":[{"role":"user","content":"Reply with ok."}]}'
```

The discovered catalog carries endpoint, context/output limits, tools, vision,
streaming, and reasoning metadata when GitHub returns it.

## Isolated Claude Code session

For the permanent global configuration (via `~/.claude/settings.json` and
`apiKeyHelper`), follow [`docs/claude-code-setup.md`](docs/claude-code-setup.md).

For an ad-hoc session that ignores global Claude settings entirely, export the
routing environment inline. It reads the isolated API key from
`.runtime/secrets.env` and points Claude Code at `http://127.0.0.1:8317`:

```sh
API_KEY=$(sed -n 's/^CLIPROXYAPI_API_KEY=//p' .runtime/secrets.env)

ANTHROPIC_BASE_URL="http://127.0.0.1:8317" \
  ANTHROPIC_AUTH_TOKEN="$API_KEY" \
  ANTHROPIC_MODEL="gpt-5.6-sol" \
  ANTHROPIC_DEFAULT_FABLE_MODEL="claude-fable-5" \
  ANTHROPIC_DEFAULT_OPUS_MODEL="gpt-5.6-sol" \
  ANTHROPIC_DEFAULT_HAIKU_MODEL="gpt-5.6-terra" \
  CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1 \
  claude --setting-sources ""
```

Append normal Claude Code arguments to the last line, for example
`--model opus`, `--model haiku`, or `--model claude-sonnet-5`.

## Threat model and trust boundary

- A Go shared-library plugin is trusted, in-process code. Review and build this
  repository before mounting its artifact.
- The plugin can make network requests only through CLIProxyAPI host callbacks;
  its generic executor HTTP method rejects destinations outside the authenticated
  Copilot API origin.
- Persistent OAuth material is confined to the new named volume. Generated API
  and management secrets remain under ignored `.runtime/`.
- Copilot tokens are memory-only. Error bodies are length-bounded and redact
  authorization headers, common GitHub token forms, and known token values.
- Debug/file logging is disabled by default because request logs may contain
  prompts. Host and Docker administrators remain inside the trust boundary.
- The Go dependency and image tag are version-pinned, but the Docker tag is not
  a digest pin. Verify the image digest if immutable supply-chain pinning is
  required.

## Current translation scope

Tests cover device polling decisions, redaction, endpoint selection, Claude
request conversion, chat and Responses conversion, and SSE translation. Text,
tool calls/results, common reasoning blocks, usage, stop reasons, and base64/URL
images are mapped.

This is an initial MVP. Less common Responses event types, provider-specific
reasoning signatures, citations/annotations, audio, computer-use blocks, and
all document variants are not exhaustively verified. Malformed tool arguments
and failed upstream Responses objects return errors rather than success-shaped
fallbacks.

## Stop, rollback, and removal

```sh
docker compose --env-file .runtime/secrets.env down
```

`down` retains the isolated OAuth volume. To permanently remove only this
new stack's credentials after it is down:

```sh
docker volume rm cliproxyapi_official_copilot_dev_home
rm -rf .runtime build .cache
```

The Compose file hard-codes the project, container, volume, and loopback-only
host port, so these commands cannot select containers from another deployment.
No existing deployment files, credentials, ports, or volumes are mounted or
referenced.
