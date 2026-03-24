package contacts

import (
	"strings"
	"testing"
	"time"
)

func TestParseVCard(t *testing.T) {
	vcard := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:test-123\r\nFN:Alice Smith\r\nEMAIL;TYPE=INTERNET:alice@example.com\r\nTEL;TYPE=CELL:+1234567890\r\nREV:20260315T140000Z\r\nEND:VCARD\r\n"

	f, err := ParseVCard(vcard)
	if err != nil {
		t.Fatal(err)
	}
	if f.UID != "test-123" {
		t.Errorf("UID = %q, want %q", f.UID, "test-123")
	}
	if f.FN != "Alice Smith" {
		t.Errorf("FN = %q, want %q", f.FN, "Alice Smith")
	}
	if f.Email != "alice@example.com" {
		t.Errorf("Email = %q, want %q", f.Email, "alice@example.com")
	}
	if f.Tel != "+1234567890" {
		t.Errorf("Tel = %q, want %q", f.Tel, "+1234567890")
	}
	if f.Rev != "20260315T140000Z" {
		t.Errorf("Rev = %q, want %q", f.Rev, "20260315T140000Z")
	}
}

func TestParseVCardNoCard(t *testing.T) {
	_, err := ParseVCard("nothing here")
	if err == nil {
		t.Error("expected error for missing VCARD")
	}
}

func TestParseVCardNoUID(t *testing.T) {
	vcard := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bob\r\nEND:VCARD\r\n"
	_, err := ParseVCard(vcard)
	if err == nil {
		t.Error("expected error for missing UID")
	}
}

func TestParseVCardMultipleEmails(t *testing.T) {
	vcard := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:multi-1\r\nFN:Multi\r\nEMAIL:first@example.com\r\nEMAIL:second@example.com\r\nEND:VCARD\r\n"

	f, err := ParseVCard(vcard)
	if err != nil {
		t.Fatal(err)
	}
	// Should capture first email only.
	if f.Email != "first@example.com" {
		t.Errorf("Email = %q, want first@example.com", f.Email)
	}
}

func TestParseVCardUnfold(t *testing.T) {
	vcard := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:fold-1\r\nFN:Very Long\r\n  Name Here\r\nEND:VCARD\r\n"

	f, err := ParseVCard(vcard)
	if err != nil {
		t.Fatal(err)
	}
	if f.FN != "Very Long Name Here" {
		t.Errorf("FN = %q, want %q", f.FN, "Very Long Name Here")
	}
}

func TestGenerateVCard(t *testing.T) {
	vcard := GenerateVCard("test-uid", "Alice Smith", "alice@example.com", "+1234567890", time.Now())

	if !strings.Contains(vcard, "UID:test-uid") {
		t.Error("missing UID")
	}
	if !strings.Contains(vcard, "FN:Alice Smith") {
		t.Error("missing FN")
	}
	if !strings.Contains(vcard, "EMAIL;TYPE=INTERNET:alice@example.com") {
		t.Error("missing EMAIL")
	}

	// Round-trip.
	f, err := ParseVCard(vcard)
	if err != nil {
		t.Fatal(err)
	}
	if f.UID != "test-uid" {
		t.Errorf("round-trip UID = %q", f.UID)
	}
	if f.FN != "Alice Smith" {
		t.Errorf("round-trip FN = %q", f.FN)
	}
}
