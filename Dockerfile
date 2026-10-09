# cuelight-control: one binary (API + embedded controller-web and website-node)
# that needs only PostgreSQL. See deploy/compose/ for a ready-to-run stack.
#
#   docker build -t cuelight-control .
#   docker run --rm cuelight-control --version

FROM node:24-alpine AS web
WORKDIR /src
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml tsconfig.base.json VERSION ./
COPY apps/controller-web/package.json apps/controller-web/
COPY apps/website-node/package.json apps/website-node/
COPY packages/api-client/package.json packages/api-client/
COPY packages/controller-ui/package.json packages/controller-ui/
RUN pnpm install --frozen-lockfile --filter @cuelight/controller-web... --filter @cuelight/website-node...
COPY apps/controller-web apps/controller-web
COPY apps/website-node apps/website-node
COPY packages packages
COPY api api
RUN pnpm --filter @cuelight/controller-web --filter @cuelight/website-node build

FROM golang:1.27-alpine AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/apps/controller-web/dist internal/control/webui/assets/controller-web
COPY --from=web /src/apps/website-node/dist internal/control/webui/assets/website-node
ARG CUELIGHT_COMMIT=""
RUN version=$(cat VERSION) && CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/jmilldotdev/cuelight/internal/buildinfo.Version=${version} -X github.com/jmilldotdev/cuelight/internal/buildinfo.Commit=${CUELIGHT_COMMIT}" \
      -o /out/cuelight-control ./cmd/cuelight-control

FROM alpine:3.22
RUN addgroup -S -g 10001 cuelight-control \
 && adduser -S -D -H -u 10001 -G cuelight-control -h /var/lib/cuelight-control cuelight-control \
 && install -d -o cuelight-control -g cuelight-control -m 0700 /var/lib/cuelight-control
COPY --from=go /out/cuelight-control /usr/local/bin/cuelight-control
USER cuelight-control
WORKDIR /var/lib/cuelight-control
VOLUME /var/lib/cuelight-control
EXPOSE 8443
# Liveness only; TLS names vary per install, so the certificate is not checked.
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s \
  CMD wget -q --no-check-certificate -O /dev/null https://127.0.0.1:8443/health || exit 1
ENTRYPOINT ["cuelight-control"]
CMD ["serve"]
