// cmd/server/main.go
package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"whatsapp-service/internal/api"
	"whatsapp-service/internal/config"
	"whatsapp-service/internal/database"
	"whatsapp-service/internal/notification"
	"whatsapp-service/internal/whatsapp"
)

func main() {
	// Carregar configurações
	cfg := config.Load()

	// Validar configuração de email antes de inicializar
	if err := cfg.ValidateEmailConfig(); err != nil {
		log.Fatalf("Erro na configuração de email: %v", err)
	}

	// Configurar modo do Gin
	if cfg.LogLevel != "DEBUG" {
		gin.SetMode(gin.ReleaseMode)
	}

	// Conectar ao banco de dados
	db, err := database.New(cfg.PostgresConnStr, cfg.AssistantAPIURL)
	if err != nil {
		log.Fatalf("Erro ao conectar ao banco de dados: %v | erro: %v", cfg.PostgresConnStr, err)
	}

	// Criar gerenciador de WhatsApp
	waMgr, err := whatsapp.NewManager(cfg.WhatsmeowConnStr, db)
	if err != nil {
		log.Fatalf("Erro ao criar gerenciador de WhatsApp: %v", err)
	}

	// Configurar sistema de notificações
	var notificationService *notification.NotificationService
	if cfg.NotificationsEnabled {
		emailConfig := &notification.EmailConfig{
			SMTPHost:     cfg.SMTPHost,
			SMTPPort:     cfg.SMTPPort,
			SMTPUser:     cfg.SMTPUser,
			SMTPPassword: cfg.SMTPPassword,
			FromEmail:    cfg.NotificationFromEmail,
			ToEmails:     cfg.NotificationToEmails,
		}

		notificationService = notification.NewNotificationService(
			db,
			cfg.AssistantAPIURL,
			emailConfig,
			cfg.NotificationWebhookURL,
		)

		// NOVO: Testar configuração de email na inicialização
		if err := testEmailConfiguration(notificationService); err != nil {
			log.Printf("⚠️  AVISO: Configuração de email pode ter problemas: %v", err)
			log.Printf("    Notificações por email podem falhar. Verifique as configurações SMTP.")
		} else {
			log.Printf("✅ Configuração de email validada com sucesso")
		}

		// Configurar notificações no manager
		waMgr.SetNotificationService(notificationService)
	} else {
		log.Printf("ℹ️  Sistema de notificações desabilitado")
	}

	// Iniciar o gerenciador, incluindo processamento de webhooks
	// Inicializar manager com limpeza //TODO validar
	err = waMgr.InitializeWithCleanup()
	if err != nil {
		log.Fatalf("Erro ao inicializar manager: %v", err)
	}
	// metodo anterior de inicialização
	// err = waMgr.Connect()
	// if err != nil {
	// 	log.Fatalf("Erro ao conectar gerenciador de WhatsApp: %v", err)
	// }

	// Configurar manipuladores de eventos globais
	waMgr.AddEventHandler(func(deviceID int64, evt interface{}) {
		// Processar eventos aqui (webhook para o serviço principal, logs, etc.)
		// Isso seria implementado para enviar os eventos para o serviço principal
	})

	// Inicializar router
	router := gin.Default()

	// Aplicar middleware de autenticação básica, se configurado
	if cfg.BasicAuthUsername != "" && cfg.BasicAuthPassword != "" {
		router.Use(api.BasicAuthMiddleware(cfg.BasicAuthUsername, cfg.BasicAuthPassword))
	}

	// Configurar handlers
	handler := api.NewHandler(db, waMgr)

	// Configurar rotas
	api.SetupRoutes(router, handler)

	// Canal para sinal de encerramento
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// Iniciar servidor em goroutine
	go func() {
		addr := fmt.Sprintf("%s:%s", cfg.Host, cfg.Port)
		if err := router.Run(addr); err != nil {
			log.Fatalf("Erro ao iniciar servidor: %v", err)
		}
	}()

	// Agendar verificação de saúde periódica
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for range ticker.C {
			waMgr.HealthCheckClients()
		}
	}()

	// Aguardar sinal de encerramento
	<-quit
	log.Println("Recebido sinal de encerramento, desconectando clientes...")

	// Desconectar todos os clientes
	devices, err := db.GetAllDevicesByStatus(database.DeviceStatusConnected)
	if err != nil {
		log.Printf("Erro ao buscar dispositivos conectados: %v", err)
	} else {
		for _, device := range devices {
			log.Printf("Desconectando dispositivo %d", device.ID)
			_ = waMgr.DisconnectClient(device.ID)
		}
	}

	log.Println("Servidor encerrado com sucesso")
}

// NOVA FUNÇÃO: Testar configuração de email na inicialização
func testEmailConfiguration(ns *notification.NotificationService) error {
	if ns == nil {
		return fmt.Errorf("notification service não inicializado")
	}

	// Criar uma notificação de teste (não será enviada)
	testNotification := &notification.DeviceNotification{
		DeviceID:        0,
		DeviceName:      "Test Device",
		TenantID:        0,
		Level:           notification.NotificationLevelInfo,
		Type:            "system_startup_test",
		Title:           "Teste de Configuração",
		Message:         "Este é um teste de configuração do sistema de email",
		Timestamp:       time.Now(),
		SuggestedAction: "Nenhuma ação necessária - apenas teste",
	}

	// Testar apenas a construção do email (não enviar)
	emails, err := ns.GetEmailsForNotification(testNotification)
	if err != nil {
		return fmt.Errorf("erro ao obter emails de destino: %w", err)
	}

	if len(emails) == 0 {
		return fmt.Errorf("nenhum email de destino configurado")
	}

	// Verificar se o email sender foi inicializado corretamente
	if ns.EmailSender == nil {
		return fmt.Errorf("email sender não foi inicializado")
	}

	log.Printf("📧 Emails de destino configurados: %v", emails)
	return nil
}
