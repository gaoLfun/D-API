FROM node:26-alpine AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /dapi ./cmd/dapi

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata && addgroup -S dapi && adduser -S -G dapi dapi
WORKDIR /app
COPY --from=go-build /dapi /app/dapi
COPY --from=web-build /src/web/dist /app/web
USER dapi
EXPOSE 8080
ENV DAPI_ADDR=:8080 DAPI_WEB_DIR=/app/web
ENTRYPOINT ["/app/dapi"]
