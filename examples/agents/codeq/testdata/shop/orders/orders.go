// Package orders places orders.
package orders

import (
	"errors"

	"example.com/ledger/journal"
	"example.com/ledger/money"
	"example.com/shop/catalog"
	"example.com/shop/internal/db"
)

// Place orders the item called id.
func Place(id string) error {
	it, ok := catalog.Find(id)
	if !ok {
		return errors.New("orders: no such item")
	}
	total := money.Add(it.Price, 0)
	if err := journal.Post(journal.Entry{Account: "sales", Amount: total}); err != nil {
		return err
	}
	return db.Save("order:"+id, it.ID)
}
