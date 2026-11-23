#ifndef SMARTVPN_CONFIG_CORE_H
#define SMARTVPN_CONFIG_CORE_H
#include <stddef.h>
#include <stdint.h>
/* No IO. Inputs/outputs caller-owned, pointers not retained.
 * Wire: each string is u64 LE byte length + UTF8 bytes.
 * 17 scalar strings: mixed port, controller port, process mode, quoted secret,
 * platform TUN snippet, entryDNS YAML snippet, chain YAML, quoted provider path,
 * quoted manual path, quoted China path, relay, DNS listen line, fake range,
 * manual name, chain exit, China name, decimal flags.
 * Then 8 lists, each starts with decimal count STRING followed by strings:
 * quoted filters, suffixes, DoH IPs, quoted game rules, quoted source rules,
 * quoted decided rules, quoted proxied rules, quoted rejected rules.
 * Flag bits 0..9: TUN,DNS,Android,source order,subscription,manual,China,
 * domestic,WebRTC,entryDNS present. Quoting/rule parsing stays in Go.
 * Max input8MiB, each list65536, output16MiB; unsupported falls back to Go.
 * op0 config,1 DNS,2 upstreams,3 domestic. Returns0 success,-1 invalid,
 * -2 capacity; written required on -2, no partial output.
 */
int32_t smartvpn_config_v1(uint32_t op,const uint8_t *input,size_t len,
 uint8_t *output,size_t cap,size_t *written);
#endif
