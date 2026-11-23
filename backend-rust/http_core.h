#ifndef SMARTVPN_HTTP_CORE_H
#define SMARTVPN_HTTP_CORE_H
#include <stdint.h>
#include <stddef.h>
/* Inputs readable, max 8 MiB each, NULL only with zero length. No retention.
 * Returns 1 allow, 0 deny, -1 invalid. No UTF-8 normalization. */
int32_t smartvpn_http_authorized_v1(const uint8_t *, size_t, const uint8_t *, size_t);
/* connected: 0/1. written required. output writable for capacity, cannot overlap
 * written. 0 success, -1 invalid, -2 capacity. Required size in written.
 * Output unchanged on error. */
int32_t smartvpn_http_identity_v1(uint32_t connected, uint8_t *output, size_t capacity, size_t *written);
/* Exact canonical paths only, not Go ServeMux path normalization/redirects.
 * windows: 0/1. Positive route ID, 0 absent, -2 wrong method, -1 invalid. */
int32_t smartvpn_http_dispatch_v1(const uint8_t *method, size_t method_len, const uint8_t *path, size_t path_len, uint32_t windows);
/* Caller snapshot selection, no probes: index >=0, -1 none, -2 invalid.
 * count<=1,000,000; preferred=-1 or valid index; delays aligned/readable. */
int64_t smartvpn_choose_reachable_v1(const int64_t *delays, size_t count, int64_t preferred);
#endif
