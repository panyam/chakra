// Package db stores records by key.
package db

var rows = map[string]string{}

// Save stores v under k.
func Save(k, v string) error {
	rows[k] = v
	return nil
}

// Load returns the value under k.
func Load(k string) (string, bool) {
	v, ok := rows[k]
	return v, ok
}
