package wp16

import (
	"context"
	"os"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFixedDatabasePoolLoadBaseline(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	requests := boundedIntEnv("OPENFOX_WP16_LOAD_REQUESTS", 64, 1, 1000)
	workers := boundedIntEnv("OPENFOX_WP16_LOAD_WORKERS", 8, 1, 64)
	if workers > requests {
		workers = requests
	}

	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = int32(workers)
	config.MinConns = 0
	config.MaxConnLifetime = 2 * time.Minute
	config.MaxConnIdleTime = time.Minute
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	latencies := make([]time.Duration, 0, requests)
	latencyMu := sync.Mutex{}
	errCh := make(chan error, requests)
	jobs := make(chan struct{}, requests)
	for i := 0; i < requests; i++ {
		jobs <- struct{}{}
	}
	close(jobs)

	var group sync.WaitGroup
	group.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer group.Done()
			for range jobs {
				requestStart := time.Now()
				var result int
				if err := pool.QueryRow(ctx, "SELECT 1").Scan(&result); err != nil {
					errCh <- err
					continue
				}
				if result != 1 {
					errCh <- strconv.ErrSyntax
					continue
				}
				latencyMu.Lock()
				latencies = append(latencies, time.Since(requestStart))
				latencyMu.Unlock()
			}
		}()
	}
	group.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if len(latencies) != requests {
		t.Fatalf("completed requests = %d, want %d", len(latencies), requests)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	stat := pool.Stat()
	p50 := latencies[(len(latencies)-1)*50/100]
	p95 := latencies[(len(latencies)-1)*95/100]
	var databaseBytes int64
	if err := pool.QueryRow(ctx, "SELECT pg_database_size(current_database())").Scan(&databaseBytes); err != nil {
		t.Fatal(err)
	}
	t.Logf("fixed db load: requests=%d workers=%d elapsed=%s p50=%s p95=%s max=%s pool_empty_acquire_count=%d pool_empty_acquire_wait=%s database_bytes=%d", requests, workers, time.Since(start).Round(time.Millisecond), p50.Round(time.Microsecond), p95.Round(time.Microsecond), latencies[len(latencies)-1].Round(time.Microsecond), stat.EmptyAcquireCount(), stat.EmptyAcquireWaitTime().Round(time.Microsecond), databaseBytes)
}

func boundedIntEnv(name string, fallback, minimum, maximum int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil || value < minimum || value > maximum {
		return fallback
	}
	return value
}
