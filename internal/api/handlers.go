// internal/api/handlers.go
package api

import (
	"context"
	"io/ioutil"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"whatsapp-service/internal/database"
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dispositivo não está aprovado para conexão"})
		return
	}

	// Obter cliente
	client, err := h.WhatsAppMgr.GetClient(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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

	// Aguardar pelo código QR ou timeout
	select {
	case qr := <-qrChan:
		c.JSON(http.StatusOK, gin.H{"qr_code": qr})
	case <-ctx.Done():
		c.JSON(http.StatusRequestTimeout, gin.H{"error": "Timeout ao aguardar código QR"})
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
		URL    string   `json:"url" binding:"required"`
		Events []string `json:"events"`
		Secret string   `json:"secret"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Aqui você implementaria a lógica para configurar um webhook
	// para o serviço principal, que seria salvo no banco de dados
	// e usado para enviar eventos do WhatsApp

	c.JSON(http.StatusOK, gin.H{"status": "webhook configurado"})
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
