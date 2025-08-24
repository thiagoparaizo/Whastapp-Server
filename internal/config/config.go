// internal/config/config.go
package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

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

	// Configurações de notificação
	NotificationWebhookURL string
	SMTPHost               string
	SMTPPort               int
	SMTPUser               string
	SMTPPassword           string
	NotificationFromEmail  string
	NotificationToEmails   []string
	NotificationsEnabled   bool
}

// Load carrega configurações do ambiente
func Load() Config {
	err := godotenv.Load()
	if err != nil {
		log.Print("Erro ao carregar o arquivo .env (normal em container)")
		log.Print("Lendo variáveis diretamente do ambiente...")
	}

	log.Print("Carregando configurações...")

	// Parse emails (separados por vírgula) - com debug
	toEmails := []string{}
	emailsStr := getEnv("NOTIFICATION_TO_EMAILS", "")
	log.Printf("DEBUG: NOTIFICATION_TO_EMAILS = '%s'", emailsStr)

	if emailsStr != "" {
		toEmails = strings.Split(emailsStr, ",")
		for i, email := range toEmails {
			toEmails[i] = strings.TrimSpace(email)
		}
		log.Printf("DEBUG: Emails parseados: %v", toEmails)
	} else {
		log.Printf("⚠️  NOTIFICATION_TO_EMAILS não configurado")
	}

	// Debug outras variáveis importantes
	smtpHost := getEnv("SMTP_HOST", "")
	smtpUser := getEnv("SMTP_USER", "")
	log.Printf("DEBUG: SMTP_HOST = '%s'", smtpHost)
	log.Printf("DEBUG: SMTP_USER = '%s'", smtpUser)
	log.Printf("DEBUG: NOTIFICATIONS_ENABLED = '%s'", getEnv("NOTIFICATIONS_ENABLED", "true"))

	return Config{
		Host:              getEnv("HOST", "0.0.0.0"),
		Port:              getEnv("PORT", "8080"),
		PostgresConnStr:   getEnv("DATABASE_URL", "postgres://USER:PASSWORD@localhost:5432/whatsapp_service?sslmode=disable"),
		WhatsmeowConnStr:  getEnv("WHATSMEOW_DB_URL", "postgres://USER:PASSWORD@localhost:5432/whatsapp_service?sslmode=disable"),
		LogLevel:          getEnv("LOG_LEVEL", "INFO"),
		BasicAuthUsername: getEnv("BASIC_AUTH_USERNAME", ""),
		BasicAuthPassword: getEnv("BASIC_AUTH_PASSWORD", ""),
		AssistantAPIURL:   getEnv("ASSISTANT_API_URL", "http://localhost:8000/api/v1"),

		// Notificações
		NotificationWebhookURL: getEnv("NOTIFICATION_WEBHOOK_URL", ""),
		SMTPHost:               smtpHost,
		SMTPPort:               getEnvInt("SMTP_PORT", 587),
		SMTPUser:               smtpUser,
		SMTPPassword:           getEnv("SMTP_PASSWORD", ""),
		NotificationFromEmail:  getEnv("NOTIFICATION_FROM_EMAIL", ""),
		NotificationToEmails:   toEmails,
		NotificationsEnabled:   getEnvBool("NOTIFICATIONS_ENABLED", true),
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

// função getEnvInt:
func getEnvInt(key string, defaultValue int) int {
	strValue := os.Getenv(key)
	if strValue == "" {
		return defaultValue
	}

	intValue, err := strconv.Atoi(strValue)
	if err != nil {
		return defaultValue
	}

	return intValue
}

func (c *Config) ValidateEmailConfig() error {
	if !c.NotificationsEnabled {
		return nil // Email não é obrigatório se notificações estão desabilitadas
	}

	if c.SMTPHost == "" {
		return fmt.Errorf("SMTP_HOST é obrigatório quando notificações estão habilitadas")
	}

	if c.SMTPUser == "" {
		return fmt.Errorf("SMTP_USER é obrigatório")
	}

	if c.SMTPPassword == "" {
		return fmt.Errorf("SMTP_PASSWORD é obrigatório")
	}

	if len(c.NotificationToEmails) == 0 {
		return fmt.Errorf("NOTIFICATION_TO_EMAILS é obrigatório (pelo menos um email)")
	}

	return nil
}
