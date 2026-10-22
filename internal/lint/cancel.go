package lint

import (
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/analysis"
)

const errgroupPkg = "golang.org/x/sync/errgroup"

var PanicCtx = &analysis.Analyzer{
	Name: "panicctx",
	Doc:  "a recovered panic is logged and recorded under context.WithoutCancel; on a canceled request the log handler would make it a WARN and RecordError would drop it",
	Run:  runPanicCtx,
}

func runPanicCtx(pass *analysis.Pass) (any, error) {
	hooks := recoverHooks(pass)
	eachFile(pass, func(w *fileWalk, file *ast.File) {
		detached := detachedAt(pass, file)
		check := func(name string, body *ast.BlockStmt) {
			ast.Inspect(body, func(n ast.Node) bool {
				if _, ok := n.(*ast.FuncLit); ok {
					return false
				}
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				target := callee(pass, call)
				if !isFunc(target, slogPkg, "ErrorContext") && !isFunc(target, telemetryPkg, "RecordError") {
					return true
				}
				if isDetached(pass, detached, call.Args[0], call.Pos()) || w.exempt(call) {
					return true
				}
				pass.Reportf(call.Pos(), "%s reports a panic under a context that can be canceled; detach it with context.WithoutCancel", name)
				return true
			})
		}
		for _, decl := range file.Decls {
			name := "declaration"
			switch d := decl.(type) {
			case *ast.FuncDecl:
				name = d.Name.Name
			case *ast.GenDecl:
				name = declName(d)
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				var body *ast.BlockStmt
				switch fn := n.(type) {
				case *ast.FuncDecl:
					body = fn.Body
				case *ast.FuncLit:
					body = fn.Body
				}
				if body != nil && (hooks[body] || callsRecover(pass, body)) {
					check(name, body)
				}
				return true
			})
		}
	})
	return nil, nil
}

// recoverHooks is the bodies of the functions given to connect.WithRecover, which
// handle a panic without calling recover themselves.
func recoverHooks(pass *analysis.Pass) map[*ast.BlockStmt]bool {
	out := map[*ast.BlockStmt]bool{}
	funcs := map[types.Object]bool{}
	for _, file := range pass.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 || !isFunc(callee(pass, call), connectPkg, "WithRecover") {
				return true
			}
			switch arg := ast.Unparen(call.Args[0]).(type) {
			case *ast.FuncLit:
				out[arg.Body] = true
			case *ast.Ident:
				funcs[pass.TypesInfo.Uses[arg]] = true
			case *ast.SelectorExpr:
				funcs[pass.TypesInfo.Uses[arg.Sel]] = true
			}
			return true
		})
	}
	for _, file := range pass.Files {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil && funcs[pass.TypesInfo.Defs[fn.Name]] {
				out[fn.Body] = true
			}
		}
	}
	return out
}

// callsRecover looks only at the function's own statements: a recover in a
// nested function literal belongs to that literal.
func callsRecover(pass *analysis.Pass, body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			if id, ok := ast.Unparen(n.Fun).(*ast.Ident); ok {
				if b, ok := pass.TypesInfo.Uses[id].(*types.Builtin); ok && b.Name() == "recover" {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

func detachedAt(pass *analysis.Pass, file *ast.File) map[types.Object][]token.Pos {
	out := map[types.Object][]token.Pos{}
	ast.Inspect(file, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != len(as.Rhs) {
			return true
		}
		for i, rhs := range as.Rhs {
			if id, ok := as.Lhs[i].(*ast.Ident); ok && isWithoutCancel(pass, rhs) {
				obj := pass.TypesInfo.ObjectOf(id)
				out[obj] = append(out[obj], as.Pos())
			}
		}
		return true
	})
	return out
}

func isDetached(pass *analysis.Pass, detached map[types.Object][]token.Pos, ctx ast.Expr, use token.Pos) bool {
	if isWithoutCancel(pass, ctx) {
		return true
	}
	id, ok := ast.Unparen(ctx).(*ast.Ident)
	if !ok {
		return false
	}
	for _, at := range detached[pass.TypesInfo.Uses[id]] {
		if at < use {
			return true
		}
	}
	return false
}

func isWithoutCancel(pass *analysis.Pass, e ast.Expr) bool {
	call, ok := ast.Unparen(e).(*ast.CallExpr)
	return ok && isFunc(callee(pass, call), "context", "WithoutCancel")
}

var GroupCtx = &analysis.Analyzer{
	Name: "groupctx",
	Doc:  "an errgroup's context is canceled once Wait returns, so nothing after Wait may use it; a failure logged under it would read as a client cancel",
	Run:  runGroupCtx,
}

func runGroupCtx(pass *analysis.Pass) (any, error) {
	eachFile(pass, func(w *fileWalk, file *ast.File) {
		groupCtx := map[types.Object]types.Object{}
		ast.Inspect(file, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Lhs) != 2 || len(as.Rhs) != 1 {
				return true
			}
			call, ok := as.Rhs[0].(*ast.CallExpr)
			if !ok || !isFunc(callee(pass, call), errgroupPkg, "WithContext") {
				return true
			}
			group, gok := as.Lhs[0].(*ast.Ident)
			ctx, cok := as.Lhs[1].(*ast.Ident)
			if gok && cok {
				groupCtx[pass.TypesInfo.ObjectOf(group)] = pass.TypesInfo.ObjectOf(ctx)
			}
			return true
		})

		type wait struct {
			group string
			end   token.Pos
		}
		waits := map[types.Object][]wait{}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isMethod(callee(pass, call), errgroupPkg, "Group", "Wait") {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if group, ok := sel.X.(*ast.Ident); ok {
				if ctx := groupCtx[pass.TypesInfo.Uses[group]]; ctx != nil {
					waits[ctx] = append(waits[ctx], wait{group.Name, call.End()})
				}
			}
			return true
		})

		ast.Inspect(file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			for _, wt := range waits[pass.TypesInfo.Uses[id]] {
				if id.Pos() > wt.end && !w.exempt(id) {
					pass.Reportf(id.Pos(), "%s is canceled once %s.Wait returns; use the parent context", id.Name, wt.group)
					break
				}
			}
			return true
		})
	})
	return nil, nil
}
