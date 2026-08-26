# Etapa de compilación
FROM golang:1.23-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/alert ./cmd/api

# Etapa final: imagen mínima sin shell ni herramientas adicionales
FROM scratch

COPY --from=builder /out/alert /alert
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

EXPOSE 8080

ENTRYPOINT ["/alert"]
