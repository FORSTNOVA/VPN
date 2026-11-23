#ifndef SMARTVPN_SERVICE_LOGIC_H
#define SMARTVPN_SERVICE_LOGIC_H
#include <stddef.h>
#include <stdint.h>
/* Optional independent exports. Callers retain ownership; no pointers retained.
 * Input buffers must be readable for their byte lengths, written/output writable.
 * No file, network, process, or system configuration operations occur here.
 * Maximum input/needle 8 MiB; maximum redaction result 16 MiB.
 */
size_t smartvpn_log_tail_v1(const uint8_t *input, size_t len, size_t limit);
/* tail returns an offset <=len, or SIZE_MAX for rejected input. */
int32_t smartvpn_log_redact_v1(const uint8_t *input, size_t len,
 const uint8_t *secret, size_t secret_len, uint8_t *output, size_t cap, size_t *written);
/* status 0: success; -1: invalid/unsupported (includes empty secret);
 * -2: insufficient cap, written reports required bytes; no partial output.
 * For invalid arguments written is unspecified; callers must ignore it.
 */
int32_t smartvpn_service_takeover_v1(uint32_t flags);
/* Bits 0 stale, 1 connected, 2 existing elevated, 3 caller elevated.
 * Return 0/1 decision, -1 invalid flags. Does not actually take over a service.
 */
#endif
