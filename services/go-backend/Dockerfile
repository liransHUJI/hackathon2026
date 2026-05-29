FROM golang:1.26-alpine AS build

WORKDIR /src/services/go-backend
COPY services/go-backend/go.mod services/go-backend/go.sum* ./
RUN go mod download
COPY services/go-backend/ ./
RUN go build -o /out/provenance-api ./cmd/provenance-api

FROM alpine:3.20

WORKDIR /app
COPY --from=build /out/provenance-api /usr/local/bin/provenance-api
COPY --from=build /src/services/go-backend/migrations ./migrations
COPY public ./public
EXPOSE 8080
CMD ["provenance-api"]
