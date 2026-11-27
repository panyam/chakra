// Package checkout runs a whole purchase.
package checkout

import (
	"example.com/shop/orders"
	"example.com/shop/payments"
	"example.com/shop/receipt"
)

// Run places the order, takes payment, and returns the receipt line.
func Run(id string) (string, error) {
	if err := orders.Place(id); err != nil {
		return "", err
	}
	if err := payments.Charge(id); err != nil {
		return "", err
	}
	return receipt.Print(id, 0), nil
}
