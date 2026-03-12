package codeintel

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// extractGoSymbols uses go/ast for accurate Go symbol extraction.
func extractGoSymbols(content string) []Symbol {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "source.go", content, parser.ParseComments)
	if err != nil {
		// Fallback to regex if AST parsing fails (e.g., incomplete code).
		return extractGoSymbolsRegex(content)
	}

	var symbols []Symbol

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			sym := Symbol{
				Name:      d.Name.Name,
				Kind:      "function",
				StartLine: fset.Position(d.Pos()).Line,
				EndLine:   fset.Position(d.End()).Line,
			}
			// Check if it's a method (has a receiver).
			if d.Recv != nil && len(d.Recv.List) > 0 {
				sym.Kind = "method"
				recvType := exprToString(d.Recv.List[0].Type)
				sym.Signature = fmt.Sprintf("func (%s) %s%s", recvType, d.Name.Name, formatFuncParams(d.Type))
			} else {
				sym.Signature = fmt.Sprintf("func %s%s", d.Name.Name, formatFuncParams(d.Type))
			}
			symbols = append(symbols, sym)

		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					sym := Symbol{
						Name:      s.Name.Name,
						StartLine: fset.Position(s.Pos()).Line,
						EndLine:   fset.Position(s.End()).Line,
					}
					switch s.Type.(type) {
					case *ast.InterfaceType:
						sym.Kind = "interface"
						sym.Signature = fmt.Sprintf("type %s interface", s.Name.Name)
					case *ast.StructType:
						sym.Kind = "type"
						sym.Signature = fmt.Sprintf("type %s struct", s.Name.Name)
					default:
						sym.Kind = "type"
						sym.Signature = fmt.Sprintf("type %s %s", s.Name.Name, exprToString(s.Type))
					}
					symbols = append(symbols, sym)

				case *ast.ValueSpec:
					kind := "variable"
					keyword := "var"
					if d.Tok == token.CONST {
						kind = "variable"
						keyword = "const"
					}
					for _, name := range s.Names {
						if name.Name == "_" {
							continue
						}
						sig := keyword + " " + name.Name
						if s.Type != nil {
							sig += " " + exprToString(s.Type)
						}
						symbols = append(symbols, Symbol{
							Name:      name.Name,
							Kind:      kind,
							StartLine: fset.Position(name.Pos()).Line,
							EndLine:   fset.Position(name.End()).Line,
							Signature: sig,
						})
					}
				}
			}
		}
	}

	return symbols
}

// exprToString converts an AST expression to a string representation.
func exprToString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return "*" + exprToString(e.X)
	case *ast.SelectorExpr:
		return exprToString(e.X) + "." + e.Sel.Name
	case *ast.ArrayType:
		return "[]" + exprToString(e.Elt)
	case *ast.MapType:
		return "map[" + exprToString(e.Key) + "]" + exprToString(e.Value)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.FuncType:
		return "func" + formatFuncParams(e)
	case *ast.ChanType:
		return "chan " + exprToString(e.Value)
	case *ast.Ellipsis:
		return "..." + exprToString(e.Elt)
	default:
		return "?"
	}
}

// formatFuncParams formats function parameters for the signature.
func formatFuncParams(ft *ast.FuncType) string {
	var params []string
	if ft.Params != nil {
		for _, p := range ft.Params.List {
			typeStr := exprToString(p.Type)
			if len(p.Names) == 0 {
				params = append(params, typeStr)
			} else {
				for _, name := range p.Names {
					params = append(params, name.Name+" "+typeStr)
				}
			}
		}
	}

	result := "(" + strings.Join(params, ", ") + ")"

	if ft.Results != nil && len(ft.Results.List) > 0 {
		var results []string
		for _, r := range ft.Results.List {
			typeStr := exprToString(r.Type)
			if len(r.Names) > 0 {
				for _, name := range r.Names {
					results = append(results, name.Name+" "+typeStr)
				}
			} else {
				results = append(results, typeStr)
			}
		}
		if len(results) == 1 {
			result += " " + results[0]
		} else {
			result += " (" + strings.Join(results, ", ") + ")"
		}
	}

	return result
}

// extractGoSymbolsRegex is a fallback for Go files that fail AST parsing.
func extractGoSymbolsRegex(content string) []Symbol {
	return extractWithPatterns(content, goPatterns)
}

var goPatterns = []symbolPattern{
	{kind: "function", pattern: `^func\s+(\w+)\s*\(`},
	{kind: "method", pattern: `^func\s+\([^)]+\)\s+(\w+)\s*\(`},
	{kind: "type", pattern: `^type\s+(\w+)\s+struct\b`},
	{kind: "interface", pattern: `^type\s+(\w+)\s+interface\b`},
	{kind: "type", pattern: `^type\s+(\w+)\s+\w`},
}
