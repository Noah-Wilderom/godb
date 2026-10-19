FROM golang:alpine AS builder

ARG GODB_VERSION=dev

ENV GO111MODULE=on \
    CGO_ENABLED=0

WORKDIR /build

COPY . .

RUN go mod tidy

RUN go build --ldflags "-s -w -extldflags -static -X github.com/Noah-Wilderom/godb/cmd/godb/cmd.Version=${GODB_VERSION}" -o ./bin/godb ./cmd/godb

FROM alpine:latest

WORKDIR /var/lib/godb

COPY --from=builder /build/bin/godb /usr/local/bin/godb

ENTRYPOINT ["/usr/local/bin/godb"]
