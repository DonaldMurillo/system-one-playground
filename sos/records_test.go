package sos

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNamedRecordConstructionAndFieldAccess(t *testing.T) {
	source := `define User:
  name as text
  age as optional integer
  active as boolean

make donald as User with:
  name from "Donald"
  active from true
to greet with user as User using name:
  return "Hello {name}"
call greet with donald called message
show name of donald
show donald.name
show age of donald
show message
`
	p, diagnostics := Parse(source)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if diagnostics = Check(source); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	result, err := Run(context.Background(), p, Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Variables["message"] != "Hello Donald" {
		t.Fatalf("message = %#v", result.Variables["message"])
	}
	if result.Variables["donald"].(map[string]any)["age"] != nil {
		t.Fatalf("optional field should remain omitted: %#v", result.Variables["donald"])
	}
}

func TestNamedRecordValidation(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   string
	}{
		{"missing", "define User:\n  name as text\n  age as optional integer\nmake u as User with:\n  age from null\n", "requires field name"},
		{"unknown", "define User:\n  name as text\nmake u as User with:\n  nope from \"x\"\n  name from \"x\"\n", "has no field nope"},
		{"duplicate", "define User:\n  name as text\nmake u as User with:\n  name from \"a\"\n  name from \"b\"\n", "appears more than once"},
		{"wrong", "define User:\n  name as text\nmake u as User with:\n  name from 4\n", "must be text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, diagnostics := Parse(tc.source)
			if len(diagnostics) != 0 {
				t.Fatal(diagnostics)
			}
			_, err := Run(context.Background(), p, Options{Dir: t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestNamedRecordJSONBoundaryAndClosedShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.sos")
	source := `define User:
  name as text
  age as optional integer
to greet with user as User:
  return name of user
read "user.json" as json called raw
call greet with raw called result
`
	if err := writeTestFile(filepath.Join(dir, "user.json"), `{"name":"Ada"}`); err != nil {
		t.Fatal(err)
	}
	p, diagnostics := LoadProgram(path, source)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	result, err := Run(context.Background(), p, Options{Dir: dir})
	if err != nil || result.Variables["result"] != "Ada" {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if err := writeTestFile(filepath.Join(dir, "user.json"), `{"name":"Ada","extra":true}`); err != nil {
		t.Fatal(err)
	}
	_, err = Run(context.Background(), p, Options{Dir: dir})
	if err == nil || !strings.Contains(err.Error(), "received record") {
		t.Fatalf("closed record error = %v", err)
	}
}

func TestNamedRecordDefinitionsRejectRecursion(t *testing.T) {
	for _, source := range []string{
		"define Node:\n  next as Node\n",
		"define Node:\n  next as optional Node\n",
		"define Node:\n  next as list of Node\n",
	} {
		if diagnostics := Check(source); len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "recursive definition") {
			t.Fatalf("diagnostics = %+v", diagnostics)
		}
	}
}

func TestNamedRecordCheckerRejectsKnownFieldTypos(t *testing.T) {
	source := "define User:\n  name as text\nmake user as User with:\n  name from \"Ada\"\nshow nickname of user\n"
	diagnostics := Check(source)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "User has no field nickname") {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}

func TestNamedRecordCheckerRejectsKnownRecordArgumentMismatch(t *testing.T) {
	source := `define User:
  name as text
define Account:
  id as text
make user as User with:
  name from "Ada"
to inspect with account as Account:
  return id of account
call inspect with user called result
`
	diagnostics := Check(source)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "inspect.account must be Account; received User") {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}

func TestNamedRecordPackageExportAndTypedCall(t *testing.T) {
	dir := t.TempDir()
	people := filepath.Join(dir, "people")
	if err := os.MkdirAll(people, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(filepath.Join(people, "people.sos"), `package people
export User
export greet
define User:
  name as text
to greet with user as User:
  return name of user
`); err != nil {
		t.Fatal(err)
	}
	source := `import "./people" as people
make user as User with:
  name from "Ada"
call people.greet with user called result
`
	p, diagnostics := LoadProgram(filepath.Join(dir, "main.sos"), source)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	result, err := Run(context.Background(), p, Options{Dir: dir})
	if err != nil || result.Variables["result"] != "Ada" {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestNamedRecordCheckerRejectsImportedActionArgumentMismatch(t *testing.T) {
	dir := t.TempDir()
	people := filepath.Join(dir, "people")
	if err := os.MkdirAll(people, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(filepath.Join(people, "people.sos"), `package people
export Account
export inspect
define Account:
  id as text
to inspect with account as Account:
  return id of account
`); err != nil {
		t.Fatal(err)
	}
	source := `import "./people" as people
define User:
  name as text
make user as User with:
  name from "Ada"
call people.inspect with user called result
`
	_, diagnostics := LoadProgram(filepath.Join(dir, "main.sos"), source)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "people.inspect.account must be Account; received User") {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}

func TestNamedRecordNestedAndListTypes(t *testing.T) {
	source := `define Address:
  city as text
define User:
  name as text
  address as Address
  friends as list of Address
make user as User with:
  name from "Ada"
  address from {city: "London"}
  friends from [{city: "Paris"}]
show city of address of user
`
	p, diagnostics := Parse(source)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if diagnostics = Check(source); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	result, err := Run(context.Background(), p, Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Variables["user"].(map[string]any)["name"] != "Ada" {
		t.Fatalf("user = %#v", result.Variables["user"])
	}
}

func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
