package engine

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"verk/internal/state"
)

// resolveTestReference checks whether the test reference points at an actual
// test in the worktree. For "test_function" Kind, it walks Go AST of files in
// Package and looks for a top-level FuncDecl named Name. For "file_line" Kind,
// it confirms File:Line falls inside a test function body. Returns an error
// with reason "test_reference_unresolved" when the reference cannot be
// confirmed.
//
// When repoRoot is empty the check is skipped and nil is returned. This
// preserves test ergonomics for unit tests that do not set up a full worktree.
func resolveTestReference(repoRoot string, ref state.TestReference) error {
	if repoRoot == "" {
		return nil
	}

	switch ref.Kind {
	case "test_function":
		return resolveTestFunction(repoRoot, ref.Package, ref.Name)
	case "file_line":
		return resolveFileLine(repoRoot, ref.File, ref.Line)
	default:
		// ValidateTestReference already guards unknown kinds; treat as
		// unresolvable rather than panicking.
		return fmt.Errorf("test_reference_unresolved: unknown kind %q", ref.Kind)
	}
}

// resolveTestFunction finds a top-level FuncDecl named name in *_test.go
// files under repoRoot/pkg.
func resolveTestFunction(repoRoot, pkg, name string) error {
	dir := filepath.Join(repoRoot, pkg)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("test_reference_unresolved: package directory %q not found", pkg)
		}
		return fmt.Errorf("test_reference_unresolved: cannot read package directory %q: %w", pkg, err)
	}

	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			continue // skip unparseable files gracefully
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Name != nil && fn.Name.Name == name {
				return nil
			}
		}
	}

	return fmt.Errorf("test_reference_unresolved: function %q not found in package %q", name, pkg)
}

// resolveFileLine confirms that File:Line inside repoRoot falls within a
// top-level FuncDecl whose name starts with "Test".
func resolveFileLine(repoRoot, file string, line int) error {
	path := filepath.Join(repoRoot, file)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("test_reference_unresolved: file %q not found", file)
		}
		return fmt.Errorf("test_reference_unresolved: cannot parse file %q: %w", file, err)
	}

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Name == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
			continue
		}
		if fn.Body == nil {
			continue
		}
		startLine := fset.Position(fn.Body.Lbrace).Line
		endLine := fset.Position(fn.Body.Rbrace).Line
		if line >= startLine && line <= endLine {
			return nil
		}
	}

	return fmt.Errorf("test_reference_unresolved: line %d in file %q is not inside a Test* function body", line, file)
}
