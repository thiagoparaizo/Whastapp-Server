// internal/whatsapp/manager.go
package whatsapp

import (
	"context"
	"fmt"
	"sync"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"

	"whatsapp-service/internal/database"
)

// Manager gerencia múltiplos clientes WhatsApp
type Manager struct {
	clients       map[int64]*Client // Mapeado por deviceID
	container     *sqlstore.Container
	db            *database.DB
	logger        waLog.Logger
	mutex         sync.Mutex
	eventHandlers []func(deviceID int64, evt interface{})
	eventHandler  *EventHandler
}

// NewManager cria um novo gerenciador de clientes
func NewManager(dbString string, postgresDB *database.DB) (*Manager, error) {
	// Inicializar logger
	logger := waLog.Stdout("WhatsApp", "INFO", true)

	// Inicializar container de dispositivos do whatsmeow
	container, err := sqlstore.New("postgres", dbString, logger)
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
	if device.JID.Valid {
		// Se já tem JID, usar para recuperar o dispositivo
		wajid, err := types.ParseJID(device.JID.String)
		if err != nil {
			return nil, fmt.Errorf("JID inválido: %w", err)
		}

		deviceStore, err = m.container.GetDevice(wajid)
		if err != nil {
			// Se não conseguir recuperar, criar um novo
			deviceStore = m.container.NewDevice()
		}
	} else {
		// Se não tem JID, criar um novo dispositivo
		deviceStore = m.container.NewDevice()
	}

	// Criar cliente
	client := NewClient(deviceID, device.TenantID, deviceStore, m.db, m.logger) //TODOadd , device.deviceName string

	// Adicionar handler global de eventos
	client.AddEventHandler(func(evt interface{}) {
		for _, handler := range m.eventHandlers {
			handler(deviceID, evt)
		}
	})

	// Armazenar cliente
	m.clients[deviceID] = client

	return client, nil
}

// ConnectClient conecta um cliente específico
func (m *Manager) ConnectClient(deviceID int64) error {
	client, err := m.GetClient(deviceID)
	if err != nil {
		return err
	}

	return client.Connect()
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

// ConnectAllApproved conecta todos os dispositivos aprovados
func (m *Manager) ConnectAllApproved() {
	devices, err := m.db.GetAllDevicesByStatus(database.DeviceStatusApproved)
	if err != nil {
		fmt.Printf("Erro ao buscar dispositivos aprovados: %v\n", err)
		return
	}

	for _, device := range devices {
		// Tentar conectar em uma goroutine separada
		go func(d database.WhatsAppDevice) {
			err := m.ConnectClient(d.ID)
			if err != nil {
				fmt.Printf("Erro ao conectar dispositivo %d: %v\n", d.ID, err)
			}
		}(device)
	}
}

// ReconnectAllConnected tenta reconectar todos os dispositivos que estavam conectados
func (m *Manager) ReconnectAllConnected() {
	devices, err := m.db.GetAllDevicesByStatus(database.DeviceStatusConnected)
	if err != nil {
		fmt.Printf("Erro ao buscar dispositivos conectados: %v\n", err)
		return
	}

	for _, device := range devices {
		// Tentar conectar em uma goroutine separada
		go func(d database.WhatsAppDevice) {
			err := m.ConnectClient(d.ID)
			if err != nil {
				fmt.Printf("Erro ao reconectar dispositivo %d: %v\n", d.ID, err)
			}
		}(device)
	}
}

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
	fmt.Println("Tentando reconectar dispositivos previamente conectados")
	go m.ReconnectAllConnected()

	return nil
}
