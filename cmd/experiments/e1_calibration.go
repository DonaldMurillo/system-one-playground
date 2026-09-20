package main

import (
	"context"
	"fmt"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// Experiment 1: measured accuracy and calibration on labelled public data.
// SST-2 (binary sentiment) compares Noul against a two-option Choice.
// dair-ai/emotion (six classes) gives a confusion matrix and a coverage/accuracy
// curve for confidence-gated routing.
func calibration(ctx context.Context, c *typesafe.Client) error {
	const n = 200

	// --- SST-2 ------------------------------------------------------------
	items, err := sst2(n)
	if err != nil {
		return err
	}
	type sstOut struct {
		noul   float64
		choice typesafe.Answer
	}
	res, errs := parallel(ctx, items, 16, func(ctx context.Context, _ int, it labelled) (sstOut, error) {
		r, err := c.SystemOne(ctx, typesafe.Request{
			State: map[string]string{"sentence": it.Text},
			Questions: typesafe.Questions{
				"noul": typesafe.Noul("Does `sentence` express a positive opinion of the movie?"),
				"choice": typesafe.Choice("What is the overall sentiment of `sentence` toward the movie?", map[string]any{
					"positive": "Favorable, praising, or recommending",
					"negative": "Unfavorable, critical, or dismissive",
				}),
			},
		})
		if err != nil {
			return sstOut{}, err
		}
		return sstOut{noul: r.Answers.Noul("noul"), choice: r.Answers.Choice("choice")}, nil
	})
	if err := firstErr(errs); err != nil {
		return err
	}

	var noulCal, choiceConfCal, choiceTopCal calib
	for i, it := range items {
		gold := it.Label == 1
		predN := res[i].noul > 0.5
		noulCal.add(max(res[i].noul, 1-res[i].noul), predN == gold)
		ch := res[i].choice
		predC := ch.Choice == "positive"
		choiceConfCal.add(ch.Confidence, predC == gold)
		choiceTopCal.add(ch.Probabilities[ch.Choice], predC == gold)
	}
	fmt.Printf("  SST-2 validation, %d sentences\n", n)
	noulCal.print("Noul, confidence = max(p, 1-p)")
	choiceConfCal.print("Choice, confidence field")
	fmt.Printf("  Choice, confidence = top probability: accuracy=%.1f%% ECE=%.3f\n", choiceTopCal.accuracy()*100, choiceTopCal.ece())

	// Disagreements between the two phrasings are worth a look.
	dis := 0
	for i := range items {
		if (res[i].noul > 0.5) != (res[i].choice.Choice == "positive") {
			dis++
			if dis <= 3 {
				fmt.Printf("    disagree: noul=%.2f choice=%s gold=%d  %q\n", res[i].noul, res[i].choice.Choice, items[i].Label, truncate(items[i].Text, 70))
			}
		}
	}
	fmt.Printf("  Noul and Choice disagreed on %d/%d\n", dis, n)

	// --- emotion ----------------------------------------------------------
	rows, err := hfRows("dair-ai/emotion", "split", "test", n)
	if err != nil {
		return err
	}
	labels := []string{"sadness", "joy", "love", "anger", "fear", "surprise"}
	opts := map[string]any{
		"sadness":  "Sad, down, hopeless, grieving, lonely",
		"joy":      "Happy, pleased, content, excited, hopeful",
		"love":     "Affection, fondness, caring, tenderness, romance",
		"anger":    "Angry, irritated, resentful, hostile, annoyed",
		"fear":     "Afraid, anxious, nervous, worried, terrified",
		"surprise": "Surprised, amazed, shocked, curious, caught off guard",
	}
	emo := make([]labelled, len(rows))
	for i, r := range rows {
		emo[i] = labelled{Text: r["text"].(string), Label: int(r["label"].(float64))}
	}
	ans, errs := parallel(ctx, emo, 16, func(ctx context.Context, _ int, it labelled) (typesafe.Answer, error) {
		r, err := c.SystemOne(ctx, typesafe.Request{
			State:     map[string]string{"text": it.Text},
			Questions: typesafe.Questions{"emotion": typesafe.Choice("Which emotion does the writer of `text` primarily express?", opts)},
		})
		if err != nil {
			return typesafe.Answer{}, err
		}
		return r.Answers.Choice("emotion"), nil
	})
	if err := firstErr(errs); err != nil {
		return err
	}

	cm := newConfusion(labels)
	var confCal, topCal calib
	for i, it := range emo {
		gold := labels[it.Label]
		cm.add(gold, ans[i].Choice)
		confCal.add(ans[i].Confidence, gold == ans[i].Choice)
		topCal.add(ans[i].Probabilities[ans[i].Choice], gold == ans[i].Choice)
	}
	fmt.Printf("\n  emotion test, %d texts, 6-way Choice: accuracy=%.1f%%\n", n, cm.accuracy()*100)
	cm.print()
	confCal.print("6-way Choice, confidence field")
	fmt.Printf("  6-way Choice, confidence = top probability: ECE=%.3f\n", topCal.ece())

	fmt.Println("  confidence-gated routing: act above threshold, escalate below")
	fmt.Printf("    %-9s %9s %14s\n", "threshold", "coverage", "acc. if acted")
	for _, th := range []float64{0.0, 0.3, 0.5, 0.7, 0.8, 0.9, 0.95} {
		covered, right := 0, 0
		for i, it := range emo {
			if ans[i].Confidence >= th {
				covered++
				if labels[it.Label] == ans[i].Choice {
					right++
				}
			}
		}
		acc := "-"
		if covered > 0 {
			acc = pctf(float64(right) / float64(covered))
		}
		fmt.Printf("    %-9.2f %9s %14s\n", th, pctf(float64(covered)/float64(n)), acc)
	}
	return nil
}
