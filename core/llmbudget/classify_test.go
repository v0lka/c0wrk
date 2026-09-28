package llmbudget

import (
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		baseURL  string
		override string
		want     Class
	}{
		{"embedded by reserved name", "embedded", "", "", ClassEmbedded},
		{"embedded name wins over remote url", "embedded", "https://api.example.com", "", ClassEmbedded},
		{"loopback ipv4", "ollama", "http://127.0.0.1:11434/v1", "", ClassLocal},
		{"loopback ipv6", "ollama", "http://[::1]:11434/v1", "", ClassLocal},
		{"loopback localhost any case", "lmstudio", "http://LOCALhost:1234/v1", "", ClassLocal},
		{"remote https", "openai", "https://api.openai.com/v1", "", ClassRemote},
		{"empty base url is remote", "openai", "", "", ClassRemote},
		{"unparseable base url is remote", "openai", "http://[::1", "", ClassRemote},
		{"explicit override beats reserved name", "embedded", "https://api.example.com", "remote", ClassRemote},
		{"explicit override beats loopback", "ollama", "http://127.0.0.1:11434", "embedded", ClassEmbedded},
		{"invalid override falls back to name", "embedded", "", "nonsense", ClassEmbedded},
		{"invalid override falls back to url", "ollama", "http://127.0.0.1:11434", "nonsense", ClassLocal},
		{"override is case and space insensitive", "openai", "https://api.example.com", " Local ", ClassLocal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.provider, tt.baseURL, tt.override); got != tt.want {
				t.Fatalf("Classify(%q, %q, %q) = %q, want %q", tt.provider, tt.baseURL, tt.override, got, tt.want)
			}
		})
	}
}

func TestParseClass(t *testing.T) {
	for _, tt := range []struct {
		value string
		ok    bool
	}{
		{"remote", true}, {"LOCAL", true}, {"embedded", true}, {" Embedded ", true},
		{"", false}, {"nonsense", false}, {"lokal", false},
	} {
		if _, ok := ParseClass(tt.value); ok != tt.ok {
			t.Errorf("ParseClass(%q) ok = %v, want %v", tt.value, ok, tt.ok)
		}
	}
	if got, ok := ParseClass("Local"); !ok || got != ClassLocal {
		t.Fatalf("ParseClass(\"Local\") = %q, %v; want %q, true", got, ok, ClassLocal)
	}
}

func TestClassFloorsAndCeilings(t *testing.T) {
	// ADR-071 D8: floors 120/300/600, ceilings 600/1800/1800, exported so
	// backend/config can validate the same figures.
	floors := map[Class]time.Duration{
		ClassRemote:   FloorRemote,
		ClassLocal:    FloorLocal,
		ClassEmbedded: FloorEmbedded,
	}
	ceilings := map[Class]time.Duration{
		ClassRemote:   CeilingRemote,
		ClassLocal:    CeilingLocal,
		ClassEmbedded: CeilingEmbedded,
	}
	wantFloors := map[Class]time.Duration{
		ClassRemote:   120 * time.Second,
		ClassLocal:    300 * time.Second,
		ClassEmbedded: 600 * time.Second,
	}
	wantCeilings := map[Class]time.Duration{
		ClassRemote:   600 * time.Second,
		ClassLocal:    1800 * time.Second,
		ClassEmbedded: 1800 * time.Second,
	}
	for _, class := range []Class{ClassRemote, ClassLocal, ClassEmbedded} {
		if floors[class] != wantFloors[class] {
			t.Errorf("%s floor = %v, want %v", class, floors[class], wantFloors[class])
		}
		if ceilings[class] != wantCeilings[class] {
			t.Errorf("%s ceiling = %v, want %v", class, ceilings[class], wantCeilings[class])
		}
		if floors[class] >= ceilings[class] {
			t.Errorf("%s: floor %v must be below ceiling %v", class, floors[class], ceilings[class])
		}
	}
}
