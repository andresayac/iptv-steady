package events

import (
	"cmp"
	"fmt"
	"io"
	"maps"
	"slices"
	"text/tabwriter"
)

const bytesPerGB = 1e9

type tally struct {
	events      int
	interrupted int
	recovered   int
	gaveUp      int
	deadLinks   int
	pauses      int
	pausedMS    int64
	served      int64
	downtimeMS  int64
}

func (t *tally) add(event Event) {
	if event.Kind == Served {
		t.served += event.Bytes
		return
	}
	t.events++
	switch event.Kind {
	case Stalled, Slow, Dropped:
		t.interrupted++
	case Recovered:
		t.recovered++
		t.downtimeMS += event.DowntimeMS
	case GaveUp:
		t.gaveUp++
	case DeadLink:
		t.deadLinks++
	case Starved:
		t.pauses++
		t.pausedMS += event.DowntimeMS
	}
}

func (t *tally) averageDowntimeMS() int64 {
	if t.recovered == 0 {
		return 0
	}
	return t.downtimeMS / int64(t.recovered)
}

func (t *tally) gigabytes() float64 {
	return float64(t.served) / bytesPerGB
}

func (t *tally) interruptedPerGB() string {
	if t.served == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f", float64(t.interrupted)/t.gigabytes())
}

func WriteReport(w io.Writer, recorded []Event) error {
	overall := &tally{}
	byHour := map[int]*tally{}
	byAccount := map[string]*tally{}
	byPlayer := map[string]*tally{}
	for _, event := range recorded {
		overall.add(event)
		tallyFor(byHour, event.Time.Local().Hour()).add(event)
		tallyFor(byAccount, event.Account).add(event)
		tallyFor(byPlayer, event.Player).add(event)
	}
	if _, err := fmt.Fprintf(w, "Events: %d, interruptions: %d, recovered: %d, gave up: %d, dead links: %d, pauses: %d\n", overall.events, overall.interrupted, overall.recovered, overall.gaveUp, overall.deadLinks, overall.pauses); err != nil {
		return err
	}
	if err := writeTable(w, "Hour", byHour, func(hour int) string { return fmt.Sprintf("%02d:00", hour) }); err != nil {
		return err
	}
	if err := writeTable(w, "Account", byAccount, label); err != nil {
		return err
	}
	return writeTable(w, "Player", byPlayer, label)
}

func label(name string) string {
	if name == "" {
		return "(none)"
	}
	return name
}

func tallyFor[K comparable](m map[K]*tally, key K) *tally {
	if m[key] == nil {
		m[key] = &tally{}
	}
	return m[key]
}

func writeTable[K cmp.Ordered](w io.Writer, title string, rows map[K]*tally, name func(K) string) error {
	if _, err := fmt.Fprintf(w, "\n%s\n", title); err != nil {
		return err
	}
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "\tinterrupted\trecovered\tgave up\tdead links\tavg recovery ms\tpauses\tpaused s\tGB\tinterrupted/GB")
	for _, key := range slices.Sorted(maps.Keys(rows)) {
		row := rows[key]
		fmt.Fprintf(table, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%.1f\t%s\n", name(key), row.interrupted, row.recovered, row.gaveUp, row.deadLinks, row.averageDowntimeMS(), row.pauses, row.pausedMS/1000, row.gigabytes(), row.interruptedPerGB())
	}
	return table.Flush()
}
