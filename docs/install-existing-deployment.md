# Install into an existing CLIProxyAPI deployment

This guide adds the GitHub Copilot plugin to an existing official CLIProxyAPI
deployment without replacing its configuration, API keys, or existing
providers. The plugin currently targets CLIProxyAPI `v7.2.118`, ABI version 1,
on Linux `amd64`.

## 1. Build the plugin

```bash
git clone https://github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin.git
cd cliproxyapi-copilot-plugin
make build
```

The resulting library is:

```text
build/plugins/linux/amd64/cliproxyapi-copilot.so
```

The filename is significant: CLIProxyAPI derives the plugin ID
`cliproxyapi-copilot` from it. Do not rename the library unless the matching key
under `plugins.configs` is also renamed.

## 2. Install the library

CLIProxyAPI searches both `<plugins.dir>/linux/amd64` and `<plugins.dir>`.
Using the platform-specific directory avoids loading an incompatible binary.

### Native CLIProxyAPI

Choose a permanent plugin directory and copy the library:

```bash
sudo install -d -m 0755 /opt/cliproxyapi/plugins/linux/amd64
sudo install -m 0755 \
  build/plugins/linux/amd64/cliproxyapi-copilot.so \
  /opt/cliproxyapi/plugins/linux/amd64/cliproxyapi-copilot.so
```

The CLIProxyAPI process must be able to read the library. Use a different
absolute directory if `/opt/cliproxyapi` does not match the deployment.

### Docker or Docker Compose

Copy the library into a persistent host directory:

```bash
install -d -m 0755 /path/to/cliproxyapi/plugins/linux/amd64
install -m 0755 \
  build/plugins/linux/amd64/cliproxyapi-copilot.so \
  /path/to/cliproxyapi/plugins/linux/amd64/cliproxyapi-copilot.so
```

Mount that directory into the existing container:

```yaml
services:
  cliproxyapi:
    volumes:
      - /path/to/cliproxyapi/plugins:/CLIProxyAPI/plugins:ro
```

Merge the mount into the existing service rather than replacing its current
configuration and auth-volume mounts.

## 3. Merge the plugin configuration

Add or merge this block in the existing `config.yaml`:

```yaml
plugins:
  enabled: true
  dir: "/opt/cliproxyapi/plugins" # Native deployment
  configs:
    cliproxyapi-copilot:
      enabled: true
      priority: 100
      github_client_id: "Iv1.b507a08c87ecfe98"
      github_scope: "read:user"
      github_base_url: "https://github.com"
      github_api_url: "https://api.github.com"
      copilot_api_url: "https://api.githubcopilot.com"
      oauth_timeout_seconds: 900
      model_cache_ttl_seconds: 600
      token_expiry_buffer_seconds: 300
```

For the Docker mount above, use:

```yaml
plugins:
  enabled: true
  dir: "/CLIProxyAPI/plugins"
```

There must be only one top-level `plugins` key. Preserve other entries already
present under `plugins.configs`. Global `plugins.enabled` and the individual
`cliproxyapi-copilot.enabled` setting must both be `true`.

The existing `auth-dir` must be writable and persistent. The plugin stores its
GitHub OAuth credential through CLIProxyAPI's normal auth storage; it does not
need a separate credential volume.

## 4. Restart and authenticate

Restart CLIProxyAPI using the deployment's normal service command. Examples:

```bash
sudo systemctl restart cliproxyapi
```

```bash
docker compose up -d --force-recreate cliproxyapi
```

The startup log should contain entries similar to:

```text
plugin loaded plugin_id=cliproxyapi-copilot
plugin registered plugin_id=cliproxyapi-copilot
```

Open the existing CLIProxyAPI management center, start the **Copilot** login,
and complete GitHub's device-code flow. The login uses the deployment's normal
management authentication and auth directory.

## 5. Verify the installation

Check plugin registration with the existing management password:

```bash
curl -fsS \
  -H "Authorization: Bearer $MANAGEMENT_PASSWORD" \
  http://127.0.0.1:8317/v0/management/plugins
```

After Copilot authentication, query the normal model endpoint with an existing
CLIProxyAPI client key:

```bash
curl -fsS \
  -H "Authorization: Bearer $API_KEY" \
  http://127.0.0.1:8317/v1/models
```

The result should include Copilot models such as `gpt-5.6-sol` and
`gpt-5.6-terra`. Existing Claude and other provider models remain available.

## Updating or removing the plugin

To update, build the new revision, replace only
`cliproxyapi-copilot.so`, and restart CLIProxyAPI.

To disable it without deleting credentials:

```yaml
plugins:
  configs:
    cliproxyapi-copilot:
      enabled: false
```

To remove it completely, stop CLIProxyAPI, delete the installed library, remove
only the `cliproxyapi-copilot` configuration entry, and restart. Delete the
plugin's Copilot auth entry through the normal management UI only if the stored
credential should also be revoked or removed.
