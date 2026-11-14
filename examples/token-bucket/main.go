// Command token-bucket demonstrates the local algorithm before the HTTP phase.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ratelimiter/ratelimiter/internal/limiter"
)

func main() {
	b, err := limiter.NewTokenBucket(limiter.TokenBucketConfig{
		Limit: 2, Burst: 1, Window: time.Second,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	for i := 1; i <= 5; i++ {
		r, err := b.Allow(context.Background(), "demo-user")
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("check=%d allowed=%t remaining=%d retry_after=%s\n", i, r.Allowed, r.Remaining, r.RetryAfter.Round(time.Millisecond))
	}
	time.Sleep(500 * time.Millisecond)
	r, err := b.Allow(context.Background(), "demo-user")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("after refill: allowed=%t remaining=%d\n", r.Allowed, r.Remaining)
}
