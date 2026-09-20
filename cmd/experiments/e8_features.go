package main

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// Experiment 8: judgments as features. Ask a handful of Score and Noul
// questions about each Yelp review, fit a ridge regression from the answers to
// the star rating, and compare with the mean baseline and with simply asking
// for the stars. A second round adds questions aimed at the first round's
// residuals, the manual version of the autoresearch loop.
func features(ctx context.Context, c *typesafe.Client) error {
	const nTrain, nTest = 240, 80
	rows, err := hfRows("Yelp/yelp_review_full", "yelp_review_full", "test", nTrain+nTest)
	if err != nil {
		return err
	}
	type review struct {
		text  string
		stars float64
	}
	revs := make([]review, len(rows))
	for i, r := range rows {
		revs[i] = review{text: truncate(r["text"].(string), 1500), stars: r["label"].(float64) + 1}
	}

	round1 := []string{"food", "service", "value", "would_return", "complaint", "tone"}
	round2 := append(append([]string(nil), round1...), "mixed", "deal_breaker", "superlatives", "never_again")
	qs := typesafe.Questions{
		"food":         typesafe.Score("How does the reviewer rate the food or product quality in `review`?", "Bad or inedible", "Mediocre", "Good", "Excellent"),
		"service":      typesafe.Score("How does the reviewer describe the service or staff in `review`?", "Rude, absent, or incompetent", "Adequate", "Friendly and competent", "Outstanding"),
		"value":        typesafe.Score("How does the reviewer feel about the price versus what they got?", "Overpriced or ripped off", "Fair", "Great value"),
		"would_return": typesafe.Noul("Does the reviewer say or clearly imply they would come back or recommend the place?"),
		"complaint":    typesafe.Noul("Does the reviewer describe a specific problem or complaint?"),
		"tone":         typesafe.Score("What is the overall tone of `review`?", "Furious or disgusted", "Disappointed", "Mixed or neutral", "Pleased", "Delighted"),
		"mixed":        typesafe.Noul("Does `review` contain both clear praise and clear criticism?"),
		"deal_breaker": typesafe.Noul("Does the reviewer describe something they treat as a deal-breaker, such as getting sick, being ignored, being overcharged, or feeling unsafe?"),
		"superlatives": typesafe.Noul("Does the reviewer use superlatives such as best, amazing, perfect, or favorite about the place?"),
		"never_again":  typesafe.Noul("Does the reviewer say they will never return or warn others to stay away?"),
		"stars":        typesafe.Score("How many stars out of five would the reviewer give?", "1 star", "2 stars", "3 stars", "4 stars", "5 stars"),
	}

	answers, errs := parallel(ctx, revs, 16, func(ctx context.Context, _ int, r review) (typesafe.Answers, error) {
		res, err := c.SystemOne(ctx, typesafe.Request{State: map[string]string{"review": r.text}, Questions: qs})
		if err != nil {
			return nil, err
		}
		return res.Answers, nil
	})
	if err := firstErr(errs); err != nil {
		return err
	}

	feat := func(a typesafe.Answers, names []string) []float64 {
		x := make([]float64, 0, len(names)+1)
		x = append(x, 1) // intercept
		for _, n := range names {
			switch ans := a[n]; ans.Type {
			case "noul":
				x = append(x, ans.Noul)
			default:
				x = append(x, ans.Normalized())
			}
		}
		return x
	}
	y := make([]float64, len(revs))
	for i, r := range revs {
		y[i] = r.stars
	}

	eval := func(name string, pred []float64) {
		var mae, mse float64
		exact := 0
		for i := nTrain; i < len(revs); i++ {
			d := pred[i] - y[i]
			mae += math.Abs(d)
			mse += d * d
			if math.Round(math.Min(5, math.Max(1, pred[i]))) == y[i] {
				exact++
			}
		}
		n := float64(nTest)
		fmt.Printf("    %-40s MAE %.3f  RMSE %.3f  exact %2d/%d\n", name, mae/n, math.Sqrt(mse/n), exact, nTest)
	}

	fmt.Printf("  Yelp reviews: %d train, %d test, target = stars 1..5\n", nTrain, nTest)
	fmt.Println("  held-out results:")

	meanTrain := 0.0
	for i := 0; i < nTrain; i++ {
		meanTrain += y[i]
	}
	meanTrain /= nTrain
	base := make([]float64, len(revs))
	direct := make([]float64, len(revs))
	for i := range revs {
		base[i] = meanTrain
		direct[i] = answers[i].Score("stars").Score + 1
	}
	eval("baseline: train mean", base)
	eval("direct: one 5-level 'stars' Score", direct)

	var w1, w2 []float64
	for _, rd := range []struct {
		name  string
		names []string
		w     *[]float64
	}{{"round 1: 6 judgments -> ridge", round1, &w1}, {"round 2: 10 judgments -> ridge", round2, &w2}} {
		X := make([][]float64, nTrain)
		for i := 0; i < nTrain; i++ {
			X[i] = feat(answers[i], rd.names)
		}
		w := ridge(X, y[:nTrain], 1.0)
		*rd.w = w
		pred := make([]float64, len(revs))
		for i := range revs {
			pred[i] = dot(w, feat(answers[i], rd.names))
		}
		eval(rd.name, pred)
	}

	// The autoresearch step: look at where round 1 was most wrong on training data.
	fmt.Println("  largest round-1 training residuals (what the added questions targeted):")
	type resid struct {
		i    int
		pred float64
	}
	var rs []resid
	for i := 0; i < nTrain; i++ {
		rs = append(rs, resid{i, dot(w1, feat(answers[i], round1))})
	}
	sort.Slice(rs, func(a, b int) bool {
		return math.Abs(rs[a].pred-y[rs[a].i]) > math.Abs(rs[b].pred-y[rs[b].i])
	})
	for _, r := range rs[:4] {
		fmt.Printf("    gold %.0f pred %.1f  %q\n", y[r.i], r.pred, truncate(revs[r.i].text, 90))
	}

	fmt.Println("  round-2 weights (features are 0..1, so weights are in stars):")
	for i, n := range round2 {
		fmt.Printf("    %-13s %+.2f\n", n, w2[i+1])
	}
	fmt.Printf("    %-13s %+.2f\n", "intercept", w2[0])
	return nil
}

func dot(a, b []float64) float64 {
	var s float64
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// ridge solves (X'X + lambda*I) w = X'y by Gaussian elimination. The intercept
// column is not regularized.
func ridge(X [][]float64, y []float64, lambda float64) []float64 {
	d := len(X[0])
	A := make([][]float64, d)
	b := make([]float64, d)
	for i := range A {
		A[i] = make([]float64, d)
	}
	for r, x := range X {
		for i := 0; i < d; i++ {
			b[i] += x[i] * y[r]
			for j := 0; j < d; j++ {
				A[i][j] += x[i] * x[j]
			}
		}
	}
	for i := 1; i < d; i++ {
		A[i][i] += lambda
	}
	// Elimination with partial pivoting.
	for col := 0; col < d; col++ {
		piv := col
		for r := col + 1; r < d; r++ {
			if math.Abs(A[r][col]) > math.Abs(A[piv][col]) {
				piv = r
			}
		}
		A[col], A[piv] = A[piv], A[col]
		b[col], b[piv] = b[piv], b[col]
		for r := col + 1; r < d; r++ {
			f := A[r][col] / A[col][col]
			for k := col; k < d; k++ {
				A[r][k] -= f * A[col][k]
			}
			b[r] -= f * b[col]
		}
	}
	w := make([]float64, d)
	for i := d - 1; i >= 0; i-- {
		s := b[i]
		for k := i + 1; k < d; k++ {
			s -= A[i][k] * w[k]
		}
		w[i] = s / A[i][i]
	}
	return w
}
