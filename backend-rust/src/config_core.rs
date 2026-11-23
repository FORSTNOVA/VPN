//! Pure configuration assembly. v2 owns rule classification and TUN templates;
//! Go supplies standard quoting, split/trim and platform scalar facts.
//! The legacy v1 entry retains its original preassembled input contract.
struct Input<'a> {
    fields: Vec<&'a str>,
    lists: Vec<Vec<&'a str>>,
    direct_tcp: Vec<String>,
    flags: u32,
}
impl Input<'_> {
    fn flag(&self, bit: u32) -> bool {
        self.flags & (1 << bit) != 0
    }
}
fn read<'a>(data: &mut &'a [u8]) -> Option<&'a str> {
    let n = usize::try_from(u64::from_le_bytes(data.get(..8)?.try_into().ok()?)).ok()?;
    *data = data.get(8..)?;
    let value = std::str::from_utf8(data.get(..n)?).ok()?;
    *data = data.get(n..)?;
    Some(value)
}
fn parse(data: &[u8]) -> Option<Input<'_>> {
    let mut data = data;
    let mut fields = Vec::new();
    for _ in 0..17 {
        fields.push(read(&mut data)?);
    }
    let flags = fields[16].parse().ok()?;
    if flags > 1023 {
        return None;
    }
    let mut lists = Vec::new();
    for _ in 0..8 {
        let count: usize = read(&mut data)?.parse().ok()?;
        if count > 65536 || count > data.len() / 8 {
            return None;
        }
        let mut list = Vec::new();
        for _ in 0..count {
            list.push(read(&mut data)?);
        }
        lists.push(list);
    }
    if !data.is_empty() {
        return None;
    }
    Some(Input {
        fields,
        lists,
        direct_tcp: Vec::new(),
        flags,
    })
}
fn upstream(i: &Input<'_>, strict: bool, domestic_web: bool, relay: &str) -> String {
    if !strict {
        return "  proxy-server-nameserver:\n    - 223.5.5.5\n    - 119.29.29.29\n  default-nameserver:\n    - 223.6.6.6\n    - 119.29.29.29\n  nameserver:\n    - https://doh.pub/dns-query\n    - https://dns.alidns.com/dns-query\n".into();
    }
    let mut s = String::new();
    if domestic_web {
        s += "  nameserver-policy:\n";
        for suffix in &i.lists[1] {
            if i.flag(10) {
                s += &format!("    \"+.{suffix}\": \"https://223.5.5.5/dns-query#DIRECT\"\n");
            } else {
                s += &format!("    \"+.{suffix}\": [\"https://223.5.5.5/dns-query#DIRECT\", \"https://223.6.6.6/dns-query#DIRECT\"]\n");
            }
        }
    }
    s += "  proxy-server-nameserver:\n    - https://223.5.5.5/dns-query\n  default-nameserver:\n    - https://223.5.5.5/dns-query\n  nameserver:\n";
    if relay.is_empty() {
        s += "    - \"https://1.1.1.1/dns-query#SmartVPN\"\n    - \"https://8.8.8.8/dns-query#SmartVPN\"\n";
    } else {
        s += &format!("    - \"tcp://{relay}\"\n");
    }
    s
}
fn dns(i: &Input<'_>) -> String {
    let mut s = format!("dns:\n  enable: true\n  ipv6: false\n{}", i.fields[11]);
    if i.flag(2) {
        s += "  enhanced-mode: redir-host\n";
    } else {
        s+=&format!("  enhanced-mode: fake-ip\n  fake-ip-range: {}\n  fake-ip-filter:\n    - \"*.lan\"\n    - \"*.local\"\n    - \"*.arpa\"\n    - \"+.msftncsi.com\"\n    - \"www.msftconnecttest.com\"\n    - \"time.*.com\"\n",i.fields[12]);
        for f in &i.lists[0] {
            s += &format!("    - {f}\n");
        }
    }
    s + &upstream(i, i.flag(1), i.flag(7), i.fields[10])
}
fn add(s: &mut String, rule: &str) {
    *s += &format!("  - \"{rule}\"\n");
}
fn domestic(i: &Input<'_>) -> String {
    let mut s = String::new();
    if i.flag(1) {
        add(&mut s, "AND,((NETWORK,tcp),(DST-PORT,53/853)),SmartVPN");
        for d in ["dns.alidns.com", "doh.pub"] {
            add(&mut s, &format!("DOMAIN-SUFFIX,{d},SmartVPN"));
        }
        for a in ["223.5.5.5", "223.6.6.6", "120.53.53.53", "1.12.12.12"] {
            add(&mut s, &format!("IP-CIDR,{a}/32,SmartVPN,no-resolve"));
        }
    }
    if i.flag(8) {
        add(
            &mut s,
            "AND,((NETWORK,tcp),(DST-PORT,3478/5349/19302)),SmartVPN",
        );
    }
    for d in ["mihoyo.com", "biligame.com", "bilibili.com", "gov.cn"] {
        add(
            &mut s,
            &format!("AND,((NETWORK,tcp),(DOMAIN-SUFFIX,{d})),DIRECT"),
        );
    }
    for rule in &i.direct_tcp {
        add(&mut s, rule);
    }
    if i.flag(8) {
        if i.fields[2] == "always" {
            add(&mut s,&format!("AND,((NETWORK,udp),(DST-PORT,443),(OR,((PROCESS-NAME,chrome.exe),(PROCESS-NAME,msedge.exe),(PROCESS-NAME,firefox.exe),(PROCESS-NAME,brave.exe),(PROCESS-NAME,opera.exe),(PROCESS-NAME,vivaldi.exe),(PROCESS-NAME,arc.exe))),(RULE-SET,{})),REJECT",i.fields[15]));
        } else if i.flag(2) {
            add(
                &mut s,
                &format!(
                    "AND,((NETWORK,udp),(DST-PORT,443),(RULE-SET,{})),REJECT",
                    i.fields[15]
                ),
            );
        }
    }
    add(
        &mut s,
        &format!("AND,((NETWORK,tcp),(RULE-SET,{})),DIRECT", i.fields[15]),
    );
    s
}
fn rules(s: &mut String, list: &[&str]) {
    for rule in list {
        *s += &format!("  - {rule}\n");
    }
}
fn config(i: &Input<'_>) -> String {
    let f = &i.fields;
    let concurrent = if i.flag(10) {
        ""
    } else {
        "tcp-concurrent: true\n"
    };
    let process = if i.flag(10) {
        String::new()
    } else {
        format!("find-process-mode: {}\n", f[2])
    };
    let mut s=format!("mixed-port: {}\nallow-lan: false\n{concurrent}bind-address: 127.0.0.1\nmode: rule\nlog-level: info\n{process}external-controller: 127.0.0.1:{}\nsecret: {}\nprofile:\n  store-selected: true\n",f[0],f[1],f[3]);
    if i.flag(0) {
        s += f[4];
        s += &dns(i);
        if !i.flag(10) {
            s += f[5];
        }
    } else if i.flag(9) && !i.flag(10) {
        s += "dns:\n  enable: true\n";
        s += &upstream(i, false, false, "");
        s += f[5];
    }
    s += f[6];
    s += "proxy-providers:\n";
    let mut uses = String::new();
    if i.flag(4) {
        s+=&format!("  SmartVPNSubscription:\n    type: file\n    path: {}\n    health-check:\n      enable: false\n",f[7]);
        uses += "\n      - SmartVPNSubscription";
    }
    if i.flag(5) {
        s += &format!(
            "  {}:\n    type: file\n    path: {}\n    health-check:\n      enable: false\n",
            f[13], f[8]
        );
        uses += &format!("\n      - {}", f[13]);
    }
    s += "proxy-groups:\n";
    let chain = !f[6].is_empty();
    if chain {
        s += &format!(
            "  - name: SmartVPN\n    type: select\n    proxies:\n      - {}\n      - REJECT\n",
            f[14]
        );
    } else {
        s+=&format!("  - name: SmartVPNAuto\n    type: url-test\n    use:{uses}\n    url: https://www.youtube.com/generate_204\n    interval: 300\n    tolerance: 100\n  - name: SmartVPNFallback\n    type: fallback\n    use:{uses}\n    url: https://www.youtube.com/generate_204\n    interval: 300\n  - name: SmartVPN\n    type: select\n    use:{uses}\n    proxies:\n      - SmartVPNAuto\n      - SmartVPNFallback\n      - REJECT\n");
    }
    let private = i.flag(0) && (i.flag(1) || i.flag(7) || i.flag(8));
    if i.flag(6) && (!private || i.flag(7)) {
        s+=&format!("rule-providers:\n  {}:\n    type: file\n    behavior: ipcidr\n    format: text\n    path: {}\n",f[15],f[9]);
    }
    s += "rules:\n";
    if !f[10].is_empty() {
        for ip in &i.lists[2] {
            s += &format!(
                "  - AND,((NETWORK,tcp),(DST-PORT,443),(IP-CIDR,{ip}/32,no-resolve)),SmartVPN\n"
            );
        }
    }
    if i.flag(10) && i.flag(0) && i.flag(8) {
        s += "  - NETWORK,udp,REJECT\n";
    }
    if i.flag(0) && f[2] == "always" {
        rules(&mut s, &i.lists[3]);
    }
    let quic = "AND,((NETWORK,udp),(DST-PORT,443)),REJECT";
    if private {
        rules(&mut s, &i.lists[7]);
        if !i.flag(8) {
            add(&mut s, quic);
        }
        if i.flag(3) {
            rules(&mut s, &i.lists[6]);
        }
        if i.flag(7) && i.flag(6) {
            s += &domestic(i);
        }
        if i.flag(10) && !i.flag(8) && !chain {
            for rule in &i.direct_tcp {
                add(&mut s, &rule.replacen("(NETWORK,tcp)", "(NETWORK,udp)", 1));
            }
        }
        s += "  - MATCH,SmartVPN\n";
        if i.flag(8) || chain {
            s += "  - MATCH,REJECT\n";
        }
        return s;
    }
    if i.flag(3) {
        rules(&mut s, &i.lists[4]);
        if i.flag(6) {
            add(&mut s, &format!("RULE-SET,{},DIRECT", f[15]));
        }
        add(&mut s, quic);
    } else {
        rules(&mut s, &i.lists[5]);
        if i.flag(6) {
            add(&mut s, &format!("RULE-SET,{},DIRECT", f[15]));
        }
        add(&mut s, quic);
        rules(&mut s, &i.lists[6]);
    }
    s += "  - MATCH,SmartVPN\n";
    if chain {
        s += "  - MATCH,REJECT\n";
    }
    s
}
/// # Safety
/// Caller owns input/outputs; valid for declared lengths. written is writable.
/// op 0=config,1=DNS,2=upstreams,3=domestic; 0 success,-1 invalid,-2 need capacity.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_config_v1(
    op: u32,
    input: *const u8,
    len: usize,
    output: *mut u8,
    cap: usize,
    written: *mut usize,
) -> i32 {
    if written.is_null()
        || input.is_null()
        || len > 8 * 1024 * 1024
        || (output.is_null() && cap != 0)
    {
        return -1;
    }
    unsafe {
        *written = 0;
    }
    let data = unsafe { std::slice::from_raw_parts(input, len) };
    let Some(i) = parse(data) else {
        return -1;
    };
    let result = match op {
        0 => config(&i),
        1 => dns(&i),
        2 => upstream(&i, i.flag(1), i.flag(7), i.fields[10]),
        3 => domestic(&i),
        _ => return -1,
    };
    if result.len() > 16 * 1024 * 1024 {
        return -1;
    }
    unsafe {
        *written = result.len();
    }
    if result.len() > cap {
        return -2;
    }
    if !result.is_empty() {
        unsafe {
            std::ptr::copy_nonoverlapping(result.as_ptr(), output, result.len());
        }
    }
    0
}

fn unquote_safe_rule_value(quoted: &str) -> Option<&str> {
    let inner = quoted.strip_prefix('"')?.strip_suffix('"')?;
    if inner.is_empty()
        || !inner
            .bytes()
            .all(|b| (0x21..=0x7e).contains(&b) && !matches!(b, b'(' | b')' | b'"' | b'\\' | b','))
    {
        return None;
    }
    Some(inner)
}
fn domestic_direct_tcp_rule(parts: &[&str]) -> Option<String> {
    if parts.len() != 3 && parts.len() != 4 {
        return None;
    }
    let kind = match parts[0] {
        "\"DOMAIN\"" => "DOMAIN",
        "\"DOMAIN-SUFFIX\"" => "DOMAIN-SUFFIX",
        "\"DOMAIN-KEYWORD\"" => "DOMAIN-KEYWORD",
        "\"IP-CIDR\"" => "IP-CIDR",
        "\"IP-CIDR6\"" => "IP-CIDR6",
        _ => return None,
    };
    let value = unquote_safe_rule_value(parts[1])?;
    if parts[2] != "\"DIRECT\"" {
        return None;
    }
    if parts.len() == 4 {
        if parts[3] != "\"no-resolve\"" {
            return None;
        }
        return Some(format!(
            "AND,((NETWORK,tcp),({kind},{value},no-resolve)),DIRECT"
        ));
    }
    Some(format!("AND,((NETWORK,tcp),({kind},{value})),DIRECT"))
}
// v2 keeps all quoting and Unicode whitespace facts in Go, but owns target
// selection, stable classification and platform TUN template decisions.
fn parse_v2(data: &[u8]) -> Option<Input<'_>> {
    let mut data = data;
    let mut fields = Vec::new();
    for _ in 0..21 {
        fields.push(read(&mut data)?);
    }
    let flags = fields[16].parse().ok()?;
    if flags > 2047 || !fields[4].is_empty() {
        return None;
    }
    let mut lists = Vec::new();
    for _ in 0..5 {
        let count: usize = read(&mut data)?.parse().ok()?;
        if count > 65536 || count > data.len() / 8 {
            return None;
        }
        let mut list = Vec::new();
        for _ in 0..count {
            list.push(read(&mut data)?);
        }
        lists.push(list);
    }
    let presence = read(&mut data)?;
    if presence != "0" && presence != "1" {
        return None;
    }
    if presence == "0" && !lists[4].is_empty() {
        return None;
    }
    let (mut decided, mut proxied, mut reject, mut direct_tcp) =
        (Vec::new(), Vec::new(), Vec::new(), Vec::new());
    for rule in &lists[4] {
        let count: usize = read(&mut data)?.parse().ok()?;
        if count > 65536 || count > data.len() / 8 {
            return None;
        }
        let mut target = "";
        let mut head = [""; 4];
        for j in 0..count {
            let part = read(&mut data)?;
            if j < 4 {
                head[j] = part;
            }
            if j == 2 {
                target = part;
            }
        }
        if target == "\"DIRECT\"" || target == "\"REJECT\"" {
            decided.push(*rule);
            if target == "\"REJECT\"" {
                reject.push(*rule);
            } else if count == 3 || count == 4 {
                if let Some(tcp_rule) = domestic_direct_tcp_rule(&head[..count]) {
                    direct_tcp.push(tcp_rule);
                }
            }
        } else {
            proxied.push(*rule);
        }
    }
    if !data.is_empty() {
        return None;
    }
    lists.extend([decided, proxied, reject]);
    Some(Input {
        fields,
        lists,
        direct_tcp,
        flags,
    })
}
fn tun(i: &Input<'_>) -> String {
    if i.flag(10) {
        format!("tun:\n  enable: true\n  device: {}\n  route-all: true\n  dns-hijack: true\n  mtu: {}\n", i.fields[18], i.fields[19])
    } else if i.flag(2) {
        format!("tun:\n  enable: true\n  stack: {}\n  file-descriptor: {}\n  auto-route: false\n  auto-detect-interface: false\n  dns-hijack:\n    - any:53\n    - tcp://any:53\n  mtu: {}\n",i.fields[17],i.fields[20],i.fields[19])
    } else {
        format!("tun:\n  enable: true\n  stack: {}\n  device: {}\n  auto-route: true\n  auto-detect-interface: true\n{}  dns-hijack:\n    - any:53\n    - tcp://any:53\n  mtu: {}\n",i.fields[17],i.fields[18],if i.flag(1){"  strict-route: true\n"}else{""},i.fields[19])
    }
}
#[no_mangle]
pub extern "C" fn smartvpn_config_version_v2() -> u32 {
    2
}
/// Pure config ABI. Inputs/outputs must reference readable/writable buffers of
/// the supplied lengths; written must be a valid aligned pointer.
/// # Safety
/// The caller owns these buffers and must not alias input with output.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_config_v2(
    op: u32,
    input: *const u8,
    len: usize,
    output: *mut u8,
    cap: usize,
    written: *mut usize,
) -> i32 {
    if written.is_null()
        || !(written as usize).is_multiple_of(std::mem::align_of::<usize>())
        || input.is_null()
        || len > 8 * 1024 * 1024
        || (output.is_null() && cap != 0)
    {
        return -1;
    }
    unsafe {
        *written = 0;
    }
    let data = unsafe { std::slice::from_raw_parts(input, len) };
    let Some(mut i) = parse_v2(data) else {
        return -1;
    };
    let template = tun(&i);
    i.fields[4] = &template;
    let result = match op {
        0 => config(&i),
        1 => dns(&i),
        2 => upstream(&i, i.flag(1), i.flag(7), i.fields[10]),
        3 => domestic(&i),
        4 => template,
        _ => return -1,
    };
    if result.len() > 16 * 1024 * 1024 {
        return -1;
    }
    unsafe {
        *written = result.len();
    }
    if result.len() > cap {
        return -2;
    }
    if !result.is_empty() {
        unsafe {
            std::ptr::copy_nonoverlapping(result.as_ptr(), output, result.len());
        }
    }
    0
}
#[cfg(test)]
mod tests {
    use super::*;
    fn input() -> Vec<u8> {
        let fields = [
            "1",
            "2",
            "always",
            "\"synthetic\"",
            "",
            "",
            "",
            "\"profile\"",
            "\"manual\"",
            "\"china\"",
            "",
            "",
            "198.18.0.1/16",
            "manual",
            "exit",
            "china",
            "0",
        ];
        let mut v = Vec::new();
        for s in fields.into_iter().chain(std::iter::repeat_n("0", 8)) {
            v.extend_from_slice(&(s.len() as u64).to_le_bytes());
            v.extend_from_slice(s.as_bytes());
        }
        v
    }
    #[test]
    fn malformed_input() {
        assert!(parse(&[]).is_none());
        let mut v = input();
        v.push(0);
        assert!(parse(&v).is_none());
        assert!(parse(&[255; 32]).is_none());
    }
    #[test]
    fn capacity_contract() {
        unsafe {
            let v = input();
            let mut n = 0;
            assert_eq!(
                smartvpn_config_v1(0, v.as_ptr(), v.len(), std::ptr::null_mut(), 0, &mut n),
                -2
            );
            assert!(n > 100);
            let mut output = vec![0; n];
            assert_eq!(
                smartvpn_config_v1(
                    0,
                    v.as_ptr(),
                    v.len(),
                    output.as_mut_ptr(),
                    output.len(),
                    &mut n
                ),
                0
            );
            assert!(std::str::from_utf8(&output)
                .unwrap()
                .ends_with("  - MATCH,SmartVPN\n"));
            assert_eq!(
                smartvpn_config_v1(
                    4,
                    v.as_ptr(),
                    v.len(),
                    output.as_mut_ptr(),
                    output.len(),
                    &mut n
                ),
                -1
            );
        }
    }
}

#[cfg(test)]
mod v2_tests {
    use super::*;
    fn put(b: &mut Vec<u8>, s: &str) {
        b.extend_from_slice(&(s.len() as u64).to_le_bytes());
        b.extend_from_slice(s.as_bytes());
    }
    fn wire(flags: u32, rules: &[(&str, Vec<&str>)], presence: bool) -> Vec<u8> {
        let mut b = Vec::new();
        for s in [
            "1",
            "2",
            "always",
            "\"synthetic\"",
            "",
            "",
            "",
            "\"p\"",
            "\"m\"",
            "\"c\"",
            "",
            "",
            "198.18.0.1/16",
            "manual",
            "exit",
            "china",
            &flags.to_string(),
            "mixed",
            "SmartVPN",
            "9000",
            "-1",
        ] {
            put(&mut b, s);
        }
        for _ in 0..4 {
            put(&mut b, "0");
        }
        put(&mut b, &rules.len().to_string());
        for (r, _) in rules {
            put(&mut b, r);
        }
        put(&mut b, if presence { "1" } else { "0" });
        for (_, parts) in rules {
            put(&mut b, &parts.len().to_string());
            for p in parts {
                put(&mut b, p);
            }
        }
        b
    }
    #[test]
    fn stable_third_field_and_reject_subset() {
        let b = wire(
            0,
            &[
                ("r1", vec!["x", "y", "\"DIRECT\""]),
                ("r2", vec!["x", "y", "\"REJECT\""]),
                ("r3", vec!["x", "\"DIRECT\""]),
                ("r4", vec!["AND", "((x", "\"REJECT\"", "other"]),
                ("r5", vec!["x", "y", "\"direct\""]),
            ],
            true,
        );
        let i = parse_v2(&b).unwrap();
        assert_eq!(i.lists[5], vec!["r1", "r2", "r4"]);
        assert_eq!(i.lists[6], vec!["r3", "r5"]);
        assert_eq!(i.lists[7], vec!["r2", "r4"]);
    }
    #[test]
    fn nil_empty_and_presence_validation() {
        for presence in [false, true] {
            assert!(parse_v2(&wire(0, &[], presence)).is_some());
        }
        assert!(parse_v2(&wire(0, &[("x", vec![])], false)).is_none());
    }
    #[test]
    fn platform_template_strict_and_fd() {
        for strict in [false, true] {
            for android in [false, true] {
                let b = wire(
                    (u32::from(strict) << 1) | (u32::from(android) << 2),
                    &[],
                    false,
                );
                let i = parse_v2(&b).unwrap();
                let t = tun(&i);
                assert_eq!(t.contains("strict-route"), strict && !android);
                assert_eq!(t.contains("file-descriptor: -1"), android);
                assert_eq!(t.contains("device: SmartVPN"), !android);
                assert!(t.ends_with("  mtu: 9000\n"));
            }
        }
    }
    #[test]
    fn exact_capacity_and_old_wire_rejection() {
        let b = wire(0, &[], false);
        let mut n = 0;
        assert_eq!(smartvpn_config_version_v2(), 2);
        unsafe {
            assert_eq!(
                smartvpn_config_v2(4, b.as_ptr(), b.len(), std::ptr::null_mut(), 0, &mut n),
                -2
            );
            let mut out = vec![0; n];
            assert_eq!(
                smartvpn_config_v2(4, b.as_ptr(), b.len(), out.as_mut_ptr(), out.len(), &mut n),
                0
            );
            assert_eq!(out, tun(&parse_v2(&b).unwrap()).as_bytes());
            assert_eq!(
                smartvpn_config_v1(0, b.as_ptr(), b.len(), out.as_mut_ptr(), out.len(), &mut n),
                -1
            );
        }
    }
    #[test]
    fn misaligned_written_pointer_is_rejected_without_write() {
        let b = wire(0, &[], false);
        let mut words = [usize::MAX; 2];
        let written = unsafe { words.as_mut_ptr().cast::<u8>().add(1).cast::<usize>() };
        assert_eq!(
            unsafe { smartvpn_config_v2(4, b.as_ptr(), b.len(), std::ptr::null_mut(), 0, written) },
            -1
        );
        assert_eq!(words, [usize::MAX; 2]);
    }
    #[test]
    fn every_truncation_and_trailing_byte_reject() {
        let b = wire(
            0,
            &[("\"x,y,REJECT\"", vec!["\"x\"", "\"y\"", "\"REJECT\""])],
            true,
        );
        for n in 0..b.len() {
            assert!(parse_v2(&b[..n]).is_none());
        }
        let mut bad = b;
        bad.push(0);
        assert!(parse_v2(&bad).is_none());
    }
}
