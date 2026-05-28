package query

import (
	"strings"
	"testing"
)

var compileTestSchema = Schema{
	Fields: map[string]FieldSpec{
		"title":     {Type: Text, Expr: "i.title"},
		"author":    {Type: Text, Expr: "i.author"},
		"type":      {Type: Keyword, Expr: "i.type"},
		"is_read":   {Type: Bool, Expr: "i.is_read"},
		"duration":  {Type: Number, Expr: "i.duration_sec"},
		"published": {Type: DateText, Expr: "i.published_at"},
		"folder":    {Type: Keyword, Expr: "f.name", Joins: []string{"folders"}},
		"tag":       {Type: ManyToMany, Join: "item_tags", JoinFK: "item_id", ValueTable: "tags", ValueFK: "tag_id", ValueColumn: "name"},
		"is": {Type: EnumPredicate, Values: map[string]string{
			"unread":     "i.is_read = 0",
			"downloaded": "EXISTS (SELECT 1 FROM downloads d WHERE d.item_id = i.id AND d.state = 'ready')",
		}},
	},
	Joins: map[string]string{"folders": "LEFT JOIN folders f ON f.id = s.folder_id"},
}

func TestCompileTextBareWordOROfLikes(t *testing.T) {
	w, args, _, err := Compile(&TextExpr{Value: "Hello"}, compileTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w, "LOWER(i.title) LIKE ?") || !strings.Contains(w, "LOWER(i.author) LIKE ?") {
		t.Fatalf("missing text-field LIKEs: %q", w)
	}
	if len(args) < 2 || args[0] != "%hello%" {
		t.Fatalf("args=%v", args)
	}
}

func TestCompileFieldEqKeyword(t *testing.T) {
	w, args, _, err := Compile(&FieldEqExpr{Field: "type", Value: "Audio"}, compileTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	if w != "LOWER(i.type) = LOWER(?)" {
		t.Fatalf("%q", w)
	}
	if len(args) != 1 || args[0] != "Audio" {
		t.Fatalf("%v", args)
	}
}

func TestCompileFieldEqFolderReturnsJoin(t *testing.T) {
	_, _, joins, err := Compile(&FieldEqExpr{Field: "folder", Value: "Dev"}, compileTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	if len(joins) != 1 || joins[0] != "folders" {
		t.Fatalf("joins=%v", joins)
	}
}

func TestCompileFieldEqTagEXISTS(t *testing.T) {
	w, _, joins, err := Compile(&FieldEqExpr{Field: "tag", Value: "weekly"}, compileTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(w, "EXISTS") {
		t.Fatalf("expected EXISTS: %q", w)
	}
	if len(joins) != 0 {
		t.Fatalf("tag must not need caller joins: %v", joins)
	}
}

func TestCompileFieldEqIsDownloaded(t *testing.T) {
	w, _, _, err := Compile(&FieldEqExpr{Field: "is", Value: "downloaded"}, compileTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w, "EXISTS (SELECT 1 FROM downloads") {
		t.Fatalf("%q", w)
	}
}

func TestCompileFieldEqIsUnknownValue(t *testing.T) {
	w, _, _, err := Compile(&FieldEqExpr{Field: "is", Value: "weird"}, compileTestSchema)
	if err != nil {
		t.Fatalf("unknown enum should not error: %v", err)
	}
	if w != "0" {
		t.Fatalf("want match-nothing predicate \"0\", got %q", w)
	}
}

func TestCompileBoolField(t *testing.T) {
	w, args, _, err := Compile(&FieldEqExpr{Field: "is_read", Value: "true"}, compileTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	if w != "i.is_read = ?" || len(args) != 1 || args[0] != 1 {
		t.Fatalf("w=%q args=%v", w, args)
	}
}

func TestCompileAndOrNot(t *testing.T) {
	e := &AndExpr{
		Left:  &FieldEqExpr{Field: "type", Value: "audio"},
		Right: &NotExpr{Inner: &FieldEqExpr{Field: "is", Value: "unread"}},
	}
	w, _, _, err := Compile(e, compileTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w, " AND ") || !strings.Contains(w, "NOT (") {
		t.Fatalf("%q", w)
	}
}

func TestCompileFieldInExpansion(t *testing.T) {
	w, args, _, err := Compile(&FieldInExpr{Field: "type", Values: []string{"audio", "video"}}, compileTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w, " OR ") || len(args) != 2 {
		t.Fatalf("w=%q args=%v", w, args)
	}
}

func TestCompileMatchAllReturnsEmpty(t *testing.T) {
	w, args, joins, err := Compile(&MatchAllExpr{}, compileTestSchema)
	if err != nil || w != "" || args != nil || joins != nil {
		t.Fatalf("got w=%q args=%v joins=%v err=%v", w, args, joins, err)
	}
}

func TestCompileUnknownField(t *testing.T) {
	w, args, joins, err := Compile(&FieldEqExpr{Field: "ghost", Value: "x"}, compileTestSchema)
	if err != nil {
		t.Fatalf("unknown field should not error: %v", err)
	}
	if w != "0" {
		t.Fatalf("want match-nothing predicate \"0\", got %q", w)
	}
	if args != nil || joins != nil {
		t.Fatalf("args=%v joins=%v", args, joins)
	}
}

func TestCompileUnknownFieldYieldsMatchNothing(t *testing.T) {
	w, args, joins, err := Compile(&FieldEqExpr{Field: "ghost", Value: "x"}, compileTestSchema)
	if err != nil {
		t.Fatalf("unknown field should not error: %v", err)
	}
	if w != "0" {
		t.Fatalf("want match-nothing predicate \"0\", got %q", w)
	}
	if args != nil || joins != nil {
		t.Fatalf("args=%v joins=%v", args, joins)
	}
}

func TestCompileUnknownEnumValueYieldsMatchNothing(t *testing.T) {
	w, _, _, err := Compile(&FieldEqExpr{Field: "is", Value: "weird"}, compileTestSchema)
	if err != nil {
		t.Fatalf("unknown enum should not error: %v", err)
	}
	if w != "0" {
		t.Fatalf("want \"0\", got %q", w)
	}
}

func TestCompileFieldInUnknownFieldYieldsMatchNothing(t *testing.T) {
	w, _, _, err := Compile(&FieldInExpr{Field: "ghost", Values: []string{"a", "b"}}, compileTestSchema)
	if err != nil {
		t.Fatalf("unknown field IN should not error: %v", err)
	}
	if w != "0" {
		t.Fatalf("want \"0\", got %q", w)
	}
}

func TestCompileTagUsesPrimaryAlias(t *testing.T) {
	sc := compileTestSchema
	sc.PrimaryAlias = "x"
	w, _, _, err := Compile(&FieldEqExpr{Field: "tag", Value: "weekly"}, sc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w, "j.item_id = x.id") {
		t.Fatalf("tag EXISTS should reference primary alias x.id: %q", w)
	}
}

func TestCompileBareWordScansKeywordFields(t *testing.T) {
	w, args, _, err := Compile(&TextExpr{Value: "audio"}, compileTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	// type is Keyword (i.type) — must be in the bare-word OR scan now.
	if !strings.Contains(w, "LOWER(i.type) LIKE ?") {
		t.Fatalf("bare word should scan keyword field i.type: %q", w)
	}
	// still scans text fields
	if !strings.Contains(w, "LOWER(i.title) LIKE ?") {
		t.Fatalf("missing title: %q", w)
	}
	_ = args
}

func TestCompileBareWordPullsFolderJoin(t *testing.T) {
	_, _, joins, err := Compile(&TextExpr{Value: "x"}, compileTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, j := range joins {
		if j == "folders" {
			found = true
		}
	}
	if !found {
		t.Fatalf("bare word over folder(Keyword w/ join) must require folders join: %v", joins)
	}
}

func TestCompileJoinsDedupedAndSorted(t *testing.T) {
	e := &AndExpr{
		Left:  &FieldEqExpr{Field: "folder", Value: "A"},
		Right: &FieldEqExpr{Field: "folder", Value: "B"},
	}
	_, _, joins, err := Compile(e, compileTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	if len(joins) != 1 || joins[0] != "folders" {
		t.Fatalf("joins=%v", joins)
	}
}
