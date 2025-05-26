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
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o whatsapp-service ./cmd/server

# Estágio final
FROM alpine:latest

# Instalar dependências necessárias
RUN apk --no-cache add \
    ca-certificates \
    ffmpeg \
    && rm -rf /var/cache/apk/*

WORKDIR /root/

# Copiar o binário compilado do estágio de build
COPY --from=builder /app/whatsapp-service .

# Criar diretórios necessários
RUN mkdir -p /root/storage/media \
    && mkdir -p /root/temp

# Verificar se ffmpeg foi instalado corretamente
RUN ffmpeg -version

# Expor porta da API
EXPOSE 8080

# Comando de execução
CMD ["./whatsapp-service"]