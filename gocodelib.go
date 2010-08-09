package main

import (
	"fmt"
	"bytes"
	"go/parser"
	"go/ast"
	"strings"
	"io/ioutil"
	"path"
	"sort"
)

//-------------------------------------------------------------------------------

const builtinUnsafePackage = `
import
$$
package unsafe 
	type "".Pointer *any
	func "".Offsetof (? any) int
	func "".Sizeof (? any) int
	func "".Alignof (? any) int
	func "".Typeof (i interface { }) interface { }
	func "".Reflect (i interface { }) (typ interface { }, addr "".Pointer)
	func "".Unreflect (typ interface { }, addr "".Pointer) interface { }
	func "".New (typ interface { }) "".Pointer
	func "".NewArray (typ interface { }, n int) "".Pointer

$$
`

func (self *AutoCompleteContext) addBuiltinUnsafe() {
	fname := findGlobalFile("unsafe")
	module := NewModuleCacheForever("unsafe", "unsafe")
	module.processPackageData(builtinUnsafePackage)
	self.mcache[fname] = module
}

func checkFuncFieldList(f *ast.FieldList) bool {
	if f == nil {
		return true
	}

	for _, field := range f.List {
		if !checkTypeExpr(field.Type) {
			return false
		}
	}
	return true
}

// checks for a type expression correctness, it the type expression has
// ast.BadExpr somewhere, returns false, otherwise true
func checkTypeExpr(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.StarExpr:
		return checkTypeExpr(t.X)
	case *ast.ArrayType:
		return checkTypeExpr(t.Elt)
	case *ast.SelectorExpr:
		return checkTypeExpr(t.X)
	case *ast.FuncType:
		a := checkFuncFieldList(t.Params)
		b := checkFuncFieldList(t.Results)
		return a && b
	case *ast.MapType:
		a := checkTypeExpr(t.Key)
		b := checkTypeExpr(t.Value)
		return a && b
	case *ast.Ellipsis:
		return checkTypeExpr(t.Elt)
	case *ast.ChanType:
		return checkTypeExpr(t.Value)
	case *ast.BadExpr:
		return false
	default:
		return true
	}
	return true
}

func filePackageName(filename string) string {
	file, _ := parser.ParseFile(filename, nil, nil, parser.PackageClauseOnly)
	return file.Name.Name()
}

type AutoCompleteContext struct {
	m map[string]*Decl // all visible modules

	current *PackageFile // currently editted file
	others map[string]*PackageFile // other files

	mcache map[string]*ModuleCache // modules cache
	pkg *Scope
	uni *Scope
}

func NewAutoCompleteContext() *AutoCompleteContext {
	self := new(AutoCompleteContext)
	self.current = NewPackageFile("", "", self)
	self.others = make(map[string]*PackageFile)
	self.mcache = make(map[string]*ModuleCache)
	self.pkg = NewScope(nil)
	self.addBuiltinUnsafe()
	self.createUniverseScope()
	return self
}

//-------------------------------------------------------------------------
// Sort interface for TriStringArrays
//-------------------------------------------------------------------------

type TriStringArrays struct {
	first []string
	second []string
	third []string
}

func (self TriStringArrays) Len() int {
	return len(self.first)
}

func (self TriStringArrays) Less(i, j int) bool {
	if self.third[i][0] == self.third[j][0] {
		return self.first[i] < self.first[j]
	}
	return self.third[i] < self.third[j]
}

func (self TriStringArrays) Swap(i, j int) {
	self.first[i], self.first[j] = self.first[j], self.first[i]
	self.second[i], self.second[j] = self.second[j], self.second[i]
	self.third[i], self.third[j] = self.third[j], self.third[i]
}

//-------------------------------------------------------------------------

func (self *AutoCompleteContext) updateOtherPackageFiles() {
	packageName := self.current.packageName
	filename := self.current.name

	dir, file := path.Split(filename)
	filesInDir, err := ioutil.ReadDir(dir)
	if err != nil {
		panic(err.String())
	}

	newothers := make(map[string]*PackageFile)
	for _, stat := range filesInDir {
		ok, _ := path.Match("*.go", stat.Name)
		if ok && stat.Name != file {
			filepath := path.Join(dir, stat.Name)
			oldother, ok := self.others[filepath]
			if ok && oldother.packageName == packageName {
				newothers[filepath] = oldother
			} else {
				pkg := filePackageName(filepath)
				if pkg == packageName {
					newothers[filepath] = NewPackageFile(filepath, packageName, self)
				}
			}
		}
	}
	self.others = newothers
}

//-------------------------------------------------------------------------

type OutBuffers struct {
	names, types, classes *bytes.Buffer
	ctx *AutoCompleteContext
}

func NewOutBuffers(ctx *AutoCompleteContext) *OutBuffers {
	b := new(OutBuffers)
	b.names = bytes.NewBuffer(make([]byte, 0, 4096))
	b.types = bytes.NewBuffer(make([]byte, 0, 4096))
	b.classes = bytes.NewBuffer(make([]byte, 0, 4096))
	b.ctx = ctx
	return b
}

func matchClass(declclass int, class int) bool {
	if class == declclass {
		return true
	}
	return false
}

func (self *OutBuffers) appendPackage(p, pak string, class int) {
	if startsWith(pak, p) || matchClass(DECL_MODULE, class) {
		fmt.Fprintf(self.names, "%s\n", pak)
		fmt.Fprintf(self.types, "\n")
		fmt.Fprintf(self.classes, "module\n")
	}
}

func (self *OutBuffers) appendDecl(p, name string, decl *Decl, class int) {
	if startsWith(name, p) || matchClass(decl.Class, class) {
		if !checkTypeExpr(decl.Type) {
			return
		}
		fmt.Fprintf(self.names, "%s\n", name)
		decl.PrettyPrintType(self.types)
		fmt.Fprintf(self.types, "\n")
		fmt.Fprintf(self.classes, "%s\n", decl.ClassName())
	}
}

func (self *OutBuffers) appendEmbedded(p string, decl *Decl, class int) {
	if decl.Embedded != nil {
		for _, emb := range decl.Embedded {
			typedecl := typeToDecl(emb, decl.Scope, self.ctx)
			if typedecl != nil {
				for _, c := range typedecl.Children {
					self.appendDecl(p, c.Name, c, class)
				}
				self.appendEmbedded(p, typedecl, class)
			}
		}
	}
}

func (self *AutoCompleteContext) appendModulesFromFile(ms map[string]*ModuleCache, f *PackageFile) {
	for _, m := range f.modules {
		if _, ok := ms[m.name]; ok {
			continue
		}
		if mod, ok := self.mcache[m.name]; ok {
			ms[m.name] = mod
		} else {
			mod = NewModuleCache(m.name, m.path)
			ms[m.name] = mod
			self.mcache[m.name] = mod
		}
	}
}

func (self *AutoCompleteContext) updateCaches() {
	ms := make(map[string]*ModuleCache)
	self.appendModulesFromFile(ms, self.current)

	stage1 := make(chan *PackageFile)
	stage2 := make(chan bool)
	for _, other := range self.others {
		go other.updateCache(stage1, stage2)
	}

	// stage 1: gather module import info
	for _ = range self.others {
		f := <-stage1
		self.appendModulesFromFile(ms, f)
	}
	self.appendModulesFromFile(ms, self.current)

	// start module cache update
	done := make(chan bool)
	for _, m := range ms {
		m.asyncUpdateCache(done)
	}

	// wait for completion
	for _ = range ms {
		<-done
	}

	// update imports and start stage2
	self.fixupModules(self.current)
	for _, f := range self.others {
		self.fixupModules(f)
		f.stage2go <- true
	}
	self.buildModulesMap(ms)
	for _ = range self.others {
		<-stage2
	}
}

func makeDeclSetRecursive(set map[string]*Decl, scope *Scope) {
	for name, ent := range scope.entities {
		if _, ok := set[name]; !ok {
			set[name] = ent
		}
	}
	if scope.parent != nil {
		makeDeclSetRecursive(set, scope.parent)
	}
}

func (self *AutoCompleteContext) makeDeclSet(scope *Scope) map[string]*Decl {
	set := make(map[string]*Decl, len(self.pkg.entities)*2)
	makeDeclSetRecursive(set, scope)
	return set
}

func (self *AutoCompleteContext) fixupModules(f *PackageFile) {
	for i := range f.modules {
		name := f.modules[i].name
		if f.modules[i].alias == "" {
			f.modules[i].alias = self.mcache[name].defalias
		}
		f.modules[i].module = self.mcache[name].main
	}
}

func (self *AutoCompleteContext) buildModulesMap(ms map[string]*ModuleCache) {
	self.m = make(map[string]*Decl)
	for _, mc := range ms {
		self.m[mc.name] = mc.main
		// TODO handle relative packages in other packages?
		for key, oth := range mc.others {
			if _, ok := ms[key]; ok {
				continue
			}
			var mod *Decl
			var ok bool
			if mod, ok = self.m[key]; !ok {
				mod = NewDecl(key, DECL_MODULE, nil)
				self.m[key] = mod
			}
			for _, decl := range oth.Children {
				mod.AddChild(decl)
			}

		}
	}
}

func (self *AutoCompleteContext) mergeDeclsFromFile(file *PackageFile) {
	for _, d := range file.decls {
		self.pkg.mergeDecl(d)
	}
	self.pkg.addChild(file.filescope)
}

func (self *AutoCompleteContext) createUniverseScope() {
	builtin := ast.NewIdent("built-in")
	self.uni = NewScope(nil)
	self.uni.addNamedDecl(NewDeclTyped("bool", DECL_TYPE, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("byte", DECL_TYPE, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("complex64", DECL_TYPE, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("complex128", DECL_TYPE, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("float32", DECL_TYPE, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("float64", DECL_TYPE, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("int8", DECL_TYPE, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("int16", DECL_TYPE, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("int32", DECL_TYPE, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("int64", DECL_TYPE, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("string", DECL_TYPE, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("uint8", DECL_TYPE, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("uint16", DECL_TYPE, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("uint32", DECL_TYPE, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("uint64", DECL_TYPE, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("complex", DECL_TYPE, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("float", DECL_TYPE, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("int", DECL_TYPE, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("uint", DECL_TYPE, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("uintptr", DECL_TYPE, builtin, self.uni))

	self.uni.addNamedDecl(NewDeclTyped("true", DECL_CONST, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("false", DECL_CONST, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("iota", DECL_CONST, builtin, self.uni))
	self.uni.addNamedDecl(NewDeclTyped("nil", DECL_CONST, builtin, self.uni))

	self.uni.addNamedDecl(NewDeclTypedNamed("cap", DECL_FUNC, "func(container) int", self.uni))
	self.uni.addNamedDecl(NewDeclTypedNamed("close", DECL_FUNC, "func(channel)", self.uni))
	self.uni.addNamedDecl(NewDeclTypedNamed("closed", DECL_FUNC, "func(channel) bool", self.uni))
	self.uni.addNamedDecl(NewDeclTypedNamed("cmplx", DECL_FUNC, "func(real, imag)", self.uni))
	self.uni.addNamedDecl(NewDeclTypedNamed("copy", DECL_FUNC, "func(dst, src)", self.uni))
	self.uni.addNamedDecl(NewDeclTypedNamed("imag", DECL_FUNC, "func(complex)", self.uni))
	self.uni.addNamedDecl(NewDeclTypedNamed("len", DECL_FUNC, "func(container) int", self.uni))
	self.uni.addNamedDecl(NewDeclTypedNamed("make", DECL_FUNC, "func(type, len[, cap]) type", self.uni))
	self.uni.addNamedDecl(NewDeclTypedNamed("new", DECL_FUNC, "func(type) *type", self.uni))
	self.uni.addNamedDecl(NewDeclTypedNamed("panic", DECL_FUNC, "func(interface{})", self.uni))
	self.uni.addNamedDecl(NewDeclTypedNamed("print", DECL_FUNC, "func(...interface{})", self.uni))
	self.uni.addNamedDecl(NewDeclTypedNamed("println", DECL_FUNC, "func(...interface{})", self.uni))
	self.uni.addNamedDecl(NewDeclTypedNamed("real", DECL_FUNC, "func(complex)", self.uni))
	self.uni.addNamedDecl(NewDeclTypedNamed("recover", DECL_FUNC, "func() interface{}", self.uni))
}

func (self *AutoCompleteContext) mergeDecls() {
	self.uni.children = nil
	self.pkg = NewScope(self.uni)
	self.mergeDeclsFromFile(self.current)
	for _, file := range self.others {
		self.mergeDeclsFromFile(file)
	}
}

// return three slices of the same length containing:
// 1. apropos names
// 2. apropos types (pretty-printed)
// 3. apropos classes
func (self *AutoCompleteContext) Apropos(file []byte, filename string, cursor int) ([]string, []string, []string, int) {
	var curctx ProcessDataContext

	self.current.cursor = cursor
	self.current.name = filename
	self.current.processDataStage1(file, &curctx)
	if filename != "" {
		self.updateOtherPackageFiles()
	}
	self.updateCaches()
	self.current.processDataStage2(&curctx)
	self.mergeDecls()
	self.current.processDataStage3(&curctx)

	b := NewOutBuffers(self)

	partial := 0
	da := self.deduceDecl(file, cursor)
	if da != nil {
		class := -1
		switch da.Partial {
		case "const":
			class = DECL_CONST
		case "var":
			class = DECL_VAR
		case "type":
			class = DECL_TYPE
		case "func":
			class = DECL_FUNC
		case "module":
			class = DECL_MODULE
		}
		if da.Decl == nil {
			// In case if no declaraion is a subject of completion, propose all:
			set := self.makeDeclSet(self.current.topscope)
			for key, value := range set {
				value.InferType(self)
				b.appendDecl(da.Partial, key, value, class)
			}
		} else {
			// propose all children of a subject declaration and
			// propose all children of its embedded types
			for _, decl := range da.Decl.Children {
				b.appendDecl(da.Partial, decl.Name, decl, class)
			}
			b.appendEmbedded(da.Partial, da.Decl, class)
		}
		partial = len(da.Partial)
	}

	if b.names.Len() == 0 || b.types.Len() == 0 || b.classes.Len() == 0 {
		return nil, nil, nil, 0
	}

	var tri TriStringArrays
	tri.first = strings.Split(b.names.String()[0:b.names.Len()-1], "\n", -1)
	tri.second = strings.Split(b.types.String()[0:b.types.Len()-1], "\n", -1)
	tri.third = strings.Split(b.classes.String()[0:b.classes.Len()-1], "\n", -1)
	if len(tri.first) != len(tri.second) || len(tri.first) != len(tri.third) {
		panic("Lengths should match")
	}
	sort.Sort(tri)
	return tri.first, tri.second, tri.third, partial
}

func (self *AutoCompleteContext) Status() string {
	buf := bytes.NewBuffer(make([]byte, 0, 4096))
	fmt.Fprintf(buf, "Number of top level packages: %d\n", len(self.m))
	if len(self.m) > 0 {
		fmt.Fprintf(buf, "\nListing packages:\n")
		for key, decl := range self.m {
			fmt.Fprintf(buf, "'%s' : %s\n", key, decl.Name)
		}
		fmt.Fprintf(buf, "\n")
	}
	return buf.String()
}
