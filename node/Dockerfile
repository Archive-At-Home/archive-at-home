FROM alpine

ARG TARGETARCH

COPY bin/${TARGETARCH}/archive-at-home-node /node

RUN mkdir -p /data

WORKDIR /
EXPOSE 8090
ENTRYPOINT ["/node"]
