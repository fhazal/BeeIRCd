# BeeIRCd test server. Build the binary first:
#   GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o beeircd ./cmd/beeircd
FROM scratch
COPY beeircd /beeircd
USER 65534:65534
ENTRYPOINT ["/beeircd", "run", "-config", "/data/beeircd.conf"]
