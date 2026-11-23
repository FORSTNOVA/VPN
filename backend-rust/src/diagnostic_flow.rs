//! Ordered diagnostics use cases. No network resources or opaque response bodies enter Rust.
#[derive(Clone, Debug, Default)]
pub struct Request {
    pub op: u8,
}
#[derive(Clone, Debug, Default)]
pub struct Snapshot {
    pub chain: bool,
    pub choice: Vec<u8>,
    pub details: i64,
    pub stuns: i64,
    pub sites: i64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub a: i64,
    pub b: i64,
    pub c: i64,
    pub text: Vec<u8>,
    pub indices: Vec<i64>,
}
#[derive(Clone, Debug, Default)]
pub struct Reply {
    pub kind: u8,
    pub ok: bool,
    pub flag: bool,
    pub value: i64,
    pub token: i64,
    pub name: Vec<u8>,
    pub error: Vec<u8>,
    pub names: Vec<Vec<u8>>,
    pub snapshot: Option<Snapshot>,
}
// Quality flags: valid JSON, raw hosting, proxy, VPN, Tor, anonymous.
// Go supplies Unicode-lowercased ISP + space + Org; Rust owns keyword inference.
// Result packed score in low byte, risk (0 unknown/1 low/2 medium/3 high) at 8,
// hosting at 10, native at 11, proxy/VPN/Tor at 12/13/14.
pub fn quality(bits: i64, lower: &[u8]) -> i64 {
    if bits & 1 == 0 {
        return 0;
    }
    let hosting = bits & (1 << 1) != 0
        || [
            b"cloudflare".as_slice(),
            b"amazon",
            b"digitalocean",
            b"microsoft",
            b"google",
            b"oracle",
            b"alibaba",
            b"tencent",
            b"hetzner",
            b"ovh",
            b"vultr",
            b"linode",
            b"choopa",
            b"m247",
            b"data center",
            b"hosting",
        ]
        .iter()
        .any(|word| lower.windows(word.len()).any(|v| v == *word));
    let proxy = bits & (1 << 2) != 0;
    let vpn = bits & (1 << 3) != 0;
    let tor = bits & (1 << 4) != 0;
    let mut score = if hosting { 25 } else { 5 };
    if proxy || vpn {
        score += 30
    }
    if tor {
        score += 50
    }
    if bits & (1 << 5) != 0 {
        score += 15
    }
    score = score.min(100);
    let risk = if score > 60 {
        3
    } else if score > 25 {
        2
    } else {
        1
    };
    score
        | (risk << 8)
        | ((hosting as i64) << 10)
        | ((!hosting as i64) << 11)
        | ((proxy as i64) << 12)
        | ((vpn as i64) << 13)
        | ((tor as i64) << 14)
}
#[derive(Clone, Debug)]
pub struct Flow {
    q: Request,
    s: Snapshot,
    e: Effect,
    temporary: bool,
    index: i64,
    last_error: i64,
    last_text: Vec<u8>,
    selected: Vec<u8>,
    quality: i64,
    verdict: i64,
    status: i64,
    code: i64,
    cleanup_failure: bool,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, &'static str> {
        if q.op > 4 {
            return Err("invalid diagnostic operation");
        }
        let k = match q.op {
            0 => 1,
            1 => 4,
            _ => 4,
        };
        let mut f = Self {
            q,
            s: Snapshot::default(),
            e: Effect::default(),
            temporary: false,
            index: 0,
            last_error: 0,
            last_text: vec![],
            selected: vec![],
            quality: 0,
            verdict: 0,
            status: 200,
            code: 0,
            cleanup_failure: false,
        };
        f.emit(k);
        Ok(f)
    }
    pub fn next(&self) -> Effect {
        self.e.clone()
    }
    fn emit(&mut self, k: u8) {
        self.e = Effect {
            kind: k,
            ..Effect::default()
        }
    }
    fn terminal(&mut self) {
        self.emit(63);
        self.e.a = self.status;
        self.e.b = self.code;
        self.e.c = if self.code == 10 { self.last_error } else { 0 };
        self.e.text = if self.code == 10 {
            self.last_text.clone()
        } else {
            vec![]
        };
        self.e.indices = vec![self.temporary as i64, self.quality, self.verdict]
    }
    fn finish(&mut self, status: i64, code: i64) {
        self.status = if self.q.op == 0 { status } else { 0 };
        self.code = code;
        if self.q.op == 0 {
            self.emit(21);
            self.e.a = status;
            self.e.b = code;
            self.e.c = if self.code == 10 { self.last_error } else { 0 };
            self.e.text = if self.code == 10 {
                self.last_text.clone()
            } else {
                vec![]
            };
            self.e.indices = vec![self.temporary as i64, self.quality, self.verdict]
        } else {
            self.terminal()
        }
    }
    fn ensured(&mut self) {
        self.temporary = true;
        if self.q.op == 0 {
            self.active()
        } else {
            self.finish(0, 0)
        }
    }
    fn active(&mut self) {
        self.emit(8);
        self.e.text = b"SmartVPN".to_vec()
    }
    fn selected(&mut self) {
        if self.selected.is_empty() || self.selected.eq_ignore_ascii_case(b"REJECT") {
            self.finish(409, 20)
        } else {
            self.emit(23)
        }
    }
    fn details(&mut self) {
        self.index = 0;
        self.last_error = 0;
        self.last_text.clear();
        self.detail_next()
    }
    fn detail_next(&mut self) {
        if self.index < self.s.details {
            self.emit(12);
            self.e.a = self.index
        } else {
            if self.q.op == 2 {
                self.finish(0, if self.last_error != 0 { 10 } else { 11 })
            } else {
                self.emit(14)
            }
        }
    }
    fn stun(&mut self) {
        self.emit(17)
    }
    fn stun_next(&mut self) {
        if self.index < self.s.stuns {
            self.emit(18);
            self.e.a = self.index
        } else if self.q.op == 4 {
            self.finish(0, if self.last_error != 0 { 10 } else { 13 })
        } else {
            self.sites()
        }
    }
    fn sites(&mut self) {
        self.emit(19);
        self.e.indices = (0..self.s.sites).collect()
    }
    fn dns_done(&mut self) {
        if self.q.op == 3 {
            self.finish(0, 0)
        } else {
            self.emit(16)
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        let k = self.e.kind;
        if k == 0 || r.kind != k {
            return Err("unexpected diagnostic reply");
        }
        if matches!(k, 1 | 4) && r.snapshot.is_none() {
            return Err("missing diagnostic snapshot");
        }
        if let Some(s) = &r.snapshot {
            if s.details < 0
                || s.stuns < 0
                || s.sites < 0
                || s.details > 100000
                || s.stuns > 100000
                || s.sites > 100000
            {
                return Err("invalid diagnostic target count");
            }
        }
        if let Some(s) = r.snapshot {
            self.s = s
        }
        match k {
            1 => self.emit(2),
            2 => {
                if r.flag {
                    if self.q.op == 0 {
                        self.active()
                    } else {
                        self.finish(0, 0)
                    }
                } else {
                    self.emit(3)
                }
            }
            3 => {
                if r.ok {
                    self.emit(4);
                    self.e.a = 1
                } else {
                    self.last_error = r.token;
                    self.last_text = r.error;
                    self.finish(502, 10)
                }
            }
            4 => match self.q.op {
                2 => self.details(),
                3 => self.emit(14),
                4 => self.stun(),
                _ => {
                    if self.q.op == 1 && self.e.a == 0 {
                        self.emit(2)
                    } else if self.s.chain && self.s.choice == b"SmartVPNChain" {
                        self.emit(5);
                        self.e.text = self.s.choice.clone()
                    } else if self.s.choice == b"SmartVPNAuto"
                        || self.s.choice == b"SmartVPNFallback"
                    {
                        self.emit(7);
                        self.e.text = self.s.choice.clone()
                    } else if !self.s.choice.is_empty() {
                        self.emit(6)
                    } else {
                        self.ensured()
                    }
                }
            },
            5 => {
                if r.ok {
                    self.ensured()
                } else {
                    self.last_error = r.token;
                    self.last_text = r.error;
                    self.cleanup_failure = true;
                    self.emit(22)
                }
            }
            6 => {
                if r.ok && r.names.contains(&self.s.choice) {
                    self.emit(7);
                    self.e.text = self.s.choice.clone()
                } else {
                    self.ensured()
                }
            }
            7 => self.ensured(),
            8 => {
                if self.e.a == 0 {
                    self.selected = if r.ok { r.name } else { vec![] };
                    if self.selected == b"SmartVPNAuto" || self.selected == b"SmartVPNFallback" {
                        self.emit(8);
                        self.e.a = 1;
                        self.e.text = self.selected.clone()
                    } else {
                        self.selected()
                    }
                } else {
                    if r.ok {
                        self.selected = r.name
                    }
                    self.selected()
                }
            }
            9 => {
                if r.name.is_empty() {
                    self.emit(10)
                } else {
                    self.emit(11)
                }
            }
            10 => {
                if r.name.is_empty() {
                    self.finish(200, 21)
                } else {
                    self.emit(11)
                }
            }
            11 => self.details(),
            12 => {
                if r.ok {
                    self.emit(13)
                } else {
                    self.last_error = r.token;
                    self.last_text = r.error;
                    self.index += 1;
                    self.detail_next()
                }
            }
            13 => {
                self.quality = quality(r.value, &r.name);
                if self.q.op == 2 {
                    self.finish(0, 0)
                } else {
                    self.emit(14)
                }
            }
            14 => {
                if r.ok && r.flag {
                    self.emit(15)
                } else {
                    self.dns_done()
                }
            }
            15 => self.dns_done(),
            16 => self.stun(),
            17 => {
                if r.ok {
                    self.index = 0;
                    self.last_error = 0;
                    self.last_text.clear();
                    self.stun_next()
                } else {
                    self.last_error = r.token;
                    self.last_text = r.error;
                    if self.q.op == 4 {
                        self.finish(0, 12)
                    } else {
                        self.sites()
                    }
                }
            }
            18 => {
                if r.ok {
                    if self.q.op == 4 {
                        self.finish(0, 0)
                    } else {
                        self.sites()
                    }
                } else {
                    self.last_error = r.token;
                    self.last_text = r.error;
                    self.index += 1;
                    self.stun_next()
                }
            }
            19 => self.emit(25),
            25 => self.emit(20),
            20 => {
                let bits = r.value;
                let dns = bits & 1 != 0 && bits & 2 != 0 && bits & 4 != 0;
                let udp = bits & 8 != 0 && bits & 16 != 0;
                let webrtc = bits & 8 != 0 && !udp;
                let dnsleak = bits & 1 != 0 && bits & 2 != 0 && !dns;
                self.verdict = (dns as i64)
                    | ((udp as i64) << 1)
                    | ((webrtc as i64) << 2)
                    | ((dnsleak as i64) << 3);
                self.finish(200, 0)
            }
            21 => {
                if self.temporary {
                    self.emit(22)
                } else {
                    self.emit(24)
                }
            }
            22 => {
                if self.cleanup_failure {
                    self.cleanup_failure = false;
                    self.finish(502, 10)
                } else {
                    self.emit(24)
                }
            }
            23 => self.emit(9),
            24 => self.terminal(),
            63 => self.emit(0),
            _ => return Err("invalid diagnostic effect"),
        }
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn run(op: u8, mut change: impl FnMut(&Effect, &mut Reply)) -> Vec<Effect> {
        let mut f = Flow::new(Request { op }).unwrap();
        let mut out = vec![];
        for _ in 0..100 {
            let e = f.next();
            if e.kind == 0 {
                return out;
            }
            let mut r = Reply {
                kind: e.kind,
                ok: true,
                flag: true,
                name: b"node".to_vec(),
                value: 1,
                names: vec![b"node".to_vec()],
                snapshot: if matches!(e.kind, 1 | 4) {
                    Some(Snapshot {
                        choice: b"node".to_vec(),
                        details: 2,
                        stuns: 2,
                        sites: 3,
                        ..Snapshot::default()
                    })
                } else {
                    None
                },
                ..Reply::default()
            };
            change(&e, &mut r);
            out.push(e);
            f.advance(r).unwrap()
        }
        panic!("loop")
    }
    fn ks(v: &[Effect]) -> Vec<u8> {
        v.iter().map(|e| e.kind).collect()
    }
    #[test]
    fn running_does_not_repin() {
        let t = run(0, |_, _| {});
        assert_eq!(
            ks(&t),
            [1, 2, 8, 23, 9, 11, 12, 13, 14, 15, 16, 17, 18, 19, 25, 20, 21, 24, 63]
        );
    }
    #[test]
    fn temporary_response_before_stop_and_unlock() {
        let t = run(0, |e, r| {
            if e.kind == 2 {
                r.flag = false
            }
        });
        let k = ks(&t);
        assert_eq!(&k[..8], &[1, 2, 3, 4, 6, 7, 8, 23]);
        assert_eq!(&k[k.len() - 4..], &[21, 22, 24, 63]);
    }
    #[test]
    fn start_failure_no_stop() {
        let t = run(0, |e, r| {
            if e.kind == 2 {
                r.flag = false
            }
            if e.kind == 3 {
                r.ok = false;
                r.token = 99
            }
        });
        assert_eq!(ks(&t), [1, 2, 3, 21, 24, 63]);
        assert_eq!(t[3].a, 502);
        assert_eq!(t[3].c, 99);
    }
    #[test]
    fn chain_pin_error_stops_before_response_once() {
        let t = run(0, |e, r| {
            if e.kind == 2 {
                r.flag = false
            }
            if let Some(s) = &mut r.snapshot {
                s.chain = true;
                s.choice = b"SmartVPNChain".to_vec()
            }
            if e.kind == 5 {
                r.ok = false;
                r.token = 4
            }
        });
        assert_eq!(ks(&t), [1, 2, 3, 4, 5, 22, 21, 24, 63]);
    }
    #[test]
    fn other_pin_failures_ignored() {
        for fail in [6, 7] {
            let t = run(0, |e, r| {
                if e.kind == 2 {
                    r.flag = false
                }
                if e.kind == fail {
                    r.ok = false
                }
            });
            assert!(ks(&t).contains(&9));
            assert_eq!(t.last().unwrap().a, 200)
        }
    }
    #[test]
    fn standalone_ensure_preserves_temp_ownership() {
        let t = run(1, |e, r| {
            if e.kind == 2 {
                r.flag = false
            }
        });
        assert_eq!(ks(&t), [4, 2, 3, 4, 6, 7, 63]);
        assert_eq!(t.last().unwrap().indices[0], 1);
        assert_eq!(t.last().unwrap().a, 0);
    }
    #[test]
    fn nested_get_error_retains_group_but_success_empty_rejects() {
        for ok in [false, true] {
            let t = run(0, |e, r| {
                if e.kind == 8 {
                    if e.a == 0 {
                        r.name = b"SmartVPNAuto".to_vec()
                    } else {
                        r.ok = ok;
                        r.name.clear()
                    }
                }
            });
            assert_eq!(t.last().unwrap().a, if ok { 409 } else { 200 });
        }
    }
    #[test]
    fn ipv4_double_empty_skips_remaining() {
        let t = run(0, |e, r| {
            if matches!(e.kind, 9 | 10) {
                r.name.clear()
            }
        });
        assert_eq!(ks(&t), [1, 2, 8, 23, 9, 10, 21, 24, 63]);
        assert_eq!(t.last().unwrap().b, 21);
    }
    #[test]
    fn detail_fallback_last_error_and_quality() {
        let t = run(2, |e, r| {
            if e.kind == 12 {
                r.ok = false;
                r.token = e.a + 1
            }
        });
        assert_eq!(ks(&t), [4, 12, 12, 63]);
        assert_eq!(t.last().unwrap().c, 2);
        let t = run(2, |e, r| {
            if e.kind == 12 && e.a == 0 {
                r.ok = false
            }
        });
        assert_eq!(ks(&t), [4, 12, 12, 13, 63]);
    }
    #[test]
    fn dns_invalid_address_no_country_and_country_error_ignored() {
        let t = run(3, |e, r| {
            if e.kind == 14 {
                r.flag = false
            }
        });
        assert_eq!(ks(&t), [4, 14, 63]);
        let t = run(3, |e, r| {
            if e.kind == 15 {
                r.ok = false
            }
        });
        assert_eq!(ks(&t), [4, 14, 15, 63]);
        assert_eq!(t.last().unwrap().b, 0);
    }
    #[test]
    fn stun_build_and_target_fallback() {
        let t = run(4, |e, r| {
            if e.kind == 17 {
                r.ok = false
            }
        });
        assert_eq!(ks(&t), [4, 17, 63]);
        assert_eq!(t.last().unwrap().b, 12);
        let t = run(4, |e, r| {
            if e.kind == 18 {
                r.ok = e.a == 1
            }
        });
        assert_eq!(ks(&t), [4, 17, 18, 18, 63]);
    }
    #[test]
    fn sites_plan_and_verdict_combinations() {
        for bits in 0..32 {
            let t = run(0, |e, r| {
                if e.kind == 20 {
                    r.value = bits
                }
            });
            let plan = t.iter().find(|e| e.kind == 19).unwrap();
            assert_eq!(plan.indices, [0, 1, 2]);
            let result = t.last().unwrap();
            let dns = bits & 7 == 7;
            let udp = bits & 24 == 24;
            let expected = (dns as i64)
                | ((udp as i64) << 1)
                | (((bits & 8 != 0 && !udp) as i64) << 2)
                | (((bits & 3 == 3 && !dns) as i64) << 3);
            assert_eq!(result.indices[2], expected);
        }
    }
    #[test]
    fn all_quality_flag_combinations() {
        for bits in 0..128 {
            let q = quality(bits, b"");
            if bits & 1 == 0 {
                assert_eq!(q, 0);
                continue;
            }
            let host = bits & 2 != 0;
            let score = ((if host { 25 } else { 5 })
                + if bits & 12 != 0 { 30 } else { 0 }
                + if bits & 16 != 0 { 50 } else { 0 }
                + if bits & 32 != 0 { 15 } else { 0 })
            .min(100);
            assert_eq!(q & 255, score);
            assert_eq!((q >> 10) & 1, host as i64);
            assert_eq!((q >> 11) & 1, (!host) as i64);
        }
    }
    #[test]
    fn reject_reply_without_advancing() {
        let mut f = Flow::new(Request { op: 0 }).unwrap();
        let old = f.next();
        assert!(f
            .advance(Reply {
                kind: 1,
                ..Reply::default()
            })
            .is_err());
        assert_eq!(f.next(), old);
        assert!(f
            .advance(Reply {
                kind: 1,
                snapshot: Some(Snapshot {
                    sites: -1,
                    ..Snapshot::default()
                }),
                ..Reply::default()
            })
            .is_err());
        assert_eq!(f.next(), old);
    }
    #[test]
    fn hosting_keyword_inference_and_substrings() {
        for word in [
            "cloudflare",
            "amazon",
            "digitalocean",
            "microsoft",
            "google",
            "oracle",
            "alibaba",
            "tencent",
            "hetzner",
            "ovh",
            "vultr",
            "linode",
            "choopa",
            "m247",
            "data center",
            "hosting",
        ] {
            assert_ne!(
                quality(1, format!("isp {word} org").as_bytes()) & (1 << 10),
                0
            );
        }
        assert_eq!(quality(1, b"residential isp") & 255, 5);
        assert_eq!(quality(1, b"amazonia") & 255, 25);
        assert_eq!(quality(0, b"amazon"), 0);
    }
}
