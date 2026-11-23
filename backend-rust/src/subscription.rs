//! Pure compatibility ports of SmartVPN subscription input classification.
//! Deliberately preserves existing Go quirks; no downloads or configuration I/O.

const PROXY_SCHEMES: [&str; 10] = [
    "vmess://",
    "vless://",
    "ss://",
    "ssr://",
    "trojan://",
    "hysteria2://",
    "hy2://",
    "hysteria://",
    "tuic://",
    "anytls://",
];

// unicode.IsSpace, as used by strings.TrimSpace/Fields, not ASCII-only trimming.
fn go_space(c: char) -> bool {
    matches!(c, '\u{0009}'..='\u{000d}' | '\u{0020}' | '\u{0085}' | '\u{00a0}'
        | '\u{1680}' | '\u{2000}'..='\u{200a}' | '\u{2028}' | '\u{2029}'
        | '\u{202f}' | '\u{205f}' | '\u{3000}')
}
fn trim_go(text: &str) -> &str {
    text.trim_matches(go_space)
}

/// Match the legacy header contract: any valid numeric pair marks the header
/// present, including unknown keys. The last valid value for each key wins.
pub(crate) fn user_info(header: &str) -> Option<[i64; 4]> {
    let mut counters = [0; 4];
    let mut present = false;
    for pair in header.split(';') {
        let Some((key, raw)) = pair.split_once('=') else {
            continue;
        };
        let Ok(value) = trim_go(raw).parse::<i64>() else {
            continue;
        };
        present = true;
        if let Some(i) = ["upload", "download", "total", "expire"]
            .iter()
            .position(|name| {
                trim_go(key)
                    .chars()
                    .map(|c| c.to_lowercase().next().unwrap_or(c))
                    .eq(name.chars())
            })
        {
            counters[i] = value;
        }
    }
    present.then_some(counters)
}

/// Supports standard/URL-safe, padded/raw Base64 like the four Go encodings.
/// Whitespace is removed anywhere. Noncanonical unused tail bits are accepted.
pub fn decode_subscription_base64(input: &str) -> Result<Vec<u8>, &'static str> {
    let compact: Vec<u8> = input
        .chars()
        .filter(|c| !go_space(*c))
        .collect::<String>()
        .into_bytes();
    for (url, padded) in [(false, true), (false, false), (true, true), (true, false)] {
        if let Some(decoded) = decode_base64(&compact, url, padded) {
            return Ok(decoded);
        }
    }
    Err("invalid Base64 subscription")
}

fn decode_base64(input: &[u8], url: bool, padded: bool) -> Option<Vec<u8>> {
    let data_len = input.iter().position(|&b| b == b'=').unwrap_or(input.len());
    let pads = input.len() - data_len;
    if padded {
        if !input.len().is_multiple_of(4) {
            return None;
        }
        if pads != 0 && !((pads == 2 && data_len % 4 == 2) || (pads == 1 && data_len % 4 == 3)) {
            return None;
        }
        if !input[data_len..].iter().all(|&b| b == b'=') {
            return None;
        }
    } else if pads != 0 || data_len % 4 == 1 {
        return None;
    }
    let mut output = Vec::with_capacity(data_len / 4 * 3 + 2);
    let mut bits: u32 = 0;
    let mut count = 0;
    for &byte in &input[..data_len] {
        let n = match byte {
            b'A'..=b'Z' => byte - b'A',
            b'a'..=b'z' => byte - b'a' + 26,
            b'0'..=b'9' => byte - b'0' + 52,
            b'+' if !url => 62,
            b'/' if !url => 63,
            b'-' if url => 62,
            b'_' if url => 63,
            _ => return None,
        };
        bits = (bits << 6) | u32::from(n);
        count += 6;
        if count >= 8 {
            count -= 8;
            output.push((bits >> count) as u8);
        }
        // Retain only the incomplete byte so shifts cannot overflow in debug.
        bits &= (1 << count) - 1;
    }
    Some(output)
}

pub fn is_proxy_uri_link(text: &str) -> bool {
    // Go uses Unicode simple lowercase; Rust's full lowercase expands U+0130.
    // Go maps it to ASCII i, which matters for e.g. HYSTERİA://.
    let lower: String = trim_go(text)
        .chars()
        .map(|c| {
            if c == '\u{0130}' {
                'i'
            } else {
                c.to_lowercase().next().unwrap_or(c)
            }
        })
        .collect();
    PROXY_SCHEMES.iter().any(|scheme| lower.starts_with(scheme))
}

/// Go's multiline regexp is case-sensitive and starts only at byte 0 or after LF.
/// Unlike is_proxy_uri_link, it intentionally does not recognize anytls://.
pub fn has_proxy_uri(text: &str) -> bool {
    text.split('\n')
        .any(|line| PROXY_SCHEMES[..9].iter().any(|s| line.starts_with(s)))
}

pub fn is_informational_node(name: &str) -> bool {
    [
        "若您未看见节点",
        "客户端版本低",
        "获取最新客户端",
        "请到",
        "永久网址",
        "剩余流量",
        "过期时间",
        "到期时间",
        "距离下次重置",
        "重置剩余",
        "网站客服",
        "联系网站客服",
        "点击订阅",
        "点击更新",
        "官方网址",
        "续费",
    ]
    .iter()
    .any(|marker| name.contains(marker))
}

pub fn is_inline_subscription(raw: &str) -> bool {
    let trimmed = trim_go(raw);
    if trimmed.is_empty() {
        return false;
    }
    if trimmed.starts_with("proxies:")
        || trimmed.contains("\nproxies:")
        || trimmed.starts_with('{')
        || has_proxy_uri(trimmed)
    {
        return true;
    }
    if let Ok(decoded) = decode_subscription_base64(trimmed) {
        // Go strings can contain invalid UTF-8. Lossy decoding preserves all ASCII
        // delimiters/prefixes here and leaves invalid bytes non-whitespace.
        let decoded = String::from_utf8_lossy(&decoded);
        let decoded = trim_go(&decoded);
        if has_proxy_uri(decoded) || decoded.starts_with("proxies:") || decoded.starts_with('{') {
            return true;
        }
    }
    false
}

pub fn categorize_subscription_input(raw: &str) -> (Vec<String>, Vec<String>) {
    let raw = raw.replace("\r\n", "\n").replace('\r', "\n");
    let mut remote = Vec::new();
    let mut inline = Vec::new();
    for part in raw.split(['|', '\n']) {
        let part = trim_go(part);
        if part.is_empty() {
            continue;
        }
        if part.starts_with("http://") || part.starts_with("https://") {
            remote.push(part.to_owned());
        } else if is_proxy_uri_link(part) || is_inline_subscription(part) {
            inline.push(part.to_owned());
        }
    }
    if remote.is_empty() && inline.is_empty() && is_inline_subscription(&raw) {
        inline.push(raw);
    }
    (remote, inline)
}
