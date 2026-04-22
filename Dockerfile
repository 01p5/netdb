FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags='-s -w' -o /out/netdb ./cmd/netdb

FROM gcr.io/distroless/static-debian12:latest
WORKDIR /data
COPY --from=builder /out/netdb /netdb
ENV NETDB_ADDR=:8080 \
    NETDB_DB=/data/netdb.sqlite
EXPOSE 8080
ENTRYPOINT ["/netdb"]
