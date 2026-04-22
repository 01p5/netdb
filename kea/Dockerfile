FROM alpine:3.20
RUN apk add --no-cache \
        kea-dhcp4 \
        kea-ctrl-agent \
        kea-hook-lease-cmds \
        ca-certificates \
    && mkdir -p /run/kea /var/lib/kea /etc/kea

# Entrypoint is supplied per service via docker-compose `command:`.
# Typical: `kea-dhcp4 -c /etc/kea/kea-dhcp4.conf`
#      or `kea-ctrl-agent -c /etc/kea/kea-ctrl-agent.conf`
CMD ["kea-dhcp4", "-c", "/etc/kea/kea-dhcp4.conf"]
