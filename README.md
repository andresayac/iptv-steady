# IPTV Steady

Serves several Xtream accounts as one stable service. Players connect to a single server; when a provider stalls, drops a connection or loses a file, the proxy repairs the stream while the player keeps playing.

It is one Go binary with no database.

## What it does

- Merges every account into one catalog and removes duplicates.
- Filters categories and titles from the configuration. Adult content is blocked by default.
- Serves lists from memory, compressed and with light rows, so players open them quickly.
- Reads movies ahead of the player and resumes at the exact byte when a provider stalls or disconnects.
- Continues on another account when it holds the same file, verified byte by byte.
- Moves dead links and full or failing accounts out of the way.
- Releases provider connections as soon as a viewer leaves or pauses for long.
- Keeps the last good catalog of each account when a provider fails.
- Records every interruption and rescue.

## Quick start

```sh
cp .env.example .env              # accounts and viewer credentials
cp config.example.yaml config.yaml
go build -o iptv-steady ./cmd/iptv-steady
./iptv-steady sync               # download catalogs and print a summary
./iptv-steady serve
```

In the player choose Xtream Codes, with your server address and the viewer credentials from `.env`.

| Command | Purpose |
|---|---|
| `serve` | Start the proxy (default) |
| `sync` | Download every catalog once and print a summary |
| `report` | Summarize interruptions and rescues by hour, account and player |

`-config` selects the configuration file and `-days` the period covered by `report`.

## Configuration

`config.yaml`, copied from `config.example.yaml`, holds all behavior. Secrets live in `.env` and are referenced as `${NAME}`. Accounts are listed in order of preference. Neither file is committed.

| Section | Controls |
|---|---|
| `file` | Movies and episodes: read-ahead, stall detection, retries, recovery budget |
| `live` | Channels: stall detection and reconnection |
| `upstream` | Timeouts, account polling, circuit breaker |
| `catalog.filters` | What to hide |
| `catalog.merge` | Which sections merge duplicates; turn all off for a pure pass-through |
| `catalog.identity` | How titles are recognized as the same |
| `catalog.categories` | Category names, merging and order |
| `catalog.copy_traits` | Which copy of a title plays first |

Restart after changing the configuration, then run `sync` or wait for the next scheduled one.

## How a movie is rescued

1. The server downloads ahead of the player, up to `file.read_ahead` per stream.
2. If a provider sends nothing for `file.stall_timeout`, or crawls below `file.slow_min_rate`, the connection is dropped and reopened at the exact next byte. The player keeps consuming the buffer.
3. If that account keeps failing, playback continues on another account whose file has the same size and matches the last bytes delivered.
4. Without an identical copy, the proxy keeps trying for `file.recovery_budget` and then closes the stream, so the player shows an error instead of hanging forever.

A failed title never splices a different file into a stream, because the result would be corrupt.

## Limits

- A provider failure can only be hidden while another attempt succeeds. If every account fails at once, playback stops.
- Problems between this server and the viewer are outside its reach. On Linux, enabling BBR helps over long distances:
  `sysctl -w net.core.default_qdisc=fq net.ipv4.tcp_congestion_control=bbr`.
- Live channels reconnect and switch accounts, but have no buffer yet.
- Channels requested as HLS, or that answer a `.ts` request with an HLS playlist, are redirected to the provider. That exposes its address to the player and leaves those channels without account switching. Providers that only offer HLS should publish `sections: [vod, series]`.
- Identity is based on names, years and posters. The same film under unrelated names stays duplicated.
- The guide comes from the first account that answers. Catch-up is not supported.

## Scale

Built and tested for small private groups, about 4 concurrent viewers on one server. Each relayed stream uses server bandwidth and a bounded memory buffer; larger deployments need their own capacity planning and testing.

This software provides no content: you are responsible for your accounts and for your providers' terms and connection limits.

## Deployment

```sh
GOOS=linux GOARCH=amd64 go build -o iptv-steady ./cmd/iptv-steady
```

Copy the binary, `config.yaml` and `.env` to the server, for example to `/opt/iptv-steady`, and install `deploy/iptv-steady.service`. Put a reverse proxy in front for HTTPS, set `PUBLIC_URL` and bind `LISTEN` to localhost. Disable response buffering there, or the rescue buffer will not work as intended; `deploy/nginx.conf.example` shows how.

Behind Cloudflare, keep the DNS record unproxied so video does not pass through its CDN.

## Tests

```sh
go test -race ./...
```

`internal/testkit` provides a fake Xtream panel that fails on purpose: silence in the middle of a file, dropped connections, trickling data, dead links and connection limits.
