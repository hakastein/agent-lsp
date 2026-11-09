package golang

import (
	"fmt"
	"os"
	"strings"

	"github.com/hakastein/agent-lsp/internal/gopls/xtools/go/packages"
)

// DEBUG
func dbgPkgs(cfg *packages.Config, patterns []string, pkgs []*packages.Package, err error) {
	if os.Getenv("AGENTLSP_DBG") == "" {
		return
	}
	wd, _ := os.Getwd()
	fmt.Fprintf(os.Stderr, "DBG load dir=%q wd=%q patterns=%q err=%v n=%d\n", cfg.Dir, wd, patterns, err, len(pkgs))
	for _, p := range pkgs {
		fmt.Fprintf(os.Stderr, "DBG  root %s name=%q go=%q compiled=%q errs=%v mod=%+v\n", p.ID, p.Name, p.GoFiles, p.CompiledGoFiles, p.Errors, p.Module)
		for path, imp := range p.Imports {
			if !strings.Contains(path, ".") {
				continue
			}
			fmt.Fprintf(os.Stderr, "DBG   imp %s -> %s name=%q go=%q compiled=%q errs=%v mod=%+v\n", path, imp.ID, imp.Name, imp.GoFiles, imp.CompiledGoFiles, imp.Errors, imp.Module)
		}
	}
}
