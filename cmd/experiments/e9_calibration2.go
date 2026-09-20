package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// Experiment 9: is the six-way calibration collapse about class count or about
// noisy labels? BoolQ is a hard binary reading task. AG News (4 classes) and
// DBpedia-14 (14 classes) both have clean labels.
func calibration2(ctx context.Context, c *typesafe.Client) error {
	const n = 300

	// --- BoolQ: hard binary -----------------------------------------------------
	rows, err := hfRows("google/boolq", "default", "validation", n)
	if err != nil {
		return err
	}
	type bq struct {
		q, passage string
		gold       bool
	}
	items := make([]bq, len(rows))
	for i, r := range rows {
		items[i] = bq{q: r["question"].(string), passage: r["passage"].(string), gold: r["answer"].(bool)}
	}
	type bqOut struct {
		p  float64
		ch typesafe.Answer
	}
	res, errs := parallel(ctx, items, 16, func(ctx context.Context, _ int, it bq) (bqOut, error) {
		r, err := c.SystemOne(ctx, typesafe.Request{
			State: map[string]string{"question": it.q, "passage": it.passage},
			Questions: typesafe.Questions{
				"yes": typesafe.Noul("According to `passage`, is the answer to `question` yes?"),
				"choice": typesafe.Choice("According to `passage`, what is the answer to `question`?", map[string]any{
					"yes": "The passage supports a yes answer",
					"no":  "The passage supports a no answer",
				}),
			},
		})
		if err != nil {
			return bqOut{}, err
		}
		return bqOut{p: r.Answers.Noul("yes"), ch: r.Answers.Choice("choice")}, nil
	})
	if err := firstErr(errs); err != nil {
		return err
	}
	var noulCal, chCal, chTop calib
	yesRate := 0
	for i, it := range items {
		if it.gold {
			yesRate++
		}
		noulCal.add(max(res[i].p, 1-res[i].p), (res[i].p > 0.5) == it.gold)
		ok := (res[i].ch.Choice == "yes") == it.gold
		chCal.add(res[i].ch.Confidence, ok)
		chTop.add(res[i].ch.Probabilities[res[i].ch.Choice], ok)
	}
	fmt.Printf("  BoolQ validation, %d passage+question pairs (majority class %.0f%%)\n", n, float64(yesRate)/float64(n)*100)
	noulCal.print("Noul, confidence = max(p, 1-p)")
	fmt.Printf("  Choice yes/no: accuracy=%.1f%% ECE(confidence)=%.3f ECE(top p)=%.3f\n\n", chCal.accuracy()*100, chCal.ece(), chTop.ece())

	// --- AG News: 4 clean classes -----------------------------------------------
	agRows, err := hfRows("fancyzhx/ag_news", "default", "test", n)
	if err != nil {
		return err
	}
	agLabels := []string{"World", "Sports", "Business", "Sci/Tech"}
	agOpts := map[string]any{
		"World":    "International news, politics, conflict, government",
		"Sports":   "Sports, teams, athletes, matches, leagues",
		"Business": "Companies, markets, economy, finance, deals",
		"Sci/Tech": "Science, technology, software, internet, research",
	}
	agItems := make([]labelled, len(agRows))
	for i, r := range agRows {
		agItems[i] = labelled{Text: r["text"].(string), Label: int(r["label"].(float64))}
	}
	agAns, errs := parallel(ctx, agItems, 16, func(ctx context.Context, _ int, it labelled) (typesafe.Answer, error) {
		r, err := c.SystemOne(ctx, typesafe.Request{
			State:     map[string]string{"headline_and_lead": it.Text},
			Questions: typesafe.Questions{"topic": typesafe.Choice("Which news section does `headline_and_lead` belong to?", agOpts)},
		})
		if err != nil {
			return typesafe.Answer{}, err
		}
		return r.Answers.Choice("topic"), nil
	})
	if err := firstErr(errs); err != nil {
		return err
	}
	agGold := make([]string, len(agItems))
	for i, it := range agItems {
		agGold[i] = agLabels[it.Label]
	}
	reportChoice("AG News test, 4-way Choice", agLabels, agGold, agAns, true)

	// --- DBpedia-14: 14 clean classes, sampled evenly (split is sorted by label) --
	dbLabels := []string{"Company", "EducationalInstitution", "Artist", "Athlete", "OfficeHolder", "MeanOfTransportation", "Building", "NaturalPlace", "Village", "Animal", "Plant", "Album", "Film", "WrittenWork"}
	dbOpts := map[string]any{
		"Company":                "A business or corporation",
		"EducationalInstitution": "A school, college, or university",
		"Artist":                 "A musician, painter, writer, actor, or other creative person",
		"Athlete":                "A sportsperson",
		"OfficeHolder":           "A politician, judge, or public official",
		"MeanOfTransportation":   "A vehicle, ship, aircraft, or vehicle model",
		"Building":               "A building or structure",
		"NaturalPlace":           "A river, mountain, lake, or other natural feature",
		"Village":                "A village or small settlement",
		"Animal":                 "An animal species",
		"Plant":                  "A plant species",
		"Album":                  "A music album",
		"Film":                   "A movie",
		"WrittenWork":            "A book, novel, magazine, or other written work",
	}
	const perClass = 21
	var dbItems []labelled
	for cls := range dbLabels {
		rows, err := hfRowsAt("fancyzhx/dbpedia_14", "dbpedia_14", "test", cls*5000+100, perClass)
		if err != nil {
			return err
		}
		for _, r := range rows {
			dbItems = append(dbItems, labelled{Text: r["title"].(string) + ". " + truncate(strings.TrimSpace(r["content"].(string)), 500), Label: int(r["label"].(float64))})
		}
	}
	dbAns, errs := parallel(ctx, dbItems, 16, func(ctx context.Context, _ int, it labelled) (typesafe.Answer, error) {
		r, err := c.SystemOne(ctx, typesafe.Request{
			State:     map[string]string{"article": it.Text},
			Questions: typesafe.Questions{"kind": typesafe.Choice("What kind of thing is the subject of `article`?", dbOpts)},
		})
		if err != nil {
			return typesafe.Answer{}, err
		}
		return r.Answers.Choice("kind"), nil
	})
	if err := firstErr(errs); err != nil {
		return err
	}
	dbGold := make([]string, len(dbItems))
	for i, it := range dbItems {
		dbGold[i] = dbLabels[it.Label]
	}
	reportChoice(fmt.Sprintf("DBpedia-14 test, 14-way Choice, %d per class", perClass), dbLabels, dbGold, dbAns, false)

	fmt.Println("\n  compare with experiment 1: emotion 6-way was 61.5% accurate with ECE 0.229")
	return nil
}

func reportChoice(name string, labels, gold []string, ans []typesafe.Answer, showMatrix bool) {
	cm := newConfusion(labels)
	var confCal, topCal calib
	for i := range gold {
		ok := gold[i] == ans[i].Choice
		cm.add(gold[i], ans[i].Choice)
		confCal.add(ans[i].Confidence, ok)
		topCal.add(ans[i].Probabilities[ans[i].Choice], ok)
	}
	fmt.Printf("  %s: accuracy=%.1f%% ECE(confidence)=%.3f ECE(top p)=%.3f (n=%d)\n", name, cm.accuracy()*100, confCal.ece(), topCal.ece(), len(gold))
	if showMatrix {
		cm.print()
	}
	fmt.Printf("    %-9s %9s %14s\n", "threshold", "coverage", "acc. if acted")
	for _, th := range []float64{0.0, 0.5, 0.7, 0.9, 0.95} {
		covered, right := 0, 0
		for i := range gold {
			if ans[i].Confidence >= th {
				covered++
				if gold[i] == ans[i].Choice {
					right++
				}
			}
		}
		acc := "-"
		if covered > 0 {
			acc = pctf(float64(right) / float64(covered))
		}
		fmt.Printf("    %-9.2f %9s %14s\n", th, pctf(float64(covered)/float64(len(gold))), acc)
	}
	// Per-class recall highlights where a many-way Choice goes wrong.
	var weak []string
	for i, l := range labels {
		tot := 0
		for j := range labels {
			tot += cm.m[i][j]
		}
		if tot > 0 && float64(cm.m[i][i])/float64(tot) < 0.7 {
			weak = append(weak, fmt.Sprintf("%s %d/%d", l, cm.m[i][i], tot))
		}
	}
	if len(weak) > 0 {
		fmt.Printf("    classes under 70%% recall: %s\n", strings.Join(weak, ", "))
	}
}
