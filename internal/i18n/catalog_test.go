package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var verb = regexp.MustCompile(`%[-+# 0]*(\[\d+\])?\d*(\.\d+)?[a-zA-Z%]`)

func TestCatalogsHaveTheSameKeysAndVerbs(t *testing.T) {
	for key, english := range en {
		french, ok := fr[key]
		if !ok {
			t.Errorf("fr lacks %q", key)
			continue
		}
		if a, b := verb.FindAllString(english, -1), verb.FindAllString(french, -1); !slices.Equal(a, b) {
			t.Errorf("%q: en verbs %v, fr verbs %v", key, a, b)
		}
	}
	for key := range fr {
		if _, ok := en[key]; !ok {
			t.Errorf("fr has %q, en does not", key)
		}
	}
}

// translatedCalls returns the literal keys passed to i18n.T/Format/NewError and to i18n.Plural in the
// non-test files of the module, and every string literal of those files (keys kept in variables).
func translatedCalls(t *testing.T) (keys, plurals map[string]string, literals map[string]bool) {
	t.Helper()
	keys, plurals, literals = map[string]string{}, map[string]string{}, map[string]bool{}
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Hidden directories hold other checkouts (.claude/worktrees) and tool output.
		if d.IsDir() && path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		// The catalogs' own keys would count every key as used.
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
			strings.HasPrefix(d.Name(), "catalog_") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					literals[s] = true
				}
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "i18n" {
				return true
			}
			keyArg := call.Args[0]
			if sel.Sel.Name == "Plural" && len(call.Args) > 1 {
				keyArg = call.Args[1] // the key is the second argument of Plural
			}
			lit, ok := keyArg.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			key, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			switch sel.Sel.Name {
			case "T", "Format", "NewError":
				keys[key] = path
			case "Plural":
				plurals[key] = path
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return keys, plurals, literals
}

func TestEveryKeyIsDefinedAndUsed(t *testing.T) {
	keys, plurals, literals := translatedCalls(t)
	used := map[string]bool{}
	for key, path := range keys {
		if _, ok := en[key]; !ok {
			t.Errorf("%s uses %q, missing from catalog_en.go", path, key)
		}
		used[key] = true
	}
	for key, path := range plurals {
		for _, form := range []string{".one", ".other"} {
			if _, ok := en[key+form]; !ok {
				t.Errorf("%s uses plural %q, missing %q from catalog_en.go", path, key, key+form)
			}
			used[key+form] = true
		}
	}
	for key := range en {
		if !used[key] && !literals[key] && !builtFromPrefix(key, literals) {
			t.Errorf("catalog_en.go has %q, no code uses it", key)
		}
	}
}

// builtFromPrefix reports whether key can be built at run time from a literal prefix ending with a
// dot, as i18n.T("config.key." + name) does.
func builtFromPrefix(key string, literals map[string]bool) bool {
	for i := range len(key) {
		if key[i] == '.' && literals[key[:i+1]] {
			return true
		}
	}
	return false
}
