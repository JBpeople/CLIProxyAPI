# CLIProxyAPI (JBpeople fork)

A practical fork of CLIProxyAPI focused on making **OpenAI-compatible upstream providers** easier to manage and actually usable without manually maintaining long model lists.

## What this fork changes

This fork adds a complete model discovery flow for `openai-compatibility` providers:

- **Automatic model discovery** from upstream `/v1/models`
- **Multi-key aggregation**: all `api-key-entries` are queried, results are merged and deduplicated
- **Model Sync management API**
  - `GET /v0/management/model-sync/status`
  - `POST /v0/management/model-sync/run`
- **Dynamic model registration into auth/routing**
  - discovered models can participate in auth selection
  - providers can work even when `openai-compatibility[].models` is empty
- **OpenAI-compatible provider UI support**
  - `Auto discover models` toggle in the provider edit page
- **Management panel additions**
  - dedicated Model Sync page
  - manual sync trigger

## Why this fork exists

Upstream OpenAI-compatible providers change models frequently.
Manually maintaining every provider's `models:` section is tedious and error-prone.

This fork aims to make the workflow closer to this:

1. add an OpenAI-compatible provider
2. enable auto-discovery
3. sync models from `/v1/models`
4. let discovered models participate in routing directly

## Current behavior

- sync once on startup
- sync every **30 minutes** by default
- manual sync is available from the management page/API
- manually configured `models` still take priority when present
- when `models: []` is empty, discovered models are used as fallback for auth registration

## OpenAI-compatible config example

```yaml
openai-compatibility:
  - name: local-relay
    base-url: http://127.0.0.1:8317/v1
    auto-discover-models: true
    api-key-entries:
      - api-key: your-key-1
      - api-key: your-key-2
    models: []
```

## Management API added by this fork

```text
GET  /v0/management/model-sync/status
POST /v0/management/model-sync/run
```

## Management panel companion

The companion frontend fork is here:

- https://github.com/JBpeople/Cli-Proxy-API-Management-Center

It adds:

- Model Sync page
- Chinese UI adjustments
- auto-discover toggle in the OpenAI-compatible provider editor

## Build

### Linux
```bash
go build -o build/cliproxyapi ./cmd/server
```

### Windows
```bash
GOOS=windows GOARCH=amd64 go build -o build/cliproxyapi.exe ./cmd/server
```

## Status

This fork is intended for practical self-hosted use.
It keeps upstream CLIProxyAPI as the base, while focusing on better OpenAI-compatible provider ergonomics.

## License

MIT
