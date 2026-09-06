# syntax=docker/dockerfile:1.7
FROM golang:1.27-alpine AS build
WORKDIR /src
ARG VERSION=0.2.0
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go test ./... && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/zyvorai/nodra/internal/version.Version=${VERSION} -X github.com/zyvorai/nodra/internal/version.Commit=${COMMIT} -X github.com/zyvorai/nodra/internal/version.BuildDate=${BUILD_DATE}" -o /out/nodra-server ./cmd/nodra-server && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/zyvorai/nodra/internal/version.Version=${VERSION} -X github.com/zyvorai/nodra/internal/version.Commit=${COMMIT} -X github.com/zyvorai/nodra/internal/version.BuildDate=${BUILD_DATE}" -o /out/nodrad ./cmd/nodrad && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/zyvorai/nodra/internal/version.Version=${VERSION} -X github.com/zyvorai/nodra/internal/version.Commit=${COMMIT} -X github.com/zyvorai/nodra/internal/version.BuildDate=${BUILD_DATE}" -o /out/nodractl ./cmd/nodractl

FROM alpine:3.22
RUN addgroup -S -g 65532 nodra && adduser -S -D -H -u 65532 -G nodra nodra && apk add --no-cache ca-certificates
COPY --from=build /out/nodra-server /usr/local/bin/nodra-server
COPY --from=build /out/nodrad /usr/local/bin/nodrad
COPY --from=build /out/nodractl /usr/local/bin/nodractl
RUN mkdir -p /var/lib/nodra && chown -R nodra:nodra /var/lib/nodra
USER 65532:65532
VOLUME ["/var/lib/nodra"]
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/nodra-server"]
