// cmd/server/main.go
package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/gin-gonic/gin"

	"whatsapp-service/internal/api"
	"whatsapp-service/internal/config"
	"whatsapp-service/internal/database"
	"whatsapp-service/internal/whatsapp"
)

func main() {
	// Carregar configurações
	cfg := config.Load()

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

	// Iniciar o gerenciador, incluindo processamento de webhooks
	err = waMgr.Connect()
	if err != nil {
		log.Fatalf("Erro ao conectar gerenciador de WhatsApp: %v", err)
	}
	//Desnecessário, pois já está conectado no Connect()
	// Tentar reconectar dispositivos existentes
	// go func() {
	// 	waMgr.ReconnectAllConnected()
	// 	waMgr.ConnectAllApproved()
	// }()

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
