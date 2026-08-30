package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFuzzyETA(t *testing.T) {
	cases := []struct {
		name      string
		remaining time.Duration
		want      string
	}{
		{"zero is almost done", 0, "almost done"},
		{"just under five minutes is almost done", 4*time.Minute + 59*time.Second, "almost done"},
		{"five minutes rounds up to the ten-minute minimum", 5 * time.Minute, "roughly 10 minutes left"},
		{"minutes bucket rounds to nearest ten", 32 * time.Minute, "roughly 30 minutes left"},
		{"minutes bucket rounds up to nearest ten", 38 * time.Minute, "roughly 40 minutes left"},
		{"lower boundary of the hour band", 45 * time.Minute, "roughly an hour left"},
		{"just below the lower boundary stays in minutes", 44*time.Minute + 59*time.Second, "roughly 40 minutes left"},
		{"middle of the hour band", 60 * time.Minute, "roughly an hour left"},
		{"upper boundary of the hour band", 90 * time.Minute, "roughly an hour left"},
		{"just above the upper boundary moves to hours", 90*time.Minute + time.Second, "roughly 2 hours left"},
		{"hours bucket rounds to nearest whole hour", 2*time.Hour + 20*time.Minute, "roughly 2 hours left"},
		{"hours bucket rounds up to nearest whole hour", 2*time.Hour + 40*time.Minute, "roughly 3 hours left"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, fuzzyETA(c.remaining))
		})
	}
}
