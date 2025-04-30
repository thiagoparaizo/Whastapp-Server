// internal/whatsapp/handlers.go
package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"go.mau.fi/whatsmeow/types/events"

	"whatsapp-service/internal/database"
)

// WebhookConfig contém a configuração para enviar eventos para um webhook
type WebhookConfig struct {
	URL    string   `json:"url"`
	Secret string   `json:"secret,omitempty"`
	Events []string `json:"events,omitempty"` // Tipos de eventos a enviar, vazio = todos
}

// EventHandler gerencia o processamento de eventos do WhatsApp
type EventHandler struct {
	DB            *database.DB
	WebhookConfig *WebhookConfig
	httpClient    *http.Client
	Manager       *Manager
}

// NewEventHandler cria um novo manipulador de eventos
func NewEventHandler(db *database.DB, manager *Manager) *EventHandler {
	return &EventHandler{
		DB: db,
		httpClient: &http.Client{
			Timeout: time.Second * 10,
		},
		Manager: manager,
	}
}

// SetWebhookConfig configura o webhook para envio de eventos
func (h *EventHandler) SetWebhookConfig(config *WebhookConfig) {
	h.WebhookConfig = config
}

// HandleEvent processa um evento do WhatsApp
func (h *EventHandler) HandleEvent(deviceID int64, evt interface{}) {
	// Atualizar status do dispositivo no banco de dados, se aplicável
	switch v := evt.(type) {
	case *events.Connected:
		h.handleConnected(deviceID)
	case *events.Disconnected:
		h.handleDisconnected(deviceID)
	case *events.LoggedOut:
		h.handleLoggedOut(deviceID)
	case *events.Message:
		h.handleMessage(deviceID, v)
	}

	// Enviar evento para o webhook, se configurado
	h.sendToWebhook(deviceID, evt)
}

// handleConnected atualiza o status de conexão no banco de dados
func (h *EventHandler) handleConnected(deviceID int64) {
	device, err := h.DB.GetDeviceByID(deviceID)
	if err != nil || device == nil {
		fmt.Printf("Erro ao buscar dispositivo %d: %v\n", deviceID, err)
		return
	}

	// Atualizar status do dispositivo
	device.Status = database.DeviceStatusConnected
	device.LastSeen = database.NullTime(time.Now())
	device.RequiresReauth = false

	err = h.DB.UpdateDevice(device)
	if err != nil {
		fmt.Printf("Erro ao atualizar dispositivo %d: %v\n", deviceID, err)
	}
}

// handleDisconnected atualiza o status de desconexão no banco de dados
func (h *EventHandler) handleDisconnected(deviceID int64) {
	device, err := h.DB.GetDeviceByID(deviceID)
	if err != nil || device == nil {
		fmt.Printf("Erro ao buscar dispositivo %d: %v\n", deviceID, err)
		return
	}

	// Não alteramos o status aqui para não perder o status "connected"
	// apenas registramos a última vez online
	device.LastSeen = database.NullTime(time.Now())

	err = h.DB.UpdateDevice(device)
	if err != nil {
		fmt.Printf("Erro ao atualizar dispositivo %d: %v\n", deviceID, err)
	}
}

// handleLoggedOut atualiza o status quando o dispositivo é desconectado
func (h *EventHandler) handleLoggedOut(deviceID int64) {
	device, err := h.DB.GetDeviceByID(deviceID)
	if err != nil || device == nil {
		fmt.Printf("Erro ao buscar dispositivo %d: %v\n", deviceID, err)
		return
	}

	// Marcar como necessitando reautenticação
	device.Status = database.DeviceStatusApproved
	device.RequiresReauth = true
	device.LastSeen = database.NullTime(time.Now())

	err = h.DB.UpdateDevice(device)
	if err != nil {
		fmt.Printf("Erro ao atualizar dispositivo %d: %v\n", deviceID, err)
	}
}

// handleMessage processa uma mensagem recebida
func (h *EventHandler) handleMessage(deviceID int64, msg *events.Message) {

	// Obter cliente para poder baixar mídia
	client, err := h.Manager.GetClient(deviceID)
	if err != nil {
		fmt.Printf("Erro ao obter cliente para dispositivo %d: %v\n", deviceID, err)
		return
	}

	// Verificar se o contato/grupo está sendo trackado
	tracked, err := h.DB.GetTrackedEntity(deviceID, msg.Info.Chat.String())
	if err != nil || !tracked.IsTracked {
		fmt.Printf("Não salvar mensagens não trackeadas para contato/grupo %s: %v\n", msg.Info.Chat.String(), err)
		return // Não salvar mensagens não trackeadas
	}

	// Registrar mensagem no banco de dados
	message := &database.WhatsAppMessage{
		DeviceID:  deviceID,
		JID:       msg.Info.Chat.String(),
		MessageID: msg.Info.ID,
		Sender:    msg.Info.Sender.String(),
		IsFromMe:  msg.Info.IsFromMe,
		IsGroup:   msg.Info.IsGroup,
		Timestamp: msg.Info.Timestamp,
		Content:   getMessageTextContent(msg),
	}

	mediaType := getMessageMediaType(msg)

	if mediaType != "text" && tracked.TrackMedia {
		if !isAllowedMediaType(mediaType, tracked.AllowedMediaTypes) {
			return
		}

		if url, content, err := h.downloadAndSaveMedia(deviceID, msg, client); err == nil {
			message.MediaType = mediaType
			message.MediaURL = url
			if content != "" {
				message.Content = content
			}
		} else {
			fmt.Printf("Erro ao baixar e salvar mídia: %v\n", err)
		}
	}

	if err := h.DB.SaveMessage(message); err != nil {
		fmt.Printf("Erro ao salvar mensagem: %v\n", err)
	}

	fmt.Printf("Dispositivo %d recebeu mensagem de %s: %s\n", deviceID, message.Sender, message.Content)
}

func getMessageTextContent(msg *events.Message) string {
	if msg.Message.GetConversation() != "" {
		return msg.Message.GetConversation()
	}
	if ext := msg.Message.GetExtendedTextMessage(); ext != nil {
		return ext.GetText()
	}
	return ""
}

func getMessageMediaType(msg *events.Message) string {
	switch {
	case msg.Message.GetImageMessage() != nil:
		return "image"
	case msg.Message.GetVideoMessage() != nil:
		return "video"
	case msg.Message.GetAudioMessage() != nil:
		return "audio"
	case msg.Message.GetDocumentMessage() != nil:
		return "document"
	default:
		return "text"
	}
}

func isAllowedMediaType(mediaType string, allowedTypes []string) bool {
	if len(allowedTypes) == 0 {
		return true
	}
	for _, allowed := range allowedTypes {
		if mediaType == allowed {
			return true
		}
	}
	return false
}

func (h *EventHandler) downloadAndSaveMedia(deviceID int64, msg *events.Message, client *Client) (string, string, error) {
	var data []byte
	var mediaType string
	var content string
	var originalFilename string
	var err error

	switch {
	case msg.Message.GetImageMessage() != nil:
		img := msg.Message.GetImageMessage()
		content = img.GetCaption()
		data, err = client.Client.Download(img)
		if err != nil {
			return "", "", fmt.Errorf("erro ao baixar imagem: %w", err)
		}
		mediaType = "image"

	case msg.Message.GetVideoMessage() != nil:
		vid := msg.Message.GetVideoMessage()
		content = vid.GetCaption()
		data, err = client.Client.Download(vid)
		if err != nil {
			return "", "", fmt.Errorf("erro ao baixar vídeo: %w", err)
		}
		mediaType = "video"

	case msg.Message.GetAudioMessage() != nil:
		audio := msg.Message.GetAudioMessage()
		data, err = client.Client.Download(audio)
		if err != nil {
			return "", "", fmt.Errorf("erro ao baixar áudio: %w", err)
		}
		mediaType = "audio"

	case msg.Message.GetDocumentMessage() != nil:
		doc := msg.Message.GetDocumentMessage()
		content = doc.GetTitle()
		data, err = client.Client.Download(doc)
		if err != nil {
			return "", "", fmt.Errorf("erro ao baixar documento: %w", err)
		}
		mediaType = "document"
		originalFilename = doc.GetFileName()
		if originalFilename == "" {
			originalFilename = fmt.Sprintf("%s.%s", msg.Info.ID, getExtensionFromMime(doc.GetMimetype()))
		}

	default:
		return "", "", fmt.Errorf("nenhuma mídia detectada")
	}

	if len(data) == 0 {
		return "", "", fmt.Errorf("nenhum dado recebido")
	}

	// Chamar nossa nova função de armazenamento
	mediaURL, err := h.storeMedia(deviceID, msg.Info.ID, mediaType, data, originalFilename)
	if err != nil {
		return "", "", fmt.Errorf("erro ao armazenar mídia: %w", err)
	}

	return mediaURL, content, nil
}

// Função que decide como armazenar o arquivo de mídia
func (h *EventHandler) storeMedia(deviceID int64, messageID string, mediaType string, data []byte, originalFilename string) (string, error) {
	// Flag para determinar se deve usar armazenamento local ou externo
	useExternalStorage := false // Altere para true quando quiser usar armazenamento externo

	if useExternalStorage {
		return h.storeMediaExternal(deviceID, messageID, mediaType, data, originalFilename)
	} else {
		return h.storeMediaLocal(deviceID, messageID, mediaType, data, originalFilename)
	}
}

// Armazenamento local
func (h *EventHandler) storeMediaLocal(deviceID int64, messageID string, mediaType string, data []byte, originalFilename string) (string, error) {
	// Gerar nome do arquivo baseado no tipo e ID
	var filename string

	if originalFilename == "" {
		// Criar nome baseado no tipo de mídia
		ext := getExtensionFromMediaType(mediaType)
		filename = fmt.Sprintf("media/%d_%s.%s", deviceID, messageID, ext)
	} else {
		filename = fmt.Sprintf("media/%d_%s", deviceID, originalFilename)
	}

	// Caminho completo para o arquivo
	mediaPath := filepath.Join("./storage", filename)

	// Garantir que o diretório existe
	if err := os.MkdirAll(filepath.Dir(mediaPath), 0755); err != nil {
		return "", fmt.Errorf("erro ao criar diretório: %w", err)
	}

	// Salvar arquivo
	if err := ioutil.WriteFile(mediaPath, data, 0644); err != nil {
		return "", fmt.Errorf("erro ao salvar arquivo: %w", err)
	}

	return filename, nil
}

// Armazenamento externo (implementação fictícia por enquanto)
func (h *EventHandler) storeMediaExternal(deviceID int64, messageID string, mediaType string, data []byte, originalFilename string) (string, error) {
	// Aqui você implementaria a chamada para um serviço externo como Dropbox, S3, etc.
	// Por enquanto, vamos simular uma implementação

	// Simular nome de arquivo ou URL que seria retornado pelo serviço
	var filename string

	if originalFilename == "" {
		ext := getExtensionFromMediaType(mediaType)
		filename = fmt.Sprintf("https://external-storage.example.com/media/%d_%s.%s", deviceID, messageID, ext)
	} else {
		filename = fmt.Sprintf("https://external-storage.example.com/media/%d_%s", deviceID, originalFilename)
	}

	// Simular uma chamada de API para um serviço externo
	// Esta parte seria substituída pela implementação real
	fmt.Printf("Simulando upload para armazenamento externo: %s (%d bytes)\n", filename, len(data))

	// Em uma implementação real, você enviaria os dados para o serviço e receberia um URL ou identificador
	// Por exemplo, com AWS S3:
	/*
	   sess := session.Must(session.NewSession())
	   svc := s3.New(sess)

	   bucket := "my-media-bucket"
	   key := fmt.Sprintf("%d/%s", deviceID, originalFilename)

	   _, err := svc.PutObject(&s3.PutObjectInput{
	       Bucket: aws.String(bucket),
	       Key:    aws.String(key),
	       Body:   bytes.NewReader(data),
	       ACL:    aws.String("private"),
	   })

	   if err != nil {
	       return "", fmt.Errorf("erro ao fazer upload para S3: %w", err)
	   }

	   // Gerar URL pré-assinado para acesso temporário ou URL público
	   req, _ := svc.GetObjectRequest(&s3.GetObjectInput{
	       Bucket: aws.String(bucket),
	       Key:    aws.String(key),
	   })

	   urlStr, err := req.Presign(24 * time.Hour)
	   if err != nil {
	       return "", fmt.Errorf("erro ao gerar URL pré-assinado: %w", err)
	   }

	   return urlStr, nil
	*/

	return filename, nil
}

// Função auxiliar para obter extensão com base no tipo de mídia
func getExtensionFromMediaType(mediaType string) string {
	switch mediaType {
	case "image":
		return "jpg"
	case "video":
		return "mp4"
	case "audio":
		return "ogg"
	case "document":
		return "pdf"
	default:
		return "bin"
	}
}

// Função auxiliar para obter extensão de arquivo a partir do tipo MIME
func getExtensionFromMime(mimeType string) string {
	switch mimeType {
	case "image/jpeg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/gif":
		return "gif"
	case "video/mp4":
		return "mp4"
	case "audio/ogg":
		return "ogg"
	case "audio/mpeg":
		return "mp3"
	case "application/pdf":
		return "pdf"
	default:
		return "bin"
	}
}

// sendToWebhook envia um evento para o webhook configurado
func (h *EventHandler) sendToWebhook(deviceID int64, evt interface{}) {
	if h.WebhookConfig == nil || h.WebhookConfig.URL == "" {
		return // Sem webhook configurado
	}

	// Verificar se este tipo de evento deve ser enviado
	if len(h.WebhookConfig.Events) > 0 {
		eventType := fmt.Sprintf("%T", evt)
		shouldSend := false

		for _, allowedType := range h.WebhookConfig.Events {
			if eventType == allowedType || allowedType == "*" {
				shouldSend = true
				break
			}
		}

		if !shouldSend {
			return // Este tipo de evento não deve ser enviado
		}
	}

	// Preparar dados do evento
	device, _ := h.DB.GetDeviceByID(deviceID)
	tenantID := int64(0)
	if device != nil {
		tenantID = device.TenantID
	}

	webhookData := map[string]interface{}{
		"device_id":  deviceID,
		"tenant_id":  tenantID,
		"event_type": fmt.Sprintf("%T", evt),
		"timestamp":  time.Now().Format(time.RFC3339),
		"event":      evt,
	}

	// Converter para JSON
	jsonData, err := json.Marshal(webhookData)
	if err != nil {
		fmt.Printf("Erro ao serializar evento para webhook: %v\n", err)
		return
	}

	// Enviar para o webhook
	req, err := http.NewRequest("POST", h.WebhookConfig.URL, bytes.NewBuffer(jsonData))
	if err != nil {
		fmt.Printf("Erro ao criar requisição para webhook: %v\n", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	if h.WebhookConfig.Secret != "" {
		req.Header.Set("X-Webhook-Secret", h.WebhookConfig.Secret)
	}

	// Enviar a requisição
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	req = req.WithContext(ctx)

	resp, err := h.httpClient.Do(req)
	if err != nil {
		fmt.Printf("Erro ao enviar evento para webhook: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		fmt.Printf("Webhook retornou status de erro: %d\n", resp.StatusCode)
	}
}
