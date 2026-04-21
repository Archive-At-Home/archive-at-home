FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o /node ./cmd/node

FROM alpine:3.19
RUN apk add --no-cache ca-certificates

COPY --from=builder /node /node

# Create data directory for SQLite
RUN mkdir -p /data

WORKDIR /
EXPOSE 8090
ENTRYPOINT ["/node"]
