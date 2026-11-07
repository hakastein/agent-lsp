package cart

// restock adds the first item of c again.
func restock(c *Cart) { c.Add(c.Items[0]) }
