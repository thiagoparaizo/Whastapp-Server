// internal/api/routes.go
package api

import (
	"github.com/gin-gonic/gin"
)

// SetupRoutes configura as rotas da API
func SetupRoutes(router *gin.Engine, handler *Handler) {

	router.Use(CORSMiddleware())
	router.Use(RequestLogger())

	router.GET("/health", handler.GetWhatsAppHealth)

	api := router.Group("/api")
	{
		// Rotas de dispositivos
		devices := api.Group("/devices")
		{
			devices.GET("", handler.GetDevices)
			devices.POST("", handler.CreateDevice)
			devices.GET("/pending", handler.GetPendingDevices)
			devices.GET("/reauth", handler.GetDevicesRequiringReauth)
			devices.GET("/:id", handler.GetDevice)
			devices.PUT("/:id/status", handler.UpdateDeviceStatus)
			devices.GET("/:id/status", handler.GetDeviceStatus)
			devices.GET("/:id/qrcode", handler.GetQRCode)
			devices.POST("/:id/send", handler.SendMessage)
			devices.POST("/:id/disconnect", handler.DisconnectDevice)
			devices.POST("/:id/reauth-done", handler.MarkDeviceAsReauthenticated)

			devices.GET("/:id/groups", handler.GetGroups)
			devices.GET("/:id/contacts", handler.GetContacts)
			devices.GET("/:id/group/:group_id/messages", handler.GetGroupMessages)
			devices.GET("/:id/contact/:contact_id/messages", handler.GetContactMessages)
			devices.POST("/:id/group/:group_id/send", handler.SendGroupMessage)
			devices.POST("/:id/send-media", handler.SendMediaMessage)
			router.Static("/media", "./storage/media")
			devices.POST("/:id/tracked", handler.SetTrackedEntity)
			devices.GET("/:id/tracked", handler.GetTrackedEntities)
			devices.DELETE("/:id/tracked/:jid", handler.DeleteTrackedEntity)
		}

		// Rotas de monitoramento e administração
		admin := api.Group("/admin")
		{
			admin.GET("/status", handler.GetSystemStatus)
			admin.POST("/devices/:id/fix", handler.FixDeviceIssue)
			admin.POST("/devices/:id/reconnect", handler.ReconnectDevice)
		}

		// // Webhook
		// webhook := api.Group("/webhook")
		// {
		// 	webhook.POST("", handler.WebhookConfig)
		// 	webhook.GET("", handler.GetWebhookConfigs)
		// 	webhook.DELETE("/:id", handler.DeleteWebhookConfig)
		// 	webhook.POST("/:id/test", handler.TestWebhook)
		// 	webhook.GET("/:id/logs", handler.GetWebhookLogs)
		// }

		// Rotas de notificação corrigidas
		api.GET("/notifications/status", handler.GetNotificationStatus)
		api.POST("/notifications/test-reauth", handler.TriggerTestReauthNotification)
		api.POST("/notifications/force", handler.ForceNotification)

		// Debug de cooldown
		api.GET("/notifications/debug-cooldown", handler.DebugCooldown)

	}
}

// Exemplo de uso das novas funcionalidades:

/*
# Ver status do sistema
GET /api/admin/status

# Limpar sessão corrompida do dispositivo 2
POST /api/admin/devices/2/fix
{
  "action": "clear_session"
}

# Forçar reconexão do dispositivo 3
POST /api/admin/devices/3/reconnect

# Reset flag de reauth
POST /api/admin/devices/2/fix
{
  "action": "reset_reauth"
}
*/
