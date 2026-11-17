FROM golang:1.27.0-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY pkg ./pkg
COPY internal ./internal
COPY api ./api
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/Noah-Wilderom/godb/cmd/godb/cmd.Version=${VERSION}" \
    -o /out/godb ./cmd/godb

FROM alpine:3.24.0 AS certificates
RUN apk add --no-cache openssl
COPY deploy/docker/certificates.sh /usr/local/bin/certificates.sh
ENTRYPOINT ["sh", "/usr/local/bin/certificates.sh"]

FROM alpine:3.24.0 AS member
RUN addgroup -g 10001 godb && adduser -D -u 10001 -G godb godb \
    && mkdir -p /var/lib/godb && chown godb:godb /var/lib/godb
COPY --from=build /out/godb /usr/local/bin/godb
COPY deploy/docker/member.sh /usr/local/bin/member.sh
USER 10001:10001
WORKDIR /var/lib/godb
ENTRYPOINT ["sh", "/usr/local/bin/member.sh"]
