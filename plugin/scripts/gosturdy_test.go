package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, source := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func ids(t *testing.T, source string) map[string]bool {
	t.Helper()
	report, err := Scan(write(t, map[string]string{"main.go": source}))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.ParseErrors) > 0 {
		t.Fatal(report.ParseErrors)
	}
	found := map[string]bool{}
	for _, finding := range report.Findings {
		found[finding.ID] = true
	}
	return found
}

func TestRules(t *testing.T) {
	cases := []struct {
		name, source string
		want, not    []string
	}{
		{"listen and serve", `package main
import "net/http"
func main() { http.ListenAndServe(":1", nil) }`, []string{"GS101", "GS301", "GS302"}, nil},
		{"aliased import", `package main
import web "net/http"
func main() { web.ListenAndServe(":1", nil) }`, []string{"GS101"}, nil},
		{"another package named http", `package main
import "example.com/http"
func main() { http.ListenAndServe(":1", nil); http.Get("x") }`, nil, []string{"GS101", "GS201"}},
		{"server with no timeouts", `package main
import "net/http"
var s = &http.Server{Addr: ":1"}`, []string{"GS102", "GS103"}, nil},
		{"server with all timeouts", `package main
import ("net/http"; "time")
var s = &http.Server{ReadHeaderTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second}`, nil, []string{"GS102", "GS103"}},
		{"timeouts assigned later", `package main
import ("net/http"; "time")
func f() { s := &http.Server{}; s.ReadHeaderTimeout = time.Second; _ = s }`, []string{"GS103"}, []string{"GS102"}},
		{"default client calls", `package main
import "net/http"
func f() { http.Get("x") }`, []string{"GS201"}, nil},
		{"default client value", `package main
import "net/http"
var c = http.DefaultClient`, []string{"GS201"}, nil},
		{"client with no timeout", `package main
import "net/http"
var c = &http.Client{}`, []string{"GS202"}, nil},
		{"client with timeout", `package main
import ("net/http"; "time")
var c = &http.Client{Timeout: time.Second}`, nil, []string{"GS202"}},
		{"graceful server", `package main
import ("context"; "net/http"; "os/signal"; "os")
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	s := &http.Server{}
	go s.ListenAndServe()
	<-ctx.Done()
	s.Shutdown(context.Background())
}`, nil, []string{"GS101", "GS301", "GS302"}},
		{"method start with no shutdown", `package main
import "net/http"
func main() { s := &http.Server{}; s.ListenAndServe() }`, []string{"GS301", "GS302"}, []string{"GS101"}},
		{"gin run", `package main
import "github.com/gin-gonic/gin"
func main() { r := gin.Default(); r.Run() }`, []string{"GS301", "GS302"}, nil},
		{"run method without a framework", `package main
type job struct{}
func (job) Run() {}
func main() { job{}.Run() }`, nil, []string{"GS301"}},
		{"grpc with graceful stop", `package main
import ("google.golang.org/grpc"; "os/signal"; "os")
func main() { s := grpc.NewServer(); signal.Notify(nil, os.Interrupt); s.GracefulStop(); s.Serve(nil) }`, nil, []string{"GS301", "GS302"}},
		{"sql with no limits", `package main
import "database/sql"
func f() { sql.Open("x", "y") }`, []string{"GS401", "GS402"}, nil},
		{"sql with limits", `package main
import "database/sql"
func f() { db, _ := sql.Open("x", "y"); db.SetMaxOpenConns(5); db.SetConnMaxLifetime(9) }`, nil, []string{"GS401", "GS402"}},
		{"background in handler", `package main
import ("context"; "net/http")
func h(w http.ResponseWriter, r *http.Request) { _ = context.Background() }`, []string{"GS501"}, nil},
		{"background outside handler", `package main
import "context"
func f() { _ = context.Background() }`, nil, []string{"GS501"}},
		{"gin handler", `package main
import ("context"; "github.com/gin-gonic/gin")
func h(c *gin.Context) { _ = context.TODO() }`, []string{"GS501"}, nil},
		{"goroutine uses request", `package main
import "net/http"
func h(w http.ResponseWriter, r *http.Request) { go func() { _ = r.URL }() }`, []string{"GS502"}, nil},
		{"goroutine leaves request alone", `package main
import "net/http"
func h(w http.ResponseWriter, r *http.Request) { go func() {}() }`, nil, []string{"GS502"}},
		{"unbounded body", `package main
import ("io"; "net/http")
func h(w http.ResponseWriter, r *http.Request) { io.ReadAll(r.Body) }`, []string{"GS503"}, nil},
		{"limited body", `package main
import ("io"; "net/http")
func h(w http.ResponseWriter, r *http.Request) { r.Body = http.MaxBytesReader(w, r.Body, 9); io.ReadAll(r.Body) }`, nil, []string{"GS503"}},
		{"exit in a library", `package store
import "log"
func f() { log.Fatal("x") }`, []string{"GS601"}, nil},
		{"exit in a Fatal method", `package logger
import "os"
type L struct{}
func (L) Fatal(m string) { os.Exit(1) }
func MustLoad() { os.Exit(1) }`, nil, []string{"GS601"}},
		{"exit in main", `package main
import "os"
func main() { os.Exit(1) }`, nil, []string{"GS601"}},
		{"ignore comment", `package main
import "net/http"
//gosturdy:ignore GS201 health check with its own deadline
func f() { http.Get("x") }`, nil, []string{"GS201"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			found := ids(t, test.source)
			for _, id := range test.want {
				if !found[id] {
					t.Errorf("missing %s, got %v", id, found)
				}
			}
			for _, id := range test.not {
				if found[id] {
					t.Errorf("unexpected %s", id)
				}
			}
		})
	}
}

func TestBareListenerNextToAGracefulServerIsMedium(t *testing.T) {
	report, err := Scan(write(t, map[string]string{"main.go": `package main
import ("context"; "net/http"; "os"; "os/signal")
func main() {
	go http.ListenAndServe(":6060", nil)
	s := &http.Server{}
	signal.Notify(nil, os.Interrupt)
	s.Shutdown(context.Background())
}`}))
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range report.Findings {
		if finding.ID == "GS101" {
			if finding.Severity != medium || !strings.Contains(finding.Message, "debug or metrics") {
				t.Fatalf("got %+v", finding)
			}
			return
		}
	}
	t.Fatal("GS101 is missing")
}

func TestFactsCrossFiles(t *testing.T) {
	report, err := Scan(write(t, map[string]string{
		"main.go":     "package main\nimport \"net/http\"\nfunc main() { s := &http.Server{}; s.ListenAndServe() }\n",
		"shutdown.go": "package main\nimport (\"context\"; \"net/http\"; \"os\"; \"os/signal\")\nfunc stop(s *http.Server) { signal.Notify(nil, os.Interrupt); s.Shutdown(context.Background()) }\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range report.Findings {
		if finding.ID == "GS301" || finding.ID == "GS302" {
			t.Errorf("shutdown in another file wasn't seen: %+v", finding)
		}
	}
}

func TestSkips(t *testing.T) {
	bad := "package main\nimport \"net/http\"\nfunc f() { http.Get(\"x\") }\n"
	report, err := Scan(write(t, map[string]string{
		"a_test.go":     bad,
		"vendor/a/a.go": bad,
		"testdata/a.go": bad,
		".hidden/a.go":  bad,
		"gen.go":        "// Code generated by tool. DO NOT EDIT.\n\n" + bad,
		"real.go":       "package main\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if report.FilesParsed != 1 || len(report.Findings) != 0 {
		t.Fatalf("got %+v", report)
	}
}

func TestParseErrorIsReported(t *testing.T) {
	root := write(t, map[string]string{"broken.go": "package main\nfunc {", "ok.go": "package main\n"})
	var out, errs bytes.Buffer
	if code := run([]string{root}, &out, &errs); code != exitFindings {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "COULDN'T PARSE") {
		t.Fatal(out.String())
	}
}

func TestExitCodes(t *testing.T) {
	medium := write(t, map[string]string{"a.go": "package main\nimport \"net/http\"\nvar c = &http.Client{}\n"})
	check := func(want int, args ...string) {
		t.Helper()
		var out, errs bytes.Buffer
		if code := run(args, &out, &errs); code != want {
			t.Errorf("%v: exit %d, want %d: %s", args, code, want, errs.String())
		}
	}
	check(exitOK, medium)
	check(exitFindings, "--fail-on", "medium", medium)
	check(exitOK, "--fail-on", "none", medium)
	check(exitUsage, "--fail-on", "soon", medium)
	check(exitUsage, "--format", "xml", medium)
	check(exitUsage, medium, medium)
	check(exitUsage, "/no/such/place")
	check(exitFindings, write(t, map[string]string{"a.txt": "not go"}))
}

func TestOutputFormats(t *testing.T) {
	root := filepath.Join("..", "..", "examples", "badservice")
	var out, errs bytes.Buffer
	if code := run([]string{"--format", "json", root}, &out, &errs); code != exitFindings {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	var report Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Findings[0].Severity != high {
		t.Fatalf("not sorted by severity: %+v", report.Findings[0])
	}
	found := map[string]bool{}
	for _, finding := range report.Findings {
		found[finding.ID] = true
	}
	for _, id := range []string{"GS101", "GS201", "GS301", "GS302", "GS401", "GS402", "GS501", "GS502", "GS503"} {
		if !found[id] {
			t.Errorf("example is missing %s", id)
		}
	}
	out.Reset()
	run([]string{"--format", "markdown", root}, &out, &errs)
	if !strings.Contains(out.String(), "| HIGH | GS101 |") {
		t.Fatal(out.String())
	}
}

func TestEveryRuleHasAFix(t *testing.T) {
	for id, definition := range rules {
		if definition.fix == "" || definition.severity == "" {
			t.Errorf("%s is incomplete", id)
		}
	}
}
