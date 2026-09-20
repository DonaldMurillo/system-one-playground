package sos

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestNewRequestBudgetRejectsNegative(t *testing.T) {
	if _, err := NewRequestBudget(-1, nil); err == nil {
		t.Error("negative total should be invalid")
	}
	if _, err := NewRequestBudget(5, map[BudgetBucket]int{BudgetEditor: -2}); err == nil {
		t.Error("negative bucket cap should be invalid")
	}
	if _, err := NewRequestBudget(0, nil); err != nil {
		t.Errorf("zero total should be valid: %v", err)
	}
	if _, err := NewRequestBudget(5, map[BudgetBucket]int{BudgetRuntime: 0}); err != nil {
		t.Errorf("zero bucket cap should be valid: %v", err)
	}
}

func TestRequestBudgetZeroDenies(t *testing.T) {
	b, _ := NewRequestBudget(0, nil)
	_, err := b.Admit(context.Background(), BudgetEditor)
	var be *BudgetError
	if !errors.As(err, &be) {
		t.Fatalf("want *BudgetError, got %v", err)
	}
	if be.Bucket != BudgetEditor || be.Used != 0 || be.Limit != 0 {
		t.Fatalf("unexpected error fields: %+v", *be)
	}
	if !strings.Contains(be.Error(), "budget exhausted") {
		t.Fatalf("message must mention budget exhausted: %q", be.Error())
	}
	b, _ = NewRequestBudget(3, map[BudgetBucket]int{BudgetRuntime: 0})
	if _, err = b.Admit(context.Background(), BudgetRuntime); err == nil {
		t.Error("zero bucket cap should deny")
	}
	if _, err = b.Admit(context.Background(), BudgetEditor); err != nil {
		t.Errorf("other buckets should still admit: %v", err)
	}
}

func TestRequestBudgetUnknownBucketRejected(t *testing.T) {
	b, _ := NewRequestBudget(5, nil)
	for _, bucket := range []BudgetBucket{"nope", ""} {
		if _, err := b.Admit(context.Background(), bucket); err == nil {
			t.Errorf("bucket %q should be rejected", bucket)
		}
	}
	if got := b.Snapshot().TotalAdmitted; got != 0 {
		t.Fatalf("rejections must not consume budget, admitted %d", got)
	}
	if _, err := b.Admit(context.Background(), BudgetRuntime); err != nil {
		t.Errorf("valid admit after rejection should pass: %v", err)
	}
}

func TestRequestBudgetCanceledContextReservesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b, _ := NewRequestBudget(2, nil)
	_, err := b.Admit(ctx, BudgetRuntime)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if got := b.Snapshot().TotalAdmitted; got != 0 {
		t.Fatalf("canceled context must not consume budget, admitted %d", got)
	}
	if _, err = b.Admit(context.Background(), BudgetRuntime); err != nil {
		t.Errorf("admission after cancel should still pass: %v", err)
	}
}

func TestRequestBudgetBucketAndTotalCeilings(t *testing.T) {
	b, _ := NewRequestBudget(3, map[BudgetBucket]int{BudgetEditor: 2, BudgetRuntime: 2})
	for i := 0; i < 2; i++ {
		if _, err := b.Admit(context.Background(), BudgetEditor); err != nil {
			t.Fatalf("editor admit %d: %v", i+1, err)
		}
	}
	_, err := b.Admit(context.Background(), BudgetEditor)
	var be *BudgetError
	if !errors.As(err, &be) || be.Bucket != BudgetEditor || be.Used != 2 || be.Limit != 2 {
		t.Fatalf("want editor bucket exhausted, got %+v (%v)", be, err)
	}
	if _, err = b.Admit(context.Background(), BudgetRuntime); err != nil {
		t.Fatalf("runtime should still admit: %v", err)
	}
	// Total is now 3 of 3, so even an unbucketed caller is refused by the overall cap.
	_, err = b.Admit(context.Background(), BudgetInterpretation)
	if !errors.As(err, &be) || be.Bucket != BudgetInterpretation || be.Used != 3 || be.Limit != 3 {
		t.Fatalf("want total exhausted at 3 of 3, got %+v (%v)", be, err)
	}
}

func TestRequestBudgetAbsentBucketInheritsTotal(t *testing.T) {
	b, _ := NewRequestBudget(2, map[BudgetBucket]int{BudgetEditor: 1})
	for i := 0; i < 2; i++ {
		if _, err := b.Admit(context.Background(), BudgetRuntime); err != nil {
			t.Fatalf("runtime admit %d should inherit overall cap: %v", i+1, err)
		}
	}
	if _, err := b.Admit(context.Background(), BudgetRuntime); err == nil {
		t.Error("runtime should stop at the overall cap")
	}
	s := b.Snapshot()
	if s.Buckets[BudgetRuntime].Limit != 2 || s.Buckets[BudgetRuntime].Requests != 2 {
		t.Fatalf("unexpected runtime snapshot: %+v", s.Buckets[BudgetRuntime])
	}
}
func TestRequestBudgetCompleteIdempotent(t *testing.T) {
	b, _ := NewRequestBudget(5, map[BudgetBucket]int{BudgetEditor: 5})
	r, _ := b.Admit(context.Background(), BudgetEditor)
	r.Complete(120, true)
	r.Complete(120, true)
	r.Complete(50, true)
	e := b.Snapshot().Buckets[BudgetEditor]
	if e.Requests != 1 || e.ReportedInputTokens != 120 || e.Unresolved != 0 {
		t.Fatalf("idempotent completion broken: %+v", e)
	}
}

func TestRequestBudgetUnknownCompletions(t *testing.T) {
	b, _ := NewRequestBudget(5, nil)
	r1, _ := b.Admit(context.Background(), BudgetEditor)
	r1.Complete(-5, true) // negative tokens count as unknown, not reported
	r2, _ := b.Admit(context.Background(), BudgetEditor)
	r2.Complete(7, false)                                                  // unreported usage
	if _, err := b.Admit(context.Background(), BudgetEditor); err != nil { // left pending
		t.Fatal(err)
	}
	e := b.Snapshot().Buckets[BudgetEditor]
	if e.Requests != 3 || e.ReportedInputTokens != 0 || e.Unresolved != 3 {
		t.Fatalf("pending and unknown must stay unresolved: %+v", e)
	}
}

func TestRequestBudgetNoRefundAfterFailure(t *testing.T) {
	b, _ := NewRequestBudget(5, map[BudgetBucket]int{BudgetRuntime: 1})
	r, _ := b.Admit(context.Background(), BudgetRuntime)
	r.Complete(0, false) // the request failed or was canceled; usage unknown
	if _, err := b.Admit(context.Background(), BudgetRuntime); err == nil {
		t.Fatal("failed request must still consume its slot")
	}
	e := b.Snapshot().Buckets[BudgetRuntime]
	if e.Requests != 1 || e.Unresolved != 1 {
		t.Fatalf("unknown reservation lost: %+v", e)
	}
}

func TestRequestBudgetSnapshotJSON(t *testing.T) {
	b, _ := NewRequestBudget(4, map[BudgetBucket]int{BudgetEditor: 2})
	r, _ := b.Admit(context.Background(), BudgetEditor)
	r.Complete(64, true)
	r2, _ := b.Admit(context.Background(), BudgetRuntime)
	r2.Complete(0, false)
	data, err := json.Marshal(b.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err = json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	buckets, _ := raw["buckets"].(map[string]any)
	editor, _ := buckets["editor"].(map[string]any)
	runtime, _ := buckets["runtime"].(map[string]any)
	if raw["totalAdmitted"].(float64) != 2 || editor["requests"].(float64) != 1 ||
		editor["reportedInputTokens"].(float64) != 64 || editor["unresolved"].(float64) != 0 ||
		runtime["requests"].(float64) != 1 || runtime["unresolved"].(float64) != 1 {
		t.Fatalf("unexpected JSON snapshot: %s", data)
	}
	if _, ok := buckets["interpretation"]; !ok {
		t.Fatalf("canonical buckets should always appear: %s", data)
	}
}

func TestRequestBudgetConcurrentAdmission(t *testing.T) {
	const total = 25
	b, _ := NewRequestBudget(total, map[BudgetBucket]int{BudgetEditor: 10})
	var wg sync.WaitGroup
	successes := make(chan int, 200)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bucket := BudgetRuntime
			if i%2 == 0 {
				bucket = BudgetEditor
			}
			r, err := b.Admit(context.Background(), bucket)
			if err != nil {
				var be *BudgetError
				if !errors.As(err, &be) {
					t.Errorf("admission failure should be *BudgetError, got %v", err)
				}
				return
			}
			successes <- 1
			r.Complete(1, true)
		}(i)
	}
	wg.Wait()
	close(successes)
	admitted := len(successes)
	s := b.Snapshot()
	if admitted != total || s.TotalAdmitted != total {
		t.Fatalf("exactly %d admissions expected, got %d (snapshot %d)", total, admitted, s.TotalAdmitted)
	}
	if e := s.Buckets[BudgetEditor]; e.Requests != 10 {
		t.Fatalf("editor cap must hold under races: %+v", e)
	}
	if e := s.Buckets[BudgetRuntime]; e.Requests != 15 {
		t.Fatalf("runtime should take the remainder: %+v", e)
	}
}

func TestRequestBudgetConcurrentCompletion(t *testing.T) {
	b, _ := NewRequestBudget(2, nil)
	r, _ := b.Admit(context.Background(), BudgetEditor)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%5 == 0 {
				r.Complete(-1, false)
			} else {
				r.Complete(9, true)
			}
		}(i)
	}
	wg.Wait()
	e := b.Snapshot().Buckets[BudgetEditor]
	if e.Requests != 1 {
		t.Fatalf("request count changed on completion: %+v", e)
	}
	// Whichever completion won the race, it must count exactly once.
	if e.ReportedInputTokens == 9 && e.Unresolved != 0 {
		t.Fatalf("reported completion overcounted: %+v", e)
	}
	if e.ReportedInputTokens == 0 && e.Unresolved != 1 {
		t.Fatalf("unknown completion overcounted: %+v", e)
	}
}

func TestRequestBudgetUnknownConfiguredBucket(t *testing.T) {
	if _, err := NewRequestBudget(10, map[BudgetBucket]int{"typo": 1}); err == nil {
		t.Fatal("unknown configured bucket accepted")
	}
}

func TestSharedBudgetCanOnlyBeNarrowed(t *testing.T) {
	b, err := NewRequestBudget(10, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := b.Admit(context.Background(), BudgetEditor)
	if err != nil {
		t.Fatal(err)
	}
	r.Complete(5, true)
	if err := b.Constrain(1); err != nil {
		t.Fatal(err)
	}
	if err := b.Constrain(100); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Admit(context.Background(), BudgetRuntime); err == nil {
		t.Fatal("shared admission escaped narrowed cap")
	}
	s := b.Snapshot()
	if s.TotalLimit != 1 || s.TotalAdmitted != 1 || s.Buckets[BudgetEditor].ReportedInputTokens != 5 {
		t.Fatalf("lost shared accounting: %+v", s)
	}
	if err := b.Constrain(-1); err == nil {
		t.Fatal("negative cap accepted")
	}
}
