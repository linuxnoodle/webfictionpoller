FROM node:alpine AS css

WORKDIR /app

COPY tailwind.config.js input.css ./
COPY internal/handlers/templates/ ./internal/handlers/templates/

RUN npm init -y && npm install tailwindcss@3 @tailwindcss/typography && \
    npx tailwindcss -i input.css -o app.css --minify

FROM golang:alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=css /app/app.css ./internal/static/app.css

# Run comprehensive tests
RUN go test -v ./...

ARG VERSION_COMMIT=dev
RUN CGO_ENABLED=1 GOOS=linux go build \
    -ldflags "-X github.com/linuxnoodle/webfictionpoller/internal/version.BuildCommit=${VERSION_COMMIT}" \
    -o /webfiction_poller ./cmd/main.go

FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata docker-cli docker-cli-compose

WORKDIR /app

COPY --from=builder /webfiction_poller /app/webfiction_poller

EXPOSE 8080

CMD ["/app/webfiction_poller"]
