package feed

import (
	"fmt"
	"log/slog"

	"git.duckfam.us/jj-software/cog/internal/query"
)

// FilterEngine evaluates stored filters against newly-ingested items and
// dispatches the matching filters' actions.
type FilterEngine struct {
	store *Store
}

// NewFilterEngine constructs an engine bound to a store. The engine reads
// filters on each Apply call so that newly-saved filters are observed without
// restart.
func NewFilterEngine(s *Store) (*FilterEngine, error) {
	return &FilterEngine{store: s}, nil
}

// ParseFilterQuery validates a filter query string. Use at filter save time.
func ParseFilterQuery(q string) (query.Expr, error) {
	return query.Parse(q)
}

// Apply runs all filters in sort_order against the given item. Tag and download
// mutations from earlier filters are reflected in the Record seen by later
// filters. A skip action deletes the item and stops further processing.
func (e *FilterEngine) Apply(itemID int64) error {
	it, err := e.store.GetItem(itemID)
	if err != nil {
		return fmt.Errorf("filter apply: load item: %w", err)
	}
	sub, err := e.store.GetSubscription(it.SubscriptionID)
	if err != nil {
		return fmt.Errorf("filter apply: load subscription: %w", err)
	}
	folder := ""
	if sub.FolderID.Valid {
		f, err := e.store.GetFolder(sub.FolderID.Int64)
		if err == nil {
			folder = f.Name
		}
	}

	filters, err := e.store.ListFilters()
	if err != nil {
		return fmt.Errorf("filter apply: list filters: %w", err)
	}

	for _, f := range filters {
		expr, perr := query.Parse(f.Query)
		if perr != nil {
			slog.Warn("feed filter: parse error at apply (skipping)",
				"filter_id", f.ID, "query", f.Query, "err", perr)
			continue
		}

		tags, _ := e.store.ItemTags(itemID)
		dl, _ := e.store.GetDownload(itemID)
		downloaded := dl.State == "ready"
		rec := BuildRecord(it, sub, folder, tags, !it.IsRead, it.IsStarred, downloaded)

		if !expr.Match(rec) {
			continue
		}
		switch f.Action {
		case "skip":
			if err := e.store.DeleteItem(itemID); err != nil {
				return fmt.Errorf("filter skip: %w", err)
			}
			return nil
		case "tag":
			if f.ActionValue == "" {
				slog.Warn("feed filter: tag action with empty value", "filter_id", f.ID)
				continue
			}
			if err := e.store.AddTag(itemID, f.ActionValue); err != nil {
				return fmt.Errorf("filter tag: %w", err)
			}
		case "download":
			if it.Type == "text" {
				continue
			}
			if err := e.store.EnqueueDownload(itemID); err != nil {
				return fmt.Errorf("filter download: %w", err)
			}
		case "mute_profanity":
			if it.Type == "text" {
				continue
			}
			if err := e.store.EnqueueProfanity(itemID); err != nil {
				return fmt.Errorf("filter mute_profanity: %w", err)
			}
			// Captions are a prerequisite for profanity muting; transcribe runs first.
			if err := e.store.EnqueueTranscribe(itemID); err != nil {
				return fmt.Errorf("filter mute_profanity transcribe: %w", err)
			}
		case "transcribe":
			if it.Type == "text" {
				continue
			}
			if err := e.store.EnqueueTranscribe(itemID); err != nil {
				return fmt.Errorf("filter transcribe: %w", err)
			}
		default:
			slog.Warn("feed filter: unknown action", "filter_id", f.ID, "action", f.Action)
		}
	}
	return nil
}
