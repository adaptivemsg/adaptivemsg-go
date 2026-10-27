package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func renderTestStruct(t *testing.T, src string) string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "message.go", "package hello\n\n"+src, 0)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	defs := collectTypeDefs(file)
	structNames := map[string]bool{}
	for _, def := range defs {
		if _, ok := def.spec.Type.(*ast.StructType); ok {
			structNames[def.name] = true
		}
	}
	out, err := renderType(defs[0], structNames)
	if err != nil {
		t.Fatalf("renderType: %v", err)
	}
	return out.text
}

// lib.rs declares every generated file in the module, whichever package was
// generated last, and each #[path] is relative to the enclosing modules.
func TestLibRSDeclaresAllGeneratedFiles(t *testing.T) {
	root := t.TempDir()
	writeFile := func(rel, contents string) {
		t.Helper()
		file := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	generated := generatedHeader + "\nuse adaptivemsg as am;\n"
	writeFile("go.mod", "module example.com/svc\n")
	writeFile("lib.rs", generatedHeader+"\npub mod api;\n")
	writeFile("api/hello/message.rs", generated)
	writeFile("api/echo/message.rs", generated)
	writeFile("api/admin/users/message.rs", generated)
	writeFile("top/message.rs", generated)
	writeFile("api/hand/written.rs", "pub struct Kept;\n")
	writeFile("other/go.mod", "module example.com/other\n")
	writeFile("other/api/message.rs", generated)

	files, err := generatedRustFiles(root)
	if err != nil {
		t.Fatalf("generatedRustFiles: %v", err)
	}
	libRS, err := renderLibRS(files)
	if err != nil {
		t.Fatalf("renderLibRS: %v", err)
	}
	want := generatedHeader + `
pub mod api {
    pub mod admin {
        #[path = "users/message.rs"]
        pub mod users;
    }
    #[path = "echo/message.rs"]
    pub mod echo;
    #[path = "hello/message.rs"]
    pub mod hello;
}
#[path = "top/message.rs"]
pub mod top;
`
	if libRS != want {
		t.Fatalf("lib.rs:\n%s\nwant:\n%s", libRS, want)
	}

	if _, err := renderLibRS([]string{"api/hello/message.rs", "api/hello/types.rs"}); err == nil {
		t.Fatal("two generated files in one directory must be rejected")
	}
}

// A WireName override reaches the Rust type, which must use the same name.
func TestWireNameOverrideCarriesToRust(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "message.go", "package hello\n\n"+
		"const echoWire = \"svc.echo.Request\"\n"+
		"type Ping struct {\n\tText string `am:\"text\"`\n}\n"+
		"func (*Ping) WireName() string { return \"am.hello.Ping2\" }\n"+
		"type Echo struct {\n\tText string `am:\"text\"`\n}\n"+
		"func (Echo) WireName() string { return echoWire }\n", 0)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	wires, err := wireNameOverrides(file)
	if err != nil {
		t.Fatalf("wireNameOverrides: %v", err)
	}
	defs := collectTypeDefs(file)
	structNames := map[string]bool{"Ping": true, "Echo": true}
	for i, want := range []string{
		"#[am::message(name = \"hello.Ping2\")]\npub struct Ping {",
		"#[am::message(ns = \"svc\", name = \"echo.Request\")]\npub struct Echo {",
	} {
		defs[i].wire = wires[defs[i].name]
		out, err := renderType(defs[i], structNames)
		if err != nil {
			t.Fatalf("renderType %s: %v", defs[i].name, err)
		}
		if !strings.HasPrefix(out.text, want) {
			t.Errorf("%s rendered as:\n%s\nwant prefix:\n%s", defs[i].name, out.text, want)
		}
	}

	computed, err := parser.ParseFile(token.NewFileSet(), "message.go", "package hello\n\n"+
		"type X struct{}\n"+
		"func (*X) WireName() string { return \"am.\" + \"x\" }\n", 0)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if _, err := wireNameOverrides(computed); err == nil {
		t.Fatal("a computed wire name must be rejected")
	}
}

// Go encodes []byte as msgpack bin; serde encodes Vec<u8> as an integer array
// unless told otherwise, and rejects bin when decoding.
func TestRenderByteSlicesAsSerdeBytes(t *testing.T) {
	text := renderTestStruct(t, "type Blob struct {\n"+
		"\tData []byte `am:\"data\"`\n"+
		"\tRaw []uint8 `am:\"raw\"`\n"+
		"\tMaybe *[]byte `am:\"maybe\"`\n"+
		"\tChunks [][]byte `am:\"chunks\"`\n"+
		"\tByName map[string][]byte `am:\"by_name\"`\n"+
		"}\n")
	for _, want := range []string{
		"    #[serde(with = \"am::__private::serde_bytes\")]\n    pub data: Vec<u8>,\n",
		"    #[serde(with = \"am::__private::serde_bytes\")]\n    pub raw: Vec<u8>,\n",
		"    #[serde(with = \"am::__private::serde_bytes\")]\n    pub maybe: Option<Vec<u8>>,\n",
		"    pub chunks: Vec<am::__private::serde_bytes::ByteBuf>,\n",
		"    pub by_name: BTreeMap<String, am::__private::serde_bytes::ByteBuf>,\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
}
