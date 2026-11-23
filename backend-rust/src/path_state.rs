//! Owned path-domain state and strict canonical byte codec. No I/O or clocks.
pub const MAX_PATH_BYTES: usize = 8 << 20;
const MAX_ITEMS: usize = 100000;
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Invalid,
    Limit,
    Stale,
    Busy,
    Overflow,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Target {
    pub label: Vec<u8>,
    pub url: Vec<u8>,
    pub builtin: bool,
    pub enabled: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Config {
    pub enabled: bool,
    pub interval: i64,
    pub targets: Option<Vec<Target>>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SiteResult {
    pub name: Vec<u8>,
    pub state: Vec<u8>,
    pub http_code: i64,
    pub latency_ms: i64,
    pub detail: Vec<u8>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Status {
    pub running: bool,
    pub active_node: Vec<u8>,
    pub results: Option<Vec<SiteResult>>,
    pub failures: Option<Vec<Vec<u8>>>,
    pub switched: Option<Vec<Vec<u8>>>,
    pub note: Vec<u8>,
    pub alert_id: u64,
    pub alert: Vec<u8>,
    pub retry_at: Vec<u8>,
    pub retry_mode: Vec<u8>,
    pub demanded: bool,
    pub phase: Vec<u8>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Sample {
    pub id: Vec<u8>,
    pub upload: i64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Throttle {
    pub label: Vec<u8>,
    pub time_token: u64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct PathState {
    pub config: Config,
    pub busy: bool,
    pub monitor_active: bool,
    pub status: Status,
    pub at_token: u64,
    pub generation: u64,
    pub next_lease: u64,
    pub lease_id: u64,
    pub previous: Option<Vec<Sample>>,
    pub throttle: Option<Vec<Throttle>>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Command {
    pub action: u8,
    pub config: Config,
    pub flag: bool,
    pub text: Vec<u8>,
    pub time_token: u64,
    pub generation: u64,
    pub lease_id: u64,
    pub strings: Option<Vec<Vec<u8>>>,
    pub results: Option<Vec<SiteResult>>,
    pub samples: Option<Vec<Sample>>,
}
struct Reader<'a> {
    b: &'a [u8],
    p: usize,
}
impl<'a> Reader<'a> {
    fn take(&mut self, n: usize) -> Result<&'a [u8], Error> {
        let end = self.p.checked_add(n).ok_or(Error::Invalid)?;
        let v = self.b.get(self.p..end).ok_or(Error::Invalid)?;
        self.p = end;
        Ok(v)
    }
    fn byte(&mut self) -> Result<u8, Error> {
        Ok(self.take(1)?[0])
    }
    fn flag(&mut self) -> Result<bool, Error> {
        match self.byte()? {
            0 => Ok(false),
            1 => Ok(true),
            _ => Err(Error::Invalid),
        }
    }
    fn n(&mut self) -> Result<u64, Error> {
        Ok(u64::from_le_bytes(
            self.take(8)?.try_into().map_err(|_| Error::Invalid)?,
        ))
    }
    fn bytes(&mut self) -> Result<Vec<u8>, Error> {
        let n = usize::try_from(self.n()?).map_err(|_| Error::Limit)?;
        if n > MAX_PATH_BYTES {
            return Err(Error::Limit);
        };
        Ok(self.take(n)?.to_vec())
    }
    fn list<T>(
        &mut self,
        mut item: impl FnMut(&mut Self) -> Result<T, Error>,
    ) -> Result<Option<Vec<T>>, Error> {
        let present = self.flag()?;
        let n = usize::try_from(self.n()?).map_err(|_| Error::Limit)?;
        if n > MAX_ITEMS {
            return Err(Error::Limit);
        };
        if !present {
            if n != 0 {
                return Err(Error::Invalid);
            };
            return Ok(None);
        };
        let mut v = Vec::with_capacity(n.min(1024));
        for _ in 0..n {
            v.push(item(self)?)
        }
        Ok(Some(v))
    }
    fn config(&mut self) -> Result<Config, Error> {
        Ok(Config {
            enabled: self.flag()?,
            interval: self.n()? as i64,
            targets: self.list(|r| {
                Ok(Target {
                    label: r.bytes()?,
                    url: r.bytes()?,
                    builtin: r.flag()?,
                    enabled: r.flag()?,
                })
            })?,
        })
    }
    fn results(&mut self) -> Result<Option<Vec<SiteResult>>, Error> {
        self.list(|r| {
            Ok(SiteResult {
                name: r.bytes()?,
                state: r.bytes()?,
                http_code: r.n()? as i64,
                latency_ms: r.n()? as i64,
                detail: r.bytes()?,
            })
        })
    }
    fn strings(&mut self) -> Result<Option<Vec<Vec<u8>>>, Error> {
        self.list(Self::bytes)
    }
    fn samples(&mut self) -> Result<Option<Vec<Sample>>, Error> {
        self.list(|r| {
            Ok(Sample {
                id: r.bytes()?,
                upload: r.n()? as i64,
            })
        })
    }
    fn done(&self) -> Result<(), Error> {
        if self.p == self.b.len() {
            Ok(())
        } else {
            Err(Error::Invalid)
        }
    }
}
struct Writer {
    b: Vec<u8>,
    bad: bool,
}
impl Writer {
    fn new() -> Self {
        Self {
            b: vec![],
            bad: false,
        }
    }
    fn put(&mut self, b: &[u8]) {
        if self.bad {
            return;
        }
        if b.len() > MAX_PATH_BYTES.saturating_sub(self.b.len()) {
            self.bad = true;
            return;
        }
        self.b.extend_from_slice(b)
    }
    fn byte(&mut self, b: u8) {
        self.put(&[b])
    }
    fn flag(&mut self, b: bool) {
        self.byte(b as u8)
    }
    fn n(&mut self, n: u64) {
        self.put(&n.to_le_bytes())
    }
    fn bytes(&mut self, b: &[u8]) {
        self.n(b.len() as u64);
        self.put(b)
    }
    fn list<T>(&mut self, v: &Option<Vec<T>>, mut item: impl FnMut(&mut Self, &T)) {
        self.flag(v.is_some());
        let n = v.as_ref().map_or(0, Vec::len);
        if n > MAX_ITEMS {
            self.bad = true;
            return;
        }
        self.n(n as u64);
        if let Some(v) = v {
            for x in v {
                if self.bad {
                    break;
                }
                item(self, x)
            }
        }
    }
    fn config(&mut self, c: &Config) {
        self.flag(c.enabled);
        self.n(c.interval as u64);
        self.list(&c.targets, |w, t| {
            w.bytes(&t.label);
            w.bytes(&t.url);
            w.flag(t.builtin);
            w.flag(t.enabled)
        })
    }
    fn results(&mut self, v: &Option<Vec<SiteResult>>) {
        self.list(v, |w, r| {
            w.bytes(&r.name);
            w.bytes(&r.state);
            w.n(r.http_code as u64);
            w.n(r.latency_ms as u64);
            w.bytes(&r.detail)
        })
    }
    fn strings(&mut self, v: &Option<Vec<Vec<u8>>>) {
        self.list(v, |w, s| w.bytes(s))
    }
    fn samples(&mut self, v: &Option<Vec<Sample>>) {
        self.list(v, |w, s| {
            w.bytes(&s.id);
            w.n(s.upload as u64)
        })
    }
    fn done(self) -> Result<Vec<u8>, Error> {
        if self.bad {
            Err(Error::Limit)
        } else {
            Ok(self.b)
        }
    }
}
fn encode_checked(s: &PathState) -> Result<Vec<u8>, Error> {
    let mut w = Writer::new();
    w.config(&s.config);
    w.flag(s.busy);
    w.flag(s.monitor_active);
    let t = &s.status;
    w.flag(t.running);
    w.bytes(&t.active_node);
    w.results(&t.results);
    w.strings(&t.failures);
    w.strings(&t.switched);
    w.bytes(&t.note);
    w.n(t.alert_id);
    w.bytes(&t.alert);
    w.bytes(&t.retry_at);
    w.bytes(&t.retry_mode);
    w.flag(t.demanded);
    w.bytes(&t.phase);
    w.n(s.at_token);
    w.n(s.generation);
    w.n(s.next_lease);
    w.n(s.lease_id);
    w.samples(&s.previous);
    w.list(&s.throttle, |w, t| {
        w.bytes(&t.label);
        w.n(t.time_token)
    });
    w.done()
}
/// Returns an oversized sentinel for an out-of-bound manually constructed state.
/// Accepted decode/apply states always encode canonically within the limit.
pub fn encode(s: &PathState) -> Vec<u8> {
    encode_checked(s).unwrap_or_else(|_| vec![0; MAX_PATH_BYTES + 1])
}
fn ordered_samples(v: &Option<Vec<Sample>>) -> bool {
    v.as_ref()
        .is_none_or(|v| v.windows(2).all(|p| p[0].id < p[1].id))
}
pub fn validate(s: &PathState) -> Result<(), Error> {
    if s.busy != (s.lease_id != 0)
        || s.lease_id > s.next_lease
        || (s.monitor_active && s.generation == 0)
        || !ordered_samples(&s.previous)
    {
        return Err(Error::Invalid);
    }
    if let Some(v) = &s.throttle {
        if v.iter().any(|x| x.time_token == 0) || !v.windows(2).all(|p| p[0].label < p[1].label) {
            return Err(Error::Invalid);
        }
    }
    encode_checked(s)?;
    Ok(())
}
pub fn decode(b: &[u8]) -> Result<PathState, Error> {
    if b.is_empty() {
        return Ok(PathState::default());
    }
    if b.len() > MAX_PATH_BYTES {
        return Err(Error::Limit);
    }
    let mut r = Reader { b, p: 0 };
    let config = r.config()?;
    let busy = r.flag()?;
    let monitor_active = r.flag()?;
    let status = Status {
        running: r.flag()?,
        active_node: r.bytes()?,
        results: r.results()?,
        failures: r.strings()?,
        switched: r.strings()?,
        note: r.bytes()?,
        alert_id: r.n()?,
        alert: r.bytes()?,
        retry_at: r.bytes()?,
        retry_mode: r.bytes()?,
        demanded: r.flag()?,
        phase: r.bytes()?,
    };
    let s = PathState {
        config,
        busy,
        monitor_active,
        status,
        at_token: r.n()?,
        generation: r.n()?,
        next_lease: r.n()?,
        lease_id: r.n()?,
        previous: r.samples()?,
        throttle: r.list(|r| {
            Ok(Throttle {
                label: r.bytes()?,
                time_token: r.n()?,
            })
        })?,
    };
    r.done()?;
    validate(&s)?;
    Ok(s)
}
pub fn encode_command(c: &Command) -> Vec<u8> {
    let mut w = Writer::new();
    w.byte(c.action);
    w.config(&c.config);
    w.flag(c.flag);
    w.bytes(&c.text);
    w.n(c.time_token);
    w.n(c.generation);
    w.n(c.lease_id);
    w.strings(&c.strings);
    w.results(&c.results);
    w.samples(&c.samples);
    w.done().unwrap_or_else(|_| vec![0; MAX_PATH_BYTES + 1])
}
pub fn decode_command(b: &[u8]) -> Result<Command, Error> {
    if b.is_empty() || b.len() > MAX_PATH_BYTES {
        return Err(Error::Limit);
    }
    let mut r = Reader { b, p: 0 };
    let c = Command {
        action: r.byte()?,
        config: r.config()?,
        flag: r.flag()?,
        text: r.bytes()?,
        time_token: r.n()?,
        generation: r.n()?,
        lease_id: r.n()?,
        strings: r.strings()?,
        results: r.results()?,
        samples: r.samples()?,
    };
    r.done()?;
    if !(1..=21).contains(&c.action) || !ordered_samples(&c.samples) {
        return Err(Error::Invalid);
    }
    Ok(c)
}
pub fn validate_payload(b: &[u8]) -> Result<(), Error> {
    decode_command(b).map(|_| ())
}
pub fn apply_payload(s: &mut PathState, b: &[u8]) -> Result<(), Error> {
    let c = decode_command(b)?;
    validate(s)?;
    let mut next = s.clone();
    match c.action {
        1 => next.config = c.config,
        2 => {
            if next.monitor_active {
                return Err(Error::Busy);
            };
            next.generation = next.generation.checked_add(1).ok_or(Error::Overflow)?;
            next.monitor_active = true;
            next.previous = None;
            next.throttle = None
        }
        3 => {
            next.monitor_active = false;
            next.previous = None;
            next.throttle = None
        }
        4 => {
            if next.busy {
                return Err(Error::Busy);
            };
            next.next_lease = next.next_lease.checked_add(1).ok_or(Error::Overflow)?;
            next.lease_id = next.next_lease;
            next.busy = true
        }
        5 => {
            if c.lease_id == 0 || c.lease_id != next.lease_id {
                return Err(Error::Stale);
            };
            next.lease_id = 0;
            next.busy = false;
            if c.flag {
                next.status.running = false
            }
        }
        6 => next.status.running = c.flag,
        7 => next.status.phase = c.text,
        8 => next.status.note = c.text,
        9 => next.status.alert = c.text,
        10 => next.status.demanded = c.flag,
        11 => next.status.retry_mode = c.text,
        12 => next.status.retry_at = c.text,
        13 => next.status.alert_id = next.status.alert_id.wrapping_add(1),
        14 => next.status.switched = c.strings,
        15 => next
            .status
            .switched
            .get_or_insert_with(Vec::new)
            .push(c.text),
        16 => next.at_token = c.time_token,
        17 => next.status.results = c.results,
        18 => next.status.failures = c.strings,
        19 => next.status.active_node = c.text,
        20 => {
            if !next.monitor_active || next.generation != c.generation {
                return Err(Error::Stale);
            };
            next.previous = c.samples
        }
        21 => {
            if !next.monitor_active || next.generation != c.generation || c.time_token == 0 {
                return Err(Error::Stale);
            };
            let v = next.throttle.get_or_insert_with(Vec::new);
            match v.binary_search_by(|x| x.label.cmp(&c.text)) {
                Ok(i) => v[i].time_token = c.time_token,
                Err(i) => v.insert(
                    i,
                    Throttle {
                        label: c.text,
                        time_token: c.time_token,
                    },
                ),
            }
        }
        _ => unreachable!(),
    };
    validate(&next)?;
    *s = next;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    fn cmd(action: u8) -> Command {
        Command {
            action,
            ..Command::default()
        }
    }
    fn apply(s: &mut PathState, c: Command) {
        apply_payload(s, &encode_command(&c)).unwrap()
    }
    fn full() -> PathState {
        PathState {
            config: Config {
                enabled: true,
                interval: -1,
                targets: Some(vec![Target {
                    label: vec![0, 255],
                    url: b"https://example.test/a?token=local".to_vec(),
                    builtin: true,
                    enabled: true,
                }]),
            },
            busy: true,
            monitor_active: true,
            status: Status {
                running: false,
                active_node: vec![0, 255],
                results: Some(vec![SiteResult {
                    name: b"test".to_vec(),
                    state: b"403".to_vec(),
                    http_code: -1,
                    latency_ms: i64::MIN,
                    detail: vec![255],
                }]),
                failures: Some(vec![]),
                switched: None,
                note: b"note".to_vec(),
                alert_id: u64::MAX,
                alert: b"alert".to_vec(),
                retry_at: b"unparsed".to_vec(),
                retry_mode: b"unknown legacy".to_vec(),
                demanded: true,
                phase: b"arbitrary".to_vec(),
            },
            at_token: u64::MAX,
            generation: 5,
            next_lease: 7,
            lease_id: 6,
            previous: Some(vec![
                Sample {
                    id: b"a".to_vec(),
                    upload: i64::MIN,
                },
                Sample {
                    id: vec![255],
                    upload: i64::MAX,
                },
            ]),
            throttle: Some(vec![
                Throttle {
                    label: b"a".to_vec(),
                    time_token: 1,
                },
                Throttle {
                    label: b"z".to_vec(),
                    time_token: u64::MAX,
                },
            ]),
        }
    }
    #[test]
    fn state_roundtrip_preserves_all_fields_and_arbitrary_bytes() {
        let s = full();
        assert_eq!(decode(&encode(&s)), Ok(s));
    }
    #[test]
    fn canonical_default_and_empty_legacy_decode() {
        let s = PathState::default();
        assert_eq!(decode(&[]), Ok(s.clone()));
        let b = encode(&s);
        assert!(!b.is_empty());
        assert_eq!(decode(&b), Ok(s));
        assert_eq!(encode(&decode(&b).unwrap()), b)
    }
    #[test]
    fn all_command_fields_roundtrip_even_unused() {
        let s = full();
        let c = Command {
            action: 13,
            config: s.config,
            flag: true,
            text: vec![0, 255],
            time_token: u64::MAX,
            generation: 9,
            lease_id: 8,
            strings: Some(vec![vec![], vec![255]]),
            results: s.status.results,
            samples: s.previous,
        };
        let b = encode_command(&c);
        assert_eq!(decode_command(&b), Ok(c));
    }
    #[test]
    fn nil_empty_shape_roundtrip_and_assignments() {
        for present in [false, true] {
            let mut s = PathState::default();
            s.config.targets = present.then(Vec::new);
            s.status.results = present.then(Vec::new);
            s.status.failures = present.then(Vec::new);
            s.status.switched = present.then(Vec::new);
            s.previous = present.then(Vec::new);
            s.throttle = present.then(Vec::new);
            assert_eq!(decode(&encode(&s)), Ok(s));
            for action in [14, 17, 18] {
                let mut s = PathState::default();
                let mut c = cmd(action);
                c.strings = present.then(Vec::new);
                c.results = present.then(Vec::new);
                apply(&mut s, c);
                let shape = match action {
                    14 => s.status.switched.is_some(),
                    17 => s.status.results.is_some(),
                    _ => s.status.failures.is_some(),
                };
                assert_eq!(shape, present)
            }
        }
    }
    #[test]
    fn config_replace_retains_legacy_unvalidated_values() {
        let mut s = PathState::default();
        let mut c = cmd(1);
        c.config = Config {
            enabled: true,
            interval: i64::MIN,
            targets: Some(vec![Target {
                url: b"not a URL".to_vec(),
                ..Target::default()
            }]),
        };
        let expected = c.config.clone();
        apply(&mut s, c);
        assert_eq!(s.config, expected);
    }
    #[test]
    fn generation_revoke_preserves_admitted_round() {
        let mut s = PathState::default();
        apply(&mut s, cmd(2));
        apply(&mut s, cmd(4));
        let lease = s.lease_id;
        apply(
            &mut s,
            Command {
                action: 6,
                flag: true,
                ..cmd(6)
            },
        );
        apply(&mut s, cmd(3));
        assert!(s.busy && s.status.running);
        assert_eq!(s.lease_id, lease);
        apply(&mut s, cmd(2));
        assert_eq!(s.generation, 2);
        apply(
            &mut s,
            Command {
                action: 5,
                lease_id: lease,
                ..cmd(5)
            },
        );
        assert!(!s.busy);
        assert!(s.status.running);
        assert_eq!(s.lease_id, 0);
    }
    #[test]
    fn recovery_finish_clears_running_only_when_requested() {
        let mut s = PathState::default();
        apply(&mut s, cmd(4));
        s.status.running = true;
        let lease = s.lease_id;
        apply(
            &mut s,
            Command {
                action: 5,
                lease_id: lease,
                flag: true,
                ..cmd(5)
            },
        );
        assert!(!s.busy && !s.status.running);
    }
    #[test]
    fn bad_lease_and_busy_rejection_have_no_mutation() {
        let mut s = PathState::default();
        apply(&mut s, cmd(4));
        let before = s.clone();
        assert_eq!(
            apply_payload(&mut s, &encode_command(&cmd(4))),
            Err(Error::Busy)
        );
        assert_eq!(s, before);
        assert_eq!(
            apply_payload(
                &mut s,
                &encode_command(&Command {
                    lease_id: 99,
                    ..cmd(5)
                })
            ),
            Err(Error::Stale)
        );
        assert_eq!(s, before)
    }
    #[test]
    fn generation_and_lease_exhaustion_are_not_wrapped() {
        let mut s = PathState {
            generation: u64::MAX,
            ..PathState::default()
        };
        let before = s.clone();
        assert_eq!(
            apply_payload(&mut s, &encode_command(&cmd(2))),
            Err(Error::Overflow)
        );
        assert_eq!(s, before);
        s.next_lease = u64::MAX;
        assert_eq!(
            apply_payload(&mut s, &encode_command(&cmd(4))),
            Err(Error::Overflow)
        );
        assert!(!s.busy)
    }
    #[test]
    fn alert_counter_wraps_exactly_and_other_fields_unchanged() {
        let mut s = full();
        let before = s.clone();
        apply(&mut s, cmd(13));
        assert_eq!(s.status.alert_id, 0);
        s.status.alert_id = u64::MAX;
        assert_eq!(s, before)
    }
    #[test]
    fn textual_and_boolean_actions_change_only_named_domain_member() {
        let mappings = [
            (7, "phase"),
            (8, "note"),
            (9, "alert"),
            (11, "retry"),
            (12, "at"),
            (19, "node"),
        ];
        for (action, text) in mappings {
            let mut s = PathState::default();
            apply(
                &mut s,
                Command {
                    text: text.as_bytes().to_vec(),
                    ..cmd(action)
                },
            );
            let observed = match action {
                7 => s.status.phase,
                8 => s.status.note,
                9 => s.status.alert,
                11 => s.status.retry_mode,
                12 => s.status.retry_at,
                _ => s.status.active_node,
            };
            assert_eq!(observed, text.as_bytes());
            assert!(!s.busy)
        }
        for action in [6, 10] {
            let mut s = PathState::default();
            apply(
                &mut s,
                Command {
                    flag: true,
                    ..cmd(action)
                },
            );
            assert_eq!(s.status.running, action == 6);
            assert_eq!(s.status.demanded, action == 10);
        }
    }
    #[test]
    fn append_switch_from_nil_and_clock_zero_are_valid() {
        let mut s = PathState::default();
        apply(
            &mut s,
            Command {
                text: vec![255],
                ..cmd(15)
            },
        );
        assert_eq!(s.status.switched, Some(vec![vec![255]]));
        apply(
            &mut s,
            Command {
                time_token: u64::MAX,
                ..cmd(16)
            },
        );
        assert_eq!(s.at_token, u64::MAX);
        apply(&mut s, cmd(16));
        assert_eq!(s.at_token, 0);
    }
    #[test]
    fn samples_and_throttle_reject_stale_generation() {
        let mut s = PathState::default();
        apply(&mut s, cmd(2));
        for action in [20, 21] {
            let before = s.clone();
            let c = Command {
                generation: 2,
                time_token: 1,
                ..cmd(action)
            };
            assert_eq!(
                apply_payload(&mut s, &encode_command(&c)),
                Err(Error::Stale)
            );
            assert_eq!(s, before)
        }
        apply(&mut s, cmd(3));
        let before = s.clone();
        assert_eq!(
            apply_payload(
                &mut s,
                &encode_command(&Command {
                    generation: 1,
                    ..cmd(20)
                })
            ),
            Err(Error::Stale)
        );
        assert_eq!(s, before);
    }
    #[test]
    fn throttle_updates_in_place_sorted_and_revoke_resets_maps() {
        let mut s = PathState::default();
        apply(&mut s, cmd(2));
        for (label, token) in [(b"z".as_slice(), 1), (b"a", 2), (b"z", 3)] {
            apply(
                &mut s,
                Command {
                    text: label.to_vec(),
                    time_token: token,
                    generation: 1,
                    ..cmd(21)
                },
            )
        }
        assert_eq!(
            s.throttle.as_ref().unwrap(),
            &vec![
                Throttle {
                    label: b"a".to_vec(),
                    time_token: 2
                },
                Throttle {
                    label: b"z".to_vec(),
                    time_token: 3
                }
            ]
        );
        apply(
            &mut s,
            Command {
                generation: 1,
                samples: Some(vec![Sample {
                    id: b"a".to_vec(),
                    upload: -7,
                }]),
                ..cmd(20)
            },
        );
        apply(&mut s, cmd(3));
        assert_eq!(s.previous, None);
        assert_eq!(s.throttle, None);
    }
    #[test]
    fn duplicate_or_unsorted_ledger_rejected() {
        for ids in [[b"b".as_slice(), b"a"], [b"a", b"a"]] {
            let s = PathState {
                previous: Some(
                    ids.into_iter()
                        .map(|id| Sample {
                            id: id.to_vec(),
                            upload: 0,
                        })
                        .collect(),
                ),
                ..PathState::default()
            };
            assert_eq!(decode(&encode(&s)), Err(Error::Invalid));
            let c = Command {
                samples: s.previous,
                ..cmd(20)
            };
            assert_eq!(validate_payload(&encode_command(&c)), Err(Error::Invalid));
        }
        let s = PathState {
            throttle: Some(vec![Throttle::default()]),
            ..PathState::default()
        };
        assert_eq!(decode(&encode(&s)), Err(Error::Invalid));
    }
    #[test]
    fn structural_state_constraints_do_not_conflate_busy_and_running() {
        for s in [
            PathState {
                busy: true,
                ..PathState::default()
            },
            PathState {
                lease_id: 1,
                next_lease: 1,
                ..PathState::default()
            },
            PathState {
                monitor_active: true,
                ..PathState::default()
            },
        ] {
            assert_eq!(decode(&encode(&s)), Err(Error::Invalid));
        }
        let s = PathState {
            status: Status {
                running: true,
                ..Status::default()
            },
            ..PathState::default()
        };
        assert_eq!(decode(&encode(&s)), Ok(s));
    }
    #[test]
    fn malformed_codec_and_trailing_bytes_do_not_apply() {
        let mut s = full();
        let original = s.clone();
        let mut trailing = encode_command(&cmd(8));
        trailing.push(0);
        for b in [vec![], vec![22], trailing, vec![0; MAX_PATH_BYTES + 1]] {
            assert!(apply_payload(&mut s, &b).is_err());
            assert_eq!(s, original)
        }
        let mut invalid = encode(&PathState::default());
        invalid[0] = 2;
        assert!(decode(&invalid).is_err());
        let mut nil_count = encode(&PathState::default());
        nil_count[10] = 1;
        assert!(decode(&nil_count).is_err());
    }
    #[test]
    fn oversized_effect_is_rejected_atomically() {
        let mut s = PathState::default();
        s.status.note = vec![1; MAX_PATH_BYTES / 2];
        let before = s.clone();
        let c = Command {
            text: vec![2; MAX_PATH_BYTES / 2],
            ..cmd(8)
        };
        apply_payload(&mut s, &encode_command(&c)).unwrap();
        assert_ne!(s, before);
        let old = s.clone();
        let c = Command {
            text: vec![3; MAX_PATH_BYTES / 2],
            ..cmd(9)
        };
        assert_eq!(
            apply_payload(&mut s, &encode_command(&c)),
            Err(Error::Limit)
        );
        assert_eq!(s, old);
    }
    #[test]
    fn one_hundred_thousand_samples_roundtrip() {
        let mut s = PathState::default();
        apply(&mut s, cmd(2));
        let samples = (0..100000)
            .map(|i| Sample {
                id: format!("{i:06}").into_bytes(),
                upload: i,
            })
            .collect();
        apply(
            &mut s,
            Command {
                samples: Some(samples),
                generation: 1,
                ..cmd(20)
            },
        );
        let b = encode(&s);
        assert!(b.len() < MAX_PATH_BYTES);
        assert_eq!(decode(&b), Ok(s));
    }
}

/// Validate the fields determined by the final acknowledged command. This cannot
/// reconstruct the prior value of a wrapping increment or prove an earlier history.
pub fn validate_last(s: &PathState, payload: &[u8]) -> Result<(), Error> {
    validate(s)?;
    let c = decode_command(payload)?;
    let ok = match c.action {
        1 => s.config == c.config,
        2 => s.monitor_active && s.generation > 0 && s.previous.is_none() && s.throttle.is_none(),
        3 => !s.monitor_active && s.previous.is_none() && s.throttle.is_none(),
        4 => s.busy && s.lease_id == s.next_lease && s.lease_id != 0,
        5 => !s.busy && s.lease_id == 0 && (!c.flag || !s.status.running) && c.lease_id != 0,
        6 => s.status.running == c.flag,
        7 => s.status.phase == c.text,
        8 => s.status.note == c.text,
        9 => s.status.alert == c.text,
        10 => s.status.demanded == c.flag,
        11 => s.status.retry_mode == c.text,
        12 => s.status.retry_at == c.text,
        13 => true,
        14 => s.status.switched == c.strings,
        15 => s.status.switched.as_ref().and_then(|v| v.last()) == Some(&c.text),
        16 => s.at_token == c.time_token,
        17 => s.status.results == c.results,
        18 => s.status.failures == c.strings,
        19 => s.status.active_node == c.text,
        20 => s.monitor_active && s.generation == c.generation && s.previous == c.samples,
        21 => {
            s.monitor_active
                && s.generation == c.generation
                && s.throttle.as_ref().is_some_and(|v| {
                    v.iter()
                        .any(|t| t.label == c.text && t.time_token == c.time_token)
                })
        }
        _ => false,
    };
    if ok {
        Ok(())
    } else {
        Err(Error::Invalid)
    }
}
#[cfg(test)]
mod checkpoint_tests {
    use super::*;
    #[test]
    fn corrupted_last_effect_field_and_nil_shape_rejected() {
        let mut s = PathState::default();
        let c = Command {
            action: 18,
            strings: Some(vec![]),
            ..Command::default()
        };
        let payload = encode_command(&c);
        apply_payload(&mut s, &payload).unwrap();
        validate_last(&s, &payload).unwrap();
        s.status.failures = None;
        assert!(validate_last(&s, &payload).is_err());
        let c = Command {
            action: 8,
            text: b"kept".to_vec(),
            ..Command::default()
        };
        let payload = encode_command(&c);
        apply_payload(&mut s, &payload).unwrap();
        s.status.note = b"tampered".to_vec();
        assert!(validate_last(&s, &payload).is_err())
    }
    #[test]
    fn alert_u64_wrap_and_increment_checkpoint_limit() {
        let mut s = PathState {
            status: Status {
                alert_id: u64::MAX,
                ..Status::default()
            },
            ..PathState::default()
        };
        let payload = encode_command(&Command {
            action: 13,
            ..Command::default()
        });
        apply_payload(&mut s, &payload).unwrap();
        assert_eq!(s.status.alert_id, 0);
        validate_last(&s, &payload).unwrap();
    }
}
