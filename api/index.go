package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

var router *gin.Engine

func init() {
	gin.SetMode(gin.ReleaseMode)

	router = gin.New()

	router.GET("/api", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "Hola mundo desde Gin 🚀",
		})
	})
	router.GET("/test", func(c *gin.Context) {
  	c.JSON(200, gin.H{
  		"status": "ok",
  	})
  })
}

func Handler(w http.ResponseWriter, r *http.Request) {
	router.ServeHTTP(w, r)
}