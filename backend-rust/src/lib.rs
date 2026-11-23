//! Opt-in SmartVPN compatibility planners and pure policies; platform effects remain Go-owned.
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr, SocketAddr};
mod android_tun_lease;
mod cached_nodes;
mod catalog_quota;
mod node_graph;
mod relay_endpoint;
mod udp_reply;
mod udp_request;

const COOKIE: u32 = 0x2112_a442;
const HEADER: usize = 20;

/// Matches Go throughputMbps, including signed duration/byte rejection.
pub fn throughput_mbps(bytes: i64, elapsed_ns: i64) -> f64 {
    if bytes <= 0 || elapsed_ns <= 0 {
        return 0.0;
    }
    // Match time.Duration.Seconds rounding, including very large durations.
    let seconds = (elapsed_ns / 1_000_000_000) as f64 + (elapsed_ns % 1_000_000_000) as f64 / 1e9;
    bytes as f64 * 8.0 / 1e6 / seconds
}

/// The caller must supply a fresh cryptographically random transaction ID.
/// This pure encoder deliberately does not own RNG, sockets, or platform state.
pub fn build_stun_request(transaction: [u8; 12]) -> [u8; HEADER] {
    let mut out = [0; HEADER];
    out[1] = 1;
    out[4..8].copy_from_slice(&COOKIE.to_be_bytes());
    out[8..].copy_from_slice(&transaction);
    out
}

/// Compatibility port of Go parseSTUNAddress, not a stricter protocol validator.
/// In particular it preserves Go's clipped-length parsing and plain-address fallback.
/// Transaction matching remains the responsibility of the network probe caller.
pub fn parse_stun_address(message: &[u8]) -> Result<String, &'static str> {
    if message.len() < HEADER {
        return Err("STUN 应答过短");
    }
    if u16::from_be_bytes([message[0], message[1]]) != 0x0101 {
        return Err("不是 STUN 绑定应答");
    }
    if message[4..8] != COOKIE.to_be_bytes() {
        return Err("STUN 应答缺少魔术字");
    }
    let end = (HEADER + u16::from_be_bytes([message[2], message[3]]) as usize).min(message.len());
    let mut offset = HEADER;
    let mut plain = None;
    while offset + 4 <= end {
        let kind = u16::from_be_bytes([message[offset], message[offset + 1]]);
        let size = u16::from_be_bytes([message[offset + 2], message[offset + 3]]) as usize;
        let value = &message[offset + 4..(offset + 4 + size).min(end)];
        match kind {
            0x0020 => {
                if let Ok(address) = read_address(value, Some(&message[8..20])) {
                    return Ok(address);
                }
            }
            0x0001 => {
                if let Ok(address) = read_address(value, None) {
                    plain = Some(address);
                }
            }
            _ => {}
        }
        offset += 4 + size + (4 - size % 4) % 4;
    }
    plain.ok_or("STUN 应答里没有地址")
}

fn read_address(value: &[u8], transaction: Option<&[u8]>) -> Result<String, &'static str> {
    if value.len() < 4 {
        return Err("STUN 地址字段过短");
    }
    let mut port = u16::from_be_bytes([value[2], value[3]]);
    let size = match value[1] {
        1 => {
            if value.len() < 8 {
                return Err("STUN IPv4 地址字段过短");
            }
            4
        }
        2 => {
            if value.len() < 20 {
                return Err("STUN IPv6 地址字段过短");
            }
            16
        }
        _ => return Err("STUN 地址类型未知"),
    };
    let mut raw = [0u8; 16];
    raw[..size].copy_from_slice(&value[4..4 + size]);
    if let Some(transaction) = transaction {
        port ^= (COOKIE >> 16) as u16;
        let mut mask = [0u8; 16];
        mask[..4].copy_from_slice(&COOKIE.to_be_bytes());
        mask[4..].copy_from_slice(transaction);
        for i in 0..size {
            raw[i] ^= mask[i];
        }
    }
    if port == 0 {
        return Err("STUN 端口无效");
    }
    let ip = if size == 4 {
        IpAddr::V4(Ipv4Addr::new(raw[0], raw[1], raw[2], raw[3]))
    } else {
        let ip = Ipv6Addr::from(raw);
        // Go net.IP.String renders IPv4-mapped IPv6 addresses as IPv4.
        match ip.to_ipv4_mapped() {
            Some(v4) => IpAddr::V4(v4),
            None => IpAddr::V6(ip),
        }
    };
    Ok(SocketAddr::new(ip, port).to_string())
}

/// C ABI: encode the result/error as UTF-8 into caller-owned memory.
/// Returns 0 for an address, 1 for a Go-compatible parser error, -1 for bad
/// pointers/capacity. This boundary performs no I/O and never retains pointers.
///
/// # Safety
/// Input must be readable for input_len bytes (null only when length is zero).
/// Output must be writable for capacity bytes; written must point to a writable
/// usize. Regions must not overlap. Caller must provide valid allocated ranges.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_parse_stun_v1(
    input: *const u8,
    input_len: usize,
    output: *mut u8,
    capacity: usize,
    written: *mut usize,
) -> i32 {
    if written.is_null()
        || output.is_null()
        || (input.is_null() && input_len != 0)
        || input_len > isize::MAX as usize
        || capacity > isize::MAX as usize
    {
        return -1;
    }
    *written = 0;
    let bytes = if input_len == 0 {
        &[]
    } else {
        std::slice::from_raw_parts(input, input_len)
    };
    let (text, status) = match parse_stun_address(bytes) {
        Ok(v) => (v, 0),
        Err(e) => (e.to_owned(), 1),
    };
    if text.len() > capacity {
        return -1;
    }
    std::ptr::copy_nonoverlapping(text.as_ptr(), output, text.len());
    *written = text.len();
    status
}

/// C ABI integer-only call surface for Go's Windows syscall bridge.
/// Returns 0 on success and -1 for a null output pointer.
///
/// # Safety
/// output must point to writable f64 storage aligned for f64.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_throughput_v1(
    bytes: i64,
    elapsed_ns: i64,
    output: *mut f64,
) -> i32 {
    if output.is_null() {
        return -1;
    }
    *output = throughput_mbps(bytes, elapsed_ns);
    0
}
mod node_metadata;
mod profile_yaml;
mod profile_yaml_events;
mod policy_yaml;
pub mod subscription;

/// Versioned pure subscription operation boundary. Operations 1..6 are decode,
/// URI prefix, multiline URI, notice filter, inline detection, classification.
/// Returns 0 for success, 1 for decode error, -1 for invalid input/op, -2 when
/// capacity is insufficient (written reports required bytes, no partial output).
/// Op 7 returns zero bytes for absent usage or four signed LE 64-bit counters.
/// Op 8 returns zero bytes or the start and exclusive end of the rule target
/// as two LE u64 byte offsets, including the target field's whitespace.
/// Ops 9 and 11 project a Clash YAML document into node metadata and raw proxy
/// objects respectively. Op 10 encodes the normalized merged document as YAML.
/// Ops 12 and 13 extend the merged writer and object reader with float tokens.
/// Op 14 projects subscription groups, rules, sections and entry DNS together.
/// Ops 15–18 handle a query-ready Clash summary, recovery document, manual
/// metadata overlay, and source-label/selection update flags.
/// Unsupported YAML representations return -1 for the compatibility decoder.
/// Classification encodes two lists, each u64 LE count then u64 lengths + UTF-8.
///
/// # Safety
/// input must be readable for len bytes (null allowed only for zero length).
/// output must be writable for capacity bytes and written must be valid/aligned.
/// All pointer ranges must be non-overlapping; no pointers are retained.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_subscription_v1(
    op: u32,
    input: *const u8,
    len: usize,
    output: *mut u8,
    capacity: usize,
    written: *mut usize,
) -> i32 {
    if written.is_null()
        || output.is_null()
        || (input.is_null() && len != 0)
        || len > isize::MAX as usize
        || capacity > isize::MAX as usize
    {
        return -1;
    }
    *written = 0;
    let bytes = if len == 0 {
        &[]
    } else {
        std::slice::from_raw_parts(input, len)
    };
    let text = match std::str::from_utf8(bytes) {
        Ok(v) => v,
        Err(_) => return -1,
    };
    let (result, status) = match op {
        1 => match subscription::decode_subscription_base64(text) {
            Ok(v) => (v, 0),
            Err(e) => (e.as_bytes().to_vec(), 1),
        },
        2 => (vec![u8::from(subscription::is_proxy_uri_link(text))], 0),
        3 => (vec![u8::from(subscription::has_proxy_uri(text))], 0),
        4 => (vec![u8::from(subscription::is_informational_node(text))], 0),
        5 => (
            vec![u8::from(subscription::is_inline_subscription(text))],
            0,
        ),
        6 => {
            let (remote, inline) = subscription::categorize_subscription_input(text);
            let mut encoded = Vec::new();
            for list in [remote, inline] {
                encoded.extend_from_slice(&(list.len() as u64).to_le_bytes());
                for item in list {
                    encoded.extend_from_slice(&(item.len() as u64).to_le_bytes());
                    encoded.extend_from_slice(item.as_bytes());
                }
            }
            (encoded, 0)
        }
        7 => (
            subscription::user_info(text)
                .into_iter()
                .flatten()
                .flat_map(i64::to_le_bytes)
                .collect(),
            0,
        ),
        8 => {
            let mut bounds = Vec::new();
            if let Some(field) = converter_names::rule_target(bytes) {
                bounds.extend_from_slice(&(field.start as u64).to_le_bytes());
                bounds.extend_from_slice(&(field.end as u64).to_le_bytes());
            }
            (bounds, 0)
        }
        9..=18 => {
            let result = match op {
                9 => profile_yaml::nodes(text),
                10 | 12 => profile_yaml::merged_yaml(text, op == 12),
                14 => policy_yaml::decode(text),
                15 => profile_yaml::query_nodes(text),
                16 => profile_yaml::repair_document(text),
                17 => node_metadata::manual_details(text),
                18 => node_metadata::annotation_flags(text),
                _ => profile_yaml::objects(text, op == 13),
            };
            match result {
                Some(bytes) => (bytes, 0),
                None => return -1,
            }
        }
        _ => return -1,
    };
    *written = result.len();
    if result.len() > capacity {
        return -2;
    }
    std::ptr::copy_nonoverlapping(result.as_ptr(), output, result.len());
    status
}
pub mod node_fields;
pub mod node_wire;
pub mod service_logic;
pub mod traffic_logic;

pub mod config_core;
pub mod http_core;

pub mod health_core;
pub mod health_flow;
pub mod health_flow_ffi;
pub mod profile_flow;
pub mod profile_flow_ffi;
pub mod registry_flow;
pub mod registry_flow_ffi;
pub mod settings_core;
pub mod settings_flow;
pub mod settings_flow_ffi;

pub mod acquisition_flow;
pub mod acquisition_merge;

pub mod quota_flow;

pub mod quota_flow_ffi;

pub mod connection_flow;

pub mod connection_flow_ffi;

pub mod kernel_flow;

pub mod kernel_flow_ffi;

pub mod kernel_exit_flow;

pub mod kernel_exit_flow_ffi;

pub mod region_flow;

pub mod region_flow_ffi;

pub mod region_job_flow;

pub mod region_job_flow_ffi;

pub mod manual_flow;

pub mod manual_flow_ffi;

pub mod chain_flow;

pub mod chain_flow_ffi;
pub mod chain_probe_flow;
pub mod chain_probe_flow_ffi;
pub mod diagnostic_flow;
pub mod diagnostic_flow_ffi;
pub mod path_flow;
pub mod path_flow_ffi;
pub mod path_monitor_flow;
pub mod path_monitor_flow_ffi;
pub mod path_monitor_logic;
pub mod path_switch;
pub mod traffic_flow;
pub mod traffic_flow_ffi;

pub mod node_query_flow;
pub mod node_query_flow_ffi;
pub mod node_query_logic;

pub mod labels_flow;
pub mod labels_flow_ffi;
pub mod lifecycle_flow;
pub mod lifecycle_flow_ffi;

pub mod service_state;
pub mod service_state_ffi;

pub mod path_state;
pub mod service_state_v2_ffi;

pub mod health_state;

pub mod service_state_v3_ffi;

pub mod control_state;
pub mod service_state_v4_ffi;

pub mod catalog_state;
pub mod service_state_v5_ffi;

pub mod china_state;

pub mod china_flow;

pub mod service_state_v6_ffi;

pub mod china_flow_ffi;

pub mod speed_flow;

pub mod speed_flow_ffi;

pub mod tun_report;

pub mod tun_report_text;

pub mod tun_report_ffi;

pub mod app_flow;

pub mod app_flow_ffi;

pub mod driver_install;
pub mod driver_install_ffi;
pub mod driver_install_text;

pub mod subscription_http_flow;
pub mod subscription_http_flow_ffi;

pub mod policy_dns;
pub mod policy_dns_ffi;
pub mod policy_dns_text;

pub mod bootstrap_flow;
pub mod bootstrap_flow_ffi;
pub mod privacy_log;
pub mod privacy_log_ffi;

pub mod adaptive_flow;

pub mod adaptive_flow_ffi;

pub mod android_resolver;

pub mod android_resolver_ffi;

pub mod converter_flow;

pub mod converter_names;

pub mod converter_flow_ffi;

pub mod node_front;

pub mod node_front_text;

pub mod node_front_ffi;

pub mod kernel_admission_flow;
pub mod kernel_admission_flow_ffi;

pub mod embedded_kernel;

pub mod embedded_kernel_text;

pub mod embedded_kernel_ffi;

pub mod traffic_projection;

pub mod metadata_projection;

pub mod metadata_projection_ffi;

pub mod metadata_projection_text;

pub mod http_dispatch_flow;
