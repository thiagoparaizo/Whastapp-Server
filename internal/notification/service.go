// ==============================================
// NOVO ARQUIVO: internal/notification/service.go
// ==============================================

package notification

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"whatsapp-service/internal/database"
)

// NotificationService gerencia envio de notificações
type NotificationService struct {
	db              *database.DB
	assistantAPIURL string
	httpClient      *http.Client
	EmailSender     *EmailSender
	mailConfig      *EmailConfig
	webhookURL      string
}

// EmailConfig configurações de email
type EmailConfig struct {
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPassword string
	FromEmail    string
	ToEmails     []string // Lista de emails para receber alertas
}

// NotificationLevel define a severidade da notificação
type NotificationLevel string

const (
	NotificationLevelInfo     NotificationLevel = "info"
	NotificationLevelWarning  NotificationLevel = "warning"
	NotificationLevelError    NotificationLevel = "error"
	NotificationLevelCritical NotificationLevel = "critical"
)

// DeviceNotification representa uma notificação de dispositivo
type DeviceNotification struct {
	DeviceID        int64                  `json:"device_id"`
	DeviceName      string                 `json:"device_name"`
	TenantID        int64                  `json:"tenant_id"`
	Level           NotificationLevel      `json:"level"`
	Type            string                 `json:"type"`
	Title           string                 `json:"title"`
	Message         string                 `json:"message"`
	Timestamp       time.Time              `json:"timestamp"`
	Details         map[string]interface{} `json:"details,omitempty"`
	ErrorCode       string                 `json:"error_code,omitempty"`
	SuggestedAction string                 `json:"suggested_action,omitempty"`
}

// NewNotificationService cria um novo serviço de notificações
func NewNotificationService(db *database.DB, assistantAPIURL string, emailConfig *EmailConfig, webhookURL string) *NotificationService {
	var emailSender *EmailSender
	if emailConfig != nil && emailConfig.SMTPHost != "" {
		emailSender = NewEmailSender(emailConfig)
	}

	return &NotificationService{
		db:              db,
		assistantAPIURL: assistantAPIURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		EmailSender: emailSender,
		webhookURL:  webhookURL,
	}
}

// SendDeviceNotification envia uma notificação sobre um dispositivo
func (ns *NotificationService) SendDeviceNotification(notification *DeviceNotification) error {
	fmt.Printf("📤 Processando notificação %s para dispositivo %d\n", notification.Type, notification.DeviceID)

	// 1. PRIMEIRO: Verificar se deve notificar (ANTES de salvar)
	if !ns.shouldNotifyAdvanced(notification) {
		fmt.Printf("❌ Notificação ignorada (cooldown): %s para dispositivo %d\n", notification.Type, notification.DeviceID)
		return nil
	}

	fmt.Printf("✅ Cooldown OK, processando notificação %s para dispositivo %d\n", notification.Type, notification.DeviceID)

	// 2. DEPOIS: Salvar no banco de dados para auditoria (apenas se vai notificar)
	if err := ns.saveNotificationLog(notification); err != nil {
		fmt.Printf("Erro ao salvar log de notificação: %v\n", err)
		// Continua mesmo com erro de log - não deve impedir a notificação
	}

	fmt.Printf("✅ Cooldown OK, enviando notificação %s para dispositivo %d\n", notification.Type, notification.DeviceID)

	// 3. Enviar para API principal (Python)
	if err := ns.sendToAssistantAPI(notification); err != nil {
		fmt.Printf("Erro ao enviar notificação para API principal: %v\n", err)
	}

	// 4. Enviar por webhook se configurado
	if ns.webhookURL != "" {
		if err := ns.sendWebhookNotification(notification); err != nil {
			fmt.Printf("Erro ao enviar notificação por webhook: %v\n", err)
		}
	}

	// 5. Enviar por email para notificações críticas
	if notification.Level == NotificationLevelCritical || notification.Level == NotificationLevelError {
		if err := ns.sendEmailNotification(notification); err != nil {
			fmt.Printf("Erro ao enviar notificação por email: %v\n", err)
		}
	}

	fmt.Printf("✅ Notificação normal processada com sucesso\n")
	return nil
}

// Tipos específicos de notificações
func (ns *NotificationService) NotifyDeviceConnectionError(deviceID int64, deviceName string, tenantID int64, err error) {
	notification := &DeviceNotification{
		DeviceID:   deviceID,
		DeviceName: deviceName,
		TenantID:   tenantID,
		Level:      NotificationLevelError,
		Type:       "device_connection_error",
		Title:      "Erro de Conexão do Dispositivo",
		Message:    fmt.Sprintf("Dispositivo %s (ID: %d) falhou ao conectar", deviceName, deviceID),
		Timestamp:  time.Now(),
		ErrorCode:  "CONN_FAILED",
		Details: map[string]interface{}{
			"error":    err.Error(),
			"retry_at": time.Now().Add(5 * time.Minute),
		},
		SuggestedAction: "Verificar status da rede e tentar reconectar o dispositivo",
	}

	ns.SendDeviceNotification(notification)
}

func (ns *NotificationService) NotifyDeviceRequiresReauth(deviceID int64, deviceName string, tenantID int64) {
	notification := &DeviceNotification{
		DeviceID:        deviceID,
		DeviceName:      deviceName,
		TenantID:        tenantID,
		Level:           NotificationLevelWarning,
		Type:            "device_requires_reauth",
		Title:           "Dispositivo Requer Reautenticação",
		Message:         fmt.Sprintf("Dispositivo %s (ID: %d) necessita ser reautenticado", deviceName, deviceID),
		Timestamp:       time.Now(),
		ErrorCode:       "REAUTH_REQUIRED",
		SuggestedAction: "Gerar novo QR Code para reconectar o dispositivo",
	}

	ns.SendDeviceNotification(notification)
}

func (ns *NotificationService) NotifyClientOutdated(deviceID int64, deviceName string, tenantID int64, clientVersion string) {
	notification := &DeviceNotification{
		DeviceID:   deviceID,
		DeviceName: deviceName,
		TenantID:   tenantID,
		Level:      NotificationLevelCritical,
		Type:       "client_outdated",
		Title:      "Cliente WhatsApp Desatualizado",
		Message:    fmt.Sprintf("Dispositivo %s (ID: %d) usando versão desatualizada do cliente", deviceName, deviceID),
		Timestamp:  time.Now(),
		ErrorCode:  "CLIENT_OUTDATED_405",
		Details: map[string]interface{}{
			"client_version":  clientVersion,
			"update_required": true,
		},
		SuggestedAction: "Atualizar biblioteca whatsmeow para a versão mais recente",
	}

	ns.SendDeviceNotification(notification)
}

func (ns *NotificationService) NotifyWebhookDeliveryFailure(deviceID int64, webhookURL string, consecutiveFailures int) {
	level := NotificationLevelWarning
	if consecutiveFailures >= 5 {
		level = NotificationLevelError
	}

	notification := &DeviceNotification{
		DeviceID:  deviceID,
		TenantID:  0, // TODO: obter do dispositivo
		Level:     level,
		Type:      "webhook_delivery_failure",
		Title:     "Falha na Entrega de Webhook",
		Message:   fmt.Sprintf("Webhook %s falhou %d vezes consecutivas", webhookURL, consecutiveFailures),
		Timestamp: time.Now(),
		ErrorCode: "WEBHOOK_FAILED",
		Details: map[string]interface{}{
			"webhook_url":          webhookURL,
			"consecutive_failures": consecutiveFailures,
		},
		SuggestedAction: "Verificar URL do webhook e conectividade de rede",
	}

	ns.SendDeviceNotification(notification)
}

func (ns *NotificationService) NotifyDeviceDisconnected(deviceID int64, deviceName string, tenantID int64, reason string) {
	notification := &DeviceNotification{
		DeviceID:   deviceID,
		DeviceName: deviceName,
		TenantID:   tenantID,
		Level:      NotificationLevelWarning,
		Type:       "device_disconnected",
		Title:      "Dispositivo Desconectado",
		Message:    fmt.Sprintf("Dispositivo %s (ID: %d) foi desconectado", deviceName, deviceID),
		Timestamp:  time.Now(),
		ErrorCode:  "DEVICE_DISCONNECTED",
		Details: map[string]interface{}{
			"reason": reason,
		},
		SuggestedAction: "Verificar status da conexão e reconectar se necessário",
	}

	ns.SendDeviceNotification(notification)
}

// Implementações dos métodos auxiliares

func (ns *NotificationService) sendToAssistantAPI(notification *DeviceNotification) error {
	url := fmt.Sprintf("%s/whatsapp-notifications/notificar_saude_dispositivos", ns.assistantAPIURL)

	jsonData, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("erro ao serializar notificação: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("erro ao criar requisição: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req = req.WithContext(ctx)

	resp, err := ns.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("erro ao enviar requisição: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("API retornou status %d", resp.StatusCode)
	}

	return nil
}

func (ns *NotificationService) sendWebhookNotification(notification *DeviceNotification) error {
	if ns.webhookURL == "" {
		return nil
	}

	payload := map[string]interface{}{
		"type":         "device_notification",
		"notification": notification,
		"timestamp":    time.Now().Unix(),
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("erro ao serializar payload: %w", err)
	}

	req, err := http.NewRequest("POST", ns.webhookURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("erro ao criar requisição: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Notification-Type", "device_alert")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req = req.WithContext(ctx)

	resp, err := ns.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("erro ao enviar webhook: %w", err)
	}
	defer resp.Body.Close()

	return nil
}

func (ns *NotificationService) sendEmailNotification(notification *DeviceNotification) error {
	if ns.EmailSender == nil {
		return fmt.Errorf("email sender não configurado")
	}

	// Buscar lista de emails de destino
	emailList, err := ns.GetEmailsForNotification(notification)
	if err != nil || len(emailList) == 0 {
		return fmt.Errorf("nenhum email de destino configurado")
	}

	// Preparar assunto e corpo HTML
	subject := ns.buildEmailSubject(notification)
	htmlBody := ns.buildEmailHTMLBody(notification)

	// Enviar para cada email na lista
	var lastError error
	successCount := 0

	for _, email := range emailList {
		err := ns.EmailSender.SendHTML(email, subject, htmlBody)
		if err != nil {
			fmt.Printf("Erro ao enviar email para %s: %v\n", email, err)
			lastError = err
		} else {
			fmt.Printf("Email enviado com sucesso para %s\n", email)
			successCount++
		}
	}

	// Considerar sucesso parcial se pelo menos um email foi enviado
	if successCount > 0 {
		return nil
	}

	return fmt.Errorf("falha ao enviar para todos os destinatários: %v", lastError)
}

// buildEmailSubject cria assunto do email baseado no tipo/nível da notificação
func (ns *NotificationService) buildEmailSubject(notification *DeviceNotification) string {
	var emoji string
	switch notification.Level {
	case NotificationLevelCritical:
		emoji = "🚨"
	case NotificationLevelError:
		emoji = "❌"
	case NotificationLevelWarning:
		emoji = "⚠️"
	case NotificationLevelInfo:
		emoji = "ℹ️"
	}

	return fmt.Sprintf("%s [WhatsApp Service] %s - %s",
		emoji, notification.Level, notification.Title)
}

// buildEmailHTMLBody cria corpo HTML do email (similar ao template Python)
func (ns *NotificationService) buildEmailHTMLBody(notification *DeviceNotification) string {
	// Template HTML similar ao usado na API Python
	template := `
<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <style>
        body { font-family: Arial, sans-serif; line-height: 1.6; color: #333; }
        .container { max-width: 600px; margin: 0 auto; padding: 20px; }
        .header { background-color: %s; color: white; padding: 20px; border-radius: 5px 5px 0 0; }
        .content { background-color: #f9f9f9; padding: 20px; border: 1px solid #ddd; }
        .details { background-color: white; padding: 15px; border-radius: 5px; margin: 15px 0; }
        .footer { background-color: #333; color: white; padding: 10px; text-align: center; border-radius: 0 0 5px 5px; }
        .level { font-weight: bold; text-transform: uppercase; }
        .suggested-action { background-color: #e7f3ff; padding: 10px; border-left: 4px solid #007cba; margin: 15px 0; }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h2>%s</h2>
            <p>Alerta do Sistema WhatsApp Service</p>
        </div>
        
        <div class="content">
            <p><strong>Nível:</strong> <span class="level" style="color: %s;">%s</span></p>
            <p><strong>Mensagem:</strong> %s</p>
            
            <div class="details">
                <h3>Detalhes do Dispositivo:</h3>
                <ul>
                    <li><strong>Nome:</strong> %s (ID: %d)</li>
                    <li><strong>Tenant:</strong> %d</li>
                    <li><strong>Timestamp:</strong> %s</li>
                    %s
                    %s
                </ul>
            </div>
            
            %s
        </div>
        
        <div class="footer">
            <p>WhatsApp Service - Sistema de Monitoramento Automático</p>
        </div>
    </div>
</body>
</html>`

	// Cores baseadas no nível
	headerColor := "#007cba"
	textColor := "#333"
	switch notification.Level {
	case NotificationLevelCritical:
		headerColor = "#dc3545"
		textColor = "#dc3545"
	case NotificationLevelError:
		headerColor = "#fd7e14"
		textColor = "#fd7e14"
	case NotificationLevelWarning:
		headerColor = "#ffc107"
		textColor = "#856404"
	}

	// Campos opcionais
	errorCodeHTML := ""
	if notification.ErrorCode != "" {
		errorCodeHTML = fmt.Sprintf("<li><strong>Código do Erro:</strong> %s</li>", notification.ErrorCode)
	}

	detailsHTML := ""
	if len(notification.Details) > 0 {
		detailsHTML = "<li><strong>Detalhes Técnicos:</strong><br><code style='background-color: #f1f1f1; padding: 5px;'>"
		for key, value := range notification.Details {
			detailsHTML += fmt.Sprintf("%s: %v<br>", key, value)
		}
		detailsHTML += "</code></li>"
	}

	suggestedActionHTML := ""
	if notification.SuggestedAction != "" {
		suggestedActionHTML = fmt.Sprintf(`
			<div class="suggested-action">
				<h4>💡 Ação Sugerida:</h4>
				<p>%s</p>
			</div>`, notification.SuggestedAction)
	}

	return fmt.Sprintf(template,
		headerColor,                                    // Header background color
		notification.Title,                             // Header title
		textColor,                                      // Level text color
		notification.Level,                             // Level text
		notification.Message,                           // Main message
		notification.DeviceName, notification.DeviceID, // Device info
		notification.TenantID,                                // Tenant ID
		notification.Timestamp.Format("02/01/2006 15:04:05"), // Timestamp
		errorCodeHTML,       // Optional error code
		detailsHTML,         // Optional details
		suggestedActionHTML, // Optional suggested action
	)
}

// getEmailsForNotification determina quais emails devem receber a notificação
func (ns *NotificationService) GetEmailsForNotification(notification *DeviceNotification) ([]string, error) {
	var emails []string

	// Para notificações críticas e de erro, usar emails de admin do sistema
	if notification.Level == NotificationLevelCritical || notification.Level == NotificationLevelError {
		// Buscar emails de admins do sistema (configuração)
		systemEmails := ns.getSystemAdminEmails()
		emails = append(emails, systemEmails...)
	}

	// Para todos os tipos, tentar buscar emails específicos do tenant
	tenantEmails, err := ns.getTenantNotificationEmails(notification.TenantID)
	if err == nil {
		emails = append(emails, tenantEmails...)
	}

	// Remover duplicatas
	emailSet := make(map[string]bool)
	var uniqueEmails []string
	for _, email := range emails {
		if email != "" && !emailSet[email] {
			emailSet[email] = true
			uniqueEmails = append(uniqueEmails, email)
		}
	}

	return uniqueEmails, nil
}

// getSystemAdminEmails
func (ns *NotificationService) getSystemAdminEmails() []string {
	// Primeiro, tentar buscar do banco de dados
	if ns.db != nil {
		emails, err := ns.db.GetSystemAdminEmails("critical") // Para alertas críticos
		if err == nil && len(emails) > 0 {
			return emails
		}

		// Log do erro mas continue com fallback
		if err != nil {
			fmt.Printf("Erro ao buscar emails de admin do banco: %v\n", err)
		}
	}

	// Fallback: buscar da configuração de ambiente
	// Assumindo que existe uma forma de acessar a config
	// Por enquanto, hardcoded - mas deveria vir da config
	fallbackEmails := []string{
		"thiagoparaizo@gmail.com", // Seu email como fallback
	}

	return fallbackEmails
}

func (ns *NotificationService) getTenantNotificationEmails(tenantID int64) ([]string, error) {
	if ns.db == nil {
		return []string{}, fmt.Errorf("database não configurado")
	}

	// Buscar emails do tenant para diferentes níveis
	var allEmails []string

	// Buscar emails para críticos e erros
	criticalEmails, err := ns.db.GetTenantNotificationEmails(tenantID, "critical")
	if err == nil {
		allEmails = append(allEmails, criticalEmails...)
	}

	errorEmails, err := ns.db.GetTenantNotificationEmails(tenantID, "error")
	if err == nil {
		allEmails = append(allEmails, errorEmails...)
	}

	// Remover duplicatas
	emailSet := make(map[string]bool)
	var uniqueEmails []string
	for _, email := range allEmails {
		if email != "" && !emailSet[email] {
			emailSet[email] = true
			uniqueEmails = append(uniqueEmails, email)
		}
	}

	return uniqueEmails, nil
}

func (ns *NotificationService) buildEmailBody(notification *DeviceNotification) string {
	return fmt.Sprintf(`
Alerta do Sistema WhatsApp Service

Dispositivo: %s (ID: %d)
Tenant: %d
Nível: %s
Tipo: %s
Mensagem: %s
Timestamp: %s
Código de Erro: %s
Ação Sugerida: %s

Detalhes:
%+v
`, notification.DeviceName, notification.DeviceID, notification.TenantID,
		notification.Level, notification.Type, notification.Message,
		notification.Timestamp.Format("2006-01-02 15:04:05"),
		notification.ErrorCode, notification.SuggestedAction, notification.Details)
}

// saveNotificationLog salva log de notificação no banco de dados
func (ns *NotificationService) saveNotificationLog(notification *DeviceNotification) error {
	if ns.db == nil {
		return nil // Não é um erro crítico se não conseguir salvar
	}

	// Serializar details para JSON
	var detailsJSON sql.NullString
	if notification.Details != nil && len(notification.Details) > 0 {
		jsonBytes, err := json.Marshal(notification.Details)
		if err != nil {
			fmt.Printf("Erro ao serializar details da notificação: %v\n", err)
		} else {
			detailsJSON = sql.NullString{String: string(jsonBytes), Valid: true}
		}
	}

	// Criar log
	log := &database.NotificationLog{
		DeviceID:        sql.NullInt64{Int64: notification.DeviceID, Valid: true},
		TenantID:        sql.NullInt64{Int64: notification.TenantID, Valid: true},
		Level:           string(notification.Level),
		Type:            notification.Type,
		Title:           notification.Title,
		Message:         notification.Message,
		ErrorCode:       sql.NullString{String: notification.ErrorCode, Valid: notification.ErrorCode != ""},
		Details:         detailsJSON,
		SuggestedAction: sql.NullString{String: notification.SuggestedAction, Valid: notification.SuggestedAction != ""},
		CreatedAt:       notification.Timestamp,
	}

	return ns.db.SaveNotificationLog(log)
}

// CooldownConfig define configurações de cooldown por tipo de notificação
type CooldownConfig struct {
	DefaultMinutes  int
	CriticalMinutes int
	TypeSpecific    map[string]int // cooldown específico por tipo
}

// shouldNotifyAdvanced versão melhorada com configuração flexível
func (ns *NotificationService) shouldNotifyAdvanced(notification *DeviceNotification) bool {
	if ns.db == nil {
		fmt.Printf("⚠️ Banco de dados não configurado, permitindo notificação\n")
		return true
	}

	// Configuração de cooldown
	cooldownConfig := CooldownConfig{
		DefaultMinutes:  30,
		CriticalMinutes: 10,
		TypeSpecific: map[string]int{
			"client_outdated":          10, // Muito crítico, pouco cooldown
			"device_requires_reauth":   30, // Moderado
			"device_connection_error":  15, // Moderado
			"webhook_delivery_failure": 60, // Longo
			"device_disconnected":      45, // Longo
		},
	}

	// Query para buscar a PENÚLTIMA notificação (não a atual que pode ter sido inserida)
	query := `
		SELECT created_at 
		FROM notification_logs 
		WHERE device_id = $1 AND type = $2 
		ORDER BY created_at DESC 
		LIMIT 1
	`

	// Determinar cooldown baseado no tipo e nível
	var cooldownMinutes int
	if specificCooldown, exists := cooldownConfig.TypeSpecific[notification.Type]; exists {
		cooldownMinutes = specificCooldown
		fmt.Printf("🔧 Usando cooldown específico para %s: %d minutos\n", notification.Type, cooldownMinutes)
	} else if notification.Level == NotificationLevelCritical {
		cooldownMinutes = cooldownConfig.CriticalMinutes
		fmt.Printf("🚨 Usando cooldown crítico: %d minutos\n", cooldownMinutes)
	} else {
		cooldownMinutes = cooldownConfig.DefaultMinutes
		fmt.Printf("⏰ Usando cooldown padrão: %d minutos\n", cooldownMinutes)
	}

	var lastNotificationTime time.Time
	err := ns.db.QueryRow(query, notification.DeviceID, notification.Type).Scan(&lastNotificationTime)

	if err != nil {
		if err == sql.ErrNoRows {
			fmt.Printf("✅ Primeira notificação %s para dispositivo %d - PERMITIDA\n",
				notification.Type, notification.DeviceID)
			return true
		}

		fmt.Printf("⚠️ Erro ao verificar cooldown: %v - Permitindo notificação\n", err)
		return true
	}

	// Debug detalhado dos tempos
	now := time.Now()
	timeSinceLastNotification := now.Sub(lastNotificationTime)
	cooldownDuration := time.Duration(cooldownMinutes) * time.Minute
	shouldNotify := timeSinceLastNotification >= cooldownDuration

	fmt.Printf("⏱️ DEBUG COOLDOWN:\n")
	fmt.Printf("   Agora: %s\n", now.Format("2006-01-02 15:04:05"))
	fmt.Printf("   Última notificação: %s\n", lastNotificationTime.Format("2006-01-02 15:04:05"))
	fmt.Printf("   Tempo transcorrido: %v\n", timeSinceLastNotification.Round(time.Second))
	fmt.Printf("   Cooldown necessário: %v\n", cooldownDuration)
	fmt.Printf("   Pode notificar: %v\n", shouldNotify)

	if shouldNotify {
		fmt.Printf("✅ Cooldown expirado para %s dispositivo %d - PERMITIDA\n",
			notification.Type, notification.DeviceID)
	} else {
		timeRemaining := cooldownDuration - timeSinceLastNotification
		fmt.Printf("❌ Cooldown ativo para %s dispositivo %d - faltam %v - IGNORADA\n",
			notification.Type, notification.DeviceID, timeRemaining.Round(time.Minute))
	}

	return shouldNotify
}

// SendDeviceNotificationForced envia notificação ignorando cooldown
func (ns *NotificationService) SendDeviceNotificationForced(notification *DeviceNotification) error {
	fmt.Printf("🚨 FORÇANDO notificação %s para dispositivo %d (ignorando cooldown)\n",
		notification.Type, notification.DeviceID)

	// 1. Salvar no banco de dados para auditoria (sempre salvar)
	if err := ns.saveNotificationLog(notification); err != nil {
		fmt.Printf("Erro ao salvar log de notificação: %v\n", err)
	}

	// 2. PULAR verificação de cooldown quando forçado
	fmt.Printf("⏭️ Pulando verificação de cooldown (forçado)\n")

	// 3. Enviar para API principal (Python)
	if err := ns.sendToAssistantAPI(notification); err != nil {
		fmt.Printf("Erro ao enviar notificação para API principal: %v\n", err)
	}

	// 4. Enviar por webhook se configurado
	if ns.webhookURL != "" {
		if err := ns.sendWebhookNotification(notification); err != nil {
			fmt.Printf("Erro ao enviar notificação por webhook: %v\n", err)
		}
	}

	// 5. Enviar por email SEMPRE quando forçado (independente do nível)
	if err := ns.sendEmailNotification(notification); err != nil {
		fmt.Printf("Erro ao enviar notificação por email: %v\n", err)
	} else {
		fmt.Printf("✅ Email de notificação forçada enviado\n")
	}

	fmt.Printf("✅ Notificação forçada processada com sucesso\n")
	return nil
}
