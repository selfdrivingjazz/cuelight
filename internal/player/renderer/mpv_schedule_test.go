package renderer

import (
	"testing"
	"time"
)

func TestSelectScheduleItemUsesHalfOpenBoundaries(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	items := []ScheduleItem{
		{ID: "first", StartsAt: start, EndsAt: start.Add(time.Hour)},
		{ID: "second", StartsAt: start.Add(time.Hour), EndsAt: start.Add(2 * time.Hour)},
	}
	for _, test := range []struct {
		name string
		now  time.Time
		want string
	}{
		{"start", start, "first"},
		{"boundary", start.Add(time.Hour), "second"},
		{"end", start.Add(2 * time.Hour), ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := SelectScheduleItem(items, test.now)
			if test.want == "" {
				if ok {
					t.Fatalf("item active after window: %#v", got)
				}
				return
			}
			if !ok || got.ID != test.want {
				t.Fatalf("selected %#v, ok=%v", got, ok)
			}
		})
	}
}
