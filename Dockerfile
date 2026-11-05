ARG GO_VERSION=1
FROM golang:${GO_VERSION}-bookworm AS builder

WORKDIR /usr/src/app
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN CGO_ENABLED=0 go build -v -o /run-app ./cmd/web/

FROM gcr.io/distroless/static-debian13

WORKDIR /app
COPY --from=builder /run-app /usr/local/bin/run-app
COPY --from=builder /usr/src/app/static ./static
COPY --from=builder /usr/src/app/views ./views
CMD ["/usr/local/bin/run-app"]
