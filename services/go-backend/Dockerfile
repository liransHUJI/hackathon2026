FROM golang:1.22-alpine AS build

WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN go build -o /out/provenance-api ./cmd/provenance-api

FROM alpine:3.20

WORKDIR /app
COPY --from=build /out/provenance-api /usr/local/bin/provenance-api
COPY migrations ./migrations
EXPOSE 8080
CMD ["provenance-api"]
