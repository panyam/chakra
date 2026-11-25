// Package cart holds what a customer is buying.
package cart

import "example.com/shopkit/money"

// Cart is a list of item prices in cents.
type Cart struct {
	Prices []int
}

// Total is what the cart costs. A nil cart costs nothing.
func (c *Cart) Total() int {
	if c == nil {
		return 0
	}
	return money.Sum(c.Prices)
}

// Priciest is the most expensive item's price.
func (c *Cart) Priciest() int {
	if c == nil {
		return 0
	}
	return money.Max(c.Prices)
}
