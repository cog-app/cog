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
	// Keyword field (i.type) must NOT appear in bare-word scan.
	if strings.Contains(w, "LOWER(i.type) LIKE") {
		t.Fatalf("bare word should not scan keyword field i.type: %q", w)
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
	if !strings.Contains(w, " AND ") || !strings.Contains(w, "NOT COALESCE(") {
		t.Fatalf("%q", w)
	}
}

// A negated field from an outer join must hold where the join found nothing:
// NOT NULL is NULL, which leaves the row out.
func TestCompileNotOuterJoinField(t *testing.T) {
	w, _, _, err := Compile(&NotExpr{Inner: &FieldEqExpr{Field: "folder", Value: "Podcasts"}}, compileTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	if w != "NOT COALESCE(LOWER(f.name) = LOWER(?), 0)" {
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

func TestCompileBareWordTextFieldsOnly(t *testing.T) {
	w, _, joins, err := Compile(&TextExpr{Value: "x"}, compileTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	// Must scan Text fields.
	if !strings.Contains(w, "LOWER(i.title) LIKE ?") {
		t.Fatalf("missing title in bare-word scan: %q", w)
	}
	if !strings.Contains(w, "LOWER(i.author) LIKE ?") {
		t.Fatalf("missing author in bare-word scan: %q", w)
	}
	// Must NOT scan Keyword field (i.type).
	if strings.Contains(w, "LOWER(i.type) LIKE") {
		t.Fatalf("bare word should not scan keyword field i.type: %q", w)
	}
	// folder is Keyword (not Text) — no folders join expected.
	for _, j := range joins {
		if j == "folders" {
			t.Fatalf("bare word should not require folders join (folder is Keyword, not Text): %v", joins)
		}
	}
}

func TestCompilePhraseAllWordsSuperset(t *testing.T) {
	w, args, _, err := Compile(&PhraseExpr{Value: "hello world"}, compileTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	// Must use AND between per-word fragments.
	if !strings.Contains(w, " AND ") {
		t.Fatalf("phrase compile should AND per-word fragments: %q", w)
	}
	// Must NOT be a single %hello world% substring (exact phrase literal).
	if strings.Contains(w, "%hello world%") {
		t.Fatalf("phrase should not produce a single %%hello world%% literal: %q", w)
	}
	// compileTestSchema has 2 Text fields (title, author).
	// 2 words × 2 text fields = 4 LIKE args, each %hello% or %world%.
	if len(args) != 4 {
		t.Fatalf("want 4 args (2 words × 2 text fields), got %d: %v", len(args), args)
	}
	helloCount, worldCount := 0, 0
	for _, a := range args {
		s, _ := a.(string)
		if s == "%hello%" {
			helloCount++
		}
		if s == "%world%" {
			worldCount++
		}
	}
	if helloCount != 2 || worldCount != 2 {
		t.Fatalf("want 2×%%hello%% and 2×%%world%%, args=%v", args)
	}
	// Both LOWER(i.title) LIKE ? occurrences (one per word).
	titleCount := strings.Count(w, "LOWER(i.title) LIKE ?")
	if titleCount < 2 {
		t.Fatalf("want at least 2 LOWER(i.title) LIKE ? (one per word), got %d in %q", titleCount, w)
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

func TestCompileDateRange(t *testing.T) {
	w, args, _, err := Compile(&FieldCompareExpr{Field: "published", Op: OpGe, Value: "2026-01-01T00:00:00Z"}, compileTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	if w != "i.published_at >= ?" {
		t.Fatalf("where=%q", w)
	}
	// Bound arg is unix seconds.
	want := int64(1767225600) // 2026-01-01T00:00:00Z
	if len(args) != 1 || args[0] != want {
		t.Fatalf("args=%v want [%d]", args, want)
	}
}

func TestCompileNumberRange(t *testing.T) {
	w, args, _, err := Compile(&FieldCompareExpr{Field: "duration", Op: OpLt, Value: "600"}, compileTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	if w != "i.duration_sec < ?" {
		t.Fatalf("where=%q", w)
	}
	if len(args) != 1 || args[0] != float64(600) {
		t.Fatalf("args=%v", args)
	}
}

func TestCompileRangeUnknownFieldMatchNothing(t *testing.T) {
	w, _, _, err := Compile(&FieldCompareExpr{Field: "ghost", Op: OpGt, Value: "1"}, compileTestSchema)
	if err != nil {
		t.Fatalf("unknown field should not error: %v", err)
	}
	if w != "0" {
		t.Fatalf("want match-nothing, got %q", w)
	}
}

func TestCompileRangeOnTextFieldErrors(t *testing.T) {
	// Range on a non-ordered field (title is Text) is a user error worth surfacing.
	_, _, _, err := Compile(&FieldCompareExpr{Field: "title", Op: OpGt, Value: "x"}, compileTestSchema)
	if err == nil {
		t.Fatal("range on Text field should error")
	}
}

func TestCompileDateRangeBadValueMatchNothing(t *testing.T) {
	w, _, _, err := Compile(&FieldCompareExpr{Field: "published", Op: OpGt, Value: "not-a-date"}, compileTestSchema)
	if err != nil {
		t.Fatalf("bad date should not error: %v", err)
	}
	if w != "0" {
		t.Fatalf("want match-nothing, got %q", w)
	}
}
