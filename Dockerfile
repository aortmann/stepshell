# --- UI build ---
FROM node:20-alpine AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm ci || npm install
COPY web/ ./
RUN node build.mjs

# --- Go build ---
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Bring in the freshly built UI so it is embedded.
COPY --from=ui /src/web/dist ./web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /stepshell ./cmd/stepshell

# --- Runtime ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /stepshell /stepshell
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/stepshell"]
