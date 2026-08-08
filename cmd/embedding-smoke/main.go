package main

import (
	"context"
	"fmt"
	"os"

	"codecodriver/internal/store"
)

func main() {
	provider := store.NewEmbeddingProviderFromEnv()
	vectors, err := provider.Embed(context.Background(), []string{"CodeCoDriver memory embedding smoke test"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "CodeCoDriver embedding failed: %v\n", err)
		os.Exit(1)
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		fmt.Fprintln(os.Stderr, "CodeCoDriver embedding returned an empty vector")
		os.Exit(1)
	}
	fmt.Printf("CodeCoDriver %s embedding OK (%d dimensions)\n", provider.Name(), len(vectors[0]))
}
