// Package api serves the shop over HTTP.
package api

import "example.com/shop/checkout"

// Handle serves one purchase request.
func Handle(id string) (string, error) {
	return checkout.Run(id)
}
