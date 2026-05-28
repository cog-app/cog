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
	_, _, _, err := Compile(&FieldEqExpr{Field: "is", Value: "weird"}, compileTestSchema)
	if err == nil {
		t.Fatal("expected error for unknown enum value")
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
	_, _, _, err := Compile(&FieldEqExpr{Field: "ghost", Value: "x"}, compileTestSchema)
	if err == nil {
		t.Fatal("expected unknown-field error")
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
