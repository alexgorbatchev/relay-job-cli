package main

import (
	"context"
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

	cobrahelptree "github.com/alexgorbatchev/cobra-help-tree/v3"
	"github.com/alexgorbatchev/relay-jobs-cli/internal/display"
	"github.com/alexgorbatchev/relay-jobs-cli/internal/job"
)

// resolvedPaths runs configure for a command line the way execution does and
// returns the paths the command would use.
func resolvedPaths(t *testing.T, args ...string) job.Paths {
	t.Helper()
	root := newRootCommand()
	cmd, rest, err := root.Find(args)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Flags().AddFlagSet(root.PersistentFlags())
	if err = cmd.ParseFlags(rest); err != nil {
		t.Fatal(err)
	}
	o := &options{paths: job.Paths{Config: job.DefaultPaths(job.CoordinatorRole).Config}}
	if err = o.configure(cmd); err != nil {
		t.Fatal(err)
	}
	return o.paths
}

// flagLine returns the help line describing flag. A wide COLUMNS keeps pflag
// from wrapping it.
func flagLine(t *testing.T, help, flag string) string {
	t.Helper()
	for _, line := range strings.Split(help, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), flag+" ") {
			return line
		}
	}
	t.Fatalf("help has no %s line:\n%s", flag, help)
	return ""
}

func TestHelpStatesResolvedPathDefaults(t *testing.T) {
	for _, xdg := range []bool{false, true} {
		t.Run(map[bool]string{false: "home", true: "xdg"}[xdg], func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("COLUMNS", "1000")
			for _, name := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
				t.Setenv(name, "")
				if xdg {
					t.Setenv(name, filepath.Join(home, strings.ToLower(name)))
				}
			}
			for _, tc := range []struct {
				args []string
				// Commands whose role is fixed register the resolved paths as
				// flag defaults; service install describes both roles in prose.
				static, paths bool
			}{
				{args: []string{"coordinator", "serve"}, static: true, paths: true},
				{args: []string{"worker", "serve"}, static: true, paths: true},
				{args: []string{"service", "install", "--role", "coordinator"}, paths: true},
				{args: []string{"service", "install", "--role", "worker"}, paths: true},
				{args: []string{"job", "list"}},
				{args: []string{"worker", "list"}},
			} {
				got := resolvedPaths(t, tc.args...)
				for _, mode := range []string{"0", "1"} {
					t.Setenv("AGENT", mode)
					help, stderr, code := execute(context.Background(), append(tc.args, "--help")...)
					if code != 0 || stderr != "" {
						t.Fatalf("%v --help: exit %d stderr %q", tc.args, code, stderr)
					}
					want := map[string]string{"--socket": got.Socket}
					if tc.paths {
						want["--state"], want["--cache"] = got.State, got.Cache
					}
					for flag, path := range want {
						line := flagLine(t, help, flag)
						if !strings.Contains(line, display.Path(path)) {
							t.Errorf("AGENT=%s %v: %s help does not state the resolved %s:\n%s", mode, tc.args, flag, display.Path(path), line)
						}
						// The socket and service install paths depend on runtime
						// input, so they register no default for pflag to print.
						registered := tc.static && flag != "--socket"
						if strings.Contains(line, "(default") != registered || registered && !strings.Contains(line, `"`+display.Path(path)+`"`) {
							t.Errorf("AGENT=%s %v: %s registered default should be %v:\n%s", mode, tc.args, flag, registered, line)
						}
					}
				}
			}
		})
	}
}

func TestHelpListsCatalogEnvironmentVariables(t *testing.T) {
	t.Setenv("COLUMNS", "1000")
	var checked int
	for path, info := range techCatalog {
		if len(info.Env) == 0 {
			continue
		}
		args := strings.Fields(path)[1:]
		for _, mode := range []string{"0", "1"} {
			t.Setenv("AGENT", mode)
			help, stderr, code := execute(context.Background(), append(args, "--help")...)
			if code != 0 || stderr != "" {
				t.Fatalf("%s --help: exit %d stderr %q", path, code, stderr)
			}
			heading := "\nEnvironment Variables:\n"
			if mode == "1" {
				heading = "\nenv:\n"
			}
			_, block, ok := strings.Cut(help, heading)
			if !ok {
				t.Fatalf("AGENT=%s %s help has no environment block:\n%s", mode, path, help)
			}
			block, _, _ = strings.Cut(block, "\n\n")
			for _, env := range info.Env {
				row := regexp.MustCompile(`(?m)^  (- )?` + env.Name + `:?\s+` + regexp.QuoteMeta(env.Description) + `$`)
				if !row.MatchString(block) {
					t.Errorf("AGENT=%s %s help omits %s:\n%s", mode, path, env.Name, block)
				}
			}
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("catalog declares no environment variables")
	}
}

// catalogEnvironment is every environment variable name the help catalog declares.
func catalogEnvironment() []string {
	var names []string
	for _, info := range techCatalog {
		for _, env := range info.Env {
			if !slices.Contains(names, env.Name) {
				names = append(names, env.Name)
			}
		}
	}
	slices.Sort(names)
	return names
}

var xdgName = regexp.MustCompile(`^"XDG_[A-Z_]+"$`)

func TestCatalogDeclaresEveryEnvironmentRead(t *testing.T) {
	declared := catalogEnvironment()
	reads := map[string]string{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join("..", ".."), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != filepath.Join("..", "..") && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		// Environment names are read with os.Getenv or os.LookupEnv, or, in
		// DefaultPaths, passed as XDG literals to a helper that reads them.
		ast.Inspect(file, func(n ast.Node) bool {
			var lit *ast.BasicLit
			switch n := n.(type) {
			case *ast.CallExpr:
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "Getenv" && sel.Sel.Name != "LookupEnv") || len(n.Args) == 0 {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "os" {
					return true
				}
				if lit, ok = n.Args[0].(*ast.BasicLit); !ok {
					// A helper's parameter; its callers' literals are scanned.
					return true
				}
			case *ast.BasicLit:
				if !xdgName.MatchString(n.Value) {
					return true
				}
				lit = n
			default:
				return true
			}
			name, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			reads[name] = fset.Position(lit.Pos()).String()
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reads) == 0 {
		t.Fatal("found no environment reads")
	}
	for name, at := range reads {
		if !slices.Contains(declared, name) {
			t.Errorf("%s reads %s, which the help catalog does not declare", at, name)
		}
	}
}

func TestSkillCheckCoversEnvironmentCatalog(t *testing.T) {
	skill := readSkill(t)
	for _, name := range catalogEnvironment() {
		without := regexp.MustCompile(`\b`+name+`\b`).ReplaceAllString(skill, "")
		missing := cobrahelptree.SkillOmissions(newRootCommand(), techCatalog, without)
		if !slices.Contains(missing, "environment variable "+name) {
			t.Errorf("SkillOmissions does not report %s missing from SKILL.md: %v", name, missing)
		}
	}
}
