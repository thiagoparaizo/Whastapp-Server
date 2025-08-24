// internal/notification/email.go

package notification

import (
	"crypto/tls"
	"fmt"
	"net/smtp"
	"strings"
)

// EmailSender gerencia envio de emails SMTP
type EmailSender struct {
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPassword string
	FromEmail    string
	UseTLS       bool
}

// NewEmailSender cria um novo sender de email
func NewEmailSender(config *EmailConfig) *EmailSender {
	return &EmailSender{
		SMTPHost:     config.SMTPHost,
		SMTPPort:     config.SMTPPort,
		SMTPUser:     config.SMTPUser,
		SMTPPassword: config.SMTPPassword,
		FromEmail:    config.FromEmail,
		UseTLS:       true, // Default TLS habilitado
	}
}

// SendHTML envia email com corpo HTML (similar ao Python)
func (e *EmailSender) SendHTML(toEmail, subject, htmlBody string) error {
	// Validações básicas
	if e.SMTPHost == "" || e.SMTPUser == "" || e.SMTPPassword == "" {
		return fmt.Errorf("configuração SMTP incompleta")
	}

	if toEmail == "" || subject == "" {
		return fmt.Errorf("email, assunto são obrigatórios")
	}

	// Preparar servidor SMTP
	smtpAddr := fmt.Sprintf("%s:%d", e.SMTPHost, e.SMTPPort)

	// Configurar autenticação
	auth := smtp.PlainAuth("", e.SMTPUser, e.SMTPPassword, e.SMTPHost)

	// Construir email no formato MIME (similar ao MIMEMultipart do Python)
	from := e.FromEmail
	if from == "" {
		from = e.SMTPUser
	}

	message := e.buildMIMEMessage(from, toEmail, subject, htmlBody)

	// Enviar email
	if e.UseTLS {
		return e.sendWithTLS(smtpAddr, auth, from, []string{toEmail}, message)
	} else {
		return smtp.SendMail(smtpAddr, auth, from, []string{toEmail}, message)
	}
}

// sendWithTLS envia email com TLS (similar ao start_tls do Python)
func (e *EmailSender) sendWithTLS(addr string, auth smtp.Auth, from string, to []string, message []byte) error {
	// Conectar ao servidor
	client, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("erro ao conectar SMTP: %w", err)
	}
	defer client.Close()

	// Iniciar TLS se suportado (equivalente ao start_tls=True do Python)
	if ok, _ := client.Extension("STARTTLS"); ok {
		config := &tls.Config{
			ServerName: e.SMTPHost,
			// Em produção, considere definir InsecureSkipVerify: false
		}
		if err := client.StartTLS(config); err != nil {
			return fmt.Errorf("erro ao iniciar TLS: %w", err)
		}
	}

	// Autenticar (equivalente ao smtp.login do Python)
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("erro de autenticação SMTP: %w", err)
	}

	// Definir remetente
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("erro ao definir remetente: %w", err)
	}

	// Definir destinatário
	for _, addr := range to {
		if err := client.Rcpt(addr); err != nil {
			return fmt.Errorf("erro ao definir destinatário %s: %w", addr, err)
		}
	}

	// Enviar dados da mensagem (equivalente ao send_message do Python)
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("erro ao iniciar dados da mensagem: %w", err)
	}

	_, err = w.Write(message)
	if err != nil {
		return fmt.Errorf("erro ao escrever mensagem: %w", err)
	}

	err = w.Close()
	if err != nil {
		return fmt.Errorf("erro ao finalizar mensagem: %w", err)
	}

	return nil
}

// buildMIMEMessage constrói mensagem MIME (equivalente ao MIMEMultipart do Python)
func (e *EmailSender) buildMIMEMessage(from, to, subject, htmlBody string) []byte {
	var message strings.Builder

	// Headers MIME (equivalente ao message["From"], message["To"], etc. do Python)
	message.WriteString(fmt.Sprintf("From: %s\r\n", from))
	message.WriteString(fmt.Sprintf("To: %s\r\n", to))
	message.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	message.WriteString("\r\n")

	// Corpo HTML (equivalente ao message.attach(MIMEText(body, "html")) do Python)
	message.WriteString(htmlBody)

	return []byte(message.String())
}
