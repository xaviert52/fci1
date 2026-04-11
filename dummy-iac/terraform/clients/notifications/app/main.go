package main

import (
	"log"
)

func main() {
	log.Println(`{"level":"info","msg":"notifications service starting"}`)

	initDB()

	// La delegación de rutas ahora está 100% en startServer() para evitar conflictos de Mux
	startServer()
}
