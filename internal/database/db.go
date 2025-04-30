// internal/database/db.go
package database

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// DB é uma instância de conexão com o banco de dados
type DB struct {
	*sqlx.DB
}

// New cria uma nova conexão com o banco de dados
func New(connectionString string) (*DB, error) {
	db, err := sqlx.Connect("postgres", connectionString)
	if err != nil {
		return nil, fmt.Errorf("falha ao conectar ao banco de dados: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("falha ao pingar o banco de dados: %w", err)
	}

	// Criar tabelas, se necessário
	if err := createTables(db); err != nil {
		return nil, err
	}

	return &DB{DB: db}, nil
}

// createTables cria as tabelas necessárias, se elas não existirem
func createTables(db *sqlx.DB) error {
	for _, query := range CreateTableQueries() {
		_, err := db.Exec(query)
		if err != nil {
			return fmt.Errorf("falha ao criar tabela: %w", err)
		}
	}
	return nil
}

// GetDeviceByID busca um dispositivo pelo ID
func (db *DB) GetDeviceByID(id int64) (*WhatsAppDevice, error) {
	var device WhatsAppDevice
	err := db.Get(&device, "SELECT * FROM whatsapp_devices WHERE id = $1", id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &device, nil
}

// GetDevicesByTenantID busca todos os dispositivos de um tenant
func (db *DB) GetDevicesByTenantID(tenantID int64) ([]WhatsAppDevice, error) {
	var devices []WhatsAppDevice
	err := db.Select(&devices, "SELECT * FROM whatsapp_devices WHERE tenant_id = $1", tenantID)
	if err != nil {
		return nil, err
	}
	return devices, nil
}

// GetDeviceByJID busca um dispositivo pelo JID
func (db *DB) GetDeviceByJID(jid string) (*WhatsAppDevice, error) {
	var device WhatsAppDevice
	err := db.Get(&device, "SELECT * FROM whatsapp_devices WHERE jid = $1", jid)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &device, nil
}

// CreateDevice cria um novo dispositivo
func (db *DB) CreateDevice(device *WhatsAppDevice) error {
	query := `
		INSERT INTO whatsapp_devices (
			tenant_id, name, description, phone_number, status
		) VALUES (
			$1, $2, $3, $4, $5
		) RETURNING id, created_at, updated_at
	`

	row := db.QueryRow(
		query,
		device.TenantID,
		device.Name,
		device.Description,
		device.PhoneNumber,
		device.Status,
	)

	return row.Scan(&device.ID, &device.CreatedAt, &device.UpdatedAt)
}

// UpdateDevice atualiza um dispositivo existente
func (db *DB) UpdateDevice(device *WhatsAppDevice) error {
	query := `
		UPDATE whatsapp_devices SET
			name = $1,
			description = $2,
			phone_number = $3,
			status = $4,
			jid = $5,
			updated_at = CURRENT_TIMESTAMP,
			last_seen = $6,
			requires_reauth = $7
		WHERE id = $8
	`

	_, err := db.Exec(
		query,
		device.Name,
		device.Description,
		device.PhoneNumber,
		device.Status,
		device.JID,
		device.LastSeen,
		device.RequiresReauth,
		device.ID,
	)

	return err
}

// UpdateDeviceStatus atualiza apenas o status de um dispositivo
func (db *DB) UpdateDeviceStatus(id int64, status DeviceStatus) error {
	_, err := db.Exec(
		"UPDATE whatsapp_devices SET status = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2",
		status, id,
	)
	return err
}

// SetDeviceRequiresReauth marca um dispositivo como necessitando reautenticação
func (db *DB) SetDeviceRequiresReauth(id int64) error {
	_, err := db.Exec(
		"UPDATE whatsapp_devices SET requires_reauth = true, updated_at = CURRENT_TIMESTAMP WHERE id = $2",
		id,
	)
	return err
}

// GetAllDevicesByStatus retorna todos os dispositivos com um determinado status
func (db *DB) GetAllDevicesByStatus(status DeviceStatus) ([]WhatsAppDevice, error) {
	var devices []WhatsAppDevice
	err := db.Select(&devices, "SELECT * FROM whatsapp_devices WHERE status = $1", status)
	if err != nil {
		return nil, err
	}
	return devices, nil
}

// GetDevicesRequiringReauth retorna todos os dispositivos que precisam ser reautenticados
func (db *DB) GetDevicesRequiringReauth() ([]WhatsAppDevice, error) {
	var devices []WhatsAppDevice
	err := db.Select(&devices, "SELECT * FROM whatsapp_devices WHERE requires_reauth = true")
	if err != nil {
		return nil, err
	}
	return devices, nil
}

// NullTime é um helper para criar sql.NullTime a partir de time.Time
func NullTime(t time.Time) sql.NullTime {
	return sql.NullTime{
		Time:  t,
		Valid: true,
	}
}

// SaveMessage salva uma mensagem no banco de dados
func (db *DB) SaveMessage(message *WhatsAppMessage) error {
	query := `
        INSERT INTO whatsapp_messages (
            device_id, jid, message_id, sender, is_from_me, is_group,
            content, media_url, media_type, timestamp
        ) VALUES (
            $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
        ) ON CONFLICT (device_id, message_id) DO NOTHING
        RETURNING id
    `

	return db.QueryRow(
		query,
		message.DeviceID,
		message.JID,
		message.MessageID,
		message.Sender,
		message.IsFromMe,
		message.IsGroup,
		message.Content,
		message.MediaURL,
		message.MediaType,
		message.Timestamp,
	).Scan(&message.ID)
}

// GetMessages obtém mensagens com base nos filtros
func (db *DB) GetMessages(deviceID int64, jid string, filter string) ([]WhatsAppMessage, error) {
	var messages []WhatsAppMessage
	var query string
	var args []interface{}

	// Base da query
	baseQuery := `
        SELECT * FROM whatsapp_messages
        WHERE device_id = $1 AND jid = $2
    `

	args = append(args, deviceID, jid)

	// Aplicar filtro de tempo
	now := time.Now()

	switch filter {
	case "new":
		// Mensagens não lidas (dependerá de uma implementação de status de leitura)
		query = baseQuery + " ORDER BY timestamp DESC"
	case "day":
		// Mensagens do dia atual
		startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		query = baseQuery + " AND timestamp >= $3 ORDER BY timestamp DESC"
		args = append(args, startOfDay)
	case "week":
		// Mensagens da semana atual
		startOfWeek := now.AddDate(0, 0, -int(now.Weekday()))
		startOfWeek = time.Date(startOfWeek.Year(), startOfWeek.Month(), startOfWeek.Day(), 0, 0, 0, 0, now.Location())
		query = baseQuery + " AND timestamp >= $3 ORDER BY timestamp DESC"
		args = append(args, startOfWeek)
	case "month":
		// Mensagens do mês atual
		startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		query = baseQuery + " AND timestamp >= $3 ORDER BY timestamp DESC"
		args = append(args, startOfMonth)
	default:
		// Todas as mensagens, ordenadas por data
		query = baseQuery + " ORDER BY timestamp DESC"
	}

	// Adicionar limite para não sobrecarregar a resposta
	query = query + " LIMIT 100"

	err := db.Select(&messages, query, args...)
	if err != nil {
		return nil, err
	}

	return messages, nil
}

// Métodos para gerenciar tracked entities
func (db *DB) GetTrackedEntities(deviceID int64) ([]TrackedEntity, error) {
	var entities []TrackedEntity
	err := db.Select(&entities, "SELECT * FROM tracked_entities WHERE device_id = $1", deviceID)
	return entities, err
}

func (db *DB) UpsertTrackedEntity(entity *TrackedEntity) error {
	query := `
        INSERT INTO tracked_entities (device_id, jid, is_tracked, track_media, allowed_media_types)
        VALUES ($1, $2, $3, $4, $5)
        ON CONFLICT (device_id, jid) DO UPDATE SET
            is_tracked = EXCLUDED.is_tracked,
            track_media = EXCLUDED.track_media,
            allowed_media_types = EXCLUDED.allowed_media_types,
            updated_at = CURRENT_TIMESTAMP
        RETURNING id, created_at, updated_at
    `
	return db.QueryRow(
		query,
		entity.DeviceID,
		entity.JID,
		entity.IsTracked,
		entity.TrackMedia,
		pq.Array(entity.AllowedMediaTypes),
	).Scan(&entity.ID, &entity.CreatedAt, &entity.UpdatedAt)
}

func (db *DB) DeleteTrackedEntity(deviceID int64, jid string) error {
	_, err := db.Exec(
		"DELETE FROM tracked_entities WHERE device_id = $1 AND jid = $2",
		deviceID, jid,
	)
	return err
}

func (db *DB) GetTrackedEntity(deviceID int64, jid string) (*TrackedEntity, error) {
	var entity TrackedEntity

	err := db.Get(&entity, `
        SELECT id, device_id, jid, is_tracked, track_media, 
               allowed_media_types::text[], created_at, updated_at
        FROM tracked_entities 
        WHERE device_id = $1 AND jid = $2
    `, deviceID, jid)

	if err != nil {
		if err == sql.ErrNoRows {
			// Retornar uma entidade padrão quando não encontrada
			return &TrackedEntity{
				DeviceID:          deviceID,
				JID:               jid,
				IsTracked:         false,
				TrackMedia:        true,
				AllowedMediaTypes: pq.StringArray{},
			}, nil
		}
		return nil, err
	}

	return &entity, nil
}
