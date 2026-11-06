package php_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReopenReusesTheIndex(t *testing.T) {
	cache(t)
	root := workspace(t, "methods")
	call := place{"src/Shop/Checkout.php", "add('apple')"}

	first, firstLog := open(t, root)
	wantDefinition, wantUses := definition(t, first, root, call), uses(t, first, root, call)
	second, secondLog := open(t, root)
	gotDefinition, gotUses := definition(t, second, root, call), uses(t, second, root, call)

	assert.Equal(t, wantDefinition, gotDefinition)
	assert.Equal(t, wantUses, gotUses)
	assert.NotEmpty(t, gotUses)
	assert.Contains(t, firstLog, built)
	assert.Contains(t, secondLog, reused)
	assert.NotContains(t, secondLog, built)
}

func TestReopenAfterAChangeAnswersFromTheDisk(t *testing.T) {
	tests := []struct {
		name   string
		change func(t *testing.T, root string)
		want   []place
	}{
		{"file changed", appendToApp, append([]place{{"src/app.php", "add('fresh')"}}, cartAddCalls...)},
		{"file added", addShop, append([]place{{"src/shop.php", "add('added')"}}, cartAddCalls...)},
		{"file removed", removeCheckout, appCartAddCalls},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache(t)
			root := workspace(t, "methods")
			first, _ := open(t, root)
			uses(t, first, root, cartAdd)

			tt.change(t, root)
			second, log := open(t, root)

			assert.ElementsMatch(t, locations(t, root, tt.want), uses(t, second, root, cartAdd))
			assert.Contains(t, log, built)
		})
	}
}

// The user's cache directory is a regular file: no index can be opened under it.
func TestOpenFailsWithoutACacheDirectory(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	require.NoError(t, os.WriteFile(home, nil, 0o600))
	cacheAt(t, home)

	_, _, err := tryOpen(t, workspace(t, "methods"))

	assert.Error(t, err)
}

func appendToApp(t *testing.T, root string) {
	t.Helper()
	app := filepath.Join(root, "src", "app.php")
	src, err := os.ReadFile(app)
	require.NoError(t, err)
	fresh := "\nfunction fresh(): void\n{\n    $basket = new Cart();\n    $basket->add('fresh');\n}\n"
	require.NoError(t, os.WriteFile(app, append(src, fresh...), 0o600))
}

func addShop(t *testing.T, root string) {
	t.Helper()
	src := "<?php\n\nfunction shop(): void\n{\n    $cart = new \\Shop\\Cart();\n    $cart->add('added');\n}\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "shop.php"), []byte(src), 0o600))
}

func removeCheckout(t *testing.T, root string) {
	t.Helper()
	require.NoError(t, os.Remove(filepath.Join(root, "src", "Shop", "Checkout.php")))
}
