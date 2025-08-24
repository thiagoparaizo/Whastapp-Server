// internal/whatsapp/client.go
package whatsapp

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	"whatsapp-service/internal/database"
)

// Client encapsula um cliente whatsmeow e informações adicionais
type Client struct {
	Client        *whatsmeow.Client
	DeviceID      int64
	TenantID      int64
	DB            *database.DB
	EventHandlers []func(evt interface{})
	mutex         sync.Mutex
	qrChannel     chan string
	connected     bool
	manager       *Manager
}

// NewClient cria um novo cliente WhatsApp
func NewClient(deviceID int64, tenantID int64, deviceStore *store.Device, db *database.DB, logger waLog.Logger, manager *Manager) *Client {
	//TODO func NewClient(deviceID int64, tenantID int64, deviceStore *store.Device, db *database.DB, logger waLog.Logger, deviceName string) *Client {

	waClient := whatsmeow.NewClient(deviceStore, logger)
	// Configurar propriedades do dispositivo
	//waClient.Store.CompanionProps.Os = proto.String(deviceName)
	//arquivo interno que seta o nome do dispositivo (linha 127)
	//C:\Users\thiago.paraizo\go\pkg\mod\go.mau.fi\whatsmeow@v0.0.0-20250424100714-086604102f64\store\clientpayload.go

	client := &Client{
		Client:        waClient,
		DeviceID:      deviceID,
		TenantID:      tenantID,
		DB:            db,
		EventHandlers: make([]func(evt interface{}), 0),
		manager:       manager,
	}

	// Adicionar handler de eventos padrão
	waClient.AddEventHandler(client.handleEvents)

	return client
}

// Connect conecta o cliente ao WhatsApp
func (c *Client) Connect() error {
	err := c.Client.Connect()
	if err != nil {
		return fmt.Errorf("falha ao conectar ao WhatsApp: %w", err)
	}

	// Atualizar status do dispositivo no banco
	device, err := c.DB.GetDeviceByID(c.DeviceID)
	if err != nil {
		return err
	}

	if device != nil && c.Client.Store.ID != nil {
		device.Status = database.DeviceStatusConnected
		device.JID = sql.NullString{
			String: c.Client.Store.ID.String(),
			Valid:  true,
		}
		device.LastSeen = sql.NullTime{
			Time:  time.Now(),
			Valid: true,
		}
		device.RequiresReauth = false

		err = c.DB.UpdateDevice(device)
		if err != nil {
			return err
		}
	}

	c.mutex.Lock()
	c.connected = true
	c.mutex.Unlock()

	return nil
}

// Disconnect desconecta o cliente do WhatsApp
func (c *Client) Disconnect() {
	c.Client.Disconnect()

	c.mutex.Lock()
	c.connected = false
	c.mutex.Unlock()
}

// IsConnected retorna se o cliente está conectado
func (c *Client) IsConnected() bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.connected
}

// GetQRChannel obtém um canal para receber o código QR
func (c *Client) GetQRChannel(ctx context.Context) (<-chan string, error) {
	if c.Client == nil {
		return nil, fmt.Errorf("cliente WhatsApp não inicializado")
	}

	if c.Client.Store == nil {
		return nil, fmt.Errorf("store do cliente não inicializado")
	}

	if c.Client.Store.ID != nil {
		return nil, fmt.Errorf("dispositivo já está conectado/autenticado")
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	qrChan := make(chan string)
	c.qrChannel = qrChan

	return qrChan, nil
}

// SendTextMessage envia uma mensagem de texto
func (c *Client) SendTextMessage(to string, text string) (string, error) {
	if !c.IsConnected() {
		return "", fmt.Errorf("cliente não está conectado")
	}

	recipient, err := types.ParseJID(to)
	if err != nil {
		return "", fmt.Errorf("JID inválido: %w", err)
	}

	msg := &waProto.Message{
		Conversation: proto.String(text),
	}

	resp, err := c.Client.SendMessage(context.Background(), recipient, msg)
	if err != nil {
		return "", fmt.Errorf("falha ao enviar mensagem: %w", err)
	}

	return resp.ID, nil
}

// handleEvents lida com eventos do WhatsApp
func (c *Client) handleEvents(evt interface{}) {
	// Primeiro, chamar outros handlers registrados
	for _, handler := range c.EventHandlers {
		handler(evt)
	}

	// Lidar com eventos específicos
	switch v := evt.(type) {
	case *events.Connected:
		c.handleConnected()

	case *events.Disconnected:
		c.handleDisconnected()

	case *events.QR:
		c.handleQR(v)

	case *events.LoggedOut:
		c.handleLoggedOut()
	}
}

// handleConnected lida com o evento de conexão
func (c *Client) handleConnected() {
	// Atualizar status do dispositivo no banco
	go func() {
		device, err := c.DB.GetDeviceByID(c.DeviceID)
		if err != nil {
			fmt.Printf("Erro ao buscar dispositivo: %v\n", err)
			return
		}

		if device != nil && c.Client.Store.ID != nil {
			device.Status = database.DeviceStatusConnected
			device.JID = sql.NullString{
				String: c.Client.Store.ID.String(),
				Valid:  true,
			}
			device.LastSeen = sql.NullTime{
				Time:  time.Now(),
				Valid: true,
			}
			device.RequiresReauth = false

			err = c.DB.UpdateDevice(device)
			if err != nil {
				fmt.Printf("Erro ao atualizar dispositivo: %v\n", err)
			}
		}
	}()

	c.mutex.Lock()
	c.connected = true
	c.mutex.Unlock()
}

// handleDisconnected lida com o evento de desconexão
func (c *Client) handleDisconnected() {
	c.mutex.Lock()
	c.connected = false
	c.mutex.Unlock()

	// IMPLEMENTAÇÃO DA NOTIFICAÇÃO
	go func() {
		if c.manager != nil && c.manager.notificationService != nil {
			device, err := c.DB.GetDeviceByID(c.DeviceID)
			if err == nil && device != nil {
				c.manager.notificationService.NotifyDeviceDisconnected(c.DeviceID, device.Name, device.TenantID, "connection_lost")
			} else {
				fmt.Printf("Erro ao buscar dispositivo para notificação de desconexão: %v\n", err)
			}
		}
	}()
}

// handleQR lida com o evento de código QR
func (c *Client) handleQR(evt *events.QR) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.qrChannel != nil {
		select {
		case c.qrChannel <- string(evt.Codes[0]): // Convertendo para string
			// QR code enviado com sucesso
		default:
			// Canal bloqueado ou fechado, ignorar
		}
	}
}

// handleLoggedOut lida com o evento de logout
func (c *Client) handleLoggedOut() {
	// Marcar dispositivo como necessitando reautenticação
	go func() {
		err := c.DB.SetDeviceRequiresReauth(c.DeviceID)
		if err != nil {
			fmt.Printf("Erro ao marcar dispositivo para reautenticação: %v\n", err)
		}

		// Atualizar status do dispositivo
		err = c.DB.UpdateDeviceStatus(c.DeviceID, database.DeviceStatusApproved)
		if err != nil {
			fmt.Printf("Erro ao atualizar status do dispositivo: %v\n", err)
		}

		// IMPLEMENTAÇÃO DA NOTIFICAÇÃO
		if c.manager != nil && c.manager.notificationService != nil {
			device, err := c.DB.GetDeviceByID(c.DeviceID)
			if err == nil && device != nil {
				c.manager.notificationService.NotifyDeviceRequiresReauth(c.DeviceID, device.Name, device.TenantID)
			} else {
				fmt.Printf("Erro ao buscar dispositivo para notificação: %v\n", err)
			}
		}
	}()

	c.mutex.Lock()
	c.connected = false
	c.mutex.Unlock()
}

// AddEventHandler adiciona um handler de eventos customizado
func (c *Client) AddEventHandler(handler func(evt interface{})) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.EventHandlers = append(c.EventHandlers, handler)
}

// GetGroups obtém a lista de grupos do cliente
func (c *Client) GetGroups() ([]*types.GroupInfo, error) {
	if !c.IsConnected() {
		return nil, fmt.Errorf("cliente não está conectado")
	}

	return c.Client.GetJoinedGroups()
}

// GetContacts obtém a lista de contatos do cliente
func (c *Client) GetContacts() (map[types.JID]types.ContactInfo, error) {
	if !c.IsConnected() {
		return nil, fmt.Errorf("cliente não está conectado")
	}

	// Adicionar context com timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return c.Client.Store.Contacts.GetAllContacts(ctx)
}

// GetGroupMessages obtém mensagens de um grupo específico
func (c *Client) GetGroupMessages(groupID string, filter string) ([]database.WhatsAppMessage, error) {
	if !c.IsConnected() {
		return nil, fmt.Errorf("cliente não está conectado")
	}

	// Converter ID para JID
	jid, err := types.ParseJID(groupID)
	if err != nil {
		return nil, fmt.Errorf("JID de grupo inválido: %w", err)
	}

	// Verificar se é um grupo
	if jid.Server != "g.us" {
		return nil, fmt.Errorf("o JID fornecido não é um grupo")
	}

	// Buscar mensagens do banco de dados
	return c.DB.GetMessages(c.DeviceID, groupID, filter)
}

// GetContactMessages obtém mensagens de um contato específico
func (c *Client) GetContactMessages(contactID string, filter string) ([]database.WhatsAppMessage, error) {
	if !c.IsConnected() {
		return nil, fmt.Errorf("cliente não está conectado")
	}

	// Converter ID para JID
	jid, err := types.ParseJID(contactID)
	if err != nil {
		return nil, fmt.Errorf("JID de contato inválido: %w", err)
	}

	// Verificar se não é um grupo
	if jid.Server == "g.us" {
		return nil, fmt.Errorf("o JID fornecido é um grupo, não um contato")
	}

	// Buscar mensagens do banco de dados
	return c.DB.GetMessages(c.DeviceID, contactID, filter)
}

// SendGroupMessage envia uma mensagem para um grupo
func (c *Client) SendGroupMessage(groupID string, text string) (string, error) {
	if !c.IsConnected() {
		return "", fmt.Errorf("cliente não está conectado")
	}

	// Converter ID para JID
	jid, err := types.ParseJID(groupID)
	if err != nil {
		return "", fmt.Errorf("JID de grupo inválido: %w", err)
	}

	// Verificar se é um grupo
	if jid.Server != "g.us" {
		return "", fmt.Errorf("o JID fornecido não é um grupo")
	}

	// Enviar mensagem
	msg := &waProto.Message{
		Conversation: proto.String(text),
	}

	resp, err := c.Client.SendMessage(context.Background(), jid, msg)
	if err != nil {
		return "", fmt.Errorf("falha ao enviar mensagem: %w", err)
	}

	return resp.ID, nil
}

// SendMediaMessage envia uma mensagem com mídia para um contato ou grupo
func (c *Client) SendMediaMessage(to string, mediaType string, data []byte, caption string) (string, error) {
	if !c.IsConnected() {
		return "", fmt.Errorf("cliente não está conectado")
	}

	recipient, err := types.ParseJID(to)
	if err != nil {
		return "", fmt.Errorf("JID inválido: %w", err)
	}

	// Converter a string mediaType para o tipo adequado
	var mediaTypeEnum whatsmeow.MediaType
	switch mediaType {
	case "image/jpeg", "image/png", "image/gif":
		mediaTypeEnum = whatsmeow.MediaImage
	case "video/mp4":
		mediaTypeEnum = whatsmeow.MediaVideo
	case "audio/ogg", "audio/mpeg", "audio/mp4":
		mediaTypeEnum = whatsmeow.MediaAudio
	default:
		mediaTypeEnum = whatsmeow.MediaDocument
	}

	uploaded, err := c.Client.Upload(context.Background(), data, mediaTypeEnum)
	if err != nil {
		return "", fmt.Errorf("falha ao fazer upload da mídia: %w", err)
	}

	var msg *waProto.Message

	switch mediaTypeEnum {
	case whatsmeow.MediaImage:
		msg = &waProto.Message{
			ImageMessage: &waProto.ImageMessage{
				URL:           proto.String(uploaded.URL),
				Mimetype:      proto.String(mediaType),
				Caption:       proto.String(caption),
				FileLength:    proto.Uint64(uploaded.FileLength),
				FileSHA256:    uploaded.FileSHA256,
				FileEncSHA256: uploaded.FileEncSHA256,
				MediaKey:      uploaded.MediaKey,
				DirectPath:    proto.String(uploaded.DirectPath),
			},
		}
	case whatsmeow.MediaVideo:
		msg = &waProto.Message{
			VideoMessage: &waProto.VideoMessage{
				URL:           proto.String(uploaded.URL),
				Mimetype:      proto.String(mediaType),
				Caption:       proto.String(caption),
				FileLength:    proto.Uint64(uploaded.FileLength),
				FileSHA256:    uploaded.FileSHA256,
				FileEncSHA256: uploaded.FileEncSHA256,
				MediaKey:      uploaded.MediaKey,
				DirectPath:    proto.String(uploaded.DirectPath),
			},
		}
	case whatsmeow.MediaAudio:
		msg = &waProto.Message{
			AudioMessage: &waProto.AudioMessage{
				URL:           proto.String(uploaded.URL),
				Mimetype:      proto.String(mediaType),
				FileLength:    proto.Uint64(uploaded.FileLength),
				FileSHA256:    uploaded.FileSHA256,
				FileEncSHA256: uploaded.FileEncSHA256,
				MediaKey:      uploaded.MediaKey,
				DirectPath:    proto.String(uploaded.DirectPath),
			},
		}
	default:
		// Para outros tipos de arquivos, usar DocumentMessage
		msg = &waProto.Message{
			DocumentMessage: &waProto.DocumentMessage{
				URL:           proto.String(uploaded.URL),
				Mimetype:      proto.String(mediaType),
				Title:         proto.String(caption),
				FileLength:    proto.Uint64(uploaded.FileLength),
				FileSHA256:    uploaded.FileSHA256,
				FileEncSHA256: uploaded.FileEncSHA256,
				MediaKey:      uploaded.MediaKey,
				DirectPath:    proto.String(uploaded.DirectPath),
			},
		}
	}

	resp, err := c.Client.SendMessage(context.Background(), recipient, msg)
	if err != nil {
		return "", fmt.Errorf("falha ao enviar mensagem: %w", err)
	}

	return resp.ID, nil
}
