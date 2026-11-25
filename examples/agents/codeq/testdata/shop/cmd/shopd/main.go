// Command shopd places and pays for one order.
package main

import (
	"log"

	"example.com/shop/orders"
	"example.com/shop/payments"
)

func main() {
	if err := orders.Place("tea"); err != nil {
		log.Fatal(err)
	}
	if err := payments.Charge("tea"); err != nil {
		log.Fatal(err)
	}
}
