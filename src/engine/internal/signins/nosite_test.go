package signins

import (
	goscanner "go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// No code of the engine names a site (CLAUDE.md, Hard conventions): a site is data, a card the owner
// keeps through the API. The names are those of the sites the box has had cards for
// (testdata/site-names.txt: each name, its host with and without www.); what is read is every
// identifier and literal of the engine's source, tests left out. Comments are the record of what each
// piece was learned on, and nothing runs or says them. The worker's half is
// src/browser/tests/test_no_site_named.py, the console's src/ui/src/test/words.test.ts.
func TestNoCodeNamesASite(t *testing.T) {
	names := siteNames(t)

	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files := token.NewFileSet()
		var s goscanner.Scanner
		s.Init(files.AddFile(path, files.Base(), len(src)), src, nil, 0) // mode 0: comments skipped
		for {
			pos, tok, lit := s.Scan()
			if tok == token.EOF {
				break
			}
			if tok != token.IDENT && tok != token.STRING && tok != token.CHAR {
				continue
			}
			for _, name := range names {
				if strings.Contains(strings.ToLower(lit), name) {
					t.Errorf("%s names %q: %s", files.Position(pos), name, lit)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// siteNames is testdata/site-names.txt's names and hosts (with and without www.), lower case.
func siteNames(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "testdata", "site-names.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.HasPrefix(line, "#") {
			continue
		}
		host := strings.ToLower(fields[len(fields)-1])
		names = append(names, strings.ToLower(strings.Join(fields[:len(fields)-1], " ")), host, strings.TrimPrefix(host, "www."))
	}
	if len(names) == 0 {
		t.Fatal("no site names in testdata/site-names.txt")
	}
	return names
}
