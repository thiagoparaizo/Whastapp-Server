// internal/config/config.go
package config

import (
	"log"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// Config contém as configurações da aplicação
type Config struct {
	Host              string
	Port              string
	PostgresConnStr   string
	WhatsmeowConnStr  string
	LogLevel          string
	BasicAuthUsername string
	BasicAuthPassword string
	AssistantAPIURL   string
}

// Load carrega configurações do ambiente
func Load() Config {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Erro ao carregar o arquivo .env")
	}

	return Config{
		Host:              getEnv("HOST", "0.0.0.0"),
		Port:              getEnv("PORT", "8080"),
		PostgresConnStr:   getEnv("DATABASE_URL", "postgres://USER:PASSWORD@localhost:5432/whatsapp_service?sslmode=disable"),
		WhatsmeowConnStr:  getEnv("WHATSMEOW_DB_URL", "postgres://USER:PASSWORD@localhost:5432/whatsapp_service?sslmode=disable"),
		LogLevel:          getEnv("LOG_LEVEL", "INFO"),
		BasicAuthUsername: getEnv("BASIC_AUTH_USERNAME", ""),
		BasicAuthPassword: getEnv("BASIC_AUTH_PASSWORD", ""),
		AssistantAPIURL:   getEnv("ASSISTANT_API_URL", "http://localhost:8000/api/v1"),
	}
}

// getEnv retorna uma variável de ambiente ou um valor padrão
func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

// getEnvBool retorna uma variável de ambiente como bool
func getEnvBool(key string, defaultValue bool) bool {
	strValue := os.Getenv(key)
	if strValue == "" {
		return defaultValue
	}

	boolValue, err := strconv.ParseBool(strValue)
	if err != nil {
		return defaultValue
	}

	return boolValue
}
