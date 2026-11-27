// Package receipt prints what an order cost.
package receipt

import (
	"strings"

	"example.com/ledger/money"
)

// Format turns an item id into the name a receipt shows.
func Format(id string) string { return strings.ToUpper(id) }

// Print renders one line of a receipt.
func Print(id string, a money.Amount) string {
	return Format(id) + " " + money.Format(a)
}
