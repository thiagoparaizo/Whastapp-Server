// internal/api/handlers.go
package api

import (
	"context"
	"database/sql"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"whatsapp-service/internal/database"
	"whatsapp-service/internal/notification"
	"whatsapp-service/internal/whatsapp"
)

// Handler contém os handlers da API
type Handler struct {
	DB          *database.DB
	WhatsAppMgr *whatsapp.Manager
}

// NewHandler cria um novo handler da API
func NewHandler(db *database.DB, waMgr *whatsapp.Manager) *Handler {
	return &Handler{
		DB:          db,
		WhatsAppMgr: waMgr,
	}
}

// GetDevices retorna todos os dispositivos de um tenant
func (h *Handler) GetDevices(c *gin.Context) {
	tenantIDStr := c.Query("tenant_id")
	if tenantIDStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id é obrigatório"})
		return
	}

	tenantID, err := strconv.ParseInt(tenantIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id inválido"})
		return
	}

	devices, err := h.DB.GetDevicesByTenantID(tenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, devices)
}

// GetDevice retorna um dispositivo específico
func (h *Handler) GetDevice(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	device, err := h.DB.GetDeviceByID(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if device == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Dispositivo não encontrado"})
		return
	}

	c.JSON(http.StatusOK, device)
}

// CreateDevice cria um novo dispositivo
func (h *Handler) CreateDevice(c *gin.Context) {
	var request struct {
		TenantID    int64  `json:"tenant_id" binding:"required"`
		Name        string `json:"name" binding:"required"`
		Description string `json:"description"`
		PhoneNumber string `json:"phone_number"`
		//TODO DeviceName	string `json:"device_name"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	device := &database.WhatsAppDevice{
		TenantID:    request.TenantID,
		Name:        request.Name,
		Description: request.Description,
		PhoneNumber: request.PhoneNumber,
		Status:      database.DeviceStatusPending, // Pendente de aprovação
		//TODO DeviceName:  request.DeviceName,
	}

	err := h.DB.CreateDevice(device)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, device)
}

// UpdateDeviceStatus atualiza o status de um dispositivo
func (h *Handler) UpdateDeviceStatus(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	var request struct {
		Status string `json:"status" binding:"required"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validar status
	status := database.DeviceStatus(request.Status)
	if status != database.DeviceStatusPending &&
		status != database.DeviceStatusApproved &&
		status != database.DeviceStatusConnected &&
		status != database.DeviceStatusDisabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Status inválido"})
		return
	}

	// Buscar dispositivo para verificar o status atual
	device, err := h.DB.GetDeviceByID(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if device == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Dispositivo não encontrado"})
		return
	}

	// Atualizar status
	err = h.DB.UpdateDeviceStatus(id, status)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Se o status foi alterado para "disabled", desconectar o cliente
	if status == database.DeviceStatusDisabled {
		_ = h.WhatsAppMgr.DisconnectClient(id)
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// GetQRCode retorna um código QR para autenticação
func (h *Handler) GetQRCode(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	// Verificar se dispositivo existe e está aprovado
	device, err := h.DB.GetDeviceByID(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if device == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Dispositivo não encontrado"})
		return
	}

	if device.Status != database.DeviceStatusApproved {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dispositivo não está aprovado para conexão ou já está conectado!"})
		return
	}

	if device.RequiresReauth {
		// Limpar dados de sessão antes de gerar novo QR
		fmt.Printf("Dispositivo %d necessita reautenticação, limpando sessão\n", id)

		// Remover cliente da memória se existir
		h.WhatsAppMgr.DisconnectClient(id)

		// Limpar JID do banco de dados
		device.JID = sql.NullString{Valid: false}
		device.RequiresReauth = false // Reset flag após limpeza
		err = h.DB.UpdateDevice(device)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Erro ao limpar sessão"})
			return
		}
	}

	// Obter cliente
	client, err := h.WhatsAppMgr.GetClient(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Erro ao obter cliente: %v", err)})
		return
	}

	// Se o cliente já está conectado, retornar
	// Se o cliente já está conectado, retornar erro
	if client.IsConnected() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dispositivo já está conectado"})
		return
	}

	// Obter canal para o código QR
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	qrChan, err := client.GetQRChannel(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Conectar cliente ao WhatsApp
	err = client.Connect()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Obter canal para o código QR
	ctx, cancel = context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	qrChan, err = client.GetQRChannel(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Erro ao obter canal QR: %v", err)})
		return
	}

	// Conectar cliente ao WhatsApp (isso deve gerar o QR)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("Panic ao conectar para QR do dispositivo %d: %v\n", id, r)
			}
		}()

		err := client.Connect()
		if err != nil {
			fmt.Printf("Erro ao conectar para QR do dispositivo %d: %v\n", id, err)
		}
	}()

	// Aguardar pelo código QR ou timeout
	select {
	case qr := <-qrChan:
		c.JSON(http.StatusOK, gin.H{"qr_code": qr})
	case <-ctx.Done():
		c.JSON(http.StatusRequestTimeout, gin.H{"error": "Timeout ao aguardar código QR (60s)"})
	}
}

// SendMessage envia uma mensagem
func (h *Handler) SendMessage(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	var request struct {
		To      string `json:"to" binding:"required"`
		Message string `json:"message" binding:"required"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Enviar mensagem
	msgID, err := h.WhatsAppMgr.SendTextMessage(id, request.To, request.Message)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message_id": msgID})
}

// GetDeviceStatus retorna o status de um dispositivo
func (h *Handler) GetDeviceStatus(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	// Buscar dispositivo
	device, err := h.DB.GetDeviceByID(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if device == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Dispositivo não encontrado"})
		return
	}

	// Verificar se o cliente está conectado
	isConnected := false
	client, err := h.WhatsAppMgr.GetClient(id)
	if err == nil && client != nil {
		isConnected = client.IsConnected()
	}

	c.JSON(http.StatusOK, gin.H{
		"id":              device.ID,
		"status":          device.Status,
		"connected":       isConnected,
		"requires_reauth": device.RequiresReauth,
		"last_seen":       device.LastSeen,
	})
}

// DisconnectDevice desconecta um dispositivo
func (h *Handler) DisconnectDevice(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	// Desconectar
	err = h.WhatsAppMgr.DisconnectClient(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "desconectado"})
}

// GetWhatsAppHealth verifica a saúde do serviço WhatsApp
func (h *Handler) GetWhatsAppHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "online",
		"time":   time.Now().Format(time.RFC3339),
	})
}

// WebhookConfig configura um webhook para envio de eventos
func (h *Handler) WebhookConfig(c *gin.Context) {
	var request struct {
		URL       string   `json:"url" binding:"required"`
		Secret    string   `json:"secret"`
		Events    []string `json:"events"`
		TenantID  int64    `json:"tenant_id" binding:"required"`
		DeviceIDs []int64  `json:"device_ids"`
		Enabled   bool     `json:"enabled" default:"true"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validar URL
	_, err := url.Parse(request.URL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "URL inválida"})
		return
	}

	// Configurar webhook no EventHandler global
	h.WhatsAppMgr.ConfigureWebhook(&whatsapp.WebhookConfig{
		URL:       request.URL,
		Secret:    request.Secret,
		Events:    request.Events,
		TenantID:  request.TenantID,
		DeviceIDs: request.DeviceIDs,
		Enabled:   request.Enabled,
	})

	// Salvar configuração no banco de dados para persistência
	webhookConfig := &database.WebhookConfig{
		TenantID:  request.TenantID,
		URL:       request.URL,
		Secret:    request.Secret,
		Events:    request.Events,
		DeviceIDs: request.DeviceIDs,
		Enabled:   request.Enabled,
	}

	err = h.DB.SaveWebhookConfig(webhookConfig)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Erro ao salvar configuração: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":    "success",
		"message":   "Webhook configurado com sucesso",
		"config_id": webhookConfig.ID,
	})
}

func (h *Handler) GetWebhookConfigs(c *gin.Context) {
	tenantIDStr := c.Query("tenant_id")

	if tenantIDStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id é obrigatório"})
		return
	}

	tenantID, err := strconv.ParseInt(tenantIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id inválido"})
		return
	}

	configs, err := h.DB.GetWebhookConfigsByTenant(tenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, configs)
}

// Adicionar método para excluir webhook
func (h *Handler) DeleteWebhookConfig(c *gin.Context) {
	configIDStr := c.Param("id")

	configID, err := strconv.ParseInt(configIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	// Obter configuração atual
	config, err := h.DB.GetWebhookConfigByID(configID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if config == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Configuração não encontrada"})
		return
	}

	// Desabilitar webhook no manager (mantendo a mesma URL mas desabilitado)
	h.WhatsAppMgr.ConfigureWebhook(&whatsapp.WebhookConfig{
		URL:       config.URL,
		TenantID:  config.TenantID,
		Events:    config.Events,
		DeviceIDs: config.DeviceIDs,
		Enabled:   false, // Marcar como desabilitado
	})

	// Excluir do banco de dados
	err = h.DB.DeleteWebhookConfig(configID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "webhook removido com sucesso"})
}

// Testar webhook
func (h *Handler) TestWebhook(c *gin.Context) {
	configIDStr := c.Param("id")

	configID, err := strconv.ParseInt(configIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	// Obter configuração
	config, err := h.DB.GetWebhookConfigByID(configID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if config == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Configuração não encontrada"})
		return
	}

	// Criar evento de teste
	testEvent := map[string]interface{}{
		"event_type": "test_event",
		"tenant_id":  config.TenantID,
		"timestamp":  time.Now().Format(time.RFC3339),
		"message":    "Este é um evento de teste para verificar a configuração do webhook",
	}

	// Tentar enviar
	success, err := h.WhatsAppMgr.SendTestWebhook(config.URL, config.Secret, testEvent)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  "error",
			"message": fmt.Sprintf("Falha ao testar webhook: %v", err),
		})
		return
	}

	if !success {
		c.JSON(http.StatusOK, gin.H{
			"status":  "error",
			"message": "O servidor de webhook retornou um erro",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Teste de webhook enviado com sucesso",
	})
}

// GetPendingDevices retorna dispositivos pendentes de aprovação
func (h *Handler) GetPendingDevices(c *gin.Context) {
	devices, err := h.DB.GetAllDevicesByStatus(database.DeviceStatusPending)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, devices)
}

// GetDevicesRequiringReauth retorna dispositivos que precisam ser reautenticados
func (h *Handler) GetDevicesRequiringReauth(c *gin.Context) {
	devices, err := h.DB.GetDevicesRequiringReauth()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, devices)
}

// MarkDeviceAsReauthenticated marca um dispositivo como reautenticado
func (h *Handler) MarkDeviceAsReauthenticated(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	// Buscar dispositivo
	device, err := h.DB.GetDeviceByID(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if device == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Dispositivo não encontrado"})
		return
	}

	// Atualizar flag de reautenticação
	device.RequiresReauth = false
	err = h.DB.UpdateDevice(device)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// GetGroups retorna a lista de grupos
func (h *Handler) GetGroups(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	// Obter cliente
	client, err := h.WhatsAppMgr.GetClient(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Obter grupos
	groups, err := client.GetGroups()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, groups)
}

// GetContacts retorna a lista de contatos
func (h *Handler) GetContacts(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	// Obter cliente
	client, err := h.WhatsAppMgr.GetClient(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Obter contatos
	contacts, err := client.GetContacts()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, contacts)
}

// GetGroupMessages retorna mensagens de um grupo específico
func (h *Handler) GetGroupMessages(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	groupID := c.Param("group_id")
	if groupID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID do grupo é obrigatório"})
		return
	}

	// Filtro (new, day, week, month)
	filter := c.DefaultQuery("filter", "day")

	// Obter cliente
	client, err := h.WhatsAppMgr.GetClient(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Obter mensagens
	messages, err := client.GetGroupMessages(groupID, filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, messages)
}

// GetContactMessages retorna mensagens de um contato específico
func (h *Handler) GetContactMessages(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	contactID := c.Param("contact_id")
	if contactID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID do contato é obrigatório"})
		return
	}

	// Filtro (new, day, week, month)
	filter := c.DefaultQuery("filter", "day")

	// Obter cliente
	client, err := h.WhatsAppMgr.GetClient(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Obter mensagens
	messages, err := client.GetContactMessages(contactID, filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, messages)
}

// SendGroupMessage envia uma mensagem para um grupo
func (h *Handler) SendGroupMessage(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	groupID := c.Param("group_id")
	if groupID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID do grupo é obrigatório"})
		return
	}

	var request struct {
		Message string `json:"message" binding:"required"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Obter cliente
	client, err := h.WhatsAppMgr.GetClient(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Enviar mensagem
	msgID, err := client.SendGroupMessage(groupID, request.Message)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message_id": msgID})
}

// SendMediaMessage envia uma mensagem com mídia
func (h *Handler) SendMediaMessage(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	// Obter destinatário
	to := c.PostForm("to")
	if to == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Destinatário é obrigatório"})
		return
	}

	// Obter legenda
	caption := c.PostForm("caption")

	// Obter arquivo
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Arquivo não fornecido"})
		return
	}

	// Obter tipo MIME
	mimeType := file.Header.Get("Content-Type")
	if mimeType == "" {
		// Tentar adivinhar pelo nome do arquivo
		ext := strings.ToLower(filepath.Ext(file.Filename))
		switch ext {
		case ".jpg", ".jpeg":
			mimeType = "image/jpeg"
		case ".png":
			mimeType = "image/png"
		case ".gif":
			mimeType = "image/gif"
		case ".mp4":
			mimeType = "video/mp4"
		case ".pdf":
			mimeType = "application/pdf"
		case ".ogg":
			mimeType = "audio/ogg"
		case ".mp3":
			mimeType = "audio/mpeg"
		default:
			mimeType = "application/octet-stream"
		}
	}

	// Abrir arquivo
	src, err := file.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Erro ao abrir arquivo"})
		return
	}
	defer src.Close()

	// Ler conteúdo do arquivo
	data, err := ioutil.ReadAll(src)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Erro ao ler arquivo"})
		return
	}

	// Obter cliente
	client, err := h.WhatsAppMgr.GetClient(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Enviar mídia
	msgID, err := client.SendMediaMessage(to, mimeType, data, caption)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message_id": msgID})
}

// Novo handler para gerenciar tracked entities
func (h *Handler) SetTrackedEntity(c *gin.Context) {
	idStr := c.Param("id")
	deviceID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	var request struct {
		JID               string   `json:"jid" binding:"required"`
		IsTracked         bool     `json:"is_tracked"`
		TrackMedia        bool     `json:"track_media"`
		AllowedMediaTypes []string `json:"allowed_media_types"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	entity := &database.TrackedEntity{
		DeviceID:          deviceID,
		JID:               request.JID,
		IsTracked:         request.IsTracked,
		TrackMedia:        request.TrackMedia,
		AllowedMediaTypes: request.AllowedMediaTypes,
	}

	err = h.DB.UpsertTrackedEntity(entity)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, entity)
}

// Handler para listar tracked entities
func (h *Handler) GetTrackedEntities(c *gin.Context) {
	idStr := c.Param("id")
	deviceID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	entities, err := h.DB.GetTrackedEntities(deviceID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, entities)
}

// Handler para deletar tracked entity
func (h *Handler) DeleteTrackedEntity(c *gin.Context) {
	idStr := c.Param("id")
	deviceID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	jid := c.Param("jid")
	if jid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "JID é obrigatório"})
		return
	}

	err = h.DB.DeleteTrackedEntity(deviceID, jid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// GetWebhookLogs retorna os logs de entrega de um webhook específico
func (h *Handler) GetWebhookLogs(c *gin.Context) {
	webhookIDStr := c.Param("id")

	webhookID, err := strconv.ParseInt(webhookIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	// Obter configuração do webhook para verificar permissão
	config, err := h.DB.GetWebhookConfigByID(webhookID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if config == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Webhook não encontrado"})
		return
	}

	// Obter query params para paginação e filtros
	limit := 50
	if limitStr := c.Query("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}

	status := c.Query("status") // filtro por status

	// Buscar logs
	logs, err := h.DB.GetWebhookLogs(webhookID, status, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, logs)
}

// GetSystemStatus retorna status detalhado do sistema
func (h *Handler) GetSystemStatus(c *gin.Context) {
	// Status dos clientes em memória
	managerStatus := h.WhatsAppMgr.GetDetailedStatus()

	// Verificar consistência do banco
	consistency, err := h.DB.CheckDeviceConsistency()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// CORREÇÃO: Obter IDs dos clientes ativos de forma mais segura
	activeClientIDs := make([]int64, 0)

	// Converter interface{} para map[string]interface{}
	if devicesInterface, exists := managerStatus["devices"]; exists {
		if devices, ok := devicesInterface.([]map[string]interface{}); ok {
			for _, device := range devices {
				if deviceID, ok := device["device_id"].(int64); ok {
					activeClientIDs = append(activeClientIDs, deviceID)
				}
			}
		}
	}

	// Buscar dispositivos conectados sem clientes
	orphanDevices, err := h.DB.GetConnectedDevicesWithoutClients(activeClientIDs)
	if err != nil {
		orphanDevices = []database.WhatsAppDevice{} // Continue mesmo com erro
	}

	response := map[string]interface{}{
		"timestamp":       time.Now().Format(time.RFC3339),
		"manager_status":  managerStatus,
		"consistency":     consistency,
		"orphan_devices":  orphanDevices,
		"recommendations": generateRecommendations(consistency, orphanDevices),
	}

	c.JSON(http.StatusOK, response)
}

// FixDeviceIssue corrige problemas específicos de dispositivos
func (h *Handler) FixDeviceIssue(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	var request struct {
		Action string `json:"action" binding:"required"` // clear_session, reset_reauth, force_approved
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validar ações permitidas
	allowedActions := map[string]string{
		"clear_session":  "Limpar sessão e resetar para aprovado",
		"reset_reauth":   "Remover flag de reautenticação",
		"force_approved": "Forçar status aprovado e limpar dados",
		"disconnect":     "Desconectar cliente da memória",
	}

	if _, valid := allowedActions[request.Action]; !valid {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":           "Ação inválida",
			"allowed_actions": allowedActions,
		})
		return
	}

	// Executar ação
	switch request.Action {
	case "disconnect":
		// CORREÇÃO: Usar método que existe
		err = h.WhatsAppMgr.DisconnectClient(id)
		if err != nil {
			// Se não conseguir desconectar, não é erro crítico
			fmt.Printf("Aviso: não foi possível desconectar cliente %d: %v\n", id, err)
		}

		// Também limpar sessão no banco
		err = h.DB.ClearDeviceSession(id)

	default:
		// Ações do banco de dados
		err = h.DB.FixSpecificDevice(id, request.Action)
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Retornar status atualizado
	device, err := h.DB.GetDeviceByID(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Erro ao buscar dispositivo atualizado"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":      "success",
		"action":      request.Action,
		"description": allowedActions[request.Action],
		"device":      device,
	})
}

// ReconnectDevice força reconexão de um dispositivo específico
func (h *Handler) ReconnectDevice(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	// Verificar se dispositivo existe
	device, err := h.DB.GetDeviceByID(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if device == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Dispositivo não encontrado"})
		return
	}

	// Verificar se tem dados para reconectar
	if !device.JID.Valid || device.JID.String == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":      "Dispositivo não tem JID válido",
			"suggestion": "Gere um novo QR Code",
		})
		return
	}

	// Tentar reconectar usando método que existe
	go func() {
		err := h.WhatsAppMgr.ConnectClient(id)
		if err != nil {
			fmt.Printf("Erro na reconexão forçada do dispositivo %d: %v\n", id, err)
		} else {
			fmt.Printf("Dispositivo %d reconectado com sucesso\n", id)
		}
	}()

	c.JSON(http.StatusOK, gin.H{
		"status":    "reconnection_started",
		"device_id": id,
		"message":   "Tentativa de reconexão iniciada em background",
	})
}

// Função auxiliar para gerar recomendações
func generateRecommendations(consistency []map[string]interface{}, orphanDevices []database.WhatsAppDevice) []string {
	var recommendations []string

	for _, item := range consistency {
		if needsAction, ok := item["needs_action"].(bool); ok && needsAction {
			deviceID := item["device_id"]
			inconsistency := item["inconsistency"]
			recommendations = append(recommendations,
				fmt.Sprintf("Dispositivo %v: %v - Requer ação manual", deviceID, inconsistency))
		}
	}

	if len(orphanDevices) > 0 {
		recommendations = append(recommendations,
			fmt.Sprintf("%d dispositivos conectados no banco sem clientes ativos", len(orphanDevices)))
	}

	if len(recommendations) == 0 {
		recommendations = append(recommendations, "Sistema funcionando normalmente")
	}

	return recommendations
}

func (h *Handler) ForceNotification(c *gin.Context) {
	var request struct {
		DeviceID int64  `json:"device_id" binding:"required"`
		Type     string `json:"type" binding:"required"`
		Force    bool   `json:"force"` // true = ignorar cooldown
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	device, err := h.DB.GetDeviceByID(request.DeviceID)
	if err != nil || device == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Dispositivo não encontrado"})
		return
	}

	notificationService := h.WhatsAppMgr.GetNotificationService()
	if notificationService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Serviço de notificação não disponível"})
		return
	}

	// Criar notificação baseada no tipo
	var notificationObj *notification.DeviceNotification
	switch request.Type {
	case "device_requires_reauth":
		notificationObj = &notification.DeviceNotification{
			DeviceID:        device.ID,
			DeviceName:      device.Name,
			TenantID:        device.TenantID,
			Level:           notification.NotificationLevelWarning,
			Type:            "device_requires_reauth",
			Title:           "Dispositivo Requer Reautenticação (FORÇADO)",
			Message:         fmt.Sprintf("Dispositivo %s (ID: %d) necessita ser reautenticado", device.Name, device.ID),
			Timestamp:       time.Now(),
			ErrorCode:       "REAUTH_REQUIRED",
			SuggestedAction: "Gerar novo QR Code para reconectar o dispositivo",
			Details: map[string]interface{}{
				"forced":        request.Force,
				"api_triggered": true,
			},
		}
	case "device_connection_error":
		notificationObj = &notification.DeviceNotification{
			DeviceID:        device.ID,
			DeviceName:      device.Name,
			TenantID:        device.TenantID,
			Level:           notification.NotificationLevelError,
			Type:            "device_connection_error",
			Title:           "Erro de Conexão (FORÇADO)",
			Message:         fmt.Sprintf("Dispositivo %s (ID: %d) com erro de conexão", device.Name, device.ID),
			Timestamp:       time.Now(),
			ErrorCode:       "CONNECTION_FAILED",
			SuggestedAction: "Verificar status da rede e tentar reconectar",
			Details: map[string]interface{}{
				"forced":        request.Force,
				"api_triggered": true,
			},
		}
	case "client_outdated":
		notificationObj = &notification.DeviceNotification{
			DeviceID:        device.ID,
			DeviceName:      device.Name,
			TenantID:        device.TenantID,
			Level:           notification.NotificationLevelCritical,
			Type:            "client_outdated",
			Title:           "Cliente Desatualizado (FORÇADO)",
			Message:         fmt.Sprintf("Dispositivo %s (ID: %d) usando versão desatualizada", device.Name, device.ID),
			Timestamp:       time.Now(),
			ErrorCode:       "CLIENT_OUTDATED_405",
			SuggestedAction: "Atualizar biblioteca whatsmeow",
			Details: map[string]interface{}{
				"forced":        request.Force,
				"api_triggered": true,
			},
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{
			"error":           "Tipo de notificação não suportado",
			"supported_types": []string{"device_requires_reauth", "device_connection_error", "client_outdated"},
		})
		return
	}

	// CORREÇÃO PRINCIPAL: Usar método correto baseado no parâmetro force
	var sendErr error
	if request.Force {
		fmt.Printf("🚨 FORÇANDO notificação via API: %s para dispositivo %d\n", request.Type, device.ID)
		sendErr = notificationService.SendDeviceNotificationForced(notificationObj)
	} else {
		fmt.Printf("📤 Enviando notificação normal via API: %s para dispositivo %d\n", request.Type, device.ID)
		sendErr = notificationService.SendDeviceNotification(notificationObj)
	}

	if sendErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Erro ao enviar notificação",
			"details": sendErr.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":      "success",
		"message":     fmt.Sprintf("Notificação %s enviada para dispositivo %s", request.Type, device.Name),
		"device_id":   device.ID,
		"device_name": device.Name,
		"type":        request.Type,
		"forced":      request.Force,
		"timestamp":   time.Now(),
	})
}

func (h *Handler) GetNotificationStatus(c *gin.Context) {
	status := gin.H{
		"notification_service_enabled": h.WhatsAppMgr.GetNotificationService() != nil,
		"timestamp":                    time.Now(),
	}

	// Verificar dispositivos que precisam de reauth
	reauthDevices, err := h.DB.GetDevicesRequiringReauth()
	if err != nil {
		status["error"] = err.Error()
	} else {
		status["devices_requiring_reauth"] = len(reauthDevices)

		if len(reauthDevices) > 0 {
			var deviceList []gin.H
			for _, device := range reauthDevices {
				deviceList = append(deviceList, gin.H{
					"id":              device.ID,
					"name":            device.Name,
					"tenant_id":       device.TenantID,
					"requires_reauth": device.RequiresReauth,
				})
			}
			status["reauth_devices"] = deviceList
		}
	}

	// Verificar emails configurados do sistema
	if h.DB != nil {
		systemEmails, err := h.DB.GetSystemAdminEmails("all")
		if err == nil {
			status["system_admin_emails_count"] = len(systemEmails)
			status["system_admin_emails"] = systemEmails
		} else {
			status["email_config_error"] = err.Error()
		}

		// Verificar últimas notificações
		logs, err := h.DB.GetNotificationLogs(nil, nil, "", "", 10)
		if err == nil {
			status["recent_notifications_count"] = len(logs)
			var recentLogs []gin.H
			for _, log := range logs {
				recentLogs = append(recentLogs, gin.H{
					"device_id":  log.DeviceID,
					"type":       log.Type,
					"level":      log.Level,
					"created_at": log.CreatedAt,
				})
			}
			status["recent_notifications"] = recentLogs
		}
	}

	c.JSON(http.StatusOK, status)
}

func (h *Handler) TriggerTestReauthNotification(c *gin.Context) {
	var request struct {
		DeviceID int64 `json:"device_id" binding:"required"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	device, err := h.DB.GetDeviceByID(request.DeviceID)
	if err != nil || device == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Dispositivo não encontrado"})
		return
	}

	notificationService := h.WhatsAppMgr.GetNotificationService()
	if notificationService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Serviço de notificação não disponível"})
		return
	}

	// Usar o método direto do notification service
	notificationService.NotifyDeviceRequiresReauth(device.ID, device.Name, device.TenantID)

	c.JSON(http.StatusOK, gin.H{
		"status":      "success",
		"message":     fmt.Sprintf("Notificação de reauth enviada para dispositivo %s", device.Name),
		"device_id":   device.ID,
		"device_name": device.Name,
	})
}

func (h *Handler) DebugCooldown(c *gin.Context) {
	deviceIDStr := c.Query("device_id")
	notificationType := c.Query("type")

	if deviceIDStr == "" || notificationType == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "device_id e type são obrigatórios"})
		return
	}

	deviceID, err := strconv.ParseInt(deviceIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "device_id inválido"})
		return
	}

	// Buscar última notificação deste tipo
	query := `
		SELECT created_at 
		FROM notification_logs 
		WHERE device_id = $1 AND type = $2 
		ORDER BY created_at DESC 
		LIMIT 1
	`

	var lastNotificationTime time.Time
	err = h.DB.QueryRow(query, deviceID, notificationType).Scan(&lastNotificationTime)

	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusOK, gin.H{
				"device_id":  deviceID,
				"type":       notificationType,
				"status":     "no_previous_notifications",
				"can_notify": true,
			})
			return
		}

		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Calcular cooldown
	timeSinceLastNotification := time.Since(lastNotificationTime)
	cooldownMinutes := 60 // device_requires_reauth tem 60 min de cooldown
	cooldownDuration := time.Duration(cooldownMinutes) * time.Minute
	canNotify := timeSinceLastNotification >= cooldownDuration
	timeRemaining := cooldownDuration - timeSinceLastNotification

	c.JSON(http.StatusOK, gin.H{
		"device_id":         deviceID,
		"type":              notificationType,
		"last_notification": lastNotificationTime,
		"time_since_last":   timeSinceLastNotification.String(),
		"cooldown_duration": cooldownDuration.String(),
		"time_remaining":    timeRemaining.String(),
		"can_notify":        canNotify,
		"status": map[string]interface{}{
			"cooldown_active":   !canNotify,
			"minutes_remaining": int(timeRemaining.Minutes()),
		},
	})
}
