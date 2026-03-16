package main

import (
	"log"
	"net/http"

	handler "tbu/api"
)

func main() {
	log.Println("Servidor en http://localhost:8080")

	err := http.ListenAndServe(":8081", http.HandlerFunc(handler.Handler))
	if err != nil {
		log.Fatal(err)
	}
}