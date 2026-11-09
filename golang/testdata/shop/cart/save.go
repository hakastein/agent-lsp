package cart

// Save keeps c in its file.
func Save(c *Cart) error { return store(c, c.path()) }

// Saved is how many carts were saved.
func Saved() int { return saved.count }
