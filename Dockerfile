FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/outline-sync .

FROM alpine:3
RUN apk add --no-cache git openssh-client ca-certificates \
 && git config --system --add safe.directory '*'
WORKDIR /app
COPY --from=builder /out/outline-sync /usr/local/bin/outline-sync
# Same paths as the Python image: /app/config.yml and /app/repos
VOLUME /app/repos
CMD ["outline-sync"]
