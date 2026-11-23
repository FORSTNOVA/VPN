//! Pure settings policy over platform-normalized values. No filesystem or network.
use std::collections::HashSet;
pub fn android_package(name: &str) -> bool {
    let parts: Vec<_> = name.split('.').collect();
    parts.len() >= 2
        && parts.iter().all(|p| {
            let b = p.as_bytes();
            !b.is_empty()
                && b[0].is_ascii_alphabetic()
                && b.iter().all(|c| c.is_ascii_alphanumeric() || *c == b'_')
        })
}
pub fn game_rule(entry: &str, clean: &str, executable_name: bool, absolute_exe: bool) -> String {
    if executable_name {
        return format!("PROCESS-NAME,{entry},DIRECT");
    }
    if !absolute_exe || entry.contains(['\r', '\n', '\0', '*', '?']) {
        return String::new();
    }
    if !clean.contains(',') {
        return format!("PROCESS-PATH,{clean},DIRECT");
    }
    let mut escaped = String::new();
    for c in clean.chars() {
        if c == ',' {
            escaped.push_str("\\x2c");
            continue;
        }
        if "\\.+*?()|[]{}^$".contains(c) {
            escaped.push('\\');
        }
        escaped.push(c);
    }
    format!("PROCESS-PATH-REGEX,^{escaped}$,DIRECT")
}
pub fn normalize(
    entries: &[(String, String, bool)],
    android: bool,
) -> Result<Vec<String>, &'static str> {
    let mut seen = HashSet::new();
    let mut out = Vec::new();
    for (name, key, valid) in entries {
        if name.is_empty() {
            continue;
        }
        if android && !android_package(name) {
            return Err(ANDROID_ERROR);
        }
        if !android && !valid {
            return Err(WINDOWS_ERROR);
        }
        if seen.insert(key) {
            out.push(name.clone());
        }
    }
    Ok(out)
}
pub fn valid_socks(port: i64, windows: bool) -> bool {
    windows && (0..=65535).contains(&port)
}
const ANDROID_ERROR: &str = "每行请输入一个完整的 Android 应用包名";
const WINDOWS_ERROR: &str = "请选择游戏 .exe，或输入完整路径或进程名";

fn read_strings(mut data: &[u8]) -> Option<Vec<String>> {
    let mut out = Vec::new();
    while !data.is_empty() {
        let len = u64::from_le_bytes(data.get(..8)?.try_into().ok()?);
        data = &data[8..];
        let len = usize::try_from(len).ok()?;
        out.push(std::str::from_utf8(data.get(..len)?).ok()?.to_owned());
        data = &data[len..];
    }
    Some(out)
}
fn encode(values: Vec<String>) -> Vec<u8> {
    let mut out = Vec::new();
    for value in values {
        out.extend_from_slice(&(value.len() as u64).to_le_bytes());
        out.extend_from_slice(value.as_bytes());
    }
    out
}
/// op1: [entry, clean, nameFlag, pathFlag]; op2/3: repeated name/key/valid triples,
/// op2 Android, op3 Windows. Flags must be 0/1. Status 0 success,1 semantic error,
/// -1 invalid, -2 capacity. No pointers retained; inputs <=8MiB.
/// # Safety
/// Input readable, output writable for specified ranges; written writable, all
/// ranges non-overlapping. Null data/output allowed only at zero length/capacity.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_settings_v1(
    op: u32,
    input: *const u8,
    len: usize,
    output: *mut u8,
    capacity: usize,
    written: *mut usize,
) -> i32 {
    if written.is_null() {
        return -1;
    }
    *written = 0;
    if len > 8 << 20 || (len > 0 && input.is_null()) || (capacity > 0 && output.is_null()) {
        return -1;
    }
    let data = if len == 0 {
        &[]
    } else {
        std::slice::from_raw_parts(input, len)
    };
    let Some(v) = read_strings(data) else {
        return -1;
    };
    let (out, status) = match op {
        1 if v.len() == 4
            && matches!(v[2].as_str(), "0" | "1")
            && matches!(v[3].as_str(), "0" | "1") =>
        {
            (
                game_rule(&v[0], &v[1], v[2] == "1", v[3] == "1").into_bytes(),
                0,
            )
        }
        2 | 3 if v.len().is_multiple_of(3) => {
            let mut entries = Vec::new();
            for c in v.chunks_exact(3) {
                if !matches!(c[2].as_str(), "0" | "1") {
                    return -1;
                }
                entries.push((c[0].clone(), c[1].clone(), c[2] == "1"));
            }
            match normalize(&entries, op == 2) {
                Ok(list) => (encode(list), 0),
                Err(e) => (e.as_bytes().to_vec(), 1),
            }
        }
        _ => return -1,
    };
    *written = out.len();
    if out.len() > capacity {
        return -2;
    }
    if !out.is_empty() {
        std::ptr::copy_nonoverlapping(out.as_ptr(), output, out.len());
    }
    status
}
#[no_mangle]
pub extern "C" fn smartvpn_valid_socks_v1(port: i64, windows: u32) -> i32 {
    if windows > 1 {
        -1
    } else {
        i32::from(valid_socks(port, windows == 1))
    }
}
