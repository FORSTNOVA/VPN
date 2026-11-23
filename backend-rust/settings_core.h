#ifndef SMARTVPN_SETTINGS_CORE_H
#define SMARTVPN_SETTINGS_CORE_H
#include <stddef.h>
#include <stdint.h>
/* Input: repeated u64-LE length + UTF8 bytes, no count. max input 8 MiB.
 * op1: entry, cleaned path, executable-name flag, absolute-exe flag.
 * op2 Android/op3 Windows: repeated normalized name, dedupe key, valid flag.
 * Flags 0/1. Go owns platform path/Unicode classification and normalization.
 * Output op1 string; op2/3 repeated length-prefixed strings, empty is empty list.
 * status 0 success,1 semantic error UTF8, -1 invalid, -2 capacity. written required.
 * All ranges valid/nonoverlapping, pointers not retained, NULL iff zero length.
 * On capacity error written contains required size, output unchanged. */
int32_t smartvpn_settings_v1(uint32_t op,const uint8_t *input,size_t len,uint8_t *output,size_t capacity,size_t *written);
/* windows 0/1. Returns 0 invalid port/platform,1 valid,-1 invalid flag. */
int32_t smartvpn_valid_socks_v1(int64_t port,uint32_t windows);
#endif
