package main

import (
	"net/http"

	"tbu/api"
)

func main() {

	http.HandleFunc("/", api.Handler)

	println("Servidor en http://localhost:8080")

	http.ListenAndServe(":8080", nil)
}