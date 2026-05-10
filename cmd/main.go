package main

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/gin-gonic/gin"
	"github.com/Endea4/studExE4-api-gateway/shared/config"
	"github.com/Endea4/studExE4-api-gateway/shared/mongo"
)

// ReverseProxy forwards requests to another microservice
func ReverseProxy(target string) gin.HandlerFunc {
	url, _ := url.Parse(target)
	proxy := httputil.NewSingleHostReverseProxy(url)

	return func(c *gin.Context) {
		// Update the request so it points to the target server
		c.Request.URL.Host = url.Host
		c.Request.URL.Scheme = url.Scheme
		c.Request.Header.Set("X-Forwarded-Host", c.Request.Header.Get("Host"))
		c.Request.Host = url.Host

		// Forward the request
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

func main() {
	config.LoadConfig()
	uri := config.GetEnv("MONGODB_URI", "mongodb://localhost:27017")
	dbName := config.GetEnv("DB_NAME", "studexdb")
	
	client, _ := mongo.ConnectDB(uri, dbName)
	defer mongo.Disconnect(client)

	r := gin.Default()

	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, PATCH")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	})

	// Base Route
	r.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"service": "api-gateway",
			"status":  "running",
		})
	})

	r.GET("/docs", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(`<!DOCTYPE html>
<html><head><link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css"></head>
<body><div id="swagger-ui"></div>
<script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js" crossorigin></script>
<script>SwaggerUIBundle({url:"/docs/openapi.yaml",dom_id:"#swagger-ui"})</script></body></html>`))
	})
	r.StaticFile("/docs/openapi.yaml", "docs/openapi.yaml")

	// --- PROXY ROUTES ---
	userServiceURL := config.GetEnv("USER_SERVICE_URL", "http://localhost:8081")
	r.Any("/users/*path", ReverseProxy(userServiceURL))
	r.Any("/users", ReverseProxy(userServiceURL))

	driverServiceURL := config.GetEnv("DRIVER_SERVICE_URL", "http://localhost:8082")
	r.Any("/drivers/*path", ReverseProxy(driverServiceURL))
	r.Any("/drivers", ReverseProxy(driverServiceURL))

	r.Any("/admin/*path", ReverseProxy(driverServiceURL))
	r.Any("/admin", ReverseProxy(driverServiceURL))

	tripServiceURL := config.GetEnv("TRIP_SERVICE_URL", "http://localhost:8083")
	r.Any("/orders/*path", ReverseProxy(tripServiceURL))
	r.Any("/orders", ReverseProxy(tripServiceURL))

	port := config.GetEnv("PORT", "8080")
	fmt.Printf("API Gateway starting on port %s...\n", port)
	r.Run(":" + port)
}
