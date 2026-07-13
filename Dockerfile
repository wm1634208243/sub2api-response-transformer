FROM golang:1.26.4-alpine AS builder

WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/transformer .

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/transformer /transformer
USER 65532:65532
HEALTHCHECK --interval=15s --timeout=5s --start-period=5s --retries=3 \
  CMD ["/transformer", "-healthcheck", "http://127.0.0.1:8888/transformer/health"]
ENTRYPOINT ["/transformer"]
CMD ["-config", "/etc/transformer/config.json"]
