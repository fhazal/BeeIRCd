# BeeIRCD test server for the homelab. The binary is built on the dev PC:
#   GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o beeircd ./cmd/beeircd
FROM scratch
COPY beeircd /beeircd
USER 65534:65534
ENTRYPOINT ["/beeircd", "run", "-config", "/data/beeircd.conf"]
