# syntax=docker/dockerfile:1.7

FROM alpine:3.22

WORKDIR /app/data

RUN apk add --no-cache bash ca-certificates jq tini tzdata

COPY --from=gcr.io/etcd-development/etcd:v3.7.2 /usr/local/bin/etcdctl /usr/local/bin/etcdctl
COPY bin/pemcast /usr/local/bin/pemcast
COPY scripts/publish-v5.sh /usr/local/bin/publish-v5.sh
COPY config/config.example.yaml /app/data/config/config.example.yaml

ENTRYPOINT ["tini", "--"]
CMD ["pemcast", "version"]
