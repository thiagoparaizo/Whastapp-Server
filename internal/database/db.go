// internal/database/db.go
package database

import (
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"whatsapp-service/internal/client"
)

// DB é uma instância de conexão com o banco de dados
type DB struct {
	*sqlx.DB
	AssistantClient *client.AssistantClient // Cliente para o Assistant API
}

// New cria uma nova conexão com o banco de dados
func New(connectionString string, assistantAPIURL string) (*DB, error) {
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

	// Criar cliente para o Assistant API
	assistantClient := client.NewAssistantClient(assistantAPIURL)

	return &DB{
		DB:              db,
		AssistantClient: assistantClient,
	}, nil
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

func (db *DB) ValidateTenant(tenantID int64) (bool, error) {
	response, err := db.AssistantClient.ValidateTenant(int(tenantID))
	if err != nil {
		// Falha ao contactar o Assistant API, considerar o tenant inválido
		return false, fmt.Errorf("falha ao validar tenant: %w", err)
	}

	return response.Exists && response.IsActive, nil
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
	// Validar o tenant antes de criar o dispositivo
	isValid, err := db.ValidateTenant(device.TenantID)
	if err != nil {
		return fmt.Errorf("erro ao validar tenant: %w", err)
	}

	if !isValid {
		return fmt.Errorf("tenant inválido ou inativo")
	}

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
	// TODO: add device_name

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
	// TODO: add device.DeviceName

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

	err := db.QueryRow(
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

	// Após salvar a mensagem, notificar o Assistant API sobre o evento
	// Este passo é assíncrono e não afeta o retorno da função
	//go db.notifyAssistantAboutMessage(message)

	return err
}

// notifyAssistantAboutMessage envia informações de mensagem para o Assistant API
func (db *DB) NotifyAssistantAboutMessage(message *WhatsAppMessage) {
	// Obter informações do dispositivo para resgatar o tenant_id
	device, err := db.GetDeviceByID(message.DeviceID)
	if err != nil || device == nil {
		// Se não conseguir obter o dispositivo, não podemos notificar
		return
	}

	// Criar evento para enviar ao Assistant
	event := map[string]interface{}{
		"device_id":  message.DeviceID,
		"tenant_id":  device.TenantID,
		"event_type": "*events.Message",
		"timestamp":  time.Now().Format(time.RFC3339),
		"event": map[string]interface{}{
			"Info": map[string]interface{}{
				"Chat":     message.JID,
				"Sender":   message.Sender,
				"IsFromMe": message.IsFromMe,
				"IsGroup":  message.IsGroup,
			},
			"Message": map[string]interface{}{
				"Conversation": message.Content,
				"MediaURL":     message.MediaURL,
				"MediaType":    message.MediaType,
			},
		},
	}

	// Enviar para o Assistant API
	err = db.AssistantClient.SendWebhookEvent(event)
	if err != nil {
		// Log do erro, mas não afeta o fluxo principal
		fmt.Printf("Erro ao notificar Assistant sobre mensagem: %v\n", err)
	}
}

// GetMessages obtém mensagens com base nos filtros
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

	// Garantir que nunca retornamos null mesmo se não houver mensagens
	if messages == nil {
		messages = []WhatsAppMessage{}
	}

	return messages, nil
}

// Métodos para gerenciar tracked entities
func (db *DB) GetTrackedEntities(deviceID int64) ([]TrackedEntity, error) {
	var entities []TrackedEntity
	err := db.Select(&entities, "SELECT * FROM tracked_entities WHERE device_id = $1", deviceID)
	// Garantir que retornamos uma lista vazia e não null quando não há resultados
	if entities == nil {
		entities = []TrackedEntity{} // Inicializa como uma lista vazia
	}

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

// GetAllTenants retorna todos os tenants do sistema
func (db *DB) GetAllTenants() ([]map[string]interface{}, error) {
	tenants, err := db.AssistantClient.ListActiveTenants()
	if err != nil {
		return nil, fmt.Errorf("falha ao listar tenants: %w", err)
	}

	// Converter para formato compatível com o código existente
	result := make([]map[string]interface{}, len(tenants))
	for i, tenant := range tenants {
		result[i] = map[string]interface{}{
			"ID":          tenant.ID,
			"name":        tenant.Name,
			"description": tenant.Description,
		}
	}

	return result, nil
}

// SaveWebhookConfig salva uma configuração de webhook
func (db *DB) SaveWebhookConfig(config *WebhookConfig) error {
	query := `
        INSERT INTO webhook_configs (
            tenant_id, url, secret, events, device_ids, enabled, created_at, updated_at
        ) VALUES (
            $1, $2, $3, $4, $5, $6, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
        ) RETURNING id, created_at, updated_at
    `

	// Converter slices para arrays de SQL
	events := pq.Array(config.Events)
	deviceIDs := pq.Array(config.DeviceIDs)

	err := db.QueryRow(
		query,
		config.TenantID,
		config.URL,
		config.Secret,
		events,
		deviceIDs,
		config.Enabled,
	).Scan(&config.ID, &config.CreatedAt, &config.UpdatedAt)

	return err
}

// UpdateWebhookConfig atualiza uma configuração de webhook existente
func (db *DB) UpdateWebhookConfig(config *WebhookConfig) error {
	query := `
        UPDATE webhook_configs SET
            url = $1,
            secret = $2,
            events = $3,
            device_ids = $4,
            enabled = $5,
            updated_at = CURRENT_TIMESTAMP
        WHERE id = $6
    `

	// Converter slices para arrays de SQL
	events := pq.Array(config.Events)
	deviceIDs := pq.Array(config.DeviceIDs)

	_, err := db.Exec(
		query,
		config.URL,
		config.Secret,
		events,
		deviceIDs,
		config.Enabled,
		config.ID,
	)

	return err
}

// GetWebhookConfigsByTenant busca configurações de webhook por tenant
func (db *DB) GetWebhookConfigsByTenant(tenantID int64) ([]WebhookConfig, error) {
	var configs []WebhookConfig

	query := `
        SELECT 
            id, tenant_id, url, secret, events, device_ids, enabled, created_at, updated_at
        FROM 
            webhook_configs
        WHERE 
            tenant_id = $1
    `

	rows, err := db.Query(query, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var config WebhookConfig
		var events, deviceIDs pq.StringArray

		err := rows.Scan(
			&config.ID,
			&config.TenantID,
			&config.URL,
			&config.Secret,
			&events,
			&deviceIDs,
			&config.Enabled,
			&config.CreatedAt,
			&config.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		// Converter arrays de SQL para slices
		config.Events = []string(events)

		// Converter string IDs para int64
		config.DeviceIDs = make([]int64, len(deviceIDs))
		for i, id := range deviceIDs {
			idInt, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				return nil, err
			}
			config.DeviceIDs[i] = idInt
		}

		configs = append(configs, config)
	}

	return configs, nil
}

// GetWebhookConfigByID busca uma configuração de webhook por ID
func (db *DB) GetWebhookConfigByID(id int64) (*WebhookConfig, error) {
	var config WebhookConfig
	var events, deviceIDs pq.StringArray

	query := `
        SELECT 
            id, tenant_id, url, secret, events, device_ids, enabled, created_at, updated_at
        FROM 
            webhook_configs
        WHERE 
            id = $1
    `

	err := db.QueryRow(query, id).Scan(
		&config.ID,
		&config.TenantID,
		&config.URL,
		&config.Secret,
		&events,
		&deviceIDs,
		&config.Enabled,
		&config.CreatedAt,
		&config.UpdatedAt,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	// Converter arrays de SQL para slices
	config.Events = []string(events)

	// Converter string IDs para int64
	config.DeviceIDs = make([]int64, len(deviceIDs))
	for i, id := range deviceIDs {
		idInt, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return nil, err
		}
		config.DeviceIDs[i] = idInt
	}

	return &config, nil
}

// DeleteWebhookConfig exclui uma configuração de webhook
func (db *DB) DeleteWebhookConfig(id int64) error {
	_, err := db.Exec("DELETE FROM webhook_configs WHERE id = $1", id)
	return err
}

// LogWebhookDelivery registra uma tentativa de entrega de webhook
func (db *DB) LogWebhookDelivery(delivery *WebhookDelivery) error {
	query := `
        INSERT INTO webhook_deliveries (
            webhook_id, event_type, payload, response_code, response_body, 
            error_message, attempt_count, status, next_retry_at,
            created_at, last_updated_at
        ) VALUES (
            $1, $2, $3, $4, $5, $6, $7, $8, $9, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
        ) RETURNING id, created_at, last_updated_at
    `

	err := db.QueryRow(
		query,
		delivery.WebhookID,
		delivery.EventType,
		delivery.Payload,
		delivery.ResponseCode,
		delivery.ResponseBody,
		delivery.ErrorMessage,
		delivery.AttemptCount,
		delivery.Status,
		delivery.NextRetryAt,
	).Scan(&delivery.ID, &delivery.CreatedAt, &delivery.LastUpdatedAt)

	return err
}

// GetPendingWebhookDeliveries busca entregas de webhook pendentes ou com falha para retentar
func (db *DB) GetPendingWebhookDeliveries() ([]WebhookDelivery, error) {
	var deliveries []WebhookDelivery

	query := `
        SELECT 
            id, webhook_id, event_type, payload, response_code, response_body,
            error_message, attempt_count, status, next_retry_at, created_at, last_updated_at
        FROM 
            webhook_deliveries
        WHERE 
            (status = 'pending' OR status = 'retrying')
            AND (next_retry_at IS NULL OR next_retry_at <= CURRENT_TIMESTAMP)
        ORDER BY
            created_at ASC
        LIMIT 100
    `

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var delivery WebhookDelivery
		err := rows.Scan(
			&delivery.ID,
			&delivery.WebhookID,
			&delivery.EventType,
			&delivery.Payload,
			&delivery.ResponseCode,
			&delivery.ResponseBody,
			&delivery.ErrorMessage,
			&delivery.AttemptCount,
			&delivery.Status,
			&delivery.NextRetryAt,
			&delivery.CreatedAt,
			&delivery.LastUpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		deliveries = append(deliveries, delivery)
	}

	return deliveries, nil
}

// UpdateWebhookDeliveryStatus atualiza o status de uma entrega de webhook
func (db *DB) UpdateWebhookDeliveryStatus(id int64, status string, responseCode int, responseBody string, errorMessage string, attemptCount int, nextRetry *time.Time) error {
	query := `
        UPDATE webhook_deliveries SET
            status = $1,
            response_code = $2,
            response_body = $3,
            error_message = $4,
            attempt_count = $5,
            next_retry_at = $6,
            last_updated_at = CURRENT_TIMESTAMP
        WHERE id = $7
    `

	_, err := db.Exec(
		query,
		status,
		responseCode,
		responseBody,
		errorMessage,
		attemptCount,
		nextRetry,
		id,
	)

	return err
}

// TODO
// Definição simplificada de Tenant para este contexto
type Tenant struct {
	ID   int64
	Name string
}

// WebhookLog representa um log de entrega de webhook para a API
type WebhookLog struct {
	ID           int64     `json:"id"`
	WebhookID    int64     `json:"webhook_id"`
	EventType    string    `json:"event_type"`
	Status       string    `json:"status"`
	AttemptCount int       `json:"attempt_count"`
	ResponseCode int       `json:"response_code"`
	ResponseBody string    `json:"response_body"`
	ErrorMessage string    `json:"error_message"`
	Payload      string    `json:"payload"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"last_updated_at"`
}

// GetWebhookLogs busca logs de entrega de um webhook específico
func (db *DB) GetWebhookLogs(webhookID int64, status string, limit int) ([]WebhookLog, error) {
	var logs []WebhookLog

	// Construir query com filtros opcionais
	query := `
        SELECT 
            id, webhook_id, event_type, status, attempt_count, 
            response_code, response_body, error_message, payload,
            created_at, last_updated_at
        FROM 
            webhook_deliveries
        WHERE 
            webhook_id = $1
    `

	args := []interface{}{webhookID}

	// Adicionar filtro por status se fornecido
	if status != "" && status != "all" {
		query += " AND status = $2"
		args = append(args, status)
	}

	// Ordenar por data de criação (mais recente primeiro)
	query += " ORDER BY created_at DESC LIMIT $" + strconv.Itoa(len(args)+1)
	args = append(args, limit)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var log WebhookLog
		err := rows.Scan(
			&log.ID,
			&log.WebhookID,
			&log.EventType,
			&log.Status,
			&log.AttemptCount,
			&log.ResponseCode,
			&log.ResponseBody,
			&log.ErrorMessage,
			&log.Payload,
			&log.CreatedAt,
			&log.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		logs = append(logs, log)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return logs, nil
}
