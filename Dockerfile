FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY main.go .
COPY src/go/ ./src/go/

ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o go_reverse_http_cache ./main.go


FROM scratch

COPY --from=alpine:latest /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /app/go_reverse_http_cache /go_reverse_http_cache

EXPOSE 8161

ENTRYPOINT ["/go_reverse_http_cache"]
