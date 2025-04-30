// internal/api/middleware.go
package api

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// BasicAuthMiddleware implementa autenticação básica
func BasicAuthMiddleware(username, password string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Se não houver credenciais definidas, pular middleware
		if username == "" || password == "" {
			c.Next()
			return
		}

		// Verificar autenticação
		authUsername, authPassword, ok := c.Request.BasicAuth()
		if !ok || authUsername != username || authPassword != password {
			c.Header("WWW-Authenticate", "Basic realm=Authorization Required")
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}

		c.Next()
	}
}

// CORSMiddleware adiciona cabeçalhos CORS para permitir acesso cross-origin
func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

// RequestLogger loga informações sobre cada requisição
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Antes da requisição
		path := c.Request.URL.Path
		method := c.Request.Method

		c.Next()

		// Após a requisição
		statusCode := c.Writer.Status()
		if statusCode >= 400 {
			// Registrar apenas erros
			c.Error(fmt.Errorf("%s %s - %d", method, path, statusCode))
		}
	}
}
