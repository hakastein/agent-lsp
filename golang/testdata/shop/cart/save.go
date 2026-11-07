package cart

// Save keeps c in its file.
func Save(c *Cart) error { return store(c, c.path()) }
