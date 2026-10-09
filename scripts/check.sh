#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

test -z "$("$(go env GOROOT)/bin/gofmt" -l .)"
go vet ./...
go test ./...

rm -rf bin
mkdir -p bin/linux-amd64 bin/linux-arm64
for command in cuelight-control cuelight-player cuelight cuelight-node-sim; do
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "bin/linux-amd64/$command" "./cmd/$command"
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "bin/linux-arm64/$command" "./cmd/$command"
done
