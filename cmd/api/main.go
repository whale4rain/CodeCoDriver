package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"codecodriver/internal/indexer"
	"codecodriver/internal/llm"
	"codecodriver/internal/runtime"
	"codecodriver/internal/server"
	"codecodriver/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://codecodriver:codecodriver@localhost:55432/codecodriver?sslmode=disable"
	}
	data, err := store.OpenPostgres(ctx, databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer data.Close()
	llmClient, err := llm.NewDeepSeekFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	engine := runtime.NewServiceWithLLM(data, indexer.New(), llmClient)
	engine.Start(ctx)
	addr := os.Getenv("CODECODRIVER_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	httpServer := &http.Server{Addr: addr, Handler: server.New(data, engine), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	log.Printf("CodeCoDriver listening on %s", addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
