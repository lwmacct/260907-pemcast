# syntax=docker/dockerfile:1.7

FROM alpine:3.22

WORKDIR /app/data

RUN apk add --no-cache bash ca-certificates tini tzdata

COPY bin/pemcast /usr/local/bin/pemcast
COPY config/config.example.yaml /app/data/config/config.example.yaml

ENTRYPOINT ["tini", "--"]
CMD ["pemcast", "version"]
