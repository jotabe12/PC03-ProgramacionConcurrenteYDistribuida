# Compilación multi-stage: binarios estáticos de Go sobre Alpine, usuario sin privilegios.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0 GOFLAGS=-mod=mod
RUN go build -trimpath -ldflags="-s -w" -o /out/coordinator ./cmd/coordinator \
 && go build -trimpath -ldflags="-s -w" -o /out/node ./cmd/node \
 && go build -trimpath -ldflags="-s -w" -o /out/loadtest ./cmd/loadtest

FROM alpine:3.20
RUN adduser -D -u 10001 app
WORKDIR /app
COPY --from=build /out/coordinator /out/node /out/loadtest /app/
USER app
EXPOSE 8080 9090
