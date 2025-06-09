// internal/whatsapp/handlers.go
package whatsapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"go.mau.fi/whatsmeow/types/events"

	"whatsapp-service/internal/database"

	"regexp"

	"sync"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// WebhookConfig contém a configuração para enviar eventos para um webhook
type WebhookConfig struct {
	URL       string   `json:"url"`
	Secret    string   `json:"secret,omitempty"`
	Events    []string `json:"events,omitempty"` // Tipos de eventos a enviar, vazio = todos
	TenantID  int64    `json:"tenant_id"`
	DeviceIDs []int64  `json:"device_ids,omitempty"` // IDs de dispositivos específicos, vazio = todos
	Enabled   bool     `json:"enabled"`              // Tipos de eventos a enviar, vazio = todos
}

// EventHandler gerencia o processamento de eventos do WhatsApp
type EventHandler struct {
	DB            *database.DB
	WebhookConfig *WebhookConfig
	httpClient    *http.Client
	Manager       *Manager
	lidCache      map[string]string // Cache LID -> PhoneNumber
	lidMutex      sync.RWMutex
}

func (h *EventHandler) cacheLIDMapping(lid, phoneNumber string) {
	h.lidMutex.Lock()
	defer h.lidMutex.Unlock()

	if h.lidCache == nil {
		h.lidCache = make(map[string]string)
	}
	h.lidCache[lid] = phoneNumber
}

func (h *EventHandler) getCachedLIDMapping(lid string) (string, bool) {
	h.lidMutex.RLock()
	defer h.lidMutex.RUnlock()

	phoneNumber, exists := h.lidCache[lid]
	return phoneNumber, exists
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

	resolvedSender := h.resolveLIDToPhoneNumberSimple(msg.Info.Sender)
	resolvedChat := h.resolveLIDToPhoneNumberSimple(msg.Info.Chat)

	// Log para debug
	if resolvedSender != msg.Info.Sender.String() {
		fmt.Printf("LID resolvido: %s -> %s\n", msg.Info.Sender.String(), resolvedSender)
	}
	if resolvedChat != msg.Info.Chat.String() {
		fmt.Printf("Chat LID resolvido: %s -> %s\n", msg.Info.Chat.String(), resolvedChat)
	}

	// Verificar se o contato/grupo está sendo trackado (usar IDs resolvidos)
	tracked, err := h.DB.GetTrackedEntity(deviceID, resolvedChat)
	if err != nil || !tracked.IsTracked {
		fmt.Printf("Não salvar mensagens não trackeadas para contato/grupo %s: %v\n", resolvedChat, err)
		if msg.Info.IsGroup {
			return
		}
	}

	// Registrar mensagem no banco de dados (usar IDs resolvidos)
	message := &database.WhatsAppMessage{
		DeviceID:  deviceID,
		JID:       resolvedChat, // Usar chat resolvido
		MessageID: msg.Info.ID,
		Sender:    resolvedSender, // Usar sender resolvido
		IsFromMe:  msg.Info.IsFromMe,
		IsGroup:   msg.Info.IsGroup,
		Timestamp: msg.Info.Timestamp,
		Content:   getMessageTextContent(msg),
	}

	// Resto do método permanece igual...
	mediaType := getMessageMediaType(msg)
	var audioBase64 string

	if mediaType != "text" && tracked.TrackMedia {
		if !isAllowedMediaType(mediaType, tracked.AllowedMediaTypes) && mediaType != "audio" {
			return
		}

		if mediaType == "audio" {
			mp3Base64, err := h.processAudioMessage(deviceID, msg, client)
			if err != nil {
				fmt.Printf("Erro ao processar áudio: %v\n", err)
			} else {
				audioBase64 = mp3Base64
				fmt.Printf("Áudio processado com sucesso para mensagem %s\n", msg.Info.ID)
			}
		} else {
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
	}

	// Salvar mensagem no banco (exceto áudios)
	if mediaType != "audio" {
		if msg.Info.IsGroup {
			if err := h.DB.SaveMessage(message); err != nil {
				fmt.Printf("Erro ao salvar mensagem: %v\n", err)
			}
		}
		go h.DB.NotifyAssistantAboutMessage(message)
	} else {
		go h.DB.NotifyAssistantAboutMessageWithAudio(message, audioBase64)
	}

	fmt.Printf("Dispositivo %d recebeu mensagem de %s: %s\n", deviceID, resolvedSender, message.Content)
}

func (h *EventHandler) resolveLIDToPhoneNumberSimple(jid types.JID) string {
	// Se não for LID, retornar como está
	if jid.Server != types.HiddenUserServer {
		return jid.String()
	}

	// Para LID, tentar converter para formato padrão
	lidNumber := jid.User
	if h.isValidPhoneNumber(lidNumber) {
		resolved := lidNumber + "@s.whatsapp.net"
		fmt.Printf("LID %s convertido para %s\n", jid.String(), resolved)
		return resolved
	}

	// Se não conseguir resolver, manter o LID original
	fmt.Printf("Mantendo LID original: %s\n", jid.String())
	return jid.String()
}

// isValidPhoneNumber verifica se uma string parece ser um número de telefone válido
func (h *EventHandler) isValidPhoneNumber(number string) bool {
	// Verificar se é apenas dígitos e tem tamanho razoável (8-15 dígitos)
	matched, _ := regexp.MatchString(`^\d{8,15}$`, number)
	return matched
}

// processAudioMessage processa uma mensagem de áudio: download, conversão para MP3 e codificação em base64
func (h *EventHandler) processAudioMessage(deviceID int64, msg *events.Message, client *Client) (string, error) {
	// Baixar o arquivo de áudio
	audio := msg.Message.GetAudioMessage()
	if audio == nil {
		return "", fmt.Errorf("mensagem de áudio não encontrada")
	}

	data, err := client.Client.Download(audio)
	if err != nil {
		return "", fmt.Errorf("erro ao baixar áudio: %w", err)
	}

	if len(data) == 0 {
		return "", fmt.Errorf("nenhum dado de áudio recebido")
	}

	// Criar arquivo temporário para o áudio original
	tempDir := "./temp"
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return "", fmt.Errorf("erro ao criar diretório temporário: %w", err)
	}

	// Arquivo de entrada (formato original do WhatsApp, geralmente OGG)
	inputFile := filepath.Join(tempDir, fmt.Sprintf("audio_%d_%s.ogg", deviceID, msg.Info.ID))
	if err := ioutil.WriteFile(inputFile, data, 0644); err != nil {
		return "", fmt.Errorf("erro ao salvar arquivo de áudio temporário: %w", err)
	}

	// Limpar arquivo temporário no final
	defer func() {
		if err := os.Remove(inputFile); err != nil {
			fmt.Printf("Aviso: erro ao remover arquivo temporário %s: %v\n", inputFile, err)
		}
	}()

	// Arquivo de saída (MP3)
	outputFile := filepath.Join(tempDir, fmt.Sprintf("audio_%d_%s.mp3", deviceID, msg.Info.ID))
	defer func() {
		if err := os.Remove(outputFile); err != nil {
			fmt.Printf("Aviso: erro ao remover arquivo MP3 temporário %s: %v\n", outputFile, err)
		}
	}()

	// Converter para MP3 usando ffmpeg
	if err := h.convertToMP3(inputFile, outputFile); err != nil {
		return "", fmt.Errorf("erro ao converter áudio para MP3: %w", err)
	}

	// Ler o arquivo MP3 convertido
	mp3Data, err := ioutil.ReadFile(outputFile)
	if err != nil {
		return "", fmt.Errorf("erro ao ler arquivo MP3 convertido: %w", err)
	}

	// Codificar em base64
	base64String := base64.StdEncoding.EncodeToString(mp3Data)

	return base64String, nil
}

// convertToMP3 converte um arquivo de áudio para MP3 usando ffmpeg
func (h *EventHandler) convertToMP3(inputFile, outputFile string) error {
	// Verificar se ffmpeg está disponível
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg não encontrado no sistema. Instale o ffmpeg para processar áudios: %w", err)
	}

	// Comando ffmpeg para conversão
	// -i: arquivo de entrada
	// -acodec libmp3lame: usar codec MP3
	// -ab 128k: bitrate de 128kbps
	// -ar 44100: sample rate de 44.1kHz
	// -y: sobrescrever arquivo de saída se existir
	cmd := exec.Command("ffmpeg",
		"-i", inputFile,
		"-acodec", "libmp3lame",
		"-ab", "128k",
		"-ar", "44100",
		"-y",
		outputFile)

	// Capturar saída de erro para debug
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// Executar comando
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("erro ao executar ffmpeg: %w, stderr: %s", err, stderr.String())
	}

	// Verificar se o arquivo de saída foi criado
	if _, err := os.Stat(outputFile); os.IsNotExist(err) {
		return fmt.Errorf("arquivo MP3 não foi criado")
	}

	return nil
}

// resolveLIDToPhoneNumber converte LID para número real quando possível
func (h *EventHandler) resolveLIDToPhoneNumber(client *whatsmeow.Client, jid types.JID) string {
	// Se não for LID, retornar como está
	if jid.Server != types.HiddenUserServer {
		return jid.String()
	}

	// É um LID - tentar resolver
	realNumber := h.getLIDMapping(client, jid)
	if realNumber != "" {
		fmt.Printf("LID %s resolvido para %s\n", jid.String(), realNumber)
		return realNumber
	}

	// Se não conseguir resolver, manter o LID original
	fmt.Printf("Não foi possível resolver LID %s, mantendo original\n", jid.String())
	return jid.String()
}

// getLIDMapping tenta encontrar o número real para um LID
func (h *EventHandler) getLIDMapping(client *whatsmeow.Client, lid types.JID) string {
	// CORREÇÃO: Usar Store.Contacts para obter contatos
	contacts, err := client.Store.Contacts.GetAllContacts()
	if err != nil {
		fmt.Printf("Erro ao obter contatos para resolução de LID: %v\n", err)
		return ""
	}

	// Procurar correspondência pelo número do LID
	lidNumber := lid.User
	for jid, contact := range contacts {
		if contact.Found && jid.Server == types.DefaultUserServer {
			// Verificar se o número do contato corresponde ao LID
			if jid.User == lidNumber {
				return jid.String()
			}
		}
	}

	// Estratégia: Tentar formato padrão assumindo que o número é válido
	if h.isValidPhoneNumber(lidNumber) {
		return lidNumber + "@s.whatsapp.net"
	}

	return ""
}

// isValidPhoneNumber verifica se uma string parece ser um número de telefone válido
func isValidPhoneNumber(number string) bool {
	// Verificar se é apenas dígitos e tem tamanho razoável (8-15 dígitos)
	matched, _ := regexp.MatchString(`^\d{8,15}$`, number)
	return matched
}

// sendEventToAssistant envia um evento para o Assistant API
// func (h *EventHandler) sendEventToAssistant(deviceID int64, evt interface{}) {
// 	device, err := h.DB.GetDeviceByID(deviceID)
// 	if err != nil || device == nil {
// 		fmt.Printf("Erro ao buscar dispositivo para envio de evento ao Assistant: %d: %v\n", deviceID, err)
// 		return
// 	}

// 	// Preparar dados do evento
// 	event := map[string]interface{}{
// 		"device_id":  deviceID,
// 		"tenant_id":  device.TenantID,
// 		"event_type": fmt.Sprintf("%T", evt),
// 		"timestamp":  time.Now().Format(time.RFC3339),
// 		"event":      evt,
// 	}

// 	// Enviar o evento para o Assistant API
// 	err = h.DB.AssistantClient.SendWebhookEvent(event)
// 	if err != nil {
// 		fmt.Printf("Erro ao enviar evento para o Assistant API: %v\n", err)
// 	}
// }

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
	// Verificar se webhook está configurado e habilitado
	if h.WebhookConfig == nil || h.WebhookConfig.URL == "" || !h.WebhookConfig.Enabled {
		return // Sem webhook configurado ou desabilitado
	}

	// Verificar se o dispositivo está na lista de dispositivos ou se está vazia (todos)
	if len(h.WebhookConfig.DeviceIDs) > 0 {
		deviceFound := false
		for _, id := range h.WebhookConfig.DeviceIDs {
			if id == deviceID {
				deviceFound = true
				break
			}
		}
		if !deviceFound {
			return // Este dispositivo não está configurado para este webhook
		}
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

	// Verificar se o tenant corresponde ao configurado
	if h.WebhookConfig.TenantID != 0 && tenantID != h.WebhookConfig.TenantID {
		return // Este evento não é para o tenant configurado
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
		// Registrar falha no banco de dados
		h.logWebhookDeliveryFailure(deviceID, fmt.Sprintf("%T", evt), jsonData, 0, "", fmt.Sprintf("Erro ao serializar: %v", err))
		return
	}

	// Criar assinatura se um segredo for fornecido
	var signature string
	if h.WebhookConfig.Secret != "" {
		signature = generateSignature(jsonData, h.WebhookConfig.Secret)
	}

	// Enviar para o webhook
	req, err := http.NewRequest("POST", h.WebhookConfig.URL, bytes.NewBuffer(jsonData))
	if err != nil {
		fmt.Printf("Erro ao criar requisição para webhook: %v\n", err)
		h.logWebhookDeliveryFailure(deviceID, fmt.Sprintf("%T", evt), jsonData, 0, "", fmt.Sprintf("Erro ao criar requisição: %v", err))
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "WhatsApp-Service-Webhook/1.0")

	if signature != "" {
		req.Header.Set("X-Webhook-Signature", signature)
	}

	if h.WebhookConfig.Secret != "" {
		req.Header.Set("X-Webhook-Secret", h.WebhookConfig.Secret)
	}

	// Enviar a requisição com timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	req = req.WithContext(ctx)

	resp, err := h.httpClient.Do(req)

	// Processar resposta ou erro
	if err != nil {
		fmt.Printf("Erro ao enviar evento para webhook: %v\n", err)
		h.logWebhookDeliveryFailure(deviceID, fmt.Sprintf("%T", evt), jsonData, 0, "", fmt.Sprintf("Erro ao enviar: %v", err))
		// Agendar reenvio em background
		go h.scheduleWebhookRetry(deviceID, fmt.Sprintf("%T", evt), jsonData)
		return
	}

	defer resp.Body.Close()

	// Ler corpo da resposta para logging
	responseBody, _ := ioutil.ReadAll(resp.Body)
	responseStr := string(responseBody)

	if resp.StatusCode >= 400 {
		fmt.Printf("Webhook retornou status de erro: %d\n", resp.StatusCode)
		h.logWebhookDeliveryFailure(deviceID, fmt.Sprintf("%T", evt), jsonData, resp.StatusCode, responseStr, fmt.Sprintf("Status de erro: %d", resp.StatusCode))
		// Agendar reenvio se for um erro temporário (5xx)
		if resp.StatusCode >= 500 {
			go h.scheduleWebhookRetry(deviceID, fmt.Sprintf("%T", evt), jsonData)
		}
	} else {
		// Registrar sucesso
		h.logWebhookDeliverySuccess(deviceID, fmt.Sprintf("%T", evt), jsonData, resp.StatusCode, responseStr)
	}
}

func (h *EventHandler) SendTestWebhook(url string, secret string, payload interface{}) (bool, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("erro ao serializar payload: %v", err)
	}

	// Criar assinatura se um segredo for fornecido
	var signature string
	if secret != "" {
		signature = generateSignature(jsonData, secret)
	}

	// Enviar para o webhook
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return false, fmt.Errorf("erro ao criar requisição: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "WhatsApp-Service-Webhook-Test/1.0")

	if signature != "" {
		req.Header.Set("X-Webhook-Signature", signature)
	}

	if secret != "" {
		req.Header.Set("X-Webhook-Secret", secret)
	}

	// Enviar a requisição com timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	req = req.WithContext(ctx)

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("erro ao enviar requisição: %v", err)
	}
	defer resp.Body.Close()

	// Verificar se o status code é de sucesso
	return resp.StatusCode >= 200 && resp.StatusCode < 300, nil
}

// Método para gerar assinatura para autenticação de webhook
func generateSignature(payload []byte, secret string) string {
	// Criar HMAC SHA256
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(payload)

	// Retornar assinatura em formato hexadecimal
	return hex.EncodeToString(h.Sum(nil))
}

// Método para registrar falha de entrega de webhook no banco de dados
func (h *EventHandler) logWebhookDeliveryFailure(deviceID int64, eventType string, payload []byte, statusCode int, responseBody string, errorMessage string) {
	// Identificar o webhook configurado (assumindo um único webhook global por enquanto)
	var webhookID int64 = 0

	// Idealmente, você buscaria o ID do webhook no banco com base no URL configurado
	// Para simplificar, vamos assumir que temos apenas um webhook configurado

	// Criar registro de entrega
	delivery := &database.WebhookDelivery{
		WebhookID:    webhookID,
		EventType:    eventType,
		Payload:      string(payload),
		ResponseCode: statusCode,
		ResponseBody: responseBody,
		ErrorMessage: errorMessage,
		AttemptCount: 1,
		Status:       "failed",
		NextRetryAt:  time.Time{}, // Sem próxima tentativa
	}

	// Salvar no banco de dados
	err := h.DB.LogWebhookDelivery(delivery)
	if err != nil {
		fmt.Printf("Erro ao registrar falha de entrega de webhook: %v\n", err)
	}
}

// Método para registrar sucesso de entrega de webhook
func (h *EventHandler) logWebhookDeliverySuccess(deviceID int64, eventType string, payload []byte, statusCode int, responseBody string) {
	// Identificar o webhook configurado (assumindo um único webhook global por enquanto)
	var webhookID int64 = 0

	// Criar registro de entrega
	delivery := &database.WebhookDelivery{
		WebhookID:    webhookID,
		EventType:    eventType,
		Payload:      string(payload),
		ResponseCode: statusCode,
		ResponseBody: responseBody,
		ErrorMessage: "",
		AttemptCount: 1,
		Status:       "success",
		NextRetryAt:  time.Time{}, // Sem próxima tentativa
	}

	// Salvar no banco de dados
	err := h.DB.LogWebhookDelivery(delivery)
	if err != nil {
		fmt.Printf("Erro ao registrar sucesso de entrega de webhook: %v\n", err)
	}
}

// Método para agendar reenvio de webhook em caso de falha
func (h *EventHandler) scheduleWebhookRetry(deviceID int64, eventType string, payload []byte) {
	// Identificar o webhook configurado
	var webhookID int64 = 0

	// Idealmente, buscar a entrega anterior para incrementar attempt_count
	// Para simplificar, vamos criar uma nova entrada

	// Calcular próxima tentativa com backoff exponencial
	attemptCount := 1
	// Backoff exponencial: 5s, 25s, 125s, etc. (5 * 5^n segundos)
	nextRetryDelay := time.Duration(5*math.Pow(5, float64(attemptCount-1))) * time.Second
	nextRetryTime := time.Now().Add(nextRetryDelay)

	// Limitar número máximo de tentativas (exemplo: 5)
	maxAttempts := 5
	if attemptCount > maxAttempts {
		fmt.Printf("Número máximo de tentativas alcançado para webhook. Desistindo.\n")
		return
	}

	// Criar registro de entrega para reenvio
	delivery := &database.WebhookDelivery{
		WebhookID:    webhookID,
		EventType:    eventType,
		Payload:      string(payload),
		ResponseCode: 0,
		ResponseBody: "",
		ErrorMessage: "Agendado para reenvio",
		AttemptCount: attemptCount,
		Status:       "pending",
		NextRetryAt:  nextRetryTime,
	}

	// Salvar no banco de dados
	err := h.DB.LogWebhookDelivery(delivery)
	if err != nil {
		fmt.Printf("Erro ao agendar reenvio de webhook: %v\n", err)
	}
}

// Método para processar reenvios pendentes
// Este método seria chamado periodicamente por uma goroutine separada
func (h *EventHandler) ProcessPendingWebhooks() {
	// Buscar webhooks pendentes
	pendingDeliveries, err := h.DB.GetPendingWebhookDeliveries()
	if err != nil {
		fmt.Printf("Erro ao buscar webhooks pendentes: %v\n", err)
		return
	}

	for _, delivery := range pendingDeliveries {
		// Obter configuração do webhook
		webhookConfig, err := h.DB.GetWebhookConfigByID(delivery.WebhookID)
		if err != nil || webhookConfig == nil {
			fmt.Printf("Configuração de webhook não encontrada para ID %d: %v\n", delivery.WebhookID, err)
			continue
		}

		// Verificar se webhook ainda está habilitado
		if !webhookConfig.Enabled {
			// Marcar como falha permanente
			h.DB.UpdateWebhookDeliveryStatus(
				delivery.ID,
				"cancelled",
				0,
				"",
				"Webhook desabilitado",
				delivery.AttemptCount,
				nil,
			)
			continue
		}

		// Tentar reenviar
		payloadBytes := []byte(delivery.Payload)

		// Criar assinatura se necessário
		var signature string
		if webhookConfig.Secret != "" {
			signature = generateSignature(payloadBytes, webhookConfig.Secret)
		}

		// Criar e enviar requisição
		req, err := http.NewRequest("POST", webhookConfig.URL, bytes.NewBuffer(payloadBytes))
		if err != nil {
			fmt.Printf("Erro ao criar requisição para reenvio: %v\n", err)

			// Atualizar status
			h.DB.UpdateWebhookDeliveryStatus(
				delivery.ID,
				"failed",
				0,
				"",
				fmt.Sprintf("Erro ao criar requisição: %v", err),
				delivery.AttemptCount+1,
				nil,
			)
			continue
		}

		req.Header.Set("Content-Type", "application/json")
		if signature != "" {
			req.Header.Set("X-Webhook-Signature", signature)
		}

		// Timeout para reenvio
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
		req = req.WithContext(ctx)

		// Enviar requisição
		resp, err := h.httpClient.Do(req)
		cancel() // Cancelar o contexto independente do resultado

		if err != nil {
			fmt.Printf("Erro ao reenviar webhook: %v\n", err)

			// Calcular próxima tentativa com backoff exponencial
			nextAttemptCount := delivery.AttemptCount + 1
			maxAttempts := 5

			if nextAttemptCount > maxAttempts {
				// Desistir após número máximo de tentativas
				h.DB.UpdateWebhookDeliveryStatus(
					delivery.ID,
					"failed",
					0,
					"",
					fmt.Sprintf("Número máximo de tentativas alcançado: %v", err),
					nextAttemptCount,
					nil,
				)
			} else {
				// Agendar próxima tentativa
				nextRetryDelay := time.Duration(5*math.Pow(5, float64(nextAttemptCount-1))) * time.Second
				nextRetryTime := time.Now().Add(nextRetryDelay)

				h.DB.UpdateWebhookDeliveryStatus(
					delivery.ID,
					"retrying",
					0,
					"",
					fmt.Sprintf("Erro ao enviar: %v", err),
					nextAttemptCount,
					&nextRetryTime,
				)
			}

			continue
		}

		// Processar resposta
		defer resp.Body.Close()
		responseBody, _ := ioutil.ReadAll(resp.Body)
		responseStr := string(responseBody)

		if resp.StatusCode >= 400 {
			fmt.Printf("Reenvio de webhook retornou status de erro: %d\n", resp.StatusCode)

			// Para erros 5xx, tentar novamente se não excedeu o limite
			if resp.StatusCode >= 500 {
				nextAttemptCount := delivery.AttemptCount + 1
				maxAttempts := 5

				if nextAttemptCount > maxAttempts {
					// Desistir após número máximo de tentativas
					h.DB.UpdateWebhookDeliveryStatus(
						delivery.ID,
						"failed",
						resp.StatusCode,
						responseStr,
						fmt.Sprintf("Número máximo de tentativas alcançado. Último status: %d", resp.StatusCode),
						nextAttemptCount,
						nil,
					)
				} else {
					// Agendar próxima tentativa
					nextRetryDelay := time.Duration(5*math.Pow(5, float64(nextAttemptCount-1))) * time.Second
					nextRetryTime := time.Now().Add(nextRetryDelay)

					h.DB.UpdateWebhookDeliveryStatus(
						delivery.ID,
						"retrying",
						resp.StatusCode,
						responseStr,
						fmt.Sprintf("Erro de servidor: %d", resp.StatusCode),
						nextAttemptCount,
						&nextRetryTime,
					)
				}
			} else {
				// Para erros 4xx, não tentar novamente (problema no cliente)
				h.DB.UpdateWebhookDeliveryStatus(
					delivery.ID,
					"failed",
					resp.StatusCode,
					responseStr,
					fmt.Sprintf("Erro no cliente: %d", resp.StatusCode),
					delivery.AttemptCount+1,
					nil,
				)
			}
		} else {
			// Sucesso!
			h.DB.UpdateWebhookDeliveryStatus(
				delivery.ID,
				"success",
				resp.StatusCode,
				responseStr,
				"",
				delivery.AttemptCount+1,
				nil,
			)
		}
	}
}
