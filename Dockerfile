# Build stage
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /goclaw ./cmd/goclaw

# Runtime stage
FROM alpine:3.21

RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    chromium \
    bash \
    git \
    curl

# chromedp needs this to find the browser
ENV CHROME_BIN=/usr/bin/chromium-browser
ENV CHROMEDP_NO_SANDBOX=true

RUN adduser -D -h /home/goclaw goclaw
USER goclaw
WORKDIR /home/goclaw

COPY --from=builder /goclaw /usr/local/bin/goclaw

# Default data directory
RUN mkdir -p /home/goclaw/.goclaw

EXPOSE 18789

ENTRYPOINT ["goclaw"]
CMD ["start"]
