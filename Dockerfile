# syntax=docker/dockerfile:1.7
FROM golang:1.26.6-alpine3.23 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/durable-webhook-delivery ./cmd/durable-webhook-delivery \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/demo-receiver ./cmd/demo-receiver

FROM scratch AS runtime

WORKDIR /app
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/durable-webhook-delivery /app/durable-webhook-delivery
COPY --from=builder /out/demo-receiver /app/demo-receiver
COPY migrations /app/migrations

USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/app/durable-webhook-delivery"]
CMD ["serve"]
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=6 \
  CMD ["/app/durable-webhook-delivery", "healthcheck", "http://127.0.0.1:8080/health/live"]
