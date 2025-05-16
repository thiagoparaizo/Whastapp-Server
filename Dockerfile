# Estágio de build
FROM golang:1.24.3-alpine AS builder

WORKDIR /app

# Copiar os arquivos de dependência
COPY go.mod go.sum ./

# Baixar dependências
RUN go mod download

# Copiar o código fonte
COPY . .

# Compilar a aplicação
#RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o whatsapp-service ./cmd/server

# Estágio final
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /root/

# Copiar o binário compilado do estágio de build
COPY --from=builder /app/whatsapp-service .

# Criar diretório para armazenamento de mídia
RUN mkdir -p /root/storage/media

# Expor porta da API
EXPOSE 8080

# Comando de execução
CMD ["./whatsapp-service"]