// internal/client/assistant.go
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"
)

// AssistantClient é um cliente para interação com a API do Assistant
type AssistantClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// TenantResponse é a resposta de validação de tenant
type TenantResponse struct {
	Exists   bool   `json:"exists"`
	IsActive bool   `json:"is_active"`
	Name     string `json:"name,omitempty"`
}

// TenantInfo é a informação básica do tenant
type TenantInfo struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// NewAssistantClient cria um novo cliente para a API do Assistant
func NewAssistantClient(baseURL string) *AssistantClient {
	return &AssistantClient{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: time.Second * 10,
		},
	}
}

// ValidateTenant verifica se um tenant existe e está ativo
func (c *AssistantClient) ValidateTenant(tenantID int) (*TenantResponse, error) {
	// Construir URL
	url := fmt.Sprintf("%s/internal/tenants/validate/%d", c.BaseURL, tenantID)

	// Fazer requisição GET
	resp, err := c.HTTPClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("erro ao validar tenant: %w", err)
	}
	defer resp.Body.Close()

	// Verificar status code
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("erro ao validar tenant, status: %d", resp.StatusCode)
	}

	// Ler resposta
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("erro ao ler resposta: %w", err)
	}

	// Decodificar resposta
	var response TenantResponse
	err = json.Unmarshal(body, &response)
	if err != nil {
		return nil, fmt.Errorf("erro ao decodificar resposta: %w", err)
	}

	return &response, nil
}

// ListActiveTenants obtém a lista de todos os tenants ativos
func (c *AssistantClient) ListActiveTenants() ([]TenantInfo, error) {
	// Construir URL
	url := fmt.Sprintf("%s/internal/tenants/list", c.BaseURL)

	// Fazer requisição GET
	resp, err := c.HTTPClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("erro ao listar tenants: %w", err)
	}
	defer resp.Body.Close()

	// Verificar status code
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("erro ao listar tenants, status: %d", resp.StatusCode)
	}

	// Ler resposta
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("erro ao ler resposta: %w", err)
	}

	// Decodificar resposta
	var tenants []TenantInfo
	err = json.Unmarshal(body, &tenants)
	if err != nil {
		return nil, fmt.Errorf("erro ao decodificar resposta: %w", err)
	}

	return tenants, nil
}

// SendWebhookEvent envia um evento de webhook para o Assistant processar
func (c *AssistantClient) SendWebhookEvent(event map[string]interface{}) error {
	// Construir URL
	url := fmt.Sprintf("%s/internal/webhooks/event", c.BaseURL)

	// Serializar evento
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("erro ao serializar evento: %w", err)
	}

	// Criar request
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(data))
	if err != nil {
		return fmt.Errorf("erro ao criar request: %w", err)
	}

	// Configurar headers
	req.Header.Set("Content-Type", "application/json")

	// Enviar request
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("erro ao enviar evento: %w", err)
	}
	defer resp.Body.Close()

	// Verificar status code
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("erro ao enviar evento, status: %d", resp.StatusCode)
	}

	return nil
}
