package modules

import (
	"fmt"
	"os"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/format"
	"cuelang.org/go/cue/parser"
)

// RemoveModuleFromConfig deletes the named module's field from its category
// struct in the config file, the inverse of writeModuleToConfig. It parses with
// comments preserved, mutates the AST, and reformats with format.Simplify() so
// the install-managed header and unrelated entries survive. An emptied category
// struct is dropped entirely; if the file then holds no category fields it is
// removed rather than left as a comment-only husk. Returns an error when the
// file, category, or module field is absent.
func RemoveModuleFromConfig(configPath, category, name string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("module %q not found in %s", name, category)
		}
		return fmt.Errorf("reading config file: %w", err)
	}

	file, err := parser.ParseFile(configPath, data, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parsing config file: %w", err)
	}

	catField := findCategoryField(file, category)
	if catField == nil {
		return fmt.Errorf("module %q not found in %s", name, category)
	}
	catStruct, ok := catField.Value.(*ast.StructLit)
	if !ok {
		return fmt.Errorf("category %q is not a struct", category)
	}

	if !removeStructField(catStruct, name) {
		return fmt.Errorf("module %q not found in %s", name, category)
	}

	if len(catStruct.Elts) == 0 {
		removeFileDecl(file, category)
	}
	if !fileHasConfigFields(file) {
		if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing emptied config file: %w", err)
		}
		return nil
	}

	formatted, err := format.Node(file, format.Simplify())
	if err != nil {
		return fmt.Errorf("formatting config: %w", err)
	}
	return os.WriteFile(configPath, formatted, 0644)
}

func fileHasConfigFields(file *ast.File) bool {
	for _, decl := range file.Decls {
		if _, ok := decl.(*ast.Field); ok {
			return true
		}
	}
	return false
}

// removeStructField removes the field labelled name from s, reporting whether it
// was present.
func removeStructField(s *ast.StructLit, name string) bool {
	for i, elt := range s.Elts {
		field, ok := elt.(*ast.Field)
		if !ok {
			continue
		}
		labelName, _, err := ast.LabelName(field.Label)
		if err != nil {
			continue
		}
		if labelName == name {
			s.Elts = append(s.Elts[:i], s.Elts[i+1:]...)
			return true
		}
	}
	return false
}

// removeFileDecl removes the top-level category field from file.
func removeFileDecl(file *ast.File, category string) {
	for i, decl := range file.Decls {
		field, ok := decl.(*ast.Field)
		if !ok {
			continue
		}
		labelName, _, err := ast.LabelName(field.Label)
		if err != nil {
			continue
		}
		if labelName == category {
			file.Decls = append(file.Decls[:i], file.Decls[i+1:]...)
			return
		}
	}
}
