# Cable

Cable is an open-source control plane and player runtime for sending media to physical screens.

The first supported path is a Raspberry Pi-class Linux player controlled through a stable HTTP API. Cable publishes immutable signed releases, assigns them to players, streams or caches content-addressed media, supervises playback, and reports what is actually running.

## Status

Early foundation work. No stable release or API compatibility guarantee yet.

## Development

Requires Go 1.27.

```sh
go fmt ./...
go vet ./...
go test ./...
./scripts/check.sh
```

The control web application, cable-style screen application, phone input, and additional player platforms are intentionally later work.

## License

Apache-2.0.
