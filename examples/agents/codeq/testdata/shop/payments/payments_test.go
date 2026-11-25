package payments

import (
	"testing"

	"example.com/ledger/money"
)

func TestCharge(t *testing.T) {
	if err := Charge("tea"); err != nil {
		t.Fatal(err)
	}
	if money.Format(450) != "$4.50" {
		t.Fatal("format")
	}
}
