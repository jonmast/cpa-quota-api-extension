# CPA Quota API Extension

A native [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) plugin that exposes sanitized, machine-readable quota data for the full credential pool through authenticated HTTP endpoints.

Use it to monitor CLIProxyAPI accounts from external dashboards and automation such as:

- [Homepage](https://gethomepage.dev/) / `gethomepage`
- [Übersicht](https://tracesof.net/uebersicht/)
- Grafana JSON/API data sources
- Home Assistant REST sensors
- health-check scripts, alerting jobs, and internal monitoring services

The plugin runs inside CLIProxyAPI. It does not start another daemon or open another port.

```text
Dashboard / monitoring client
        │
        │ HTTPS + CLIProxyAPI management key
        ▼
CLIProxyAPI :8317
        │
        └── CPA Quota API Extension
              ├── enumerates the credential pool
              ├── queries supported provider quota APIs
              ├── caches results for 30 minutes
              └── returns sanitized JSON
```

## Features

- Exports every eligible physical credential through a versioned JSON API.
- Supports Codex, Gemini CLI, Antigravity, and Claude OAuth quota sources.
- Keeps unsupported providers visible instead of silently dropping them.
- Refreshes quota only when requested; no cron job or background polling.
- Caches snapshots for 30 minutes by default.
- Deduplicates concurrent refreshes so multiple dashboards share one upstream scan.
- Isolates provider failures per account instead of failing the whole response.
- Supports provider/status filtering and cursor pagination.
- Never returns access tokens, refresh tokens, ID tokens, cookies, or raw upstream response bodies.

## Provider support

| Provider | Upstream quota source | Exported data |
|---|---|---|
| Codex | `chatgpt.com/backend-api/wham/usage` | Primary and secondary usage windows, reset times, plan |
| Gemini CLI | `cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota` | Per-model quota buckets and reset times |
| Antigravity | `cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels` | Per-model remaining quota and reset times |
| Claude OAuth | `api.anthropic.com/api/oauth/usage` | Five-hour and seven-day usage windows |

Every eligible physical runtime credential appears in the exported inventory. Runtime-only records, records without an `auth_index`, and disabled credentials (unless `include-disabled: true`) are excluded from quota scans. Credentials for other providers remain visible with `supported: false` and `status: "unsupported"`.

## Requirements

- CLIProxyAPI `v7.2.61` or a compatible later release.
- A plugin-capable CLIProxyAPI build with CGO support.
- `plugins.enabled: true` in CLIProxyAPI configuration.
- A configured CLIProxyAPI management key.
- Go 1.24 or later and a working C compiler when building from source.

Official CLIProxyAPI `_no-plugin` builds use `CGO_ENABLED=0` and cannot load native dynamic-library plugins. The current build and production verification target is Linux AMD64; other platforms require a native build and platform-specific testing.

## Let an AI agent install it for you

If you do not want to perform the installation manually, give this repository to a coding or operations AI agent that has access to your CLIProxyAPI host:

```text
Install CPA Quota API Extension from:
https://github.com/dinhkarate/cpa-quota-api-extension

Before changing anything, inspect my CLIProxyAPI version, build variant, operating
system, architecture, service manager, plugin directory, and current configuration.
Confirm that the CLIProxyAPI build supports native plugins. Back up the existing
configuration, then install the plugin, enable and configure it, and restart
CLIProxyAPI with minimal downtime.

Do not print or expose my management key, OAuth credentials, access tokens, refresh
tokens, ID tokens, cookies, or auth files. Do not disable or remove existing plugins
or legacy services unless I explicitly ask you to.

After installation, verify the plugin is registered and call the authenticated
/status, /accounts, and /quotas endpoints. Confirm cache behavior, check service logs
for load errors or crashes, and roll back if CLIProxyAPI becomes unhealthy. Report
the installed artifact path and SHA-256, configuration changes, backup path, endpoint
status codes, and sanitized quota totals only.
```

Review the AI agent's plan before allowing production changes, and give it credentials through a secure secret mechanism rather than pasting them into source files or public logs.

## Installation

### Build from source

```bash
git clone --recurse-submodules https://github.com/dinhkarate/cpa-quota-api-extension.git
cd cpa-quota-api-extension
make verify-upstream
make test
make build
```

The Linux artifact is written to:

```text
dist/cpa-quota-api-extension.so
```

Copy it into the CLIProxyAPI platform plugin directory:

```bash
install -m 0755 dist/cpa-quota-api-extension.so \
  /path/to/CLIProxyAPI/plugins/linux/amd64/cpa-quota-api-extension.so
```

Then configure and restart CLIProxyAPI.

Verify registration after restart:

```bash
curl -fsS \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" \
  http://127.0.0.1:8317/v0/management/plugins/cpa-quota-api-extension/v1/status
```

The response should contain `"plugin_id":"cpa-quota-api-extension"`. The plugin also appears in `GET /v0/management/plugins` and in the Management Center plugin list.

### CLIProxyAPI configuration

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-quota-api-extension:
      enabled: true
      priority: 100
      cache-ttl: 30m
      request-timeout: 30s
      max-concurrency: 8
      include-disabled: false
```

## External monitoring API

The plugin extends CLIProxyAPI's existing Management API. It does not expose a separate unauthenticated service.

Base path:

```text
https://cpa.example.com/v0/management/plugins/cpa-quota-api-extension/v1
```

Authentication uses the normal CLIProxyAPI management key:

```http
Authorization: Bearer <MANAGEMENT_KEY>
```

`X-Management-Key: <MANAGEMENT_KEY>` is also accepted by CLIProxyAPI.

If the dashboard runs on another host, CLIProxyAPI must allow remote management requests:

```yaml
remote-management:
  allow-remote: true
  secret-key: "replace-with-a-strong-management-key"
```

CLIProxyAPI also accepts the `MANAGEMENT_PASSWORD` environment variable as its management credential. Restart CLIProxyAPI after changing these settings.

Prefer a private network, VPN, or authenticated TLS reverse proxy instead of exposing the Management API directly to the public internet. Leave `allow-remote: false` when the dashboard calls CPA through localhost or a same-host proxy.

### Pool quota snapshot

```http
GET /v0/management/plugins/cpa-quota-api-extension/v1/quotas
```

```bash
curl -fsS \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" \
  "https://cpa.example.com/v0/management/plugins/cpa-quota-api-extension/v1/quotas"
```

Useful query parameters:

| Parameter | Example | Purpose |
|---|---|---|
| `refresh` | `refresh=true` | Bypass a fresh cache once and scan upstream providers |
| `provider` | `provider=codex` | Return one provider |
| `status` | `status=available` | Return one normalized status |
| `limit` | `limit=200` | Page size, maximum 1000 |
| `cursor` | `cursor=MjAw` | Continue pagination |

Example summary:

```json
{
  "generated_at": "2026-07-27T10:40:34Z",
  "cache_ttl": "30m0s",
  "cached": true,
  "refresh_mode": "request-triggered",
  "summary": {
    "total": 43,
    "supported": 43,
    "available": 9,
    "exhausted": 6,
    "disabled": 0,
    "errors": 28,
    "by_provider": {
      "antigravity": 10,
      "claude": 1,
      "codex": 27,
      "gemini-cli": 5
    },
    "by_status": {
      "available": 9,
      "error": 28,
      "exhausted": 6
    }
  },
  "accounts": [],
  "page": {
    "count": 0,
    "total": 43
  }
}
```

Do not add `refresh=true` to routine dashboard polling. Let dashboards read the cached snapshot; use forced refresh only for explicit operator actions.

### Redacted account inventory

```http
GET /v0/management/plugins/cpa-quota-api-extension/v1/accounts
```

Returns redacted runtime credential metadata for coverage diagnostics. Raw credential JSON is never returned.

### Plugin status

```http
GET /v0/management/plugins/cpa-quota-api-extension/v1/status
```

Returns effective cache settings and current refresh state.

## Homepage / gethomepage example

Homepage's `customapi` widget can read nested values from the quota summary and send custom authentication headers.

```yaml
- AI Infrastructure:
    - CLIProxyAPI Quota:
        href: https://cpa.example.com
        description: OAuth account pool quota
        widget:
          type: customapi
          url: https://cpa.example.com/v0/management/plugins/cpa-quota-api-extension/v1/quotas?limit=1
          refreshInterval: 60000
          headers:
            Authorization: Bearer {{HOMEPAGE_VAR_CPA_MANAGEMENT_KEY}}
          mappings:
            - field: summary.total
              label: Accounts
              format: number
            - field: summary.available
              label: Available
              format: number
            - field: summary.exhausted
              label: Exhausted
              format: number
            - field: summary.errors
              label: Errors
              format: number
```

Set the secret in the Homepage container environment:

```yaml
environment:
  HOMEPAGE_VAR_CPA_MANAGEMENT_KEY: "replace-with-the-management-key"
```

Homepage replaces `{{HOMEPAGE_VAR_CPA_MANAGEMENT_KEY}}` in its configuration at runtime. A Docker secret can instead be mounted and referenced with Homepage's `HOMEPAGE_FILE_...` mechanism.

If Homepage and CLIProxyAPI share a private Docker network, use the internal CLIProxyAPI service name instead of a public hostname. Protect the Homepage deployment itself because its server-side widget proxy can access the management credential.

Homepage custom API documentation: <https://gethomepage.dev/widgets/services/customapi/>

## Übersicht example

Übersicht can execute a command on a schedule and render its JSON output. Store the management key outside the widget source:

```bash
mkdir -p ~/.config/cpa-quota-api
printf '%s' '<MANAGEMENT_KEY>' > ~/.config/cpa-quota-api/management-key
chmod 600 ~/.config/cpa-quota-api/management-key
```

Create a widget file such as `~/Library/Application Support/Übersicht/widgets/cpa-quota-api.jsx`:

```jsx
export const command = `
  curl -fsS \
    -H "Authorization: Bearer $(cat \"$HOME/.config/cpa-quota-api/management-key\")" \
    "https://cpa.example.com/v0/management/plugins/cpa-quota-api-extension/v1/quotas?limit=1"
`;

export const refreshFrequency = 60 * 1000;

export const render = ({ output, error }) => {
  if (error) return <div>CPA quota unavailable</div>;

  const data = JSON.parse(output);
  const quota = data.summary;

  return (
    <div>
      CPA: {quota.available} available · {quota.exhausted} exhausted · {quota.errors} errors
    </div>
  );
};
```

Übersicht documentation: <https://tracesof.net/uebersicht/>

## Security model

- Every plugin route inherits CLIProxyAPI Management API authentication.
- The management key grants access to other CLIProxyAPI management operations, not only quota reads. Give it only to trusted dashboards.
- Prefer HTTPS plus network restrictions, VPN, WireGuard, Tailscale, or a private Docker network.
- Do not embed the key in public client-side JavaScript or a publicly served static page.
- Responses exclude OAuth tokens, cookies, client secrets, and raw provider bodies.
- Upstream failures are reduced to an error category and HTTP status.
- The plugin is trusted in-process code; install only reviewed artifacts.

## How to appear in the CLIProxyAPI Plugin Store

The official [CLIProxyAPI Plugins Store](https://github.com/router-for-me/CLIProxyAPI-Plugins-Store) is a registry, not a binary host. A plugin appears in Management Center after its registry pull request is accepted.

Publication flow:

1. Set the plugin metadata to the exact intended release version, rebuild, and verify that the status endpoint reports the same version as the tag.
2. Create a stable GitHub release tag in `v<version>` form, such as `v0.1.0`.
3. Attach a platform zip named:

   ```text
   cpa-quota-api-extension_0.1.0_linux_amd64.zip
   ```

4. Put exactly one dynamic library at the zip root:

   ```text
   cpa-quota-api-extension.so
   ```

5. Attach `checksums.txt` using `sha256sum` format:

   ```text
   <sha256>  cpa-quota-api-extension_0.1.0_linux_amd64.zip
   ```

6. Open a pull request against `router-for-me/CLIProxyAPI-Plugins-Store` that adds this entry to `registry.json`:

   ```json
   {
     "id": "cpa-quota-api-extension",
     "name": "CPA Quota API Extension",
     "description": "Exports sanitized, provider-neutral quota data for the full CLIProxyAPI credential pool through authenticated Management API endpoints for dashboards and monitoring automation.",
     "author": "dinhkarate",
     "repository": "https://github.com/dinhkarate/cpa-quota-api-extension",
     "homepage": "https://github.com/dinhkarate/cpa-quota-api-extension",
     "license": "MIT",
     "tags": ["Management", "Usage", "Quota", "Monitoring", "API"]
   }
   ```

The pull request should link the release and show that the platform zip and `checksums.txt` exist. After the first registry entry is accepted, publishing a newer valid GitHub release is enough for CLIProxyAPI to discover plugin updates; the registry does not need a version edit for every release.

Official registry requirements: <https://github.com/router-for-me/CLIProxyAPI-Plugins-Store#release-requirements>

## Development

The official CLIProxyAPI repository is included as a Git submodule at `upstream/CLIProxyAPI` and pinned to the production compatibility baseline, `v7.2.61`.

```bash
git submodule update --init --recursive
make verify-upstream
make test
make build
```

To test a newer CLIProxyAPI release, update the submodule on a separate branch and verify the intended compatibility tag before changing the production baseline.

## Known limitations

- OAuth token refresh is not implemented yet. Expired credentials return account-level errors without failing the whole pool.
- Gemini CLI and Antigravity use internal Google endpoints that may change.
- Antigravity's reported `remainingFraction` may not always match generation-time `429` behavior.
- Claude's OAuth usage endpoint is aggressively rate-limited; the default 30-minute cache reduces calls.
- API-key providers often expose billing rather than subscription quota and remain unsupported until a reliable adapter is available.

## Documentation

- [HTTP API contract](docs/api.md)
- [Research and compatibility notes](docs/research.md)
- [Contributing](CONTRIBUTING.md)
