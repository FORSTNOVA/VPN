#ifndef SMARTVPN_CORE_H
#define SMARTVPN_CORE_H
#include <stdint.h>
#include <stddef.h>
/* Whole SOCKS5 datagram -> matching DNS payload offset, -2 to ignore,
 * or -1 for an invalid call. The DNS transaction ID is in host byte order. */
intptr_t smartvpn_dns_relay_reply_v1(const uint8_t *packet, size_t len, uintptr_t id);
/* Same result contract for a STUN success reply and its 12-byte transaction ID. */
intptr_t smartvpn_stun_relay_reply_v1(const uint8_t *packet, size_t len, const uint8_t id[12]);
/* Parse one UDP ASSOCIATE reply prefix. Positive: exact bytes still needed.
 * -2: invalid version, -3: unknown address type, -(256+REP): refusal,
 * -(512+n): complete n-byte host:port, -1: unsupported call/buffer. */
intptr_t smartvpn_relay_endpoint_v1(const uint8_t *input, size_t len, uint8_t *output, size_t capacity);
/* DNS question (op 0, transaction ID), SOCKS5 envelope (op 1/3/4, port),
 * STUN binding request (op 2, input = 12 ID bytes and value = 0),
 * SOCKS greeting (op 5, empty input, value = 0), or UDP ASSOCIATE
 * request (op 6, method reply as input, value = 0; -3 rejects method).
 * Input is the name or raw address. Returns bytes written, -2 for a rejected
 * DNS name, or -1 for unsupported input/insufficient space (Go fallback). */
intptr_t smartvpn_udp_request_v1(uintptr_t op, const uint8_t *input, size_t len,
                                uintptr_t value, uint8_t *output, size_t capacity);
/* 1 for matching success type and transaction ID, 0 for mismatch, SIZE_MAX for null. */
uintptr_t smartvpn_stun_match_v1(const uint8_t id[12], const uint8_t header[20]);
/* Returns a byte offset for SOCKS5 UDP payload (op 1) or first DNS A
 * address (op 2). -1: unsupported/invalid call, -2: malformed envelope,
 * -3: unknown address type, -4: short DNS header, -5: truncated name,
 * -6: truncated DNS record/question, -7: no A address. */
intptr_t smartvpn_udp_reply_offset_v1(uintptr_t op, const uint8_t *packet, size_t len);
/* Version 1; Windows x64 bridge only. Caller owns all buffers.
 * parse: 0 success, 1 UTF-8 parser error, -1 invalid buffer/capacity.
 * Transaction ID verification is the caller's responsibility. */
int32_t smartvpn_parse_stun_v1(const uint8_t *input, size_t input_len,
                             uint8_t *output, size_t capacity, size_t *written);
/* Signed elapsed nanoseconds. Returns 0, or -1 for null output. */
int32_t smartvpn_throughput_v1(int64_t bytes, int64_t elapsed_ns, double *output);
/* Subscription operations: 1 Base64, 2 URI prefix, 3 multiline URI,
 * 4 announcement filter, 5 inline detection, 6 input classification.
 * UTF-8 input; invalid UTF-8 returns -1. 0 success, 1 decode error,
 * -2 needs more capacity (written = required bytes, no partial output).
 * Classification: two lists, u64 LE count followed by u64 length + UTF-8.
 * Bool results: one byte 0 or 1. Decode result: arbitrary bytes. */
int32_t smartvpn_subscription_v1(uint32_t op, const uint8_t *input, size_t input_len,
                               uint8_t *output, size_t capacity, size_t *written);
/* Node-field input: length-prefixed UTF-8 kind/server/port/name/secret,
 * followed by query key/value pairs. Typed output documented in node_wire.rs.
 * 0 success; 1 semantic error; 2 unsupported transport; -1 invalid wire;
 * -2 insufficient output capacity (written gives required bytes). */
int32_t smartvpn_node_fields_v1(const uint8_t *input, size_t input_len,
                              uint8_t *output, size_t capacity, size_t *written);
/* v2 uses the identical wire/status contract, advertising TUIC/Hysteria v1.
 * TUIC userinfo uses adapter-owned query keys, never remote query values. */
int32_t smartvpn_node_fields_v2(const uint8_t *input, size_t input_len,
                              uint8_t *output, size_t capacity, size_t *written);
int32_t smartvpn_node_fields_v3(const uint8_t *input, size_t input_len,
                              uint8_t *output, size_t capacity, size_t *written);
#endif
