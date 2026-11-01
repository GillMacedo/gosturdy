// Command gosturdy finds production risks in a Go service's source, such as
// missing timeouts and no graceful shutdown. It only parses files and never
// builds or runs them. It uses the standard library, so `go run` needs no setup.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	exitOK       = 0
	exitFindings = 1
	exitUsage    = 2
)

const (
	high   = "HIGH"
	medium = "MEDIUM"
	low    = "LOW"
)

// Finding is one thing to check. A parser sees shape, not behavior, so it isn't proof of a bug.
type Finding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Message  string `json:"message"`
	Fix      string `json:"fix"`
}

// Report is everything one scan found, including what it couldn't read.
type Report struct {
	Root        string    `json:"root"`
	FilesParsed int       `json:"files_parsed"`
	Findings    []Finding `json:"findings"`
	ParseErrors []string  `json:"parse_errors"`
	Ignored     int       `json:"ignored"`
}

type rule struct{ severity, fix string }

var rules = map[string]rule{
	"GS101": {high, "Use an http.Server with timeouts, and call Shutdown when the process gets SIGTERM."},
	"GS102": {high, "Set ReadHeaderTimeout at least, so slow clients can't hold connections open."},
	"GS103": {medium, "Set the missing timeouts, unless a long-lived response needs WriteTimeout left at zero."},
	"GS201": {high, "Use an http.Client with a Timeout. The default client waits forever on a stuck server."},
	"GS202": {medium, "Set Timeout, or confirm that every request carries a context with a deadline."},
	"GS301": {high, "Stop the server on SIGTERM and call Shutdown, so a deploy doesn't cut off requests in flight."},
	"GS302": {high, "Use signal.NotifyContext for SIGTERM and SIGINT, then start the shutdown from it."},
	"GS401": {medium, "Call SetMaxOpenConns, so a traffic spike can't exhaust the database's connections."},
	"GS402": {low, "Call SetConnMaxLifetime, so connections get replaced after failovers and network changes."},
	"GS501": {medium, "Use the request's context, so work stops when the client goes away or a deadline passes."},
	"GS502": {medium, "Copy what the goroutine needs first. The request and its context end when the handler returns."},
	"GS503": {medium, "Wrap the body with http.MaxBytesReader, so one large request can't exhaust memory."},
	"GS601": {medium, "Return an error instead, and let main decide. Exiting here skips deferred cleanup and shutdown."},
}

// frameworkServers maps framework imports to methods that start a server with no shutdown of their own.
var frameworkServers = map[string]string{
	"github.com/gin-gonic/gin":    "Run",
	"github.com/labstack/echo/v4": "Start",
	"github.com/gofiber/fiber/v2": "Listen",
	"github.com/gofiber/fiber/v3": "Listen",
	"google.golang.org/grpc":      "Serve",
}

// handlerContextTypes are parameter types that mark a function as a request handler.
var handlerContextTypes = map[string]bool{
	"net/http.Request":                    true,
	"github.com/gin-gonic/gin.Context":    true,
	"github.com/labstack/echo/v4.Context": true,
	"github.com/gofiber/fiber/v2.Ctx":     true,
	"github.com/gofiber/fiber/v3.Ctx":     true,
}

var skipDirs = map[string]bool{"vendor": true, "testdata": true, "node_modules": true}

type position struct {
	file string
	line int
}

// facts are gathered across the whole scan, because shutdown or pool settings often live in another file.
type facts struct {
	serverStarts []position
	hasShutdown  bool
	hasSignal    bool
	bodyLimited  bool
	sqlOpens     []position
	setMaxOpen   bool
	setLifetime  bool
	bodyReads    []position
}

type scanner struct {
	fset    *token.FileSet
	report  *Report
	facts   facts
	ignores map[position]map[string]bool
}

func (s *scanner) add(id string, at position, message string) {
	if s.ignores[at][id] {
		s.report.Ignored++
		return
	}
	definition := rules[id]
	s.report.Findings = append(s.report.Findings, Finding{ID: id, Severity: definition.severity, File: at.file, Line: at.line, Message: message, Fix: definition.fix})
}

func (s *scanner) pos(file string, node token.Pos) position {
	return position{file, s.fset.Position(node).Line}
}

func importNames(file *ast.File) map[string]string {
	names := map[string]string{}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if strings.HasPrefix(name, "v") && len(name) > 1 && strings.Trim(name[1:], "0123456789") == "" {
			trimmed := strings.TrimSuffix(path, "/"+name)
			name = trimmed[strings.LastIndex(trimmed, "/")+1:]
		}
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name != "_" && name != "." {
			names[name] = path
		}
	}
	return names
}

// pkgCall returns the import path and name for a selector like http.Get.
func pkgCall(names map[string]string, expr ast.Expr) (string, string) {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", ""
	}
	ident, ok := selector.X.(*ast.Ident)
	if !ok {
		return "", ""
	}
	return names[ident.Name], selector.Sel.Name
}

func methodName(expr ast.Expr) string {
	if selector, ok := expr.(*ast.SelectorExpr); ok {
		return selector.Sel.Name
	}
	return ""
}

func literalFields(literal *ast.CompositeLit) map[string]bool {
	fields := map[string]bool{}
	for _, element := range literal.Elts {
		if pair, ok := element.(*ast.KeyValueExpr); ok {
			if key, ok := pair.Key.(*ast.Ident); ok {
				fields[key.Name] = true
			}
		}
	}
	return fields
}

// assignedFields finds fields set after a literal, like srv.ReadTimeout = time.Second.
func assignedFields(file *ast.File) map[string]bool {
	fields := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		if assign, ok := node.(*ast.AssignStmt); ok {
			for _, target := range assign.Lhs {
				if selector, ok := target.(*ast.SelectorExpr); ok {
					fields[selector.Sel.Name] = true
				}
			}
		}
		return true
	})
	return fields
}

// handlerParam returns the name of a function's request or framework context parameter.
func handlerParam(names map[string]string, params *ast.FieldList) string {
	if params == nil {
		return ""
	}
	for _, field := range params.List {
		kind := field.Type
		if star, ok := kind.(*ast.StarExpr); ok {
			kind = star.X
		}
		path, name := pkgCall(names, kind)
		if path != "" && handlerContextTypes[path+"."+name] && len(field.Names) > 0 {
			return field.Names[0].Name
		}
	}
	return ""
}

func usesIdent(node ast.Node, name string) bool {
	found := false
	ast.Inspect(node, func(inner ast.Node) bool {
		if ident, ok := inner.(*ast.Ident); ok && ident.Name == name {
			found = true
		}
		return !found
	})
	return found
}

func isTerminating(path, name string) bool {
	return (path == "log" && (name == "Fatal" || name == "Fatalf" || name == "Fatalln")) || (path == "os" && name == "Exit")
}

// exitRanges finds functions whose purpose is to exit, like a logger's Fatal method.
func exitRanges(file *ast.File) [][2]token.Pos {
	var ranges [][2]token.Pos
	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok {
			name := function.Name.Name
			if strings.HasPrefix(name, "Fatal") || strings.HasPrefix(name, "Exit") || strings.HasPrefix(name, "Must") {
				ranges = append(ranges, [2]token.Pos{function.Pos(), function.End()})
			}
		}
	}
	return ranges
}

func within(ranges [][2]token.Pos, at token.Pos) bool {
	for _, item := range ranges {
		if at >= item[0] && at < item[1] {
			return true
		}
	}
	return false
}

func (s *scanner) scanFile(path string, file *ast.File) {
	names := importNames(file)
	exits := exitRanges(file)
	assigned := assignedFields(file)
	isMain := file.Name.Name == "main"
	frameworkMethods := map[string]bool{}
	for _, imported := range names {
		if method, ok := frameworkServers[imported]; ok {
			frameworkMethods[method] = true
		}
	}

	ast.Inspect(file, func(node ast.Node) bool {
		switch current := node.(type) {
		case *ast.CompositeLit:
			s.checkLiteral(path, names, assigned, current)
		case *ast.SelectorExpr:
			if pkg, name := pkgCall(names, current); pkg == "net/http" && name == "DefaultClient" {
				s.add("GS201", s.pos(path, current.Pos()), "http.DefaultClient has no timeout")
			}
		case *ast.CallExpr:
			s.checkCall(path, names, frameworkMethods, isMain || within(exits, current.Pos()), current)
		case *ast.FuncDecl:
			if current.Body != nil {
				s.checkHandler(path, names, current.Type.Params, current.Body)
			}
		case *ast.FuncLit:
			s.checkHandler(path, names, current.Type.Params, current.Body)
		}
		return true
	})
}

func (s *scanner) checkLiteral(path string, names map[string]string, assigned map[string]bool, literal *ast.CompositeLit) {
	pkg, name := pkgCall(names, literal.Type)
	if pkg != "net/http" {
		return
	}
	fields := literalFields(literal)
	has := func(field string) bool { return fields[field] || assigned[field] }
	at := s.pos(path, literal.Pos())
	switch name {
	case "Server":
		if !has("ReadHeaderTimeout") && !has("ReadTimeout") {
			s.add("GS102", at, "http.Server has no ReadHeaderTimeout or ReadTimeout")
		}
		var missing []string
		for _, field := range []string{"ReadTimeout", "WriteTimeout", "IdleTimeout"} {
			if !has(field) {
				missing = append(missing, field)
			}
		}
		if len(missing) > 0 {
			s.add("GS103", at, "http.Server doesn't set "+strings.Join(missing, ", "))
		}
	case "Client":
		if !has("Timeout") {
			s.add("GS202", at, "http.Client has no Timeout")
		}
	}
}

func (s *scanner) checkCall(path string, names map[string]string, frameworkMethods map[string]bool, mayExit bool, call *ast.CallExpr) {
	at := s.pos(path, call.Pos())
	pkg, name := pkgCall(names, call.Fun)
	switch {
	case pkg == "net/http" && (name == "ListenAndServe" || name == "ListenAndServeTLS" || name == "Serve" || name == "ServeTLS"):
		s.add("GS101", at, "http."+name+" has no timeouts and can't shut down gracefully")
		s.facts.serverStarts = append(s.facts.serverStarts, at)
	case pkg == "net/http" && (name == "Get" || name == "Post" || name == "PostForm" || name == "Head"):
		s.add("GS201", at, "http."+name+" uses the default client, which has no timeout")
	case pkg == "net/http" && name == "MaxBytesReader":
		s.facts.bodyLimited = true
	case pkg == "database/sql" && (name == "Open" || name == "OpenDB"):
		s.facts.sqlOpens = append(s.facts.sqlOpens, at)
	case pkg == "os/signal" && (name == "Notify" || name == "NotifyContext"):
		s.facts.hasSignal = true
	case pkg == "io" && name == "LimitReader":
		s.facts.bodyLimited = true
	case isTerminating(pkg, name) && !mayExit:
		s.add("GS601", at, pkg+"."+name+" in a library package exits the whole process")
	case pkg == "":
		method := methodName(call.Fun)
		switch {
		case method == "ListenAndServe" || method == "ListenAndServeTLS":
			s.facts.serverStarts = append(s.facts.serverStarts, at)
		case frameworkMethods[method]:
			s.facts.serverStarts = append(s.facts.serverStarts, at)
		case method == "Shutdown" || method == "GracefulStop":
			s.facts.hasShutdown = true
		case method == "SetMaxOpenConns":
			s.facts.setMaxOpen = true
		case method == "SetConnMaxLifetime" || method == "SetConnMaxIdleTime":
			s.facts.setLifetime = true
		}
	}
}

func (s *scanner) checkHandler(path string, names map[string]string, params *ast.FieldList, body *ast.BlockStmt) {
	request := handlerParam(names, params)
	if request == "" {
		return
	}
	ast.Inspect(body, func(node ast.Node) bool {
		switch current := node.(type) {
		case *ast.CallExpr:
			pkg, name := pkgCall(names, current.Fun)
			at := s.pos(path, current.Pos())
			if pkg == "context" && (name == "Background" || name == "TODO") {
				s.add("GS501", at, "context."+name+" inside a request handler ignores the request's cancellation")
			}
			if isBodyRead(names, current, request) {
				s.facts.bodyReads = append(s.facts.bodyReads, at)
			}
		case *ast.GoStmt:
			if usesIdent(current.Call, request) {
				s.add("GS502", s.pos(path, current.Pos()), "this goroutine uses "+request+" after the handler may have returned")
			}
		}
		return true
	})
}

// isBodyRead matches io.ReadAll(r.Body) and json.NewDecoder(r.Body), the common unbounded reads.
func isBodyRead(names map[string]string, call *ast.CallExpr, request string) bool {
	pkg, name := pkgCall(names, call.Fun)
	if !((pkg == "io" || pkg == "io/ioutil") && name == "ReadAll") && !(pkg == "encoding/json" && name == "NewDecoder") {
		return false
	}
	for _, argument := range call.Args {
		if selector, ok := argument.(*ast.SelectorExpr); ok && selector.Sel.Name == "Body" {
			if ident, ok := selector.X.(*ast.Ident); ok && ident.Name == request {
				return true
			}
		}
	}
	return false
}

func (s *scanner) checkAcrossFiles() {
	if s.facts.hasShutdown && s.facts.hasSignal {
		for index := range s.report.Findings {
			finding := &s.report.Findings[index]
			if finding.ID == "GS101" {
				finding.Severity = medium
				finding.Message += ". The scan also found a graceful shutdown, so this may be a debug or metrics server, which still shouldn't be public"
			}
		}
	}
	if len(s.facts.serverStarts) > 0 {
		first := s.facts.serverStarts[0]
		if !s.facts.hasShutdown {
			s.add("GS301", first, "a server starts here, and nothing in this scan calls Shutdown or GracefulStop")
		}
		if !s.facts.hasSignal {
			s.add("GS302", first, "a server starts here, and nothing in this scan listens for SIGTERM")
		}
	}
	if !s.facts.bodyLimited {
		for _, at := range s.facts.bodyReads {
			s.add("GS503", at, "reads the whole request body, and nothing in this scan limits body size")
		}
	}
	for _, at := range s.facts.sqlOpens {
		if !s.facts.setMaxOpen {
			s.add("GS401", at, "sql.Open here, and nothing in this scan calls SetMaxOpenConns")
		}
		if !s.facts.setLifetime {
			s.add("GS402", at, "sql.Open here, and nothing in this scan sets a connection lifetime")
		}
	}
}

// readIgnores finds `//gosturdy:ignore GS101 reason` comments, which apply to their line and the next.
func (s *scanner) readIgnores(path string, file *ast.File) {
	for _, group := range file.Comments {
		for _, comment := range group.List {
			fields := strings.Fields(strings.TrimPrefix(comment.Text, "//"))
			if len(fields) < 2 || fields[0] != "gosturdy:ignore" {
				continue
			}
			line := s.fset.Position(comment.Pos()).Line
			for _, at := range []position{{path, line}, {path, line + 1}} {
				if s.ignores[at] == nil {
					s.ignores[at] = map[string]bool{}
				}
				s.ignores[at][fields[1]] = true
			}
		}
	}
}

func isGenerated(file *ast.File) bool {
	for _, group := range file.Comments {
		if group.Pos() > file.Package {
			return false
		}
		for _, comment := range group.List {
			if strings.HasPrefix(comment.Text, "// Code generated") && strings.HasSuffix(comment.Text, "DO NOT EDIT.") {
				return true
			}
		}
	}
	return false
}

// Scan parses every non-test Go file under root.
func Scan(root string) (*Report, error) {
	if info, err := os.Stat(root); err != nil {
		return nil, err
	} else if !info.IsDir() {
		return nil, fmt.Errorf("%s isn't a folder", root)
	}
	report := &Report{Root: root, Findings: []Finding{}, ParseErrors: []string{}}
	s := &scanner{fset: token.NewFileSet(), report: report, ignores: map[position]map[string]bool{}}
	parsed := map[string]*ast.File{}
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			report.ParseErrors = append(report.ParseErrors, walkErr.Error())
			return nil
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != root && (skipDirs[name] || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(s.fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			report.ParseErrors = append(report.ParseErrors, parseErr.Error())
			return nil
		}
		if isGenerated(file) {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			relative = path
		}
		relative = filepath.ToSlash(relative)
		parsed[relative] = file
		paths = append(paths, relative)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	report.FilesParsed = len(paths)
	for _, path := range paths {
		s.readIgnores(path, parsed[path])
	}
	for _, path := range paths {
		s.scanFile(path, parsed[path])
	}
	s.checkAcrossFiles()
	sortFindings(report.Findings)
	return report, nil
}

func severityRank(severity string) int {
	switch severity {
	case high:
		return 0
	case medium:
		return 1
	}
	return 2
}

func sortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if severityRank(a.Severity) != severityRank(b.Severity) {
			return severityRank(a.Severity) < severityRank(b.Severity)
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.ID < b.ID
	})
}

func writeText(out io.Writer, report *Report) {
	fmt.Fprintf(out, "%s: %d findings in %d Go files (tests and generated code skipped)\n", report.Root, len(report.Findings), report.FilesParsed)
	for _, finding := range report.Findings {
		fmt.Fprintf(out, "\n%-6s %s %s:%d\n  %s\n  Fix: %s\n", finding.Severity, finding.ID, finding.File, finding.Line, finding.Message, finding.Fix)
	}
	if report.Ignored > 0 {
		fmt.Fprintf(out, "\n%d findings ignored by //gosturdy:ignore comments\n", report.Ignored)
	}
	for _, problem := range report.ParseErrors {
		fmt.Fprintf(out, "\nCOULDN'T PARSE %s\n", problem)
	}
	fmt.Fprintln(out, "\nThese come from the code's shape, without running it. Check each one, because a wrapper or framework may already handle it.")
}

func writeMarkdown(out io.Writer, report *Report) {
	fmt.Fprintf(out, "## gosturdy: %d findings in %d Go files\n\n| Severity | Rule | Where | Finding | Fix |\n|---|---|---|---|---|\n", len(report.Findings), report.FilesParsed)
	for _, finding := range report.Findings {
		fmt.Fprintf(out, "| %s | %s | `%s:%d` | %s | %s |\n", finding.Severity, finding.ID, finding.File, finding.Line, finding.Message, finding.Fix)
	}
	for _, problem := range report.ParseErrors {
		fmt.Fprintf(out, "\n**Couldn't parse** %s\n", problem)
	}
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("gosturdy", flag.ContinueOnError)
	flags.SetOutput(stderr)
	format := flags.String("format", "text", "text, markdown or json")
	failOn := flags.String("fail-on", "high", "exit 1 at this severity or above: high, medium, low or none")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if *format != "text" && *format != "markdown" && *format != "json" {
		fmt.Fprintln(stderr, "gosturdy: --format must be text, markdown or json")
		return exitUsage
	}
	threshold, ok := map[string]int{"high": 0, "medium": 1, "low": 2, "none": -1}[*failOn]
	if !ok {
		fmt.Fprintln(stderr, "gosturdy: --fail-on must be high, medium, low or none")
		return exitUsage
	}
	root := "."
	if flags.NArg() > 1 {
		fmt.Fprintln(stderr, "gosturdy: give one folder at most")
		return exitUsage
	}
	if flags.NArg() == 1 {
		root = flags.Arg(0)
	}
	report, err := Scan(root)
	if err != nil {
		fmt.Fprintf(stderr, "gosturdy: %v\n", err)
		return exitUsage
	}
	switch *format {
	case "json":
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fmt.Fprintf(stderr, "gosturdy: %v\n", err)
			return exitUsage
		}
	case "markdown":
		writeMarkdown(stdout, report)
	default:
		writeText(stdout, report)
	}
	if report.FilesParsed == 0 {
		fmt.Fprintln(stderr, "gosturdy: no Go files found, so nothing was checked")
		return exitFindings
	}
	if len(report.ParseErrors) > 0 {
		return exitFindings
	}
	for _, finding := range report.Findings {
		if severityRank(finding.Severity) <= threshold {
			return exitFindings
		}
	}
	return exitOK
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
