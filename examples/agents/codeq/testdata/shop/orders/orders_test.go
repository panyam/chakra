package orders

import "testing"

func TestPlace(t *testing.T) {
	if err := Place("tea"); err != nil {
		t.Fatal(err)
	}
}
