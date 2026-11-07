//go:build !windows

package cart

// store keeps c in the file at path.
func store(c *Cart, path string) error { return nil }

// path is the file a cart is kept in.
func (c *Cart) path() string { return "/var/lib/cart" }
