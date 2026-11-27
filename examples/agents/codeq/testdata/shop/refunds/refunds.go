// Package refunds gives money back.
package refunds

import (
	"example.com/ledger/audit"
	"example.com/ledger/journal"
)

// Issue refunds an order by replaying its entries reversed.
func Issue(entries []journal.Entry) error {
	for i := range entries {
		entries[i].Amount = -entries[i].Amount
	}
	return audit.Replay(entries)
}
