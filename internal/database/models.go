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

		// Índices para buscas rápidas
		`CREATE INDEX IF NOT EXISTS idx_messages_device_jid ON whatsapp_messages(device_id, jid)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_timestamp ON whatsapp_messages(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_tracked_entities_device ON tracked_entities(device_id)`,
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
