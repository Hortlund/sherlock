FROM node:24-alpine AS web
WORKDIR /build/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS go
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY migrations/ migrations/
RUN CGO_ENABLED=0 go build -trimpath -o /sherlock ./cmd/sherlock && CGO_ENABLED=0 go build -trimpath -o /demo ./cmd/demo && CGO_ENABLED=0 go build -trimpath -o /admin ./cmd/admin

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -g 10001 sherlock && adduser -D -u 10001 -G sherlock sherlock
WORKDIR /app
COPY --from=go /sherlock /usr/local/bin/sherlock
COPY --from=go /demo /usr/local/bin/sherlock-demo
COPY --from=go /admin /usr/local/bin/sherlock-admin
COPY --from=web /build/web/dist /app/web/dist
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["sherlock"]
