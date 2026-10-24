FROM golang:1.26 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /video-worker ./cmd

FROM alpine:3.22
RUN apk add --no-cache ffmpeg
COPY --from=builder /video-worker /usr/local/bin/video-worker
ENTRYPOINT ["video-worker"]
