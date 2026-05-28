package feed_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/query"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestFeedSearchSchemaValidates(t *testing.T) {
	if err := feed.SearchSchema().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestFeedSearchSchemaHasExpectedFields(t *testing.T) {
	s := feed.SearchSchema()
	for _, name := range []string{"title", "author", "content", "url", "type", "kind", "subscription", "folder", "tag", "is", "published"} {
		if _, ok := s.Fields[name]; !ok {
			t.Errorf("missing field %q", name)
		}
	}
	if s.Fields["is"].Type != query.EnumPredicate {
		t.Errorf("is field type = %v", s.Fields["is"].Type)
	}
}
