# syntax=docker/dockerfile:1.7
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/ob-mcp     ./cmd/ob-mcp
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/ob-reembed ./cmd/ob-reembed

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ob-mcp     /app/ob-mcp
COPY --from=build /out/ob-reembed /app/ob-reembed
USER nonroot:nonroot
ENTRYPOINT ["/app/ob-mcp"]
