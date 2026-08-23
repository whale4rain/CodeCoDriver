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
	"codecodriver/internal/lease"
	"codecodriver/internal/llm"
	"codecodriver/internal/runtime"
	"codecodriver/internal/sandbox"
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
	embeddingProvider := store.NewEmbeddingProviderFromEnv()
	data, err := store.OpenPostgresWithEmbedding(ctx, databaseURL, embeddingProvider)
	if err != nil {
		log.Fatal(err)
	}
	defer data.Close()
	log.Printf("CodeCoDriver using memory embedding provider %s (%d dimensions)", embeddingProvider.Name(), embeddingProvider.Dimensions())
	llmClient, err := llm.NewDeepSeekFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	engine := runtime.NewServiceWithLLM(data, indexer.New(), llmClient)
	engine.SetWorkspaceFactory(sandbox.NewWorkspaceFromEnv)
	skillsDir := os.Getenv("CODECODRIVER_SKILLS_DIR")
	if skillsDir == "" {
		skillsDir = "skills"
	}
	if err := engine.SetSkillsDir(skillsDir); err != nil {
		log.Fatalf("load skills directory %s: %v", skillsDir, err)
	}
	log.Printf("CodeCoDriver using skills directory %s", skillsDir)
	if skillsFile := os.Getenv("CODECODRIVER_SKILLS_FILE"); skillsFile != "" {
		if err := engine.LoadSkillFile(skillsFile); err != nil {
			log.Fatalf("load skills file %s: %v", skillsFile, err)
		}
		log.Printf("CodeCoDriver loaded skills from %s", skillsFile)
	}
	if redisAddr := os.Getenv("CODECODRIVER_REDIS_ADDR"); redisAddr != "" {
		leaser, err := lease.NewRedis(ctx, redisAddr)
		if err != nil {
			log.Fatal(err)
		}
		defer leaser.Close()
		engine.SetLeaser(leaser)
		log.Printf("CodeCoDriver using Redis leaser at %s", redisAddr)
	}
	engine.Start(ctx)
	addr := os.Getenv("CODECODRIVER_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	httpServer := &http.Server{Addr: addr, Handler: server.New(data, engine), ReadHeaderTimeout: server.HTTPTimeoutFromEnv("CODECODRIVER_READ_HEADER_TIMEOUT_SECONDS", 5*time.Second), WriteTimeout: server.HTTPTimeoutFromEnv("CODECODRIVER_WRITE_TIMEOUT_SECONDS", 30*time.Second), IdleTimeout: server.HTTPTimeoutFromEnv("CODECODRIVER_IDLE_TIMEOUT_SECONDS", 60*time.Second)}
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
