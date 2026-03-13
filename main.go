package main

import (
	"net/http"

	"tbu/api"
)

func main() {

	http.HandleFunc("/", api.Handler)

	println("Servidor en http://localhost:8080")

	http.ListenAndServe("0.0.0.0:8080", nil)
}