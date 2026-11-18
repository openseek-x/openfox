package gongfeng

import (
	"context"
	"errors"
	"sync/atomic"
)

// A worker pass has a single budget shared by all client calls, including
// pagination and permission revalidation. Exhaustion is an unsent request,
// never an UNKNOWN remote mutation.
var ErrBudgetExhausted = errors.New("gongfeng: request budget exhausted")

type requestBudgetKey struct{}
type requestBudget struct{ remaining atomic.Int32 }

func WithRequestBudget(ctx context.Context, limit int) context.Context {
	b := &requestBudget{}
	b.remaining.Store(int32(max(0, min(limit, 5))))
	return context.WithValue(ctx, requestBudgetKey{}, b)
}
func RemainingRequests(ctx context.Context) int {
	if b, ok := ctx.Value(requestBudgetKey{}).(*requestBudget); ok {
		return int(max(0, b.remaining.Load()))
	}
	return 5
}
func takeRequest(ctx context.Context) bool {
	b, ok := ctx.Value(requestBudgetKey{}).(*requestBudget)
	if !ok {
		return true
	}
	for {
		n := b.remaining.Load()
		if n <= 0 {
			return false
		}
		if b.remaining.CompareAndSwap(n, n-1) {
			return true
		}
	}
}
