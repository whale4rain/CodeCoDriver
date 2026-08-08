package lease

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestRedisLeaserIntegration(t *testing.T) {
	addr := os.Getenv("CODECODRIVER_REDIS_ADDR")
	if addr == "" {
		t.Skip("CODECODRIVER_REDIS_ADDR not set")
	}
	ctx := context.Background()
	leaser, err := NewRedis(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer leaser.Close()
	taskID := fmt.Sprintf("task-lease-%d", time.Now().UnixNano())

	first, ok, err := leaser.TryClaim(ctx, taskID, 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("first claim ok=%v err=%v", ok, err)
	}
	if _, ok, err := leaser.TryClaim(ctx, taskID, 2*time.Second); err != nil || ok {
		t.Fatalf("second claim ok=%v err=%v", ok, err)
	}
	if err := leaser.Renew(ctx, first, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := leaser.Release(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := leaser.TryClaim(ctx, taskID, 2*time.Second); err != nil || !ok {
		t.Fatalf("reclaim ok=%v err=%v", ok, err)
	}
}
