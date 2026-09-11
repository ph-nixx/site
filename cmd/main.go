package main

import (
	"log"
	"net/http"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", home)
	mux.HandleFunc("GET /snippet/view/{id}", snippetView)
	mux.HandleFunc("POST /snippet/create", snippetCreate)

	port := ":8080"
	log.Println("Starting server", port)
	if err := http.ListenAndServe(port, mux); err != nil {
		log.Fatalln(err)
	}
}
