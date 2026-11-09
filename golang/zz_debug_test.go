package golang_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hakastein/agent-lsp/golang"
)

// DEBUG
func TestZZDebugCwd(t *testing.T) {
	for _, variant := range []string{"pkgdir", "empty", "mod", "work", "root"} {
		for _, rootName := range []string{"vendored", "shop"} {
			t.Run(variant+"/"+rootName, func(t *testing.T) {
				cache(t)
				t.Setenv("AGENTLSP_DBG", "1")
				testdata := filepath.Dir(newRoot(t))
				root := filepath.Join(testdata, rootName)
				side := filepath.Join(testdata, "side")
				require.NoError(t, os.MkdirAll(side, 0o755))
				switch variant {
				case "mod":
					require.NoError(t, os.WriteFile(filepath.Join(side, "go.mod"), []byte("module example.com/side\n\ngo 1.26\n"), 0o600))
				case "work":
					require.NoError(t, os.WriteFile(filepath.Join(side, "go.mod"), []byte("module example.com/side\n\ngo 1.26\n"), 0o600))
					require.NoError(t, os.WriteFile(filepath.Join(side, "go.work"), []byte("go 1.26\n\nuse .\n"), 0o600))
				}
				switch variant {
				case "empty", "mod", "work":
					t.Chdir(side)
				case "root":
					t.Chdir(root)
				}
				wd, _ := os.Getwd()
				t.Logf("CWD %s ROOT %s", wd, root)
				log := session(t, root, func(ix *golang.Index) {
					p := place{"main.go", "Amount(300)"}
					if rootName == "shop" {
						p = place{cartGo, "Plus(it.Price)"}
					}
					got, err := ix.Definition(p.position(t, root))
					t.Logf("RESULT %s/%s def=%d %+v err=%v", variant, rootName, len(got), got, err)
				})
				t.Logf("LOG %s", log)
			})
		}
	}
}
