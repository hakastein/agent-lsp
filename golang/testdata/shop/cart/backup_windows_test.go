package cart

import "testing"

func TestBackup(t *testing.T) {
	if err := backup(fill()); err != nil {
		t.Fatal(err)
	}
}
