package main

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

const email = `From: Priya Natarajan <priya@northwind-labs.io>
To: billing@vendor.example
Subject: Invoice 4471 overpaid

Hi, we paid invoice 4471 twice. The first payment of $1,250.00 went through on March 3 and a
duplicate $1,250.00 on March 5. Our accounts team also flagged an unrelated late fee of $35.00
from January. Please refund the duplicate to the card ending 4482. For questions call
+1 (415) 555-0142 or my colleague Sam at sam.okafor@northwind-labs.io. Our old billing
contact ap-old@northwind-labs.io no longer works.`

// candidates turns every regex match into an undescribed Choice option, plus a no-match option.
func candidates(re *regexp.Regexp, none string) map[string]any {
	opts := map[string]any{"none": none}
	for _, m := range re.FindAllString(email, -1) {
		opts[m] = nil
	}
	return opts
}

// Scenario 6: select instead of generate. Regex finds candidate spans; a Choice picks
// the intended one; code normalizes the verbatim value. The model never invents a value.
func selection(ctx context.Context, c *typesafe.Client) error {
	amounts := candidates(regexp.MustCompile(`\$\d[\d,]*(?:\.\d{2})?`), "No amount in the list is the requested refund")
	emails := candidates(regexp.MustCompile(`[\w.+-]+@[\w-]+(?:\.[\w-]+)+`), "None of these")
	phones := candidates(regexp.MustCompile(`\+?\d[\d\s().-]{8,}\d`), "None of these")
	fmt.Printf("  candidates: amounts=%d emails=%d phones=%d (each plus 'none')\n", len(amounts)-1, len(emails)-1, len(phones)-1)

	res, err := c.SystemOne(ctx, typesafe.Request{
		State: map[string]string{"email": email},
		Questions: typesafe.Questions{
			"refund_amount":     typesafe.Choice("Which amount in `email` is the refund the sender is asking for?", amounts),
			"reply_to":          typesafe.Choice("Which address in `email` should a reply about this refund go to?", emails),
			"callback_phone":    typesafe.Choice("Which phone number in `email` should be used to call the sender back?", phones),
			"mentions_late_fee": typesafe.Noul("Does `email` mention a late fee?"),
			"late_fee_in_scope": typesafe.Noul("Is the sender asking for the late fee to be refunded?"),
		},
	})
	if err != nil {
		return err
	}
	for _, id := range []string{"refund_amount", "reply_to", "callback_phone", "mentions_late_fee", "late_fee_in_scope"} {
		fmt.Printf("  %s: %s\n", id, res.Answers[id])
	}

	if amt := res.Answers.Choice("refund_amount").Choice; amt != "none" {
		dollars, err := strconv.ParseFloat(strings.NewReplacer("$", "", ",", "").Replace(amt), 64)
		if err != nil {
			return fmt.Errorf("normalize %q: %w", amt, err)
		}
		fmt.Printf("  normalized: refund_cents=%d reply_to=%s\n", int(math.Round(dollars*100)), res.Answers.Choice("reply_to").Choice)
	}
	return nil
}
