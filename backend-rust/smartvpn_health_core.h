#ifndef SMARTVPN_HEALTH_CORE_H
#define SMARTVPN_HEALTH_CORE_H
#include <stdint.h>
/* Pure numeric policy only. No clocks, probes, storage, routes or process control.
 * All arrays must be correctly aligned and valid for the declared lengths.
 * Null pointer returns -1. Inputs and outputs may overlap; inputs are copied.
 * No pointers retained. Return 0 success; -1 invalid, output unchanged. */
/* input/output six i64: timeout_ms, patrol_seconds, failure_threshold,
 * cooldown_seconds, minimum_dwell_seconds, recovery_successes. */
int32_t smartvpn_health_normalize_v1(const int64_t input[6], int64_t output[6]);
/* input eight i64: prior_state (0 other,1 cooldown,2 recovery), failure count,
 * success count, observed latency, success (0/1), cooldown active (0/1),
 * failure threshold, recovery threshold.
 * output four i64: state (1 healthy,2 suspect,3 unhealthy,4 cooldown,5 recovery),
 * failures, successes, latency. Counter increments wrap like Go int64.
 * Caller owns time comparisons/assignments and preserving unrelated fields. */
int32_t smartvpn_health_transition_v1(const int64_t input[8], int64_t output[4]);
#endif
