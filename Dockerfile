FROM golang:1.25-alpine AS builder
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /finance-api ./cmd/finance-api

FROM alpine:3.21
RUN apk add --no-cache ca-certificates && mkdir -p /tmp
COPY --from=builder /finance-api /finance-api
EXPOSE 8080
CMD ["/finance-api"]
