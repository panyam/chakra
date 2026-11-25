// Package audit replays entries into a fresh journal.
package audit

import "example.com/ledger/journal"

// Replay posts every entry again.
func Replay(entries []journal.Entry) error {
	for _, e := range entries {
		if err := journal.Post(e); err != nil {
			return err
		}
	}
	return nil
}
