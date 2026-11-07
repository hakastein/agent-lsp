//go:build !windows

package cart

import "testing"

func TestSave(t *testing.T) {
	if err := Save(fill()); err != nil {
		t.Fatal(err)
	}
}
