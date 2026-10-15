# CLIProxyAPI GitHub Copilot plugin

Licensed under the [MIT License](LICENSE).

For an end-to-end deployment and Claude Code configuration walkthrough, see
[`docs/claude-code-setup.md`](docs/claude-code-setup.md).

To add the plugin and its required paired host to an existing CLIProxyAPI installation, see
[`docs/install-existing-deployment.md`](docs/install-existing-deployment.md).

For feature-by-feature implementation, validation and external acceptance limits, see
[`docs/feature-completion.md`](docs/feature-completion.md).

GitHub Copilot subscription plugin built against the official `router-for-me/CLIProxyAPI` v8.0.30 SDK and plugin ABI version 1.
Runtime requests require the paired maintained host selected by `go.mod`.
The official v8.0.30 server lacks the required `host.payload.finalize` callback.
The repository also defines a strictly isolated Docker deployment that retains CLIProxyAPI's built-in Claude subscription OAuth support.

This stack uses only:

- container/project: `cliproxyapi-official-copilot-dev`
- host address: `127.0.0.1:8317`
- auth volume: `cliproxyapi_official_copilot_dev_home`
- repository-local config and plugin bind mounts
- image: `eceasy/cli-proxy-api:v8.0.30`

It does not map ports 3458 or 54545 on the host.

## Architecture

`cmd/cliproxyapi-copilot` implements ABI version 1 and registration schema 6 using
the official `sdk/pluginabi` and `sdk/pluginapi` contracts. It registers:

- `AuthProvider`: GitHub device-code OAuth and host-owned credential storage
- `ModelProvider`: authenticated discovery from the Copilot `/models` endpoint
- `ProviderExecutor`: non-streaming, SSE streaming, and restricted provider HTTP

The provider packages are intentionally separated:

- `internal/provider`: OAuth, storage, Copilot token exchange/cache, models,
  endpoint selection, and execution
- `internal/translate`: official translator SDK integration and missing protocol routes
- `internal/transport`: host HTTP/stream callback abstraction
- `internal/sse`: chunk-safe SSE framing
- `internal/redact`: bounded, token-redacting error text

Chat Completions, Responses, and Messages requests can use Copilot Chat Completions, Responses, or Messages endpoints.
The plugin supports nine routes between the three client formats and three Copilot endpoints.
The plugin selects an endpoint from model metadata.
Set `model_endpoint_overrides` when a model needs a fixed route.
Configured override IDs are registered for the authenticated account even when the Copilot catalog omits them.
The host can select the configured route, but Copilot availability and model capabilities remain unverified.
An override does not make a catalog model with a known non-enabled policy available.
The plugin preserves provider-native reasoning state for same-format Messages and Responses requests.
The plugin preserves tool and reasoning correlation across cross-format turns.
The plugin stores an authenticated replay carrier with the Copilot credential.
It accepts the carrier only for the same account, model, and endpoint.
The plugin rejects foreign signed or encrypted reasoning on Copilot Chat with HTTP 422.
Send full caller history to Copilot Chat or Messages because the plugin does not store conversations.
Send full caller history for cross-format requests because the bridge cannot reconstruct it from `previous_response_id` alone.
Use `previous_response_id` only with a native Copilot Responses endpoint that supports it.
The plugin derives `prompt_cache_key` for Copilot Responses requests when a stable session identity exists.
Set `support-prompt-cache-key: false` in the plugin configuration to omit generated keys.
The plugin preserves explicit caller keys.
Copilot's implicit cache lifetime can differ from Codex's.
Responses compaction is optional and requires explicit configuration for the model.
The official CLIProxyAPI v8.0.30 host supports both plugin compaction routes.
Claude token-count requests are estimated locally with the same O200k tokenizer
approach used by CLIProxyAPI for translated Claude requests.
The deployment lists model exclusions under CLIProxyAPI's native `oauth.excluded-models.copilot` setting.
CLIProxyAPI applies exact model IDs and `*` wildcard patterns to the Copilot catalog.
The template sets `gpt-6.1-sol` and `gpt-6-luna` context limits to 272000 through `oauth.settings.copilot`.
Native model aliases accept hyphenated Claude versions while preserving Copilot's original dotted model IDs upstream.

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
volume.
The default `token_exchange` mode obtains a short-lived token from
`https://api.github.com/copilot_internal/v2/token`.
The plugin caches that token only in process memory, refreshes it before expiry,
and never deliberately logs it.

Set `auth_mode: direct_oauth` in the plugin configuration to use the stored
GitHub OAuth access token directly.
For example, add this field under the Copilot plugin configuration:

```yaml
auth_mode: direct_oauth
```

Direct OAuth sends a bearer token to
`{github_api_url}/copilot_internal/user` and requires its `endpoints.api` value.
The plugin uses that discovered API origin for catalog and inference requests.
It does not call the v2 token endpoint or substitute `copilot_api_url` when the
account response lacks an API endpoint.
Direct OAuth uses the plugin's own user agent without editor or Copilot Chat
integration headers.
The `model_cache_ttl_seconds` setting bounds the in-memory discovery and model
catalog caches.
Response metadata includes `token_expires_at` only when auth storage contains a
known `expires_at` value.
The discovery cache lifetime does not represent credential expiry.
Direct OAuth returns upstream HTTP 401 and 403 responses after invalidating the
cached authentication and model context, without retrying the request.

The default `token_exchange` mode uses the recognized VS Code Copilot
integration headers because Copilot rejects unrecognized
`Copilot-Integration-Id` values.

## Build and test

Use Docker with Compose v2, Go 1.27.2 or newer, and Python 3.9 or newer for `make test`.
The production plugin build uses `golang:1-bookworm`, matching the Debian Bookworm runtime of the official image.

```sh
make test
make build
make prepare-runtime-host
make test-native
```

For opt-in calls against real Copilot models and isolated Claude Code/Codex client checks, see
[`docs/live-copilot-validation.md`](docs/live-copilot-validation.md).

`make test-native` and `make prepare-runtime-host` prepare a CLIProxyAPI host from the version or maintained-fork replacement selected in `go.mod` and compile the integration test executable.
Set `NATIVE_HOST_SOURCE` to a readable local CLIProxyAPI checkout to use unreleased host changes.
Relative paths are resolved from this repository's root.
Container preparation mounts that checkout read-only at `/native-host-source`, then verifies its module identity.
For example:

```sh
NATIVE_HOST_SOURCE=../CLIProxyAPI make test-native
NATIVE_HOST_SOURCE="../CLIProxyAPI checkout" make prepare-runtime-host
```

When `NATIVE_HOST_SOURCE` is unset, preparation uses the published host source selected in `go.mod`.
Preparation may download dependencies.
The plugin uses the official v8.0.30 SDK and ABI. Payload-rule finalization requires
the published maintained host replacement selected in `go.mod`; the official
v8.0.30 release does not provide that callback. The development Compose file still
defaults to the official image as its runtime base. `make prepare-runtime-host`
builds the selected host and mounts it over the image's server binary, so the default
development stack runs the maintained host while reusing the official image runtime.
Set `CLI_PROXY_API_IMAGE` only when you intentionally want to change the runtime base.
The runtime loads the built plugin and uses committed synthetic seeds under `integration/testdata/native/v1`.
Linux runs the prepared binaries in a container with `--network=none` and only loopback available.
The fixtures require no Copilot login.
Missing artifacts or seed files fail the required lane.
Failed native runs preserve their temporary request and response logs; set
`NATIVE_HOST_KEEP_ARTIFACTS=1` to retain them after a successful run as well.

To test a prebuilt plugin for the local platform, prepare the host and test executable before running the fixtures.

```sh
integration/prepare-native-host.sh
integration/test-native-host.sh
```

macOS requires `sandbox-exec` to run the prepared fixtures with only loopback networking and an isolated child environment.
The runner fails if the sandbox tool or profile is unavailable.

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
The release archive includes `cliproxyapi-copilot.host-requirements.json`,
which records the required `host.payload.finalize` callback and Go's selected
versioned host module source. The plugin loader does not consume this metadata.
Deploy the paired maintained host module recorded in the JSON; the official
v8.0.30 server does not provide that callback.

## CI and releases

Every push and pull request runs the Go and release-metadata tests and builds a production-compatible
Linux `amd64` marketplace package. Pushes do not publish releases.

CI and release builds run the native host fixtures against the packaged plugin before publishing artifacts.
The suite covers all nine client-to-endpoint routes over JSON and SSE, original tool IDs, reasoning replay, native model settings, and compaction.
Canonical model cases use the checked-in deployment template for normal summary requests, configured compaction, and plugin Responses WebSocket calls.

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

The ZIP contains `cliproxyapi-copilot.so` and `cliproxyapi-copilot.host-requirements.json` at its root.
The shared library matches the official CLIProxyAPI Plugins Store requirements.
The JSON records the required host callback and paired host module source.

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

make prepare-runtime-host
docker compose --env-file .runtime/secrets.env up -d
```

`make prepare-runtime-host` builds both the plugin and its paired CLIProxyAPI
host. The plugin uses the official v8.0.30 SDK and ABI, while payload-rule
finalization requires the published maintained host module selected by the
`go.mod` replacement. Compose overlays that built host on the official runtime
image; the official v8.0.30 server alone does not implement the required
callback.

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
  -d '{"model":"gpt-6-luna","input":"Reply with ok."}'
```

Claude Messages request:

```sh
curl http://127.0.0.1:8317/v1/messages \
  -H "x-api-key: $API_KEY" \
  -H 'anthropic-version: 2023-06-01' \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-6-luna","max_tokens":32,"messages":[{"role":"user","content":"Reply with ok."}]}'
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
  ANTHROPIC_MODEL="gpt-6-luna" \
  ANTHROPIC_DEFAULT_FABLE_MODEL="claude-fable-5-1" \
  ANTHROPIC_DEFAULT_OPUS_MODEL="gpt-6-luna" \
  ANTHROPIC_DEFAULT_HAIKU_MODEL="gpt-6-luna" \
  CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1 \
  claude --setting-sources ""
```

Append normal Claude Code arguments to the last line, for example
`--model opus`, `--model haiku`, or `--model claude-sonnet-5-5`.

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

Tests cover all nine client-to-endpoint combinations over JSON and SSE.
The bridge maps supported text, tool calls/results, reasoning state, usage, stop reasons, and base64/URL images.
It rejects unsupported content and foreign signed or encrypted reasoning sent through Copilot Chat with HTTP 422.
Copilot can issue opaque tool item IDs longer than 64 characters.
The bridge preserves those IDs and lets Copilot validate native requests.
Use a matching native protocol endpoint for content the bridge cannot translate.

Responses history can replay a completed Claude-compatible web search through Messages.
The bridge reuses the SDK conversion and preserves the server-tool ID, exact query, result content, and opaque replay tokens.
An explicit empty result list and a native search-error object retain their original shapes.
Native Responses identities, additional action state, incomplete searches, and missing result content fail with HTTP 422 before inference.
Keep richer provider search history on its native Responses endpoint.

These replay checks use synthetic fixtures and retained sanitized provider captures.
They do not establish live Claude WebSearch support, correct result/citation URL pairing, PNG answer quality, or original-client `response.interrupt` emission.

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
