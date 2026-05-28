package query

import (
	"reflect"
	"strings"
	"testing"
)

func textSchema() Schema {
	return Schema{
		PrimaryAlias: "i",
		Fields: map[string]FieldSpec{
			"title":  {Type: Text, Expr: "i.title"},
			"author": {Type: Text, Expr: "i.author"},
			"type":   {Type: Keyword, Expr: "i.type"},
			"folder": {Type: Keyword, Expr: "f.name", Joins: []string{"folders"}},
			"tag": {
				Type:        ManyToMany,
				Join:        "item_tags", JoinFK: "item_id",
				ValueTable:  "tags", ValueFK: "tag_id", ValueColumn: "name",
			},
			"is": {
				Type: EnumPredicate,
				Values: map[string]string{
					"unread":     "i.is_read = 0",
					"starred":    "i.is_starred = 1",
					"downloaded": "EXISTS (SELECT 1 FROM downloads d WHERE d.item_id = i.id AND d.state = 'ready')",
				},
			},
		},
		Joins: map[string]string{
			"folders": "LEFT JOIN folders f ON f.id = s.folder_id",
		},
	}
}

func TestSchemaValidateOK(t *testing.T) {
	if err := textSchema().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaDefaultTextFields(t *testing.T) {
	got := textSchema().DefaultTextFields()
	want := []string{"author", "title"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestSchemaValidateFieldNameRules(t *testing.T) {
	s := Schema{Fields: map[string]FieldSpec{"BadName": {Type: Text, Expr: "x"}}}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "field name") {
		t.Fatalf("want field-name error, got %v", err)
	}
}

func TestSchemaValidateRequiresExpr(t *testing.T) {
	s := Schema{Fields: map[string]FieldSpec{"title": {Type: Text}}}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "Expr") {
		t.Fatalf("want Expr error, got %v", err)
	}
}

func TestSchemaValidateManyToManyRequiresAll(t *testing.T) {
	s := Schema{Fields: map[string]FieldSpec{"tag": {Type: ManyToMany, Join: "item_tags"}}}
	if err := s.Validate(); err == nil {
		t.Fatal("want ManyToMany missing-field error")
	}
}

func TestSchemaValidateEnumPredicateRequiresValues(t *testing.T) {
	s := Schema{Fields: map[string]FieldSpec{"is": {Type: EnumPredicate}}}
	if err := s.Validate(); err == nil {
		t.Fatal("want EnumPredicate empty-Values error")
	}
}

func TestSchemaValidateJoinReferencesMustExist(t *testing.T) {
	s := Schema{
		Fields: map[string]FieldSpec{"folder": {Type: Keyword, Expr: "f.name", Joins: []string{"missing"}}},
		Joins:  map[string]string{},
	}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("want join-ref error, got %v", err)
	}
}
