//go:build windows && amd64 && rustcore

package main

import (
	"math"
	"math/rand"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestRustHealthCoreParity(t *testing.T) {
	expected := os.Getenv("SMARTVPN_TEST_EXPECT_HEALTH_RUST") == "1"
	if healthRustAvailable() != expected {
		t.Fatal("health DLL availability mismatch")
	}
	rng := rand.New(rand.NewSource(713))
	states := []string{"", healthHealthy, healthSuspect, healthUnhealthy, healthCooldown, healthRecovery, "unknown\xff"}
	now := time.Now() // Preserves monotonic clock metadata through the Go boundary.
	ints := []int{math.MinInt, math.MaxInt, -1, 0, 1, 2, 3, 10, 600, 15000}
	for i := 0; i < 12000; i++ {
		value := func() int {
			if i%3 == 0 {
				return ints[rng.Intn(len(ints))]
			}
			return int(rng.Int63()) - int(rng.Int63())
		}
		p := healthParams{value(), value(), value(), value(), value(), value()}
		if got, want := p.normalized(), p.normalizedGo(); got != want {
			t.Fatalf("normalization %d", i)
		}
		cooldown := time.Time{}
		switch i % 4 {
		case 1:
			cooldown = now.Add(time.Second)
		case 2:
			cooldown = now
		case 3:
			cooldown = now.Add(-time.Second)
		}
		current := nodeHealth{Name: "synthetic\xff", State: states[i%len(states)], ConsecutiveFailures: value(), ConsecutiveSuccesses: value(), LatencyMs: value(), CooldownUntil: cooldown, LastSuccessAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute)}
		success := i%2 == 0
		latency := value()
		got, want := nextHealthState(current, success, latency, now, p), nextHealthStateGo(current, success, latency, now, p)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("transition mismatch %d: %#v / %#v", i, got, want)
		}
		if expected {
			direct, used := rustNextHealthState(current, success, latency, now, p)
			if !used || !reflect.DeepEqual(direct, want) {
				t.Fatal("transition silently fell back")
			}
			pn, used := rustHealthNormalized(p)
			if !used || pn != p.normalizedGo() {
				t.Fatal("normalization silently fell back")
			}
		}
	}
	t.Log("12000 state and normalization comparisons")
}
