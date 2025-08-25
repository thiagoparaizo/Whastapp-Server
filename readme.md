# build
go build -o whatsapp-service.exe ./cmd/server

# Notificação / Configurações:  
Conectar:
psql -h localhost -p 5432 -U postgres -d whatsapp_service

INSERT INTO system_admin_emails VALUES (DEFAULT, 'thiagoparaizo@gmail.com', 'Thiago Paraizo', true, ARRAY['critical', 'error', 'warning'], DEFAULT) ON CONFLICT DO NOTHING;
INSERT INTO notification_email_configs (tenant_id, email_type, email_address, notification_types, is_active) VALUES (4, 'admin', 'thiagoparaizo@gmail.com', ARRAY['critical', 'error', 'warning'], true) ON CONFLICT DO NOTHING;
select * from notification_email_configs;
INSERT INTO notification_email_configs (tenant_id, email_type, email_address, notification_types, is_active) VALUES (4, 'client', 'homeparaizo@gmail.com', ARRAY['critical', 'error'], true) ON CONFLICT DO NOTHING;


Ver todos os emails ativos:
SELECT * FROM system_admin_emails WHERE is_active = true;
SELECT * FROM notification_email_configs WHERE is_active = true;

Desativar um email:
UPDATE system_admin_emails SET is_active = false WHERE email_address = 'email@remover.com';

===

// Configuração de cooldown
	cooldownConfig := CooldownConfig{
		DefaultMinutes:  30,
		CriticalMinutes: 15,
		TypeSpecific: map[string]int{
			"client_outdated":          15, // Muito crítico, pouco cooldown
			"device_requires_reauth":   60, // Menos crítico, cooldown maior
			"device_connection_error":  30, // Moderado
			"webhook_delivery_failure": 60, // Default
			"device_disconnected":      45, // Menos urgente
		},
	}
