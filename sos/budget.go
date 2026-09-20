package sos

import (
	"context"
	"fmt"
	"sync"
)

// BudgetBucket names the caller category a request is billed against.
type BudgetBucket string

const (
	BudgetEditor         BudgetBucket = "editor"
	BudgetInterpretation BudgetBucket = "interpretation"
	BudgetRuntime        BudgetBucket = "runtime"
)

var budgetBuckets = map[BudgetBucket]bool{
	BudgetEditor:         true,
	BudgetInterpretation: true,
	BudgetRuntime:        true,
}

// BudgetError reports that admission was refused because a cap is full.
// Used and Limit describe the exhausted scope: the per-bucket cap when the
// bucket itself is full, or the overall cap when only the total is.
type BudgetError struct {
	Bucket BudgetBucket `json:"bucket"`
	Used   int          `json:"used"`
	Limit  int          `json:"limit"`
}

func (e *BudgetError) Error() string {
	return fmt.Sprintf("%s budget exhausted: %d of %d requests used", e.Bucket, e.Used, e.Limit)
}

// RequestBudget is an in-memory, concurrency-safe cap on how many model
// requests a single operation may issue, shared by compiler, editor and
// runtime callers. It lives for one operation and is not persisted or shared
// across processes: a new operation starts from a fresh NewRequestBudget, and
// nothing survives the process. It counts requests and reported input tokens
// only; it carries no prices and makes no money-enforcement claims.
type RequestBudget struct {
	mu      sync.Mutex
	total   int
	used    int
	buckets map[BudgetBucket]*budgetCounter
}

type budgetCounter struct {
	limit    int
	used     int
	pending  int
	reported int
	unknown  int
}

// NewRequestBudget builds a budget admitting at most total requests overall.
// A nil or partial buckets map leaves the unlisted buckets inheriting the
// overall cap. Negative totals or bucket caps are invalid; zero is valid and
// denies every admission.
func NewRequestBudget(total int, buckets map[BudgetBucket]int) (*RequestBudget, error) {
	if total < 0 {
		return nil, fmt.Errorf("total budget must not be negative, got %d", total)
	}
	b := &RequestBudget{total: total, buckets: map[BudgetBucket]*budgetCounter{}}
	for bucket, limit := range buckets {
		if !budgetBuckets[bucket] {
			return nil, fmt.Errorf("unknown budget bucket %q", bucket)
		}
		if limit < 0 {
			return nil, fmt.Errorf("budget for %s must not be negative, got %d", bucket, limit)
		}
		b.buckets[bucket] = &budgetCounter{limit: limit}
	}
	return b, nil
}

// Constrain narrows a shared total without resetting admissions. A caller with
// a stricter effective policy cannot enlarge an existing allowance. Already
// admitted work remains counted even when the new ceiling is below usage.
func (b *RequestBudget) Constrain(total int) error {
	if total < 0 {
		return fmt.Errorf("total budget must not be negative, got %d", total)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if total < b.total {
		b.total = total
	}
	return nil
}

// Admit atomically consumes one request slot in both the overall cap and the
// bucket's cap and returns a Reservation the caller must Complete once the
// request's input usage is known. A canceled context admits nothing and
// consumes nothing. An unknown bucket is rejected without consuming. Buckets
// absent from the constructor map inherit the overall cap. Once admitted, the
// request count stays consumed even if the request later fails or is
// canceled: billing attribution is only known at completion, so there are no
// refunds; report the outcome with Reservation.Complete.
func (b *RequestBudget) Admit(ctx context.Context, bucket BudgetBucket) (*Reservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !budgetBuckets[bucket] {
		return nil, fmt.Errorf("unknown budget bucket %q", bucket)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.used >= b.total {
		return nil, &BudgetError{Bucket: bucket, Used: b.used, Limit: b.total}
	}
	c, ok := b.buckets[bucket]
	if ok && c.used >= c.limit {
		return nil, &BudgetError{Bucket: bucket, Used: c.used, Limit: c.limit}
	}
	if !ok {
		c = &budgetCounter{limit: b.total}
		b.buckets[bucket] = c
	}
	b.used++
	c.used++
	c.pending++
	return &Reservation{budget: b, bucket: bucket}, nil
}

// Reservation is one admitted request whose usage is not yet reported.
// Complete exactly once, whatever the outcome; repeated calls are no-ops.
type Reservation struct {
	budget *RequestBudget
	bucket BudgetBucket
	done   bool
}

// Complete records the reservation's input usage. With reported true and
// inputTokens >= 0 the tokens are added to the bucket's reported total;
// otherwise (reported false, or a negative token count) the completion is
// recorded as unknown usage. It is thread safe and idempotent, never refunds
// the consumed request count, and keeps the reservation counted as unresolved
// when usage is unknown.
func (r *Reservation) Complete(inputTokens int, reported bool) {
	if r == nil || r.budget == nil {
		return
	}
	b := r.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.done {
		return
	}
	r.done = true
	c := b.buckets[r.bucket]
	if c == nil {
		return
	}
	c.pending--
	if reported && inputTokens >= 0 {
		c.reported += inputTokens
	} else {
		c.unknown++
	}
}

// BucketSnapshot is the JSON-friendly per-bucket tally at a point in time.
// Unresolved counts reservations that are still pending plus completions
// whose usage is unknown (failed or unreported requests).
type BucketSnapshot struct {
	Requests            int `json:"requests"`
	Limit               int `json:"limit"`
	ReportedInputTokens int `json:"reportedInputTokens"`
	Unresolved          int `json:"unresolved"`
}

// BudgetSnapshot is the JSON-friendly state of a whole RequestBudget.
type BudgetSnapshot struct {
	TotalAdmitted int                             `json:"totalAdmitted"`
	TotalLimit    int                             `json:"totalLimit"`
	Buckets       map[BudgetBucket]BucketSnapshot `json:"buckets"`
}

// Snapshot returns the current counts: requests admitted overall, and per
// bucket the request count, reported input tokens and unresolved (pending or
// unknown-usage) reservations. The three canonical buckets always appear;
// buckets that were not given their own cap inherit the overall one.
func (b *RequestBudget) Snapshot() BudgetSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := BudgetSnapshot{TotalAdmitted: b.used, TotalLimit: b.total, Buckets: map[BudgetBucket]BucketSnapshot{}}
	for bucket := range budgetBuckets {
		s.Buckets[bucket] = b.snapshotBucket(bucket)
	}
	for bucket := range b.buckets {
		s.Buckets[bucket] = b.snapshotBucket(bucket)
	}
	return s
}

func (b *RequestBudget) snapshotBucket(bucket BudgetBucket) BucketSnapshot {
	s := BucketSnapshot{Limit: b.total}
	if c, ok := b.buckets[bucket]; ok {
		s.Limit = c.limit
		s.Requests = c.used
		s.ReportedInputTokens = c.reported
		s.Unresolved = c.pending + c.unknown
	}
	return s
}
