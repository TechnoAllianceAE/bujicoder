package codeintel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewParser(t *testing.T) {
	p := NewParser()
	if p == nil {
		t.Fatal("NewParser should not return nil")
	}
	exts := p.SupportedExtensions()
	if len(exts) == 0 {
		t.Error("should have at least one supported extension")
	}
}

func TestIsSupported(t *testing.T) {
	p := NewParser()
	tests := []struct {
		path string
		want bool
	}{
		{"main.go", true},
		{"app.py", true},
		{"index.ts", true},
		{"app.tsx", true},
		{"main.rs", true},
		{"data.csv", false},
		{"image.png", false},
	}
	for _, tt := range tests {
		got := p.IsSupported(tt.path)
		if got != tt.want {
			t.Errorf("IsSupported(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestExtractGoSymbols(t *testing.T) {
	p := NewParser()
	src := `package main

import "fmt"

const MaxSize = 100

type User struct {
	Name string
	Age  int
}

type Validator interface {
	Validate() error
}

func main() {
	fmt.Println("hello")
}

func (u *User) Save() error {
	return nil
}

func helper(x, y int) int {
	return x + y
}
`
	symbols := p.ExtractSymbols("main.go", []byte(src))

	// Should find: MaxSize (var), User (type), Validator (interface), main (func), Save (method), helper (func)
	if len(symbols) < 5 {
		t.Fatalf("expected at least 5 symbols, got %d: %v", len(symbols), symbols)
	}

	found := map[string]bool{}
	for _, s := range symbols {
		found[s.Name+"/"+s.Kind] = true
	}

	expected := []string{
		"User/type",
		"Validator/interface",
		"main/function",
		"Save/method",
		"helper/function",
	}
	for _, want := range expected {
		if !found[want] {
			t.Errorf("missing symbol %q in %v", want, symbolNames(symbols))
		}
	}
}

func TestExtractGoSymbols_MethodSignature(t *testing.T) {
	p := NewParser()
	src := `package models

type User struct{}

func (u *User) Save(ctx context.Context) error {
	return nil
}
`
	symbols := p.ExtractSymbols("models.go", []byte(src))
	for _, sym := range symbols {
		if sym.Name == "Save" {
			if sym.Kind != "method" {
				t.Errorf("Save should be a method, got %q", sym.Kind)
			}
			if sym.Signature == "" {
				t.Error("Save should have a signature")
			}
			return
		}
	}
	t.Error("Save method not found")
}

func TestExtractPythonSymbols(t *testing.T) {
	p := NewParser()
	src := `
class UserService:
    def __init__(self, db):
        self.db = db

    def get_user(self, user_id):
        return self.db.find(user_id)

def create_app():
    app = Flask(__name__)
    return app

async def fetch_data():
    pass

MAX_RETRIES = 5
`
	symbols := p.ExtractSymbols("app.py", []byte(src))

	if len(symbols) < 3 {
		t.Fatalf("expected at least 3 symbols, got %d", len(symbols))
	}

	found := map[string]bool{}
	for _, s := range symbols {
		found[s.Name] = true
	}

	if !found["UserService"] {
		t.Error("missing UserService class")
	}
	if !found["create_app"] {
		t.Error("missing create_app function")
	}
	if !found["fetch_data"] {
		t.Error("missing fetch_data async function")
	}
}

func TestExtractTSSymbols(t *testing.T) {
	p := NewParser()
	src := `
export interface UserProps {
    name: string;
    age: number;
}

export class UserService {
    constructor(private db: Database) {}

    async findUser(id: string): Promise<User> {
        return this.db.find(id);
    }
}

export function createRouter(): Router {
    return new Router();
}

export const handleRequest = async (req: Request) => {
    return new Response();
};

export type UserID = string;

enum Status {
    Active,
    Inactive,
}
`
	symbols := p.ExtractSymbols("service.ts", []byte(src))

	if len(symbols) < 4 {
		t.Fatalf("expected at least 4 symbols, got %d: %v", len(symbols), symbolNames(symbols))
	}

	found := map[string]bool{}
	for _, s := range symbols {
		found[s.Name] = true
	}

	if !found["UserProps"] {
		t.Error("missing UserProps interface")
	}
	if !found["UserService"] {
		t.Error("missing UserService class")
	}
	if !found["createRouter"] {
		t.Error("missing createRouter function")
	}
}

func TestExtractRustSymbols(t *testing.T) {
	p := NewParser()
	src := `
pub struct Config {
    pub host: String,
    pub port: u16,
}

pub enum Status {
    Active,
    Inactive,
}

pub trait Handler {
    fn handle(&self, req: Request) -> Response;
}

pub fn create_server(config: Config) -> Server {
    Server::new(config)
}

pub async fn run(server: Server) {
    server.start().await;
}

const MAX_CONNECTIONS: usize = 100;
`
	symbols := p.ExtractSymbols("main.rs", []byte(src))

	if len(symbols) < 4 {
		t.Fatalf("expected at least 4 symbols, got %d", len(symbols))
	}

	found := map[string]bool{}
	for _, s := range symbols {
		found[s.Name] = true
	}

	if !found["Config"] {
		t.Error("missing Config struct")
	}
	if !found["Handler"] {
		t.Error("missing Handler trait")
	}
	if !found["create_server"] {
		t.Error("missing create_server function")
	}
}

func TestExtractSymbolsFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte("package main\n\nfunc Hello() {}\n"), 0o644)

	p := NewParser()
	symbols, err := p.ExtractSymbolsFromFile(path)
	if err != nil {
		t.Fatalf("ExtractSymbolsFromFile: %v", err)
	}
	if len(symbols) == 0 {
		t.Error("should find at least one symbol")
	}
}

func TestFormatSymbols(t *testing.T) {
	symbols := []Symbol{
		{Name: "main", Kind: "function", StartLine: 1, EndLine: 5, Signature: "func main()"},
		{Name: "User", Kind: "type", StartLine: 7, EndLine: 10, Signature: "type User struct"},
	}
	result := FormatSymbols(symbols)
	if result == "" {
		t.Error("FormatSymbols should return non-empty string")
	}
	if !contains(result, "function main") {
		t.Error("should contain function main")
	}
	if !contains(result, "type User") {
		t.Error("should contain type User")
	}
}

func TestFormatSymbolsCompact(t *testing.T) {
	symbols := []Symbol{
		{Name: "main", Kind: "function"},
		{Name: "Save", Kind: "method"},
		{Name: "User", Kind: "class"},
	}
	result := FormatSymbolsCompact(symbols)
	if result == "" {
		t.Error("FormatSymbolsCompact should return non-empty string")
	}
	if !contains(result, "func main()") {
		t.Error("should contain func main()")
	}
}

func TestIndexProject(t *testing.T) {
	dir := t.TempDir()

	// Create test files.
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\nfunc helper() {}\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)
	os.WriteFile(filepath.Join(dir, "pkg", "util.go"), []byte("package pkg\n\ntype Config struct{}\n\nfunc New() *Config { return nil }\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "data.csv"), []byte("a,b,c\n"), 0o644) // not supported

	p := NewParser()
	index := p.IndexProject(dir, nil)
	if len(index) != 2 {
		t.Fatalf("expected 2 indexed files, got %d", len(index))
	}

	formatted := FormatIndex(index)
	if formatted == "" {
		t.Error("FormatIndex should return non-empty string")
	}
	if !contains(formatted, "main.go") {
		t.Error("should contain main.go")
	}
}

func TestIndexProject_SpecificFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\n\nfunc B() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "c.go"), []byte("package c\n\nfunc C() {}\n"), 0o644)

	p := NewParser()
	index := p.IndexProject(dir, []string{"a.go", "c.go"})
	if len(index) != 2 {
		t.Fatalf("expected 2 indexed files, got %d", len(index))
	}
}

func TestSearchSymbols(t *testing.T) {
	index := []FileSymbols{
		{Path: "main.go", Symbols: []Symbol{
			{Name: "main", Kind: "function"},
			{Name: "handleRequest", Kind: "function"},
		}},
		{Path: "user.go", Symbols: []Symbol{
			{Name: "User", Kind: "type"},
			{Name: "FindUser", Kind: "function"},
		}},
	}

	matches := SearchSymbols(index, "user")
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches for 'user', got %d", len(matches))
	}
}

func TestSymbolNames(t *testing.T) {
	index := []FileSymbols{
		{Path: "a.go", Symbols: []Symbol{{Name: "B"}, {Name: "A"}}},
		{Path: "b.go", Symbols: []Symbol{{Name: "C"}, {Name: "A"}}},
	}
	names := SymbolNames(index)
	if len(names) != 3 {
		t.Fatalf("expected 3 unique names, got %d: %v", len(names), names)
	}
	// Should be sorted.
	if names[0] != "A" || names[1] != "B" || names[2] != "C" {
		t.Errorf("names should be sorted: %v", names)
	}
}

func TestFormatSymbols_Empty(t *testing.T) {
	result := FormatSymbols(nil)
	if result != "No symbols found." {
		t.Errorf("expected 'No symbols found.', got %q", result)
	}
}

func TestUnsupportedFile(t *testing.T) {
	p := NewParser()
	symbols := p.ExtractSymbols("data.csv", []byte("a,b,c\n"))
	if symbols != nil {
		t.Errorf("expected nil for unsupported file, got %v", symbols)
	}
}

// --- helpers ---

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func symbolNames(symbols []Symbol) []string {
	names := make([]string, len(symbols))
	for i, s := range symbols {
		names[i] = s.Name + "/" + s.Kind
	}
	return names
}
