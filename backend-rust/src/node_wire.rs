use crate::node_fields::{convert_node, ConversionError, NodeInput, Value};
use std::collections::BTreeMap;
fn read_string<'a>(bytes: &mut &'a [u8]) -> Option<&'a str> {
    if bytes.len() < 8 {
        return None;
    }
    let len = usize::try_from(u64::from_le_bytes(bytes[..8].try_into().ok()?)).ok()?;
    *bytes = &bytes[8..];
    if len > bytes.len() {
        return None;
    }
    let s = std::str::from_utf8(&bytes[..len]).ok()?;
    *bytes = &bytes[len..];
    Some(s)
}
fn write_string(out: &mut Vec<u8>, value: &str) {
    out.extend_from_slice(&(value.len() as u64).to_le_bytes());
    out.extend_from_slice(value.as_bytes());
}
fn encode(out: &mut Vec<u8>, value: &Value) {
    match value {
        Value::String(s) => {
            out.push(1);
            write_string(out, s);
        }
        Value::Bool(b) => {
            out.push(2);
            out.push(u8::from(*b));
        }
        Value::Int(i) => {
            out.push(3);
            out.extend_from_slice(&i.to_le_bytes());
        }
        Value::Array(a) => {
            out.push(4);
            out.extend_from_slice(&(a.len() as u64).to_le_bytes());
            for v in a {
                encode(out, v);
            }
        }
        Value::Object(m) => {
            out.push(5);
            out.extend_from_slice(&(m.len() as u64).to_le_bytes());
            for (k, v) in m {
                write_string(out, k);
                encode(out, v);
            }
        }
    }
}
fn convert_wire(mut input: &[u8]) -> Option<(Vec<u8>, i32)> {
    let kind = read_string(&mut input)?;
    let server = read_string(&mut input)?;
    let port = read_string(&mut input)?.parse::<i64>().ok()?;
    let name = read_string(&mut input)?;
    let secret = read_string(&mut input)?;
    if (kind == "vmess" && port == 0) || (kind != "vmess" && !(1..=65535).contains(&port)) {
        return None;
    }
    let mut query = BTreeMap::new();
    while !input.is_empty() {
        let k = read_string(&mut input)?;
        let v = read_string(&mut input)?;
        query.insert(k.to_owned(), v.to_owned());
    }
    match convert_node(NodeInput {
        kind,
        server,
        port,
        name,
        secret,
        query: &query,
    }) {
        Ok(map) => {
            let mut out = Vec::new();
            encode(&mut out, &Value::Object(map));
            Some((out, 0))
        }
        Err(ConversionError::Message(e)) => Some((e.into_bytes(), 1)),
        Err(ConversionError::UnsupportedTransport(e)) => Some((e.into_bytes(), 2)),
    }
}
/// Pure version-1 node field converter. Input: five length-prefixed UTF-8 strings
/// (kind, server, decimal port, name, secret) followed by query key/value pairs.
/// Output tags: 1 UTF-8 string, 2 bool byte, 3 i64 LE, 4 array, 5 object.
/// All string lengths and collection counts are u64 LE. Objects have untagged
/// string keys followed by typed values. Status 1 is a semantic error, 2 is an
/// unsupported transport (Go formats the original quoted error), -2 needs space.
///
/// # Safety
/// Input/output/written must be valid non-overlapping ranges for their lengths;
/// written must be aligned, input may be null only with zero length. No retention.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_node_fields_v1(
    input: *const u8,
    len: usize,
    output: *mut u8,
    capacity: usize,
    written: *mut usize,
) -> i32 {
    if output.is_null()
        || written.is_null()
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
    let Some((out, status)) = convert_wire(bytes) else {
        return -1;
    };
    *written = out.len();
    if capacity < out.len() {
        return -2;
    }
    std::ptr::copy_nonoverlapping(out.as_ptr(), output, out.len());
    status
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn malformed_wire_is_rejected() {
        for b in [vec![], vec![0], vec![255; 8], vec![0; 40]] {
            assert!(convert_wire(&b).is_none());
        }
    }
    #[test]
    fn primitive_wire_tags() {
        let mut o = Vec::new();
        encode(
            &mut o,
            &Value::Array(vec![
                Value::Bool(true),
                Value::Int(443),
                Value::String("x".into()),
            ]),
        );
        assert_eq!(o[0], 4);
        assert_eq!(u64::from_le_bytes(o[1..9].try_into().unwrap()), 3);
    }
}

#[cfg(test)]
mod boundary_tests {
    use super::*;
    #[test]
    fn capacity_and_error_contract() {
        let mut input = Vec::new();
        for s in ["trojan", "node.example.invalid", "443", "test", "secret"] {
            write_string(&mut input, s);
        }
        let mut out = [0u8; 512];
        let mut written = 0usize;
        unsafe {
            assert_eq!(
                smartvpn_node_fields_v1(
                    input.as_ptr(),
                    input.len(),
                    out.as_mut_ptr(),
                    1,
                    &mut written
                ),
                -2
            );
            assert!(written > 1);
            assert_eq!(
                smartvpn_node_fields_v1(
                    input.as_ptr(),
                    input.len(),
                    out.as_mut_ptr(),
                    512,
                    &mut written
                ),
                0
            );
            assert_eq!(out[0], 5);
            assert_eq!(
                smartvpn_node_fields_v1(std::ptr::null(), 1, out.as_mut_ptr(), 512, &mut written),
                -1
            );
            assert_eq!(
                smartvpn_node_fields_v1(std::ptr::null(), 0, out.as_mut_ptr(), 512, &mut written),
                -1
            );
        }
    }
}

/// Version 2 advertises TUIC and Hysteria v1 field conversion in addition to v1.
/// Wire format and return codes are unchanged. No network or persistence.
///
/// # Safety
/// Same pointer validity, alignment, length and non-overlap requirements as v1.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_node_fields_v2(
    input: *const u8,
    len: usize,
    output: *mut u8,
    capacity: usize,
    written: *mut usize,
) -> i32 {
    smartvpn_node_fields_v1(input, len, output, capacity, written)
}

/// Version 3 adds VMess, Shadowsocks and ShadowsocksR normalized fields.
/// Go retains URL/Base64/JSON preprocessing and supplies trusted internal fields.
/// # Safety
/// Same pointer validity and non-overlap requirements as version 1.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_node_fields_v3(
    input: *const u8,
    len: usize,
    output: *mut u8,
    capacity: usize,
    written: *mut usize,
) -> i32 {
    smartvpn_node_fields_v1(input, len, output, capacity, written)
}
