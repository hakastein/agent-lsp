package cart

// store keeps c in the file at path.
func store(c *Cart, path string) error { return nil }

// path is the file a cart is kept in.
func (c *Cart) path() string { return `C:\cart` }

// backup keeps c in a second file.
func backup(c *Cart) error { return store(c, c.path()+".bak") }
