// Package catalog lists what the shop sells.
package catalog

import "example.com/ledger/money"

// Item is one thing for sale.
type Item struct {
	ID    string
	Price money.Amount
}

var items = map[string]Item{"tea": {ID: "tea", Price: 450}}

// Find returns the item called id.
func Find(id string) (Item, bool) {
	it, ok := items[id]
	return it, ok
}
