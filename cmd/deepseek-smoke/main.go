package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"codecodriver/internal/llm"
)

func main() {
	client, err := llm.NewDeepSeekFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	answer, err := client.Complete(ctx, "You are a connectivity test. Reply briefly.", "Reply with exactly: CodeCoDriver DeepSeek connection OK")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(answer)
}
