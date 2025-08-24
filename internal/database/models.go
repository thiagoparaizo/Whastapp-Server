// internal/database/models.go
package database

import (
	"database/sql"
	"time"

	"github.com/lib/pq"
)

// DeviceStatus define o status de um dispositivo WhatsApp
type DeviceStatus string

const (
	DeviceStatusPending   DeviceStatus = "pending"   // Pendente de aprovação
	DeviceStatusApproved  DeviceStatus = "approved"  // Aprovado, aguardando vinculação
	DeviceStatusConnected DeviceStatus = "connected" // Vinculado e conectado
	DeviceStatusDisabled  DeviceStatus = "disabled"  // Desativado
)

// WhatsAppDevice representa um dispositivo/número de WhatsApp
type WhatsAppDevice struct {
	ID             int64          `db:"id"`
	TenantID       int64          `db:"tenant_id"`
	Name           string         `db:"name"`
	Description    string         `db:"description"`
	PhoneNumber    string         `db:"phone_number"`
	Status         DeviceStatus   `db:"status"`
	JID            sql.NullString `db:"jid"` // JID do dispositivo (quando conectado)
	CreatedAt      time.Time      `db:"created_at"`
	UpdatedAt      time.Time      `db:"updated_at"`
	LastSeen       sql.NullTime   `db:"last_seen"`       // Última vez que o dispositivo esteve online
	RequiresReauth bool           `db:"requires_reauth"` // Indica se precisa ser reautenticado
}

// CreateTableQueries retorna as queries para criar as tabelas necessárias
func CreateTableQueries() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS tenants (
			id SERIAL PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			description TEXT,
			is_active BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,

		// Tabela de dispositivos (já existente)
		`CREATE TABLE IF NOT EXISTS whatsapp_devices (
			id SERIAL PRIMARY KEY,
			tenant_id INTEGER NOT NULL,
			name VARCHAR(100) NOT NULL,
			description TEXT,
			phone_number VARCHAR(20),
			status VARCHAR(20) NOT NULL DEFAULT 'pending',
			jid VARCHAR(100),
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_seen TIMESTAMP,
			requires_reauth BOOLEAN NOT NULL DEFAULT FALSE
		)`,

		// Nova tabela de mensagens
		`CREATE TABLE IF NOT EXISTS whatsapp_messages (
            id SERIAL PRIMARY KEY,
            device_id INTEGER NOT NULL,
            jid VARCHAR(100) NOT NULL,
            message_id VARCHAR(100) NOT NULL,
            sender VARCHAR(100) NOT NULL,
            is_from_me BOOLEAN NOT NULL,
            is_group BOOLEAN NOT NULL,
            content TEXT,
            media_url TEXT,
            media_type VARCHAR(50),
            timestamp TIMESTAMP NOT NULL,
            received_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
            UNIQUE(device_id, message_id)
        )`,

		// Nova tabela para tracked entities
		`CREATE TABLE IF NOT EXISTS tracked_entities (
            id SERIAL PRIMARY KEY,
            device_id INTEGER NOT NULL REFERENCES whatsapp_devices(id) ON DELETE CASCADE,
            jid VARCHAR(100) NOT NULL,
            is_tracked BOOLEAN NOT NULL DEFAULT FALSE,
            track_media BOOLEAN NOT NULL DEFAULT TRUE,
            allowed_media_types TEXT[],
            created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
            updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
            UNIQUE(device_id, jid)
        )`,

		// Tabela de configurações de webhook
		// `CREATE TABLE IF NOT EXISTS webhook_configs (
		// 	id SERIAL PRIMARY KEY,
		// 	tenant_id INTEGER NOT NULL,
		// 	url VARCHAR(255) NOT NULL,
		// 	secret VARCHAR(255),
		// 	events TEXT[],
		// 	device_ids INTEGER[],
		// 	enabled BOOLEAN NOT NULL DEFAULT TRUE,
		// 	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		// 	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		// )`,

		// // Tabela de entregas de webhook
		// `CREATE TABLE IF NOT EXISTS webhook_deliveries (
		// 	id SERIAL PRIMARY KEY,
		// 	webhook_id INTEGER NOT NULL,
		// 	event_type VARCHAR(100) NOT NULL,
		// 	payload TEXT NOT NULL,
		// 	response_code INTEGER,
		// 	response_body TEXT,
		// 	error_message TEXT,
		// 	attempt_count INTEGER NOT NULL DEFAULT 0,
		// 	status VARCHAR(20) NOT NULL,
		// 	next_retry_at TIMESTAMP,
		// 	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		// 	last_updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		// 	FOREIGN KEY (webhook_id) REFERENCES webhook_configs(id) ON DELETE CASCADE
		// )`,

		// Índices para buscas rápidas
		`CREATE INDEX IF NOT EXISTS idx_messages_device_jid ON whatsapp_messages(device_id, jid)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_timestamp ON whatsapp_messages(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_tracked_entities_device ON tracked_entities(device_id)`,
		// `CREATE INDEX IF NOT EXISTS idx_webhook_configs_tenant ON webhook_configs(tenant_id)`,
		// `CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_status ON webhook_deliveries(status)`,
		// `CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_next_retry ON webhook_deliveries(next_retry_at)`,

		// NOVA TABELA: notification_logs
		`CREATE TABLE IF NOT EXISTS notification_logs (
			id SERIAL PRIMARY KEY,
			device_id BIGINT,
			tenant_id BIGINT,
			level VARCHAR(20) NOT NULL,
			type VARCHAR(50) NOT NULL,
			title VARCHAR(255) NOT NULL,
			message TEXT NOT NULL,
			error_code VARCHAR(50),
			details JSONB,
			suggested_action TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,

		// Índices para buscas rápidas
		`CREATE INDEX IF NOT EXISTS idx_messages_device_jid ON whatsapp_messages(device_id, jid)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_timestamp ON whatsapp_messages(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_tracked_entities_device ON tracked_entities(device_id)`,

		// NOVOS ÍNDICES: para notification_logs
		`CREATE INDEX IF NOT EXISTS idx_notification_logs_device_id ON notification_logs(device_id)`,
		`CREATE INDEX IF NOT EXISTS idx_notification_logs_type ON notification_logs(type)`,
		`CREATE INDEX IF NOT EXISTS idx_notification_logs_created_at ON notification_logs(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_notification_logs_level ON notification_logs(level)`,
		`CREATE INDEX IF NOT EXISTS idx_notification_logs_tenant_id ON notification_logs(tenant_id)`,
	}
}

// WhatsAppMessage representa uma mensagem do WhatsApp
type WhatsAppMessage struct {
	ID         int64     `db:"id"`
	DeviceID   int64     `db:"device_id"`
	JID        string    `db:"jid"`         // JID do contato/grupo
	MessageID  string    `db:"message_id"`  // ID da mensagem no WhatsApp
	Sender     string    `db:"sender"`      // JID do remetente
	IsFromMe   bool      `db:"is_from_me"`  // Se foi enviada por nós
	IsGroup    bool      `db:"is_group"`    // Se é uma mensagem de grupo
	Content    string    `db:"content"`     // Conteúdo da mensagem
	MediaURL   string    `db:"media_url"`   // URL da mídia (se houver)
	MediaType  string    `db:"media_type"`  // Tipo de mídia
	Timestamp  time.Time `db:"timestamp"`   // Hora da mensagem
	ReceivedAt time.Time `db:"received_at"` // Hora em que foi recebida pelo nosso sistema
}

// Modelo TrackedEntity
type TrackedEntity struct {
	ID                int64          `db:"id"`
	DeviceID          int64          `db:"device_id"`
	JID               string         `db:"jid"`
	IsTracked         bool           `db:"is_tracked"`
	TrackMedia        bool           `db:"track_media"`
	AllowedMediaTypes pq.StringArray `db:"allowed_media_types"` // Alterado para pq.StringArray
	CreatedAt         time.Time      `db:"created_at"`
	UpdatedAt         time.Time      `db:"updated_at"`
}

type WebhookConfig struct {
	ID        int64     `db:"id"`
	TenantID  int64     `db:"tenant_id"`
	URL       string    `db:"url"`
	Secret    string    `db:"secret"`
	Events    []string  `db:"events"`
	DeviceIDs []int64   `db:"device_ids"`
	Enabled   bool      `db:"enabled"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// WebhookDelivery representa um evento de entrega de webhook
type WebhookDelivery struct {
	ID            int64     `db:"id"`
	WebhookID     int64     `db:"webhook_id"`
	EventType     string    `db:"event_type"`
	Payload       string    `db:"payload"`
	ResponseCode  int       `db:"response_code"`
	ResponseBody  string    `db:"response_body"`
	ErrorMessage  string    `db:"error_message"`
	AttemptCount  int       `db:"attempt_count"`
	Status        string    `db:"status"` // success, failed, pending, retrying
	NextRetryAt   time.Time `db:"next_retry_at"`
	CreatedAt     time.Time `db:"created_at"`
	LastUpdatedAt time.Time `db:"last_updated_at"`
}

// NotificationLog representa um log de notificação
type NotificationLog struct {
	ID              int64          `db:"id"`
	DeviceID        sql.NullInt64  `db:"device_id"`
	TenantID        sql.NullInt64  `db:"tenant_id"`
	Level           string         `db:"level"`
	Type            string         `db:"type"`
	Title           string         `db:"title"`
	Message         string         `db:"message"`
	ErrorCode       sql.NullString `db:"error_code"`
	Details         sql.NullString `db:"details"` // JSON como string
	SuggestedAction sql.NullString `db:"suggested_action"`
	CreatedAt       time.Time      `db:"created_at"`
}
