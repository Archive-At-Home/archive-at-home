FROM alpine
RUN apk add --no-cache ca-certificates

ARG TARGETARCH

COPY bin/${TARGETARCH}/archive-at-home-node /node

RUN mkdir -p /data

WORKDIR /
EXPOSE 8090
ENTRYPOINT ["/node"]
