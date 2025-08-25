// internal/whatsapp/manager.go
package whatsapp

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"

	"whatsapp-service/internal/database"
	"whatsapp-service/internal/notification"
)

// Manager gerencia múltiplos clientes WhatsApp
type Manager struct {
	clients             map[int64]*Client // Mapeado por deviceID
	container           *sqlstore.Container
	db                  *database.DB
	logger              waLog.Logger
	mutex               sync.Mutex
	eventHandlers       []func(deviceID int64, evt interface{})
	eventHandler        *EventHandler
	notificationService *notification.NotificationService
}

// método para configurar notificações:
func (m *Manager) SetNotificationService(ns *notification.NotificationService) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.notificationService = ns

	// Também configurar no eventHandler se já existir
	if m.eventHandler != nil {
		// EventHandler já tem referência ao manager, então não precisa fazer nada extra
		fmt.Println("Notification service configurado no manager e disponível para EventHandler")
	}
}

// GetDetailedStatus retorna status detalhado do manager
func (m *Manager) GetDetailedStatus() map[string]interface{} {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	status := map[string]interface{}{
		"clients_in_memory": len(m.clients),
		"devices":           make([]map[string]interface{}, 0),
	}

	for deviceID, client := range m.clients {
		deviceStatus := map[string]interface{}{
			"device_id": deviceID,
			"connected": false,
			"has_store": false,
			"jid":       "",
		}

		if client != nil {
			deviceStatus["connected"] = client.IsConnected()

			if client.Client != nil {
				deviceStatus["has_store"] = client.Client.Store != nil

				if client.Client.Store != nil && client.Client.Store.ID != nil {
					deviceStatus["jid"] = client.Client.Store.ID.String()
				}
			}
		}

		status["devices"] = append(status["devices"].([]map[string]interface{}), deviceStatus)
	}

	return status
}

// NewManager cria um novo gerenciador de clientes
func NewManager(dbString string, postgresDB *database.DB) (*Manager, error) {
	// Inicializar logger
	logger := waLog.Stdout("WhatsApp", "INFO", true)

	// Criar context para inicialização
	ctx := context.Background()

	// Inicializar container de dispositivos do whatsmeow
	container, err := sqlstore.New(ctx, "postgres", dbString, logger)
	if err != nil {
		return nil, fmt.Errorf("falha ao criar container: %w", err)
	}

	// Criar o manager primeiro (sem o eventHandler)
	manager := &Manager{
		clients:       make(map[int64]*Client),
		container:     container,
		db:            postgresDB,
		logger:        logger,
		eventHandlers: make([]func(deviceID int64, evt interface{}), 0),
	}

	// Agora criar o eventHandler passando o manager
	eventHandler := NewEventHandler(postgresDB, manager)

	// Atribuir o eventHandler ao manager
	manager.eventHandler = eventHandler

	// Adicionar o handler de eventos ao pipeline global
	manager.AddEventHandler(eventHandler.HandleEvent)

	return manager, nil
}

// GetClient obtém ou cria um cliente para um dispositivo
func (m *Manager) GetClient(deviceID int64) (*Client, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Verificar se o cliente já existe
	if client, exists := m.clients[deviceID]; exists {
		return client, nil
	}

	// Buscar dispositivo no banco
	device, err := m.db.GetDeviceByID(deviceID)
	if err != nil {
		return nil, err
	}

	if device == nil {
		return nil, fmt.Errorf("dispositivo não encontrado")
	}

	// Verificar se o dispositivo está aprovado ou conectado
	if device.Status != database.DeviceStatusApproved &&
		device.Status != database.DeviceStatusConnected {
		return nil, fmt.Errorf("dispositivo não está aprovado para conexão ou já está conectado")
	}

	// Obtendo o dispositivo do whatsmeow
	var deviceStore *store.Device
	var needsReauth bool = false
	if device.JID.Valid && device.JID.String != "" {
		// Dispositivo tem JID, tentar recuperar sessão
		wajid, err := types.ParseJID(device.JID.String)
		if err != nil {
			fmt.Printf("JID inválido para dispositivo %d: %v\n", deviceID, err)
			needsReauth = true
		} else {
			// Tentar obter sessão existente com context
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			deviceStore, err = m.container.GetDevice(ctx, wajid)
			if err != nil || deviceStore == nil {
				fmt.Printf("Sessão não encontrada para dispositivo %d (JID: %s)\n", deviceID, device.JID.String)
				needsReauth = true
			}
		}
	}

	// Se não conseguiu recuperar sessão ou não tem JID, criar nova
	if deviceStore == nil || needsReauth {
		fmt.Printf("Criando nova sessão para dispositivo %d\n", deviceID)
		deviceStore = m.container.NewDevice()

		// Se tinha JID mas perdeu a sessão, marcar para reautenticação
		if device.JID.Valid && device.JID.String != "" {
			fmt.Printf("Dispositivo %d perdeu sessão, marcando para reautenticação\n", deviceID)

			// Limpar JID do dispositivo no banco
			device.JID = sql.NullString{Valid: false}
			device.RequiresReauth = true
			err = m.db.UpdateDevice(device)
			if err != nil {
				fmt.Printf("Erro ao atualizar dispositivo para reauth: %v\n", err)
			}
		}
	}

	// Criar cliente
	client := NewClient(deviceID, device.TenantID, deviceStore, m.db, m.logger, m) // Último parâmetro é o manager //TODO add , device.deviceName string

	// Adicionar handler global de eventos
	client.AddEventHandler(func(evt interface{}) {
		for _, handler := range m.eventHandlers {
			handler(deviceID, evt)
		}
	})

	// Adicionar handler de eventos do manager ao cliente
	if m.eventHandler != nil {
		client.EventHandlers = append(client.EventHandlers, func(evt interface{}) {
			for _, handler := range m.eventHandlers {
				handler(deviceID, evt)
			}
		})
	}

	// Armazenar cliente
	m.clients[deviceID] = client

	return client, nil
}

// ConnectClient conecta um cliente específico
func (m *Manager) ConnectClient(deviceID int64) error {
	// client, err := m.GetClient(deviceID)
	// if err != nil {
	// 	return err
	// }

	// return client.Connect()
	return m.ConnectClientSafely(deviceID)
}

// DisconnectClient desconecta um cliente específico
func (m *Manager) DisconnectClient(deviceID int64) error {
	m.mutex.Lock()
	client, exists := m.clients[deviceID]
	m.mutex.Unlock()

	if !exists {
		return fmt.Errorf("cliente não encontrado")
	}

	client.Disconnect()
	return nil
}

// GetQRChannel obtém um canal para o código QR de um dispositivo
func (m *Manager) GetQRChannel(ctx context.Context, deviceID int64) (<-chan string, error) {
	client, err := m.GetClient(deviceID)
	if err != nil {
		return nil, err
	}

	return client.GetQRChannel(ctx)
}

// Método auxiliar para acessar notification service
func (h *EventHandler) GetNotificationService2() *notification.NotificationService {
	if h.Manager != nil {
		return h.Manager.notificationService
	}
	return nil
}

// SendTextMessage envia uma mensagem de texto de um dispositivo específico
func (m *Manager) SendTextMessage(deviceID int64, to string, text string) (string, error) {
	client, err := m.GetClient(deviceID)
	if err != nil {
		return "", err
	}

	return client.SendTextMessage(to, text)
}

// AddEventHandler adiciona um handler global de eventos
func (m *Manager) AddEventHandler(handler func(deviceID int64, evt interface{})) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	m.eventHandlers = append(m.eventHandlers, handler)
}

// // ConnectAllApproved conecta todos os dispositivos aprovados
// func (m *Manager) ConnectAllApproved() {
// 	devices, err := m.db.GetAllDevicesByStatus(database.DeviceStatusApproved)
// 	if err != nil {
// 		fmt.Printf("Erro ao buscar dispositivos aprovados: %v\n", err)
// 		return
// 	}

// 	for _, device := range devices {
// 		// Tentar conectar em uma goroutine separada
// 		go func(d database.WhatsAppDevice) {
// 			err := m.ConnectClient(d.ID)
// 			if err != nil {
// 				fmt.Printf("Erro ao conectar dispositivo %d: %v\n", d.ID, err)
// 			}
// 		}(device)
// 	}
// }

// ConnectAllApproved conecta todos os dispositivos aprovados com tratamento de erro robusto
func (m *Manager) ConnectAllApproved() {
	fmt.Println("Iniciando conexão de dispositivos...")

	// Buscar dispositivos que podem ser conectados
	devices, err := m.db.Query(`
		SELECT id, name, status, jid, requires_reauth
		FROM whatsapp_devices 
		WHERE status IN ('approved', 'connected') 
		AND status != 'disabled'
		ORDER BY updated_at DESC
	`)
	if err != nil {
		fmt.Printf("Erro ao buscar dispositivos: %v\n", err)
		return
	}
	defer devices.Close()

	var approvedDevices, connectedDevices []database.WhatsAppDevice

	for devices.Next() {
		var device database.WhatsAppDevice
		if err := devices.Scan(&device.ID, &device.Name, &device.Status, &device.JID, &device.RequiresReauth); err != nil {
			continue
		}

		if device.Status == database.DeviceStatusApproved {
			approvedDevices = append(approvedDevices, device)
		} else if device.Status == database.DeviceStatusConnected {
			connectedDevices = append(connectedDevices, device)
		}
	}

	fmt.Printf("Encontrados %d dispositivos aprovados e %d conectados\n",
		len(approvedDevices), len(connectedDevices))

	// Usar um semáforo para limitar conexões simultâneas
	semaphore := make(chan struct{}, 2) // Máximo 2 conexões simultâneas

	// Primeiro, tentar reconectar dispositivos que estavam conectados
	for _, device := range connectedDevices {
		go func(d database.WhatsAppDevice) {
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			fmt.Printf("Tentando reconectar dispositivo %d (%s)\n", d.ID, d.Name)

			err := m.ConnectClientSafely(d.ID)
			if err != nil {
				fmt.Printf("Erro ao reconectar dispositivo %d (%s): %v\n", d.ID, d.Name, err)

				// Se falhar na reconexão, marcar como approved para permitir novo QR
				if m.isCriticalConnectionError(err) {
					fmt.Printf("Erro crítico na reconexão, marcando dispositivo %d como approved\n", d.ID)
					m.db.UpdateDeviceStatus(d.ID, database.DeviceStatusApproved)
				}
			} else {
				fmt.Printf("Dispositivo %d (%s) reconectado com sucesso\n", d.ID, d.Name)
			}
		}(device)
	}

	// Depois, conectar dispositivos aprovados que nunca foram conectados
	for _, device := range approvedDevices {
		// Só tentar conectar se tem JID válido
		if device.JID.Valid && device.JID.String != "" && !device.RequiresReauth {
			go func(d database.WhatsAppDevice) {
				semaphore <- struct{}{}
				defer func() { <-semaphore }()

				fmt.Printf("Tentando conectar dispositivo aprovado %d (%s)\n", d.ID, d.Name)

				err := m.ConnectClientSafely(d.ID)
				if err != nil {
					fmt.Printf("Erro ao conectar dispositivo aprovado %d (%s): %v\n", d.ID, d.Name, err)
				} else {
					fmt.Printf("Dispositivo aprovado %d (%s) conectado com sucesso\n", d.ID, d.Name)
				}
			}(device)
		} else {
			fmt.Printf("Dispositivo %d (%s) aguardando QR Code (sem JID ou requer reauth)\n",
				device.ID, device.Name)
		}
	}

	// Buscar dispositivos que necessitam reautenticação e notificar
	reauthDevices, err := m.db.GetDevicesRequiringReauth()
	if err != nil {
		fmt.Printf("Erro ao buscar dispositivos que requerem reauth: %v\n", err)
	} else if len(reauthDevices) > 0 {
		fmt.Printf("Encontrados %d dispositivos que necessitam reautenticação\n", len(reauthDevices))

		// Notificar sobre cada dispositivo que precisa de reauth
		for _, device := range reauthDevices {
			if m.notificationService != nil {
				fmt.Printf("🔔 Notificando reautenticação necessária para dispositivo %d (%s)\n", device.ID, device.Name)
				m.notificationService.NotifyDeviceRequiresReauth(device.ID, device.Name, device.TenantID)
			}
		}
	}
}

// GetNotificationService retorna o serviço de notificação
func (m *Manager) GetNotificationService() *notification.NotificationService {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	return m.notificationService
}

// ConnectClientSafely conecta um cliente com tratamento de erro mais robusto
func (m *Manager) ConnectClientSafely(deviceID int64) error {
	fmt.Printf("Tentando conectar dispositivo %d\n", deviceID)

	// Verificar se já existe e está conectado
	m.mutex.Lock()
	if client, exists := m.clients[deviceID]; exists {
		if client.IsConnected() {
			m.mutex.Unlock()
			fmt.Printf("Dispositivo %d já está conectado\n", deviceID)
			return nil
		}

		// Se existe mas não está conectado, remover
		fmt.Printf("Removendo cliente desconectado para dispositivo %d\n", deviceID)
		delete(m.clients, deviceID)
	}
	m.mutex.Unlock()

	// Usar GetClient que já tem toda a lógica necessária
	client, err := m.GetClient(deviceID)
	if err != nil {
		// NOTIFICAÇÃO 1: Erro ao obter/criar cliente
		if m.notificationService != nil {
			device, dbErr := m.db.GetDeviceByID(deviceID)
			if dbErr == nil && device != nil {
				m.notificationService.NotifyDeviceConnectionError(deviceID, device.Name, device.TenantID, err)
			}
		}
		return fmt.Errorf("erro ao obter/criar cliente: %w", err)
	}

	// Tentar conectar com timeout
	connectChan := make(chan error, 1)
	go func() {
		connectChan <- client.Connect()
	}()

	// Aguardar conexão com timeout de 30 segundos
	select {
	case err := <-connectChan:
		if err != nil {
			// NOTIFICAÇÃO 2: Erro na conexão efetiva
			if m.notificationService != nil {
				device, dbErr := m.db.GetDeviceByID(deviceID)
				if dbErr == nil && device != nil {
					// Verificar tipo específico de erro
					if strings.Contains(err.Error(), "Client outdated") {
						// Extrair versão do cliente se possível
						clientVersion := extractClientVersion(err.Error())
						m.notificationService.NotifyClientOutdated(deviceID, device.Name, device.TenantID, clientVersion)
					} else if strings.Contains(err.Error(), "websocket") {
						m.notificationService.NotifyDeviceConnectionError(deviceID, device.Name, device.TenantID, err)
					} else {
						m.notificationService.NotifyDeviceConnectionError(deviceID, device.Name, device.TenantID, err)
					}
				}
			}
			return fmt.Errorf("falha na conexão: %w", err)
		}

		fmt.Printf("Dispositivo %d conectado com sucesso\n", deviceID)
		return nil

	case <-time.After(30 * time.Second):
		// NOTIFICAÇÃO 3: Timeout na conexão
		if m.notificationService != nil {
			device, dbErr := m.db.GetDeviceByID(deviceID)
			if dbErr == nil && device != nil {
				timeoutErr := fmt.Errorf("timeout na conexão após 30 segundos")
				m.notificationService.NotifyDeviceConnectionError(deviceID, device.Name, device.TenantID, timeoutErr)
			}
		}
		return fmt.Errorf("timeout ao conectar dispositivo %d", deviceID)
	}
}

// Função auxiliar para extrair versão do cliente do erro
func extractClientVersion(errorMsg string) string {
	// Regex para encontrar padrões como "client version: 2.3000.1022192018"
	re := regexp.MustCompile(`client version:\s*([0-9.]+)`)
	matches := re.FindStringSubmatch(errorMsg)
	if len(matches) > 1 {
		return matches[1]
	}
	return "unknown"
}

// createClientWithRetry cria um cliente com tentativas de retry
func (m *Manager) createClientWithRetry(deviceID int64, maxRetries int) (*Client, error) {
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		fmt.Printf("Tentativa %d/%d de criar cliente para dispositivo %d\n", attempt, maxRetries, deviceID)

		client, err := m.GetClient(deviceID)
		if err == nil {
			return client, nil
		}

		lastErr = err
		fmt.Printf("Falha na tentativa %d para dispositivo %d: %v\n", attempt, deviceID, err)

		if attempt < maxRetries {
			// Aguardar antes da próxima tentativa
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
	}

	return nil, fmt.Errorf("falha após %d tentativas: %w", maxRetries, lastErr)
}

// isCriticalConnectionError verifica se um erro de conexão é crítico
func (m *Manager) isCriticalConnectionError(err error) bool {
	if err == nil {
		return false
	}

	errorStr := err.Error()

	// Erros que indicam necessidade de reautenticação
	criticalErrors := []string{
		"invalid memory address",
		"nil pointer dereference",
		"session not found",
		"invalid session",
		"unauthorized",
		"logged out",
		"connection refused",
		"handshake failed",
	}

	for _, criticalErr := range criticalErrors {
		if strings.Contains(strings.ToLower(errorStr), criticalErr) {
			return true
		}
	}

	return false
}

// Método auxiliar para limpeza de sessões corrompidas
func (m *Manager) CleanCorruptedSessions() error {
	fmt.Println("Verificando sessões para limpeza...")

	ctx := context.Background() // Context para operações de banco/whatsmeow

	// CORREÇÃO: Buscar apenas dispositivos com problemas reais
	// NÃO limpar dispositivos conectados que só têm requires_reauth=true
	devices, err := m.db.Query(`
		SELECT id, jid, name, status, requires_reauth 
		FROM whatsapp_devices 
		WHERE requires_reauth = true 
		AND status NOT IN ('connected')  -- NÃO limpar dispositivos conectados
		AND status != 'disabled'        -- NÃO mexer em dispositivos desabilitados
	`)
	if err != nil {
		return fmt.Errorf("erro ao buscar dispositivos para limpeza: %w", err)
	}
	defer devices.Close()

	var cleanedCount int
	for devices.Next() {
		var deviceID int64
		var jid sql.NullString
		var name, status string
		var requiresReauth bool

		if err := devices.Scan(&deviceID, &jid, &name, &status, &requiresReauth); err != nil {
			continue
		}

		fmt.Printf("Analisando dispositivo %d (%s) - Status: %s, Reauth: %v\n",
			deviceID, name, status, requiresReauth)

		// Verificar se realmente precisa de limpeza
		needsCleaning := false

		if jid.Valid && jid.String != "" {
			// Verificar se a sessão existe no whatsmeow
			wajid, err := types.ParseJID(jid.String)
			if err != nil {
				fmt.Printf("JID inválido para dispositivo %d: %v\n", deviceID, err)
				needsCleaning = true
			} else {
				// Tentar obter sessão
				deviceStore, err := m.container.GetDevice(ctx, wajid)
				if err != nil || deviceStore == nil {
					fmt.Printf("Sessão não encontrada no whatsmeow para dispositivo %d\n", deviceID)
					needsCleaning = true
				}
			}
		} else if status == "approved" {
			// Dispositivo aprovado sem JID é normal, não precisa limpeza
			fmt.Printf("Dispositivo %d aprovado sem JID - normal\n", deviceID)
		}

		if needsCleaning {
			fmt.Printf("Limpando sessão corrompida do dispositivo %d (%s)\n", deviceID, name)

			// Remover cliente da memória se existir
			if client, exists := m.clients[deviceID]; exists {
				if client.Client != nil {
					client.Client.Disconnect()
				}
				delete(m.clients, deviceID)
			}

			// Limpar dados de sessão do banco
			err := m.db.ClearDeviceSession(deviceID)
			if err != nil {
				fmt.Printf("Erro ao limpar sessão do dispositivo %d: %v\n", deviceID, err)
			} else {
				cleanedCount++
			}
		} else {
			fmt.Printf("Dispositivo %d não precisa de limpeza\n", deviceID)
		}
	}

	if cleanedCount > 0 {
		fmt.Printf("Limpeza concluída: %d dispositivos limpos\n", cleanedCount)
	} else {
		fmt.Printf("Nenhum dispositivo precisou de limpeza\n")
	}

	return nil
}

// Método para verificar saúde dos clientes conectados
func (m *Manager) HealthCheckClients() {
	log.Printf("HealthCheckClients INIT: Verificando saúde dos clientes conectados...")
	//fmt.Println("HealthCheckClients INIT: Verificando saúde dos clientes conectados...")

	for deviceID, client := range m.clients {
		if client == nil || client.Client == nil {
			log.Printf("Cliente inválido encontrado para dispositivo %d, removendo\n", deviceID)
			delete(m.clients, deviceID)
			continue
		}

		if !client.IsConnected() {
			log.Printf("Cliente desconectado encontrado para dispositivo %d, removendo\n", deviceID)
			delete(m.clients, deviceID)

			// Atualizar status no banco
			device, err := m.db.GetDeviceByID(deviceID)
			if err == nil && device != nil {
				device.Status = database.DeviceStatusApproved
				device.RequiresReauth = true
				m.db.UpdateDevice(device)
			}
		}
	}

	// Buscar dispositivos que necessitam reautenticação e notificar
	log.Printf("Buscando dispositivos que precisam de reautenticação...")
	reauthDevices, err := m.db.GetDevicesRequiringReauth()
	if err != nil {
		log.Printf("Erro ao buscar dispositivos que requerem reauth: %v\n", err)
	} else if len(reauthDevices) > 0 {
		log.Printf("Encontrados %d dispositivos que necessitam reautenticação\n", len(reauthDevices))

		// Notificar sobre cada dispositivo que precisa de reauth
		for _, device := range reauthDevices {
			if m.notificationService != nil {
				log.Printf("🔔 Notificando reautenticação necessária para dispositivo %d (%s)\n", device.ID, device.Name)
				m.notificationService.NotifyDeviceRequiresReauth(device.ID, device.Name, device.TenantID)
			}
		}
	}

	log.Printf("HealthCheckClients END")
}

// Adicionar ao método de inicialização do Manager
func (m *Manager) InitializeWithCleanup() error {
	fmt.Println("Inicializando Manager com limpeza...")

	// Primeiro, limpar sessões corrompidas
	if err := m.CleanCorruptedSessions(); err != nil {
		fmt.Printf("Aviso: erro na limpeza de sessões: %v\n", err)
	}

	// Verificar saúde dos clientes
	m.HealthCheckClients()

	// Aguardar um pouco antes de tentar reconectar
	time.Sleep(2 * time.Second)

	// Conectar dispositivos aprovados
	m.ConnectAllApproved()

	return nil
}

// ReconnectAllConnected tenta reconectar todos os dispositivos que estavam conectados
// func (m *Manager) ReconnectAllConnected() {
// 	devices, err := m.db.GetAllDevicesByStatus(database.DeviceStatusConnected)
// 	if err != nil {
// 		fmt.Printf("Erro ao buscar dispositivos conectados: %v\n", err)
// 		return
// 	}

// 	for _, device := range devices {
// 		// Tentar conectar em uma goroutine separada
// 		go func(d database.WhatsAppDevice) {
// 			err := m.ConnectClient(d.ID)
// 			if err != nil {
// 				fmt.Printf("Erro ao reconectar dispositivo %d: %v\n", d.ID, err)
// 			}
// 		}(device)
// 	}
// }

func (m *Manager) ConfigureWebhook(config *WebhookConfig) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Configurar webhook no EventHandler
	if m.eventHandler != nil {
		m.eventHandler.SetWebhookConfig(config)
	}

	// Armazenar configuração no banco de dados para persistência
	if m.db != nil {
		dbConfig := &database.WebhookConfig{
			TenantID:  config.TenantID,
			URL:       config.URL,
			Secret:    config.Secret,
			Events:    config.Events,
			DeviceIDs: config.DeviceIDs,
			Enabled:   config.Enabled,
		}

		// Verificar se já existe uma configuração para este tenant
		existingConfigs, err := m.db.GetWebhookConfigsByTenant(config.TenantID)
		if err != nil {
			fmt.Printf("Erro ao buscar configurações existentes: %v\n", err)
		} else if len(existingConfigs) > 0 {
			// Atualizar configuração existente
			dbConfig.ID = existingConfigs[0].ID
			err = m.db.UpdateWebhookConfig(dbConfig)
			if err != nil {
				fmt.Printf("Erro ao atualizar configuração de webhook: %v\n", err)
			}
		} else {
			// Criar nova configuração
			err = m.db.SaveWebhookConfig(dbConfig)
			if err != nil {
				fmt.Printf("Erro ao salvar configuração de webhook: %v\n", err)
			}
		}
	}
}

// Adicionar método para enviar evento de teste
func (m *Manager) SendTestWebhook(url string, secret string, payload interface{}) (bool, error) {
	if m.eventHandler != nil {
		return m.eventHandler.SendTestWebhook(url, secret, payload)
	}
	return false, fmt.Errorf("event handler não está inicializado")
}

// Iniciar worker de processamento de reenvio de webhooks
// func (m *Manager) StartWebhookProcessor() {
// 	go func() {
// 		// Processar a cada 30 segundos
// 		ticker := time.NewTicker(30 * time.Second)
// 		defer ticker.Stop()

// 		for {
// 			select {
// 			case <-ticker.C:
// 				if m.eventHandler != nil {
// 					m.eventHandler.ProcessPendingWebhooks()
// 				}
// 			}
// 		}
// 	}()
// }

func (m *Manager) Connect() error {
	//IGNORANDO, POIS OS WEBHOOKS SÃO PROCESSADOS NA API
	// Iniciar o serviço de processamento de webhooks em background
	//m.StartWebhookProcessor()
	// fmt.Println("Iniciado processador de webhooks pendentes")

	// // Carregar configurações de webhook do banco de dados
	// if m.db != nil {
	// 	// Obter todos os tenants
	// 	allTenants, err := m.db.GetAllTenants()
	// 	if err != nil {
	// 		fmt.Printf("Erro ao buscar tenants: %v\n", err)
	// 	} else {
	// 		// Para cada tenant, verificar configurações de webhook
	// 		for _, tenant := range allTenants {
	// 			configs, err := m.db.GetWebhookConfigsByTenant(tenant["ID"].(int64))
	// 			if err != nil {
	// 				fmt.Printf("Erro ao buscar configurações para tenant %d: %v\n", tenant["ID"], err)
	// 				continue
	// 			}

	// 			// Usar a primeira configuração ativa encontrada
	// 			var enabledConfig *WebhookConfig
	// 			for _, config := range configs {
	// 				if config.Enabled {
	// 					// Converter para o formato do webhook
	// 					enabledConfig = &WebhookConfig{
	// 						URL:       config.URL,
	// 						Secret:    config.Secret,
	// 						Events:    config.Events,
	// 						TenantID:  config.TenantID,
	// 						DeviceIDs: config.DeviceIDs,
	// 						Enabled:   config.Enabled,
	// 					}
	// 					break
	// 				}
	// 			}

	// 			// Se encontrou configuração ativa, definir no event handler
	// 			if enabledConfig != nil {
	// 				fmt.Printf("Configurando webhook para tenant %d: %s\n", tenant["ID"], enabledConfig.URL)
	// 				if m.eventHandler != nil {
	// 					m.eventHandler.SetWebhookConfig(enabledConfig)
	// 				}
	// 			}
	// 		}
	// 	}
	// }

	// Conectar todos os dispositivos aprovados
	fmt.Println("Iniciando conexão de dispositivos aprovados")
	go m.ConnectAllApproved()

	// Reconectar dispositivos anteriormente conectados
	//fmt.Println("Tentando reconectar dispositivos previamente conectados")
	//go m.ReconnectAllConnected()

	return nil
}
