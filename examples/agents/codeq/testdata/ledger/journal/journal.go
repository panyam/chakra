// Package journal records entries against accounts.
package journal

import "example.com/ledger/money"

// Entry is one posting.
type Entry struct {
	Account string
	Amount  money.Amount
}

var posted []Entry

// Post records e.
func Post(e Entry) error {
	posted = append(posted, e)
	return nil
}

// Balance sums the entries for account.
func Balance(account string) money.Amount {
	var total money.Amount
	for _, e := range posted {
		if e.Account == account {
			total = money.Add(total, e.Amount)
		}
	}
	return total
}
