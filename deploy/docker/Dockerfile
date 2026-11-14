# The BeeIRCd image: the one program on a small Alpine base (certificates,
# time zones, cron for the daily backup). Built by the release from the
# signed release programs:
#   docker buildx build --platform linux/amd64,linux/arm64 -f deploy/docker/Dockerfile .
# Everything that changes lives in the /data volume. docs/INSTALL.md "Docker".
FROM alpine:3.22

ARG TARGETARCH
RUN apk add --no-cache ca-certificates tzdata su-exec \
	&& addgroup -S -g 10001 beeircd \
	&& adduser -S -D -H -u 10001 -G beeircd -h /data -s /sbin/nologin beeircd \
	&& mkdir -p /data /run/beeircd /opt/beeircd \
	&& chown beeircd:beeircd /data /run/beeircd \
	&& echo "0 4 * * * /usr/local/bin/beeircd backup >/proc/1/fd/1 2>&1" > /etc/crontabs/root

COPY --chmod=755 dist/beeircd-linux-${TARGETARCH} /opt/beeircd/beeircd
COPY --chmod=755 deploy/docker/beeircd.sh /usr/local/bin/beeircd
COPY --chmod=755 deploy/docker/entrypoint.sh /usr/local/bin/entrypoint.sh

ENV BEEIRCD_CONTAINER=1
VOLUME /data
EXPOSE 6667 6697 7000 8787
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["run"]
