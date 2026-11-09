package events

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestJournalRoundTripAndReport(t *testing.T) {
	dir := t.TempDir()
	journal, err := Open(dir, 30)
	if err != nil {
		t.Fatal(err)
	}
	moment := time.Date(2026, 11, 8, 21, 30, 0, 0, time.Local)
	journal.Record(Event{Time: moment, Kind: Stalled, Account: "one", Player: "VLC", Title: "A Movie"})
	journal.Record(Event{Time: moment, Kind: Recovered, Account: "one", Player: "VLC", Title: "A Movie", DowntimeMS: 300})
	journal.Record(Event{Time: moment, Kind: GaveUp, Account: "two", Player: "VLC", Title: "Another"})
	journal.Close()

	recorded, err := Read(dir, moment.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(recorded) != 3 {
		t.Fatalf("events = %d", len(recorded))
	}
	var out bytes.Buffer
	if err := WriteReport(&out, recorded); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"interruptions: 1", "recovered: 1", "gave up: 1", "21:00", "one", "VLC"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("report lacks %q:\n%s", want, out.String())
		}
	}
}

func TestReportCountsPausesAndInterruptionsPerGigabyte(t *testing.T) {
	moment := time.Date(2026, 11, 8, 22, 5, 0, 0, time.Local)
	recorded := []Event{
		{Time: moment, Kind: Stalled, Account: "one"},
		{Time: moment, Kind: Stalled, Account: "one"},
		{Time: moment, Kind: Starved, Account: "one", DowntimeMS: 21_000},
		{Time: moment, Kind: Served, Account: "one", Bytes: 4_000_000_000},
		{Time: moment, Kind: Stalled, Account: "two"},
	}
	var out bytes.Buffer
	if err := WriteReport(&out, recorded); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "Events: 4, interruptions: 3, recovered: 0, gave up: 0, dead links: 0, pauses: 1\n") {
		t.Fatalf("a served connection must not count as an event:\n%s", out.String())
	}
	for _, want := range []string{"1       21        4.0  0.5", "0       0         0.0  -"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("report lacks %q:\n%s", want, out.String())
		}
	}
}

func TestReadIgnoresOlderEvents(t *testing.T) {
	dir := t.TempDir()
	journal, err := Open(dir, 30)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	journal.Record(Event{Time: now.Add(-48 * time.Hour), Kind: Stalled})
	journal.Record(Event{Time: now, Kind: Stalled})
	journal.Close()

	recorded, err := Read(dir, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(recorded) != 1 {
		t.Fatalf("events = %d", len(recorded))
	}
}

func TestOldFilesAreRemoved(t *testing.T) {
	dir := t.TempDir()
	journal, err := Open(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	journal.Record(Event{Time: now.AddDate(0, 0, -10), Kind: Stalled})
	journal.Record(Event{Time: now, Kind: Stalled})
	journal.Close()

	recorded, err := Read(dir, now.AddDate(0, 0, -30))
	if err != nil {
		t.Fatal(err)
	}
	if len(recorded) != 1 {
		t.Fatalf("events = %d", len(recorded))
	}
}
