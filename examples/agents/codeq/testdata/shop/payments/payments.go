// Package payments charges for orders.
package payments

import (
	"example.com/ledger/journal"
	"example.com/shop/internal/db"
)

// Charge takes payment for the order called id.
func Charge(id string) error {
	if err := journal.Post(journal.Entry{Account: "cash"}); err != nil {
		return err
	}
	return db.Save("paid:"+id, "yes")
}
