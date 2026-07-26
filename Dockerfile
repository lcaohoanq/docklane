FROM node:26-alpine AS web

WORKDIR /src/web
RUN npm install --global pnpm@11.5.2
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY --from=web /src/internal/webui/dist/ ./internal/webui/dist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/docklane ./cmd/docklane

FROM alpine:3.22

COPY --from=build /out/docklane /usr/local/bin/docklane
EXPOSE 4646
ENTRYPOINT ["/usr/local/bin/docklane"]
