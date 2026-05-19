FROM alpine
RUN apk add --no-cache ca-certificates

COPY archive-at-home-server /server

EXPOSE 8080
ENTRYPOINT ["/server"]