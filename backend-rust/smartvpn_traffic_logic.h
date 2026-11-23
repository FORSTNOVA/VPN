#ifndef SMARTVPN_TRAFFIC_LOGIC_H
#define SMARTVPN_TRAFFIC_LOGIC_H
#include <stdint.h>
#include <stddef.h>
/* Caller-owned buffers, no retained pointers, no IO.
 * values input: seven i64s [previous up, previous down, current up,
 * current down, elapsed nanoseconds, first sample(0/1), operation].
 * operation0: counter delta(current up, previous up), output[1]=0.
 * operation1: rates; output two i64s (up/down).
 * Return0 success; -1 unsupported/malformed, output must be ignored.
 * Float conversions out of i64 range are intentionally rejected for Go fallback.
 */
int32_t smartvpn_traffic_values_v1(const int64_t *input, int64_t *output);
/* Descending stable ranking of caller-computed wrapped i64 byte totals.
 * output must hold len size_t indices; max65536 rows; null allowed only if len0.
 * Return0 success; -1 invalid size/pointers. */
int32_t smartvpn_traffic_ranking_v1(const int64_t *totals,size_t len,size_t *output);
#endif
