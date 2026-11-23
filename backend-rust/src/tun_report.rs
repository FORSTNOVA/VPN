//! Platform-specific TUN reports. Replay is pure: only the driver executes yielded effects.
use crate::tun_report_text::TEXT;
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Request {
    pub op: String,
    pub name: String,
    pub ipv6: bool,
}
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Wintun {
    pub installed: bool,
    pub path: String,
    pub version: String,
    pub sha256: String,
    pub signer: String,
    pub detail: String,
}
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Adapter {
    pub name: String,
    pub present: bool,
    pub status: String,
    pub ipv4_route: bool,
    pub ipv6_route: bool,
    pub route_via: Vec<String>,
}
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Reply {
    pub flag: bool,
    pub number: i64,
    pub text: String,
    pub names: Vec<String>,
    pub other: Vec<String>,
    pub error: Option<String>,
    pub wintun: Wintun,
    pub adapter: Adapter,
    pub exit_address: String,
    pub exit_error: String,
    pub region_country: String,
    pub region_exit: String,
    pub names_nil: bool,
    pub other_nil: bool,
}
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Effect {
    pub kind: String,
    pub text: String,
    pub result: String,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Invalid,
    Limit,
    Done,
}
#[derive(Clone, Debug)]
enum J {
    Null,
    Bool(bool),
    Num(i64),
    Str(String),
    Arr(Vec<J>),
    Obj(Vec<(String, J)>),
}
fn quote(s: &str) -> String {
    let mut o = String::from("\"");
    for c in s.chars() {
        match c {
            '"' => o.push_str("\\\""),
            '\\' => o.push_str("\\\\"),
            '\n' => o.push_str("\\n"),
            '\r' => o.push_str("\\r"),
            '\t' => o.push_str("\\t"),
            c if c < ' ' => o.push_str(&format!("\\u{:04x}", c as u32)),
            c => o.push(c),
        }
    }
    o.push('"');
    o
}
impl J {
    fn json(&self) -> String {
        match self {
            Self::Null => "null".into(),
            Self::Bool(v) => v.to_string(),
            Self::Num(v) => v.to_string(),
            Self::Str(v) => quote(v),
            Self::Arr(v) => format!(
                "[{}]",
                v.iter().map(Self::json).collect::<Vec<_>>().join(",")
            ),
            Self::Obj(v) => format!(
                "{{{}}}",
                v.iter()
                    .map(|(k, v)| format!("{}:{}", quote(k), v.json()))
                    .collect::<Vec<_>>()
                    .join(",")
            ),
        }
    }
}
fn st(v: impl Into<String>) -> J {
    J::Str(v.into())
}
fn obj(v: Vec<(&str, J)>) -> J {
    J::Obj(v.into_iter().map(|(k, v)| (k.into(), v)).collect())
}
fn arr(v: &[String]) -> J {
    J::Arr(v.iter().cloned().map(st).collect())
}
fn fmt(t: &str, args: &[String]) -> String {
    let mut out = String::new();
    let mut chars = t.chars();
    let mut index = 0;
    while let Some(c) = chars.next() {
        if c != '%' {
            out.push(c);
            continue;
        }
        match chars.next() {
            Some('%') => out.push('%'),
            Some(v @ ('s' | 'd' | 'q')) => {
                if let Some(arg) = args.get(index) {
                    out.push_str(arg);
                    index += 1
                } else {
                    out.push('%');
                    out.push(v)
                }
            }
            Some(v) => {
                out.push('%');
                out.push(v)
            }
            None => out.push('%'),
        }
    }
    out
}

#[derive(Clone)]
struct Check {
    name: String,
    state: String,
    value: String,
    detail: String,
}
impl Check {
    fn new(name: impl Into<String>, state: &str, detail: impl Into<String>) -> Self {
        Self {
            name: name.into(),
            state: state.into(),
            value: String::new(),
            detail: detail.into(),
        }
    }
    fn json(&self) -> J {
        let mut v = vec![("name", st(&self.name)), ("state", st(&self.state))];
        if !self.value.is_empty() {
            v.push(("value", st(&self.value)))
        }
        v.push(("detail", st(&self.detail)));
        obj(v)
    }
}
const TUNREPORTNOADMIN: &str = "TUN 需要管理员权限：请以管理员身份重启 SmartVPN";
const TUNREPORTNOWINTUN: &str = "缺少 wintun.dll：请先下载，或在设置中指定它的路径";
const TUNREPORTNOAUTH: &str = "VPN 未授权：请在系统的连接请求里允许 SmartVPN";
const TUNREPORTNOFD: &str = "隧道尚未建立：先打开连接开关";
const TUNREPORTNOTINSTALLED: &str = "尚未安装";

const IPV6_DISABLED:&str="IPv6 未启用：订阅节点通常只有 IPv4 出口，而隧道会接管 IPv6 默认路由，若下发 AAAA 记录，每个连接都会先尝试一条走不通的路径";

struct Engine<'a> {
    replies: &'a [Reply],
    pos: usize,
}
type Step<T> = Result<T, Effect>;
impl Engine<'_> {
    fn call(&mut self, k: &str, t: &str) -> Step<Reply> {
        if let Some(r) = self.replies.get(self.pos) {
            self.pos += 1;
            Ok(r.clone())
        } else {
            Err(Effect {
                kind: k.into(),
                text: t.into(),
                result: String::new(),
            })
        }
    }
    fn wintun(&mut self) -> Step<Wintun> {
        let target = self.call("wintun-path", "")?.text;
        let mut s = Wintun {
            path: target.clone(),
            ..Default::default()
        };
        let r = self.call("wintun-stat", &target)?;
        if r.error.is_some() || r.flag {
            s.detail = TUNREPORTNOTINSTALLED.into();
            return Ok(s);
        }
        s.installed = true;
        s.version = "0.14.1".into();
        s.signer = "WireGuard LLC".into();
        let h = self.call("wintun-hash", &target)?;
        if h.error.is_none() {
            s.sha256 = h.text
        }
        Ok(s)
    }
    fn adapter(&mut self) -> Step<Result<Adapter, String>> {
        let r = self.call("adapter-read", "SmartVPN")?;
        if let Some(e) = r.error {
            return Ok(Err(e));
        }
        let r = self.call("adapter-parse", "SmartVPN")?;
        if let Some(e) = r.error {
            return Ok(Err(e));
        }
        let mut a = Adapter {
            name: "SmartVPN".into(),
            present: r.flag,
            status: r.text,
            ..Default::default()
        };
        for (i, prefix) in r.names.iter().enumerate() {
            let Some(alias) = r.other.get(i) else {
                return Ok(Err("invalid adapter route facts".into()));
            };
            if !self.call("adapter-alias", alias)?.flag {
                continue;
            }
            a.route_via.push(prefix.clone());
            a.ipv4_route |= prefix == "0.0.0.0/0";
            a.ipv6_route |= prefix == "::/0";
        }
        Ok(Ok(a))
    }
    fn unavailable(&mut self, android: bool) -> Step<String> {
        if android {
            let r = self.call("tunnel-state", "")?;
            return Ok(if !r.flag {
                TUNREPORTNOAUTH
            } else if r.number <= 0 {
                TUNREPORTNOFD
            } else {
                ""
            }
            .into());
        }
        if !self.call("elevated", "")?.flag {
            return Ok(TUNREPORTNOADMIN.into());
        }
        if !self.wintun()?.installed {
            return Ok(TUNREPORTNOWINTUN.into());
        }
        Ok(String::new())
    }
    fn dns(&mut self) -> Step<Check> {
        let mut c = Check::new(TEXT[29], "fail", "");
        let r = self.call("lookup", "")?;
        if r.error.is_some() || r.names.is_empty() {
            c.detail = TEXT[30].into();
            return Ok(c);
        }
        c.value = r.names.join(", ");
        for address in r.names {
            if self.call("fake-address", &address)?.flag {
                c.state = "ok".into();
                c.detail = TEXT[31].into();
                return Ok(c);
            }
        }
        c.detail = TEXT[32].into();
        Ok(c)
    }
    fn node(&mut self) -> Step<Check> {
        let mut c = Check::new(TEXT[33], "warn", "");
        if self.call("read-profile", "")?.error.is_some() {
            c.detail = TEXT[34].into();
            return Ok(c);
        }
        let host = self.call("hostname", "")?.text;
        if host.is_empty() {
            c.state = "ok".into();
            c.detail = TEXT[35].into();
            return Ok(c);
        }
        c.value = host.clone();
        let r = self.call("lookup", &host)?;
        if r.error.is_some() || r.names.is_empty() {
            c.state = "fail".into();
            c.detail = TEXT[36].into();
            return Ok(c);
        }
        c.value = format!("{}{}{}", host, TEXT[37], r.names.join(", "));
        for a in r.names {
            if self.call("fake-address", &a)?.flag {
                c.state = "fail".into();
                c.detail = TEXT[38].into();
                return Ok(c);
            }
        }
        c.state = "ok".into();
        c.detail = TEXT[39].into();
        Ok(c)
    }
    fn path(&mut self, name: &str) -> Step<Check> {
        let mut c = Check::new(name, "", "");
        let t = self.call("exit-tunnel", "")?;
        let s = self.call("exit-system", "")?;
        if t.exit_address.is_empty() && s.exit_address.is_empty() {
            c.state = "warn".into();
            c.detail = fmt(TEXT[48], &[t.exit_error, s.exit_error])
        } else if s.exit_address.is_empty() {
            c.state = "warn".into();
            c.value = t.exit_address;
            c.detail = format!("{}{}", TEXT[49], s.exit_error)
        } else if t.exit_address.is_empty() {
            c.state = "fail".into();
            c.value = s.exit_address;
            c.detail = TEXT[50].into()
        } else if s.exit_address == t.exit_address {
            c.state = "ok".into();
            c.value = s.exit_address;
            c.detail = TEXT[51].into()
        } else {
            c.state = "fail".into();
            c.value = fmt(TEXT[52], &[s.exit_address, t.exit_address]);
            c.detail = TEXT[53].into()
        }
        Ok(c)
    }
    fn region(&mut self) -> Step<Check> {
        let mut c = Check::new(TEXT[40], "warn", "");
        let node = self.call("active-node", "")?.text;
        let record = self.call("region", &node)?;
        if !record.flag || record.region_exit.is_empty() {
            c.detail = TEXT[41].into();
            return Ok(c);
        }
        let measured = self.call("exit-tunnel", "")?;
        if measured.exit_address.is_empty() {
            c.state = "fail".into();
            c.detail = format!("{}{}", TEXT[42], measured.exit_error);
            return Ok(c);
        }
        c.value = format!(
            "{}{}{}",
            measured.exit_address, TEXT[43], record.region_country
        );
        if measured.exit_address != record.region_exit {
            c.state = "fail".into();
            c.detail = fmt(TEXT[44], &[record.region_exit, record.region_country]);
            return Ok(c);
        }
        if !self.call("locked-region", "")?.text.is_empty() && !self.call("locked-region", "")?.flag
        {
            c.state = "fail".into();
            c.detail = fmt(
                TEXT[45],
                &[self.call("locked-region", "")?.text, record.region_country],
            );
            return Ok(c);
        }
        c.state = "ok".into();
        c.detail = if self.call("locked-region", "")?.text.is_empty() {
            TEXT[46]
        } else {
            TEXT[47]
        }
        .into();
        Ok(c)
    }
    fn windows_status(&mut self) -> Step<J> {
        self.call("lock", "")?;
        let reason = self.unavailable(false)?;
        let mode = self.call("mode", "")?.text;
        let elevated = self.call("elevated", "")?.flag;
        let w = self.wintun()?;
        let active = self.call("running", "")?.flag && self.call("mode", "")?.text == "tun";
        Ok(obj(vec![
            ("mode", st(mode)),
            ("elevated", J::Bool(elevated)),
            ("wintun", w.json()),
            ("interface", st("SmartVPN")),
            ("stack", st("gvisor")),
            ("mtu", J::Num(1500)),
            ("available", J::Bool(reason.is_empty())),
            ("reason", st(reason)),
            ("active", J::Bool(active)),
        ]))
    }
    fn windows_checks(&mut self) -> Step<J> {
        self.call("lock", "")?;
        let admin = self.call("elevated", "")?.flag;
        let mut checks = vec![Check::new(
            TEXT[0],
            if admin { "ok" } else { "fail" },
            if admin { TEXT[1] } else { TUNREPORTNOADMIN },
        )];
        let s = self.wintun()?;
        let mut check = Check::new(
            "wintun.dll",
            if s.installed { "ok" } else { "fail" },
            if s.installed {
                let digest = go_lossy(&s.sha256.as_bytes()[..s.sha256.len().min(16)]);
                fmt(TEXT[2], &["0.14.1".into(), digest])
            } else {
                TUNREPORTNOWINTUN.into()
            },
        );
        check.value = s.path;
        checks.push(check);
        let active = self.call("running", "")?.flag && self.call("mode", "")?.text == "tun";
        if !active {
            checks.push(Check::new(TEXT[3], "warn", TEXT[4]));
            return Ok(checks_json(false, checks, None));
        }
        match self.adapter()? {
            Err(e) => checks.push(Check::new(TEXT[5], "fail", e)),
            Ok(a) => {
                let good = a.present && a.status == "Up";
                let mut c = Check::new(
                    TEXT[5],
                    if good { "ok" } else { "fail" },
                    if good {
                        TEXT[6].into()
                    } else {
                        let q = self.call("quote", &a.status)?.text;
                        fmt(TEXT[7], &[q])
                    },
                );
                c.value = a.name;
                checks.push(c);
                let both = a.ipv4_route && a.ipv6_route;
                let any = a.ipv4_route || a.ipv6_route;
                let mut c = Check::new(
                    TEXT[8],
                    if both {
                        "ok"
                    } else if any {
                        "warn"
                    } else {
                        "fail"
                    },
                    if both {
                        TEXT[9]
                    } else if any {
                        TEXT[10]
                    } else {
                        TEXT[11]
                    },
                );
                if any {
                    c.value = a.route_via.join(" ")
                }
                checks.push(c)
            }
        }
        checks.push(self.dns()?);
        checks.push(self.node()?);
        checks.push(self.path(TEXT[12])?);
        checks.push(Check::new(TEXT[13], "warn", IPV6_DISABLED));
        Ok(checks_json(true, checks, Some("SmartVPN".into())))
    }
    fn android_status(&mut self) -> Step<J> {
        self.call("refresh", "")?;
        self.call("lock", "")?;
        let t = self.call("tunnel-state", "")?;
        let reason = self.unavailable(true)?;
        let mode = self.call("mode", "")?.text;
        let active = self.call("running", "")?.flag && t.number > 0;
        Ok(obj(vec![
            ("mode", st(mode)),
            ("elevated", J::Bool(t.flag)),
            ("interface", st(t.text)),
            ("stack", st("gvisor")),
            ("mtu", J::Num(1500)),
            ("routes", if t.names_nil { J::Null } else { arr(&t.names) }),
            ("dns", if t.other_nil { J::Null } else { arr(&t.other) }),
            ("available", J::Bool(reason.is_empty())),
            ("reason", st(reason)),
            ("active", J::Bool(active)),
            ("descriptor", J::Num(t.number)),
        ]))
    }
    fn android_checks(&mut self) -> Step<J> {
        self.call("refresh", "")?;
        self.call("lock", "")?;
        let t = self.call("tunnel-state", "")?;
        let mut checks = vec![Check::new(
            TEXT[14],
            if t.flag { "ok" } else { "fail" },
            if t.flag { TEXT[15] } else { TEXT[16] },
        )];
        if t.number > 0 {
            let alive = self.call("descriptor-alive", "")?.flag;
            let detail = if alive {
                fmt(TEXT[17], &[t.number.to_string()])
            } else {
                fmt(
                    &format!("{}{}", TEXT[18], TEXT[19]),
                    &[t.number.to_string()],
                )
            };
            let mut c = Check::new(TEXT[20], if alive { "ok" } else { "fail" }, detail);
            c.value = t.text.clone();
            checks.push(c)
        } else {
            checks.push(Check::new(TEXT[20], "fail", TEXT[21]))
        }
        let default = t.names.iter().any(|s| s == "0.0.0.0/0" || s == "::/0");
        let mut c = Check::new(
            TEXT[8],
            if t.names.is_empty() {
                "warn"
            } else if default {
                "ok"
            } else {
                "fail"
            },
            if t.names.is_empty() {
                TEXT[22]
            } else if default {
                TEXT[23]
            } else {
                TEXT[24]
            },
        );
        if !t.names.is_empty() {
            c.value = t.names.join(" ")
        }
        checks.push(c);
        if !t.other.is_empty() {
            let mut c = Check::new(TEXT[25], "ok", TEXT[26]);
            c.value = t.other.join(", ");
            checks.push(c)
        }
        let active = self.call("running", "")?.flag && t.number > 0;
        if !active {
            checks.push(Check::new(TEXT[27], "warn", TEXT[28]));
            return Ok(checks_json(false, checks, None));
        }
        checks.push(Check::new(
            TEXT[54],
            "warn",
            format!("{}{}", TEXT[55], TEXT[56]),
        ));
        checks.push(self.node()?);
        checks.push(self.region()?);
        checks.push(Check::new(TEXT[13], "warn", IPV6_DISABLED));
        Ok(checks_json(true, checks, Some(t.text)))
    }
    fn run(&mut self, q: &Request) -> Step<J> {
        match q.op.as_str() {
            "windows-status" => self.windows_status(),
            "windows-checks" => self.windows_checks(),
            "android-status" => self.android_status(),
            "android-checks" => self.android_checks(),
            "dns" => Ok(obj(vec![("check", self.dns()?.json())])),
            "node" => Ok(obj(vec![("check", self.node()?.json())])),
            "path" => Ok(obj(vec![("check", self.path(&q.name)?.json())])),
            "region" => Ok(obj(vec![("check", self.region()?.json())])),
            _ => unreachable!(),
        }
    }
}
impl Wintun {
    fn json(&self) -> J {
        let mut v = vec![
            ("installed", J::Bool(self.installed)),
            ("path", st(&self.path)),
        ];
        for (k, t) in [
            ("version", &self.version),
            ("sha256", &self.sha256),
            ("signer", &self.signer),
            ("detail", &self.detail),
        ] {
            if !t.is_empty() {
                v.push((k, st(t)))
            }
        }
        obj(v)
    }
}
fn checks_json(active: bool, checks: Vec<Check>, interface: Option<String>) -> J {
    let mut v = vec![
        ("active", J::Bool(active)),
        ("checks", J::Arr(checks.iter().map(Check::json).collect())),
    ];
    if let Some(i) = interface {
        v.push(("interface", st(i)))
    }
    obj(v)
}
pub struct Flow {
    q: Request,
    replies: Vec<Reply>,
    e: Effect,
    budget: usize,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, Error> {
        if ![
            "windows-status",
            "windows-checks",
            "android-status",
            "android-checks",
            "dns",
            "node",
            "path",
            "region",
        ]
        .contains(&q.op.as_str())
        {
            return Err(Error::Invalid);
        }
        let mut s = Self {
            q,
            replies: vec![],
            e: Effect::default(),
            budget: 0,
        };
        s.compute();
        Ok(s)
    }
    fn compute(&mut self) {
        let mut e = Engine {
            replies: &self.replies,
            pos: 0,
        };
        self.e = match e.run(&self.q) {
            Ok(j) => Effect {
                kind: "result".into(),
                result: j.json(),
                text: String::new(),
            },
            Err(e) => e,
        }
    }
    pub fn next(&self) -> &Effect {
        &self.e
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), Error> {
        if self.e.kind == "done" {
            return Err(Error::Done);
        }
        if self.e.kind == "result" {
            self.e = Effect {
                kind: "done".into(),
                ..Default::default()
            };
            return Ok(());
        }
        let size = std::mem::size_of::<Reply>()
            + (r.names.len() + r.other.len() + r.adapter.route_via.len())
                * std::mem::size_of::<String>()
            + r.text.len()
            + r.names.iter().map(String::len).sum::<usize>()
            + r.other.iter().map(String::len).sum::<usize>()
            + r.error.as_ref().map_or(0, String::len)
            + r.wintun.path.len()
            + r.wintun.version.len()
            + r.wintun.sha256.len()
            + r.wintun.signer.len()
            + r.wintun.detail.len()
            + r.adapter.name.len()
            + r.adapter.status.len()
            + r.adapter.route_via.iter().map(String::len).sum::<usize>()
            + r.exit_address.len()
            + r.exit_error.len()
            + r.region_country.len()
            + r.region_exit.len();
        if self.replies.len() >= 256 || self.budget.saturating_add(size) > 8 << 20 {
            return Err(Error::Limit);
        }
        self.budget += size;
        self.replies.push(r);
        self.compute();
        Ok(())
    }
}

fn go_lossy(mut b: &[u8]) -> String {
    let mut out = String::new();
    while !b.is_empty() {
        match std::str::from_utf8(b) {
            Ok(s) => {
                out.push_str(s);
                break;
            }
            Err(e) => {
                let n = e.valid_up_to();
                out.push_str(std::str::from_utf8(&b[..n]).unwrap());
                b = &b[n..];
                let n = e.error_len().unwrap_or(b.len());
                for _ in 0..n {
                    out.push('\u{fffd}')
                }
                b = &b[n..];
            }
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    fn run(op: &str, mut fx: impl FnMut(&Effect) -> Reply) -> (String, Vec<Effect>) {
        let mut f = Flow::new(Request {
            op: op.into(),
            name: "name".into(),
            ..Default::default()
        })
        .unwrap();
        let mut t = vec![];
        for _ in 0..256 {
            let e = f.next().clone();
            if e.kind == "result" {
                return (e.result, t);
            }
            assert_eq!(&e, f.next());
            t.push(e.clone());
            f.advance(fx(&e)).unwrap()
        }
        panic!("not finished")
    }
    fn base(e: &Effect) -> Reply {
        match e.kind.as_str() {
            "elevated" | "running" | "descriptor-alive" | "adapter-alias" => Reply {
                flag: true,
                ..Default::default()
            },
            "mode" => Reply {
                text: "tun".into(),
                ..Default::default()
            },
            "wintun-path" => Reply {
                text: "wintun.dll".into(),
                ..Default::default()
            },
            "wintun-hash" => Reply {
                text: "0123456789abcdefLONG".into(),
                ..Default::default()
            },
            "adapter-parse" => Reply {
                flag: true,
                text: "Up".into(),
                names: vec!["0.0.0.0/0".into(), "::/0".into()],
                other: vec!["SmartVPN".into(); 2],
                ..Default::default()
            },
            "tunnel-state" => Reply {
                flag: true,
                number: 9,
                text: "tun0".into(),
                names: vec!["0.0.0.0/0".into()],
                other: vec!["198.18.0.2".into()],
                ..Default::default()
            },
            "exit-tunnel" | "exit-system" => Reply {
                exit_address: "1.2.3.4".into(),
                ..Default::default()
            },
            "region" => Reply {
                flag: true,
                region_exit: "1.2.3.4".into(),
                region_country: "JP".into(),
                ..Default::default()
            },
            "locked-region" => Reply {
                text: "JP".into(),
                flag: true,
                ..Default::default()
            },
            _ => Reply::default(),
        }
    }
    #[test]
    fn windows_status_rechecks_permission_and_wintun_in_original_order() {
        let (r, t) = run("windows-status", base);
        assert!(r.contains("\"available\":true"));
        assert!(r.contains("\"active\":true"));
        assert_eq!(
            t.iter().map(|e| e.kind.as_str()).collect::<Vec<_>>(),
            vec![
                "lock",
                "elevated",
                "wintun-path",
                "wintun-stat",
                "wintun-hash",
                "mode",
                "elevated",
                "wintun-path",
                "wintun-stat",
                "wintun-hash",
                "running",
                "mode"
            ]
        );
    }
    #[test]
    fn unavailable_reason_uses_first_snapshot_not_later_wintun() {
        let mut stats = 0;
        let (r, _) = run("windows-status", |e| {
            if e.kind == "wintun-stat" {
                stats += 1;
                Reply {
                    flag: stats == 1,
                    ..Default::default()
                }
            } else {
                base(e)
            }
        });
        assert!(r.contains("\"available\":false"));
        assert!(r.contains("\"installed\":true"));
    }
    #[test]
    fn inactive_windows_checks_stop_before_adapter_or_network() {
        let (r, t) = run("windows-checks", |e| {
            if e.kind == "running" {
                Reply::default()
            } else {
                base(e)
            }
        });
        assert!(r.contains("\"active\":false"));
        assert!(!t
            .iter()
            .any(|e| e.kind == "adapter-read" || e.kind == "lookup"));
        assert!(!r.contains("\"interface\""));
    }
    #[test]
    fn windows_adapter_route_filter_and_both_families() {
        let (r, t) = run("windows-checks", base);
        assert!(r.contains(TEXT[9]));
        assert!(r.contains("0.0.0.0/0 ::/0"));
        assert_eq!(t.iter().filter(|e| e.kind == "adapter-alias").count(), 2);
        assert!(r.contains(IPV6_DISABLED));
        assert!(!t.iter().any(|e| e.kind == "unlock"));
    }
    #[test]
    fn adapter_error_skips_route_classification_but_keeps_later_checks() {
        let (r, t) = run("windows-checks", |e| {
            if e.kind == "adapter-read" {
                Reply {
                    error: Some("adapterfailed".into()),
                    ..Default::default()
                }
            } else {
                base(e)
            }
        });
        assert!(r.contains("adapterfailed"));
        assert!(!r.contains(TEXT[9]));
        assert!(t.iter().any(|e| e.kind == "lookup"));
    }
    #[test]
    fn android_status_preserves_nil_lists_and_independent_authorization_snapshot() {
        let mut calls = 0;
        let (r, t) = run("android-status", |e| {
            if e.kind == "tunnel-state" {
                calls += 1;
                if calls == 1 {
                    Reply {
                        number: 9,
                        names_nil: true,
                        other_nil: false,
                        ..Default::default()
                    }
                } else {
                    Reply {
                        flag: true,
                        number: -1,
                        ..Default::default()
                    }
                }
            } else {
                base(e)
            }
        });
        assert!(r.contains("\"routes\":null"));
        assert!(r.contains("\"dns\":[]"));
        assert!(r.contains("\"elevated\":false"));
        assert!(r.contains("\"active\":true"));
        assert!(r.contains(TUNREPORTNOFD));
        assert_eq!(t[0].kind, "refresh");
        assert_eq!(t[1].kind, "lock");
    }
    #[test]
    fn android_closed_descriptor_reports_failure_but_does_not_hide_other_checks() {
        let (r, t) = run("android-checks", |e| {
            if e.kind == "descriptor-alive" {
                Reply::default()
            } else {
                base(e)
            }
        });
        assert!(r.contains(TEXT[19]));
        assert!(r.contains(TEXT[54]));
        assert!(r.contains("\"active\":true"));
        assert!(!t.iter().any(|e| e.kind == "lookup"));
    }
    #[test]
    fn dns_stops_at_first_fake_answer() {
        let (r, t) = run("dns", |e| match e.kind.as_str() {
            "lookup" => Reply {
                names: vec!["1".into(), "2".into(), "3".into()],
                ..Default::default()
            },
            "fake-address" => Reply {
                flag: e.text == "2",
                ..Default::default()
            },
            _ => base(e),
        });
        assert!(r.contains(TEXT[31]));
        assert_eq!(t.iter().filter(|e| e.kind == "fake-address").count(), 2);
    }
    #[test]
    fn node_domain_error_priority_and_fake_loop() {
        let (r, _) = run("node", |e| match e.kind.as_str() {
            "hostname" => Reply {
                text: "node.test".into(),
                ..Default::default()
            },
            "lookup" => Reply {
                names: vec!["198.18.0.1".into()],
                ..Default::default()
            },
            "fake-address" => Reply {
                flag: true,
                ..Default::default()
            },
            _ => base(e),
        });
        assert!(r.contains(TEXT[38]));
        assert!(r.contains("node.test"));
        let (r, t) = run("node", |e| {
            if e.kind == "read-profile" {
                Reply {
                    error: Some("missing".into()),
                    ..Default::default()
                }
            } else {
                base(e)
            }
        });
        assert!(r.contains(TEXT[34]));
        assert_eq!(t.len(), 1);
    }
    #[test]
    fn exit_path_all_five_classifications() {
        for (tunnel, system, detail) in [
            ("", "", 48),
            ("1", "", 49),
            ("", "1", 50),
            ("1", "1", 51),
            ("1", "2", 53),
        ] {
            let (r, t) = run("path", |e| {
                if e.kind == "exit-tunnel" {
                    Reply {
                        exit_address: tunnel.into(),
                        ..Default::default()
                    }
                } else {
                    Reply {
                        exit_address: system.into(),
                        ..Default::default()
                    }
                }
            });
            assert_eq!(t.len(), 2);
            assert!(r.contains(TEXT[detail].split("%s").next().unwrap()));
        }
    }
    #[test]
    fn exit_region_preserves_three_locked_reads_and_casefold_fact() {
        let mut calls = 0;
        let (r, _) = run("region", |e| {
            if e.kind == "locked-region" {
                calls += 1;
                Reply {
                    text: "jp".into(),
                    flag: true,
                    ..Default::default()
                }
            } else {
                base(e)
            }
        });
        assert_eq!(calls, 3);
        assert!(r.contains(TEXT[47]));
    }
    #[test]
    fn report_strings_escape_json_and_detail_is_always_present() {
        assert_eq!(quote("a\n\"\\"), "\"a\\n\\\"\\\\\"");
        assert!(Check::new("a", "ok", "")
            .json()
            .json()
            .contains("\"detail\":\"\""));
        assert_eq!(go_lossy(&[0xe2, 0x82]), "\u{fffd}\u{fffd}");
    }
    #[test]
    fn replay_history_and_bytes_are_bounded_without_consuming_rejected_reply() {
        let mut f = Flow::new(Request {
            op: "dns".into(),
            ..Default::default()
        })
        .unwrap();
        f.advance(Reply {
            names: vec!["x".into(); 1000],
            ..Default::default()
        })
        .unwrap();
        for _ in 1..256 {
            f.advance(Reply::default()).unwrap()
        }
        let e = f.next().clone();
        assert_eq!(f.advance(Reply::default()), Err(Error::Limit));
        assert_eq!(f.next(), &e);
    }
    #[test]
    fn formatting_never_interprets_placeholder_like_argument_text() {
        assert_eq!(
            fmt("%s / %s", &["bad %s %d %q".into(), "other".into()]),
            "bad %s %d %q / other"
        );
        assert_eq!(
            fmt("%d then %s", &["7".into(), "value".into()]),
            "7 then value"
        );
        let (r, _) = run("path", |e| Reply {
            exit_error: if e.kind == "exit-tunnel" {
                "first %s".into()
            } else {
                "second".into()
            },
            ..Default::default()
        });
        assert!(r.contains("first %s / second"));
    }
}
