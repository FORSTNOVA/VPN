#ifndef SMARTVPN_STORE_FFI_H
#define SMARTVPN_STORE_FFI_H
#include <stdint.h>
#include <stddef.h>
/* v1: functions return 0 transport success; negative transport errors never
 * authorize retry/fallback. Returned result envelope: LE u32 category (0ok,
 * 1invalid,2SQL,3closed/poison,4other), u32 sqlitecode, u32 outcome (0unchanged,
 * 1success,2unknown), u64 payload length, then opaque operation payload.
 * Store open is UTF16 (no NUL), not bytes; all SQL text uses arbitrary bytes.
 * Open results own an unclaimed store. After parsing success call claim(store,
 * result) BEFORE free(result); otherwise free closes that unclaimed store.
 * No pointer retention; caller buffers valid, aligned and nonoverlapping.
 * Limits: 64 stores,256 results,8MiB input,16MiB result,64MiB total results.
 * Entire lifecycle serialized by registry mutex; cached result reads never SQL.
 */
uint32_t smartvpn_store_version_v1(void);
uint64_t smartvpn_store_capabilities_v1(void);
int32_t smartvpn_store_open_v1(const uint16_t*,size_t,uint64_t*,uint64_t*);
int32_t smartvpn_store_claim_v1(uint64_t store,uint64_t result);
int32_t smartvpn_store_call_v1(uint64_t store,uint32_t op,const uint8_t*,size_t,uint64_t*);
int32_t smartvpn_store_close_v1(uint64_t store,uint64_t*);
int32_t smartvpn_store_result_size_v1(uint64_t result,size_t*);
/* capacity too small: -2 with required written; output unchanged; safe retry read only. */
int32_t smartvpn_store_result_read_v1(uint64_t result,uint8_t*,size_t,size_t*);
int32_t smartvpn_store_result_free_v1(uint64_t result);
#endif
