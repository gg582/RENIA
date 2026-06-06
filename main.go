package main

import (
	"log"
	"net/http"
	"runtime/debug"
	"time"

	"renia/ai"
	"renia/db"
	"renia/web"
)

// @title Renia API
// @version 1.0
// @description Ultra-lightweight Go backend for BitNet RWKV inference.
// @host localhost:8080
// @BasePath /
func main() {
	debug.SetGCPercent(20)

	database, err := db.Open("./renia.db")
	if err != nil {
		log.Fatalf("db open: %v", err)
	}
	defer database.Close()

	aiClient := ai.NewClient()
	mux := web.NewRouter(database, aiClient)

	server := &http.Server{
		Addr:         ":8080",
		Handler:      web.LoggingAndAuthMiddleware(mux, database),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("renia listening on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}
}
