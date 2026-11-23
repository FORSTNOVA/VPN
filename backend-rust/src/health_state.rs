//! Health/region/job authority. Keys are independent of record names; clocks are opaque tokens.
use std::collections::BTreeMap;
pub const MAX_HEALTH_BYTES: usize = 16 << 20;
/// Reserve room for query reply metadata and default record envelopes.
pub const MAX_STATE_BYTES: usize = MAX_HEALTH_BYTES - 1024;
const MAX_ITEMS: usize = 100000;
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Invalid,
    Limit,
    Nil,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Params {
    pub values: [i64; 6],
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct HealthRecord {
    pub name: Vec<u8>,
    pub state: Vec<u8>,
    pub failures: i64,
    pub successes: i64,
    pub latency: i64,
    pub last_success: u64,
    pub cooldown: u64,
    pub updated: u64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct RegionRecord {
    pub name: Vec<u8>,
    pub exit_ip: Vec<u8>,
    pub country: Vec<u8>,
    pub status: Vec<u8>,
    pub sources: Option<Vec<Vec<u8>>>,
    pub detail: Vec<u8>,
    pub verified: u64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Job {
    pub running: bool,
    pub total: i64,
    pub done: i64,
    pub verified: i64,
    pub skipped: i64,
    pub failed: i64,
    pub started_at: Vec<u8>,
    pub finished_at: Vec<u8>,
    pub last_error: Vec<u8>,
}
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HealthState {
    params: Params,
    health: Option<BTreeMap<Vec<u8>, HealthRecord>>,
    regions: Option<BTreeMap<Vec<u8>, RegionRecord>>,
    job: Option<Job>,
    blocked: bool,
    block_reason: Vec<u8>,
    sweep_note: Vec<u8>,
    last_switch: u64,
    health_bytes: usize,
    region_bytes: usize,
    encoded_size: usize,
}
impl Default for HealthState {
    fn default() -> Self {
        Self {
            params: Params::default(),
            health: None,
            regions: None,
            job: None,
            blocked: false,
            block_reason: vec![],
            sweep_note: vec![],
            last_switch: 0,
            health_bytes: 9,
            region_bytes: 9,
            encoded_size: 92,
        }
    }
}
impl HealthState {
    pub fn job_running(&self) -> bool {
        self.job.as_ref().is_some_and(|j| j.running)
    }
    pub fn params(&self) -> &Params {
        &self.params
    }
    pub fn job(&self) -> Option<&Job> {
        self.job.as_ref()
    }
    pub fn blocked(&self) -> bool {
        self.blocked
    }
    pub fn block_reason(&self) -> &[u8] {
        &self.block_reason
    }
    pub fn sweep_note(&self) -> &[u8] {
        &self.sweep_note
    }
    pub fn last_switch(&self) -> u64 {
        self.last_switch
    }
    pub fn health(&self) -> Option<&BTreeMap<Vec<u8>, HealthRecord>> {
        self.health.as_ref()
    }
    pub fn regions(&self) -> Option<&BTreeMap<Vec<u8>, RegionRecord>> {
        self.regions.as_ref()
    }
    pub fn encoded_size(&self) -> usize {
        self.encoded_size
    }
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Command {
    pub action: u8,
    pub params: Params,
    pub health: Option<BTreeMap<Vec<u8>, HealthRecord>>,
    pub regions: Option<BTreeMap<Vec<u8>, RegionRecord>>,
    pub key: Vec<u8>,
    pub health_record: HealthRecord,
    pub region_record: RegionRecord,
    pub job: Option<Job>,
    pub flag: bool,
    pub text: Vec<u8>,
    pub detail: Vec<u8>,
    pub count: i64,
    pub time_token: u64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct QueryReply {
    pub found: bool,
    pub state: HealthState,
    pub health_record: HealthRecord,
    pub region_record: RegionRecord,
}
fn bounded(n: usize) -> Result<usize, Error> {
    if n > MAX_HEALTH_BYTES {
        Err(Error::Limit)
    } else {
        Ok(n)
    }
}
fn state_bounded(n: usize) -> Result<usize, Error> {
    if n > MAX_STATE_BYTES {
        Err(Error::Limit)
    } else {
        Ok(n)
    }
}
fn size_health(h: &HealthRecord) -> usize {
    64 + h.name.len() + h.state.len()
}
fn size_region(h: &RegionRecord) -> Result<usize, Error> {
    let mut n =
        57 + h.name.len() + h.exit_ip.len() + h.country.len() + h.status.len() + h.detail.len();
    if let Some(v) = &h.sources {
        if v.len() > MAX_ITEMS {
            return Err(Error::Limit);
        }
        for s in v {
            n = n.checked_add(8 + s.len()).ok_or(Error::Limit)?
        }
    }
    bounded(n)
}
fn size_job(j: &Option<Job>) -> usize {
    j.as_ref().map_or(1, |j| {
        66 + j.started_at.len() + j.finished_at.len() + j.last_error.len()
    })
}
fn size_map<T>(
    m: &Option<BTreeMap<Vec<u8>, T>>,
    mut size: impl FnMut(&T) -> Result<usize, Error>,
) -> Result<usize, Error> {
    let mut n = 9usize;
    if let Some(m) = m {
        if m.len() > MAX_ITEMS {
            return Err(Error::Limit);
        }
        for (k, v) in m {
            n = n.checked_add(8 + k.len() + size(v)?).ok_or(Error::Limit)?;
            bounded(n)?;
        }
    }
    Ok(n)
}
fn budget(s: &HealthState, old: usize, new: usize) -> Result<usize, Error> {
    state_bounded(
        s.encoded_size
            .checked_sub(old)
            .and_then(|x| x.checked_add(new))
            .ok_or(Error::Limit)?,
    )
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
        if b.len() > MAX_HEALTH_BYTES.saturating_sub(self.b.len()) {
            self.bad = true;
            return;
        }
        self.b.extend_from_slice(b)
    }
    fn flag(&mut self, b: bool) {
        self.put(&[b as u8])
    }
    fn n(&mut self, n: u64) {
        self.put(&n.to_le_bytes())
    }
    fn str(&mut self, b: &[u8]) {
        self.n(b.len() as u64);
        self.put(b)
    }
    fn count(&mut self, p: bool, n: usize) {
        if n > MAX_ITEMS {
            self.bad = true;
            return;
        }
        self.flag(p);
        self.n(n as u64)
    }
    fn params(&mut self, p: &Params) {
        for n in p.values {
            self.n(n as u64)
        }
    }
    fn health(&mut self, h: &HealthRecord) {
        self.str(&h.name);
        self.str(&h.state);
        for n in [h.failures, h.successes, h.latency] {
            self.n(n as u64)
        }
        for n in [h.last_success, h.cooldown, h.updated] {
            self.n(n)
        }
    }
    fn region(&mut self, h: &RegionRecord) {
        for s in [&h.name, &h.exit_ip, &h.country, &h.status] {
            self.str(s)
        }
        self.count(h.sources.is_some(), h.sources.as_ref().map_or(0, Vec::len));
        if let Some(v) = &h.sources {
            for s in v {
                self.str(s)
            }
        }
        self.str(&h.detail);
        self.n(h.verified)
    }
    fn map<T>(&mut self, m: &Option<BTreeMap<Vec<u8>, T>>, mut f: impl FnMut(&mut Self, &T)) {
        self.count(m.is_some(), m.as_ref().map_or(0, BTreeMap::len));
        if let Some(m) = m {
            for (k, v) in m {
                if self.bad {
                    break;
                }
                self.str(k);
                f(self, v)
            }
        }
    }
    fn job(&mut self, j: &Option<Job>) {
        self.flag(j.is_some());
        if let Some(j) = j {
            self.flag(j.running);
            for n in [j.total, j.done, j.verified, j.skipped, j.failed] {
                self.n(n as u64)
            }
            for s in [&j.started_at, &j.finished_at, &j.last_error] {
                self.str(s)
            }
        }
    }
    fn state(&mut self, s: &HealthState, metadata: bool) {
        self.params(&s.params);
        if metadata {
            self.count(false, 0);
            self.count(false, 0)
        } else {
            self.map(&s.health, Self::health);
            self.map(&s.regions, Self::region)
        }
        self.job(&s.job);
        self.flag(s.blocked);
        self.str(&s.block_reason);
        self.str(&s.sweep_note);
        self.n(s.last_switch)
    }
    fn done(self) -> Result<Vec<u8>, Error> {
        if self.bad {
            Err(Error::Limit)
        } else {
            Ok(self.b)
        }
    }
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
    fn str(&mut self) -> Result<Vec<u8>, Error> {
        let n = usize::try_from(self.n()?).map_err(|_| Error::Limit)?;
        bounded(n)?;
        Ok(self.take(n)?.to_vec())
    }
    fn count(&mut self) -> Result<(bool, usize), Error> {
        let p = self.flag()?;
        let n = usize::try_from(self.n()?).map_err(|_| Error::Limit)?;
        if n > MAX_ITEMS || (!p && n != 0) {
            return Err(Error::Invalid);
        }
        Ok((p, n))
    }
    fn params(&mut self) -> Result<Params, Error> {
        let mut p = Params::default();
        for n in &mut p.values {
            *n = self.n()? as i64
        }
        Ok(p)
    }
    fn health(&mut self) -> Result<HealthRecord, Error> {
        Ok(HealthRecord {
            name: self.str()?,
            state: self.str()?,
            failures: self.n()? as i64,
            successes: self.n()? as i64,
            latency: self.n()? as i64,
            last_success: self.n()?,
            cooldown: self.n()?,
            updated: self.n()?,
        })
    }
    fn region(&mut self) -> Result<RegionRecord, Error> {
        let name = self.str()?;
        let exit_ip = self.str()?;
        let country = self.str()?;
        let status = self.str()?;
        let (p, n) = self.count()?;
        let sources = if p {
            let mut v = Vec::with_capacity(n.min(1024));
            for _ in 0..n {
                v.push(self.str()?)
            }
            Some(v)
        } else {
            None
        };
        Ok(RegionRecord {
            name,
            exit_ip,
            country,
            status,
            sources,
            detail: self.str()?,
            verified: self.n()?,
        })
    }
    fn map<T>(
        &mut self,
        mut f: impl FnMut(&mut Self) -> Result<T, Error>,
    ) -> Result<Option<BTreeMap<Vec<u8>, T>>, Error> {
        let (p, n) = self.count()?;
        if !p {
            return Ok(None);
        }
        let mut map = BTreeMap::new();
        let mut last = None;
        for _ in 0..n {
            let k = self.str()?;
            if last.as_ref().is_some_and(|l: &Vec<u8>| l >= &k) {
                return Err(Error::Invalid);
            }
            last = Some(k.clone());
            map.insert(k, f(self)?);
        }
        Ok(Some(map))
    }
    fn job(&mut self) -> Result<Option<Job>, Error> {
        if !self.flag()? {
            return Ok(None);
        }
        Ok(Some(Job {
            running: self.flag()?,
            total: self.n()? as i64,
            done: self.n()? as i64,
            verified: self.n()? as i64,
            skipped: self.n()? as i64,
            failed: self.n()? as i64,
            started_at: self.str()?,
            finished_at: self.str()?,
            last_error: self.str()?,
        }))
    }
    fn state(&mut self) -> Result<HealthState, Error> {
        let mut s = HealthState {
            params: self.params()?,
            health: self.map(Self::health)?,
            regions: self.map(Self::region)?,
            job: self.job()?,
            blocked: self.flag()?,
            block_reason: self.str()?,
            sweep_note: self.str()?,
            last_switch: self.n()?,
            ..HealthState::default()
        };
        s.health_bytes = size_map(&s.health, |h| bounded(size_health(h)))?;
        s.region_bytes = size_map(&s.regions, size_region)?;
        s.encoded_size = state_bounded(
            48 + s.health_bytes
                + s.region_bytes
                + size_job(&s.job)
                + 1
                + 8
                + s.block_reason.len()
                + 8
                + s.sweep_note.len()
                + 8,
        )?;
        Ok(s)
    }
    fn done(&self) -> Result<(), Error> {
        if self.p == self.b.len() {
            Ok(())
        } else {
            Err(Error::Invalid)
        }
    }
}
pub fn decode(b: &[u8]) -> Result<HealthState, Error> {
    if b.is_empty() {
        return Ok(HealthState::default());
    }
    bounded(b.len())?;
    let mut r = Reader { b, p: 0 };
    let s = r.state()?;
    r.done()?;
    Ok(s)
}
pub fn encode(s: &HealthState) -> Result<Vec<u8>, Error> {
    state_bounded(s.encoded_size)?;
    let mut w = Writer::new();
    w.state(s, false);
    w.done()
}
pub fn encode_command(c: &Command) -> Result<Vec<u8>, Error> {
    let mut w = Writer::new();
    w.put(&[c.action]);
    w.params(&c.params);
    w.map(&c.health, Writer::health);
    w.map(&c.regions, Writer::region);
    w.str(&c.key);
    w.health(&c.health_record);
    w.region(&c.region_record);
    w.job(&c.job);
    w.flag(c.flag);
    w.str(&c.text);
    w.str(&c.detail);
    w.n(c.count as u64);
    w.n(c.time_token);
    w.done()
}
pub fn decode_command(b: &[u8]) -> Result<Command, Error> {
    bounded(b.len())?;
    let mut r = Reader { b, p: 0 };
    let c = Command {
        action: r.byte()?,
        params: r.params()?,
        health: r.map(Reader::health)?,
        regions: r.map(Reader::region)?,
        key: r.str()?,
        health_record: r.health()?,
        region_record: r.region()?,
        job: r.job()?,
        flag: r.flag()?,
        text: r.str()?,
        detail: r.str()?,
        count: r.n()? as i64,
        time_token: r.n()?,
    };
    r.done()?;
    if !(1..=15).contains(&c.action) {
        return Err(Error::Invalid);
    }
    Ok(c)
}
pub fn validate_payload(b: &[u8]) -> Result<(), Error> {
    decode_command(b).map(|_| ())
}
pub fn apply_payload(s: &mut HealthState, b: &[u8]) -> Result<(), Error> {
    let c = decode_command(b)?;
    match c.action {
        1 => s.params = c.params,
        2 => {
            let n = size_map(&c.health, |h| bounded(size_health(h)))?;
            let total = budget(s, s.health_bytes, n)?;
            s.health = c.health;
            s.health_bytes = n;
            s.encoded_size = total
        }
        3 => {
            let n = size_map(&c.regions, size_region)?;
            let total = budget(s, s.region_bytes, n)?;
            s.regions = c.regions;
            s.region_bytes = n;
            s.encoded_size = total
        }
        4 => {
            let map = s.health.as_ref().ok_or(Error::Nil)?;
            let old = map.get(&c.key);
            if old.is_none() && map.len() >= MAX_ITEMS {
                return Err(Error::Limit);
            }
            let old = old.map_or(0, |h| 8 + c.key.len() + size_health(h));
            let new = 8 + c.key.len() + size_health(&c.health_record);
            let total = budget(s, old, new)?;
            s.health.as_mut().unwrap().insert(c.key, c.health_record);
            s.health_bytes = s.health_bytes - old + new;
            s.encoded_size = total
        }
        5 => {
            let map = s.regions.as_ref().ok_or(Error::Nil)?;
            let old = map.get(&c.key);
            if old.is_none() && map.len() >= MAX_ITEMS {
                return Err(Error::Limit);
            }
            let old = match old {
                Some(h) => 8 + c.key.len() + size_region(h)?,
                None => 0,
            };
            let new = 8 + c.key.len() + size_region(&c.region_record)?;
            let total = budget(s, old, new)?;
            s.regions.as_mut().unwrap().insert(c.key, c.region_record);
            s.region_bytes = s.region_bytes - old + new;
            s.encoded_size = total
        }
        6 => {
            s.encoded_size = s.encoded_size - s.health_bytes + 9;
            s.health_bytes = 9;
            s.health = Some(BTreeMap::new())
        }
        7 => {
            s.encoded_size = s.encoded_size - s.region_bytes + 9;
            s.region_bytes = 9;
            s.regions = Some(BTreeMap::new())
        }
        8 => s.blocked = c.flag,
        9 => {
            let total = budget(s, s.block_reason.len(), c.text.len())?;
            s.block_reason = c.text;
            s.encoded_size = total
        }
        10 => {
            let total = budget(s, s.sweep_note.len(), c.text.len())?;
            s.sweep_note = c.text;
            s.encoded_size = total
        }
        11 => s.last_switch = c.time_token,
        12 => {
            let total = budget(s, size_job(&s.job), size_job(&c.job))?;
            s.job = c.job;
            s.encoded_size = total
        }
        13 => s.job.as_mut().ok_or(Error::Nil)?.total = c.count,
        14 => {
            let j = s.job.as_mut().ok_or(Error::Nil)?;
            j.done = j.done.wrapping_add(1);
            match c.text.as_slice() {
                b"skip" => j.skipped = j.skipped.wrapping_add(1),
                b"verified" => j.verified = j.verified.wrapping_add(1),
                b"failed" => j.failed = j.failed.wrapping_add(1),
                _ => {}
            }
        }
        15 => {
            if let Some(j) = &s.job {
                let old = j.finished_at.len() + if c.flag { j.last_error.len() } else { 0 };
                let new = c.text.len() + if c.flag { c.detail.len() } else { 0 };
                let total = budget(s, old, new)?;
                let j = s.job.as_mut().unwrap();
                j.running = false;
                j.finished_at = c.text;
                if c.flag {
                    j.last_error = c.detail
                };
                s.encoded_size = total
            }
        }
        _ => unreachable!(),
    };
    Ok(())
}
pub fn validate_last(s: &HealthState, b: &[u8]) -> Result<(), Error> {
    let c = decode_command(b)?;
    let valid = match c.action {
        1 => s.params == c.params,
        2 => s.health == c.health,
        3 => s.regions == c.regions,
        4 => s.health.as_ref().and_then(|m| m.get(&c.key)) == Some(&c.health_record),
        5 => s.regions.as_ref().and_then(|m| m.get(&c.key)) == Some(&c.region_record),
        6 => s.health.as_ref().is_some_and(BTreeMap::is_empty),
        7 => s.regions.as_ref().is_some_and(BTreeMap::is_empty),
        8 => s.blocked == c.flag,
        9 => s.block_reason == c.text,
        10 => s.sweep_note == c.text,
        11 => s.last_switch == c.time_token,
        12 => s.job == c.job,
        13 => s.job.as_ref().is_some_and(|j| j.total == c.count),
        // A checkpoint has no pre-command job counters. Presence is the only
        // postcondition provable here; this does not authenticate prior history
        // or prove the wrapping increment from an unavailable previous value.
        14 => s.job.is_some(),
        // Finishing a missing job is intentionally the original no-op.
        15 => s.job.as_ref().is_none_or(|j| {
            !j.running && j.finished_at == c.text && (!c.flag || j.last_error == c.detail)
        }),
        _ => false,
    };
    if valid {
        Ok(())
    } else {
        Err(Error::Invalid)
    }
}
pub fn encode_query(kind: u8, key: &[u8]) -> Result<Vec<u8>, Error> {
    let mut w = Writer::new();
    w.put(&[kind]);
    w.str(key);
    w.done()
}
pub fn query(s: &HealthState, b: &[u8]) -> Result<Vec<u8>, Error> {
    bounded(b.len())?;
    let mut r = Reader { b, p: 0 };
    let kind = r.byte()?;
    let key = r.str()?;
    r.done()?;
    let mut w = Writer::new();
    let empty = HealthState::default();
    let h = HealthRecord::default();
    let g = RegionRecord::default();
    match kind {
        0 => {
            w.flag(false);
            w.state(s, true);
            w.health(&h);
            w.region(&g)
        }
        1 => {
            let v = s.health.as_ref().and_then(|m| m.get(&key));
            w.flag(v.is_some());
            w.state(&empty, false);
            w.health(v.unwrap_or(&h));
            w.region(&g)
        }
        2 => {
            let v = s.regions.as_ref().and_then(|m| m.get(&key));
            w.flag(v.is_some());
            w.state(&empty, false);
            w.health(&h);
            w.region(v.unwrap_or(&g))
        }
        3 => {
            w.flag(false);
            w.state(s, false);
            w.health(&h);
            w.region(&g)
        }
        _ => return Err(Error::Invalid),
    }
    w.done()
}
pub fn decode_reply(b: &[u8]) -> Result<QueryReply, Error> {
    bounded(b.len())?;
    let mut r = Reader { b, p: 0 };
    let q = QueryReply {
        found: r.flag()?,
        state: r.state()?,
        health_record: r.health()?,
        region_record: r.region()?,
    };
    r.done()?;
    Ok(q)
}

#[cfg(test)]
mod tests {
    use super::*;
    fn c(action: u8) -> Command {
        Command {
            action,
            ..Command::default()
        }
    }
    fn apply(s: &mut HealthState, c: Command) {
        let b = encode_command(&c).unwrap();
        apply_payload(s, &b).unwrap();
        validate_last(s, &b).unwrap();
        assert_eq!(encode(s).unwrap().len(), s.encoded_size())
    }
    fn h() -> HealthRecord {
        HealthRecord {
            name: vec![0, 255],
            state: b"UNKNOWN".to_vec(),
            failures: i64::MIN,
            successes: i64::MAX,
            latency: -9,
            last_success: u64::MAX,
            cooldown: 3,
            updated: 4,
        }
    }
    fn g() -> RegionRecord {
        RegionRecord {
            name: b"record-name-not-key".to_vec(),
            exit_ip: b"not canonical".to_vec(),
            country: b"XX".to_vec(),
            status: b"other".to_vec(),
            sources: Some(vec![vec![], vec![255]]),
            detail: vec![0],
            verified: u64::MAX,
        }
    }
    #[test]
    fn default_canonical_size_and_nil_empty_map_distinction() {
        let mut s = decode(&[]).unwrap();
        assert_eq!(s.encoded_size(), 92);
        assert_eq!(decode(&encode(&s).unwrap()).unwrap(), s);
        assert!(s.health().is_none());
        apply(&mut s, c(6));
        apply(&mut s, c(7));
        assert_eq!(s.health().unwrap().len(), 0);
        assert_eq!(s.regions().unwrap().len(), 0);
        assert_eq!(s.encoded_size(), 92);
        assert_eq!(decode(&encode(&s).unwrap()).unwrap(), s);
    }
    #[test]
    fn roundtrip_full_fields_rawbytes_and_independent_map_names() {
        let mut s = HealthState::default();
        apply(
            &mut s,
            Command {
                params: Params {
                    values: [-1, i64::MIN, i64::MAX, 4, 5, 6],
                },
                ..c(1)
            },
        );
        apply(
            &mut s,
            Command {
                health: Some(BTreeMap::from([(b"key".to_vec(), h())])),
                ..c(2)
            },
        );
        apply(
            &mut s,
            Command {
                regions: Some(BTreeMap::from([(vec![255], g())])),
                ..c(3)
            },
        );
        apply(
            &mut s,
            Command {
                job: Some(Job {
                    running: true,
                    total: -7,
                    done: i64::MAX,
                    verified: 5,
                    skipped: 6,
                    failed: 7,
                    started_at: b"offset-preserved".to_vec(),
                    finished_at: b"old".to_vec(),
                    last_error: vec![0, 255],
                }),
                ..c(12)
            },
        );
        apply(&mut s, Command { flag: true, ..c(8) });
        apply(
            &mut s,
            Command {
                text: vec![255],
                ..c(9)
            },
        );
        apply(
            &mut s,
            Command {
                text: b"anomaly".to_vec(),
                ..c(10)
            },
        );
        apply(
            &mut s,
            Command {
                time_token: u64::MAX,
                ..c(11)
            },
        );
        let b = encode(&s).unwrap();
        assert_eq!(decode(&b).unwrap(), s);
        assert_eq!(s.health().unwrap()[b"key".as_slice()].name, vec![0, 255]);
    }
    #[test]
    fn command_roundtrip_all_unused_payload_fields() {
        let v = Command {
            action: 8,
            params: Params {
                values: [1, 2, 3, 4, 5, 6],
            },
            health: Some(BTreeMap::from([(vec![0], h())])),
            regions: Some(BTreeMap::from([(vec![255], g())])),
            key: vec![0, 255],
            health_record: h(),
            region_record: g(),
            job: Some(Job::default()),
            flag: true,
            text: vec![255],
            detail: vec![0],
            count: i64::MIN,
            time_token: u64::MAX,
        };
        assert_eq!(decode_command(&encode_command(&v).unwrap()), Ok(v));
    }
    #[test]
    fn record_nil_map_rejects_and_clear_creates_non_nil() {
        let mut s = HealthState::default();
        for action in [4, 5] {
            assert_eq!(
                apply_payload(&mut s, &encode_command(&c(action)).unwrap()),
                Err(Error::Nil)
            );
            assert_eq!(s, HealthState::default())
        }
        apply(&mut s, c(6));
        apply(&mut s, c(7));
        apply(
            &mut s,
            Command {
                key: b"k".to_vec(),
                health_record: h(),
                ..c(4)
            },
        );
        apply(
            &mut s,
            Command {
                key: b"k".to_vec(),
                region_record: g(),
                ..c(5)
            },
        );
        assert_eq!(s.health().unwrap()[b"k".as_slice()], h());
        assert_eq!(s.regions().unwrap()[b"k".as_slice()], g());
    }
    #[test]
    fn restore_nil_empty_and_source_nil_empty() {
        let mut s = HealthState::default();
        apply(&mut s, c(6));
        apply(&mut s, c(2));
        assert!(s.health().is_none());
        for present in [false, true] {
            let mut rec = g();
            rec.sources = present.then(Vec::new);
            apply(
                &mut s,
                Command {
                    regions: Some(BTreeMap::from([(b"r".to_vec(), rec)])),
                    ..c(3)
                },
            );
            assert_eq!(decode(&encode(&s).unwrap()).unwrap(), s);
            assert_eq!(
                s.regions().unwrap()[b"r".as_slice()].sources.is_some(),
                present
            )
        }
    }
    #[test]
    fn late_finish_updates_current_job_no_generation_and_keeps_old_error() {
        let mut s = HealthState::default();
        apply(
            &mut s,
            Command {
                job: Some(Job {
                    running: true,
                    last_error: b"older".to_vec(),
                    ..Job::default()
                }),
                ..c(12)
            },
        );
        apply(
            &mut s,
            Command {
                job: Some(Job {
                    running: true,
                    last_error: b"new-job-error".to_vec(),
                    ..Job::default()
                }),
                ..c(12)
            },
        );
        apply(
            &mut s,
            Command {
                text: b"late-old-finish".to_vec(),
                detail: b"must-ignore".to_vec(),
                ..c(15)
            },
        );
        let j = s.job().unwrap();
        assert!(!j.running);
        assert_eq!(j.finished_at, b"late-old-finish");
        assert_eq!(j.last_error, b"new-job-error");
        apply(
            &mut s,
            Command {
                flag: true,
                text: b"second".to_vec(),
                detail: b"explicit".to_vec(),
                ..c(15)
            },
        );
        assert_eq!(s.job().unwrap().last_error, b"explicit");
    }
    #[test]
    fn job_nil_finish_noop_total_advance_reject() {
        let mut s = HealthState::default();
        apply(&mut s, c(15));
        for action in [13, 14] {
            assert_eq!(
                apply_payload(&mut s, &encode_command(&c(action)).unwrap()),
                Err(Error::Nil)
            )
        }
        apply(
            &mut s,
            Command {
                job: Some(Job::default()),
                ..c(12)
            },
        );
        apply(&mut s, c(12));
        assert!(s.job().is_none());
    }
    #[test]
    fn job_count_wrapping_and_unknown_progress_only_done() {
        let mut s = HealthState::default();
        apply(
            &mut s,
            Command {
                job: Some(Job {
                    done: i64::MAX,
                    skipped: i64::MAX,
                    verified: i64::MAX,
                    failed: i64::MAX,
                    ..Job::default()
                }),
                ..c(12)
            },
        );
        apply(
            &mut s,
            Command {
                count: i64::MIN,
                ..c(13)
            },
        );
        for text in [b"skip".as_slice(), b"verified", b"failed", b"other"] {
            apply(
                &mut s,
                Command {
                    text: text.to_vec(),
                    ..c(14)
                },
            )
        }
        let j = s.job().unwrap();
        assert_eq!(j.total, i64::MIN);
        assert_eq!(j.done, i64::MIN + 3);
        assert_eq!(
            (j.skipped, j.verified, j.failed),
            (i64::MIN, i64::MIN, i64::MIN)
        );
    }
    #[test]
    fn anomaly_only_does_not_block_or_change_regions() {
        let mut s = HealthState::default();
        apply(
            &mut s,
            Command {
                regions: Some(BTreeMap::from([(b"r".to_vec(), g())])),
                ..c(3)
            },
        );
        let old = s.regions().unwrap().clone();
        apply(
            &mut s,
            Command {
                text: b"bulk failed, independent HTTPS alive".to_vec(),
                ..c(10)
            },
        );
        assert!(!s.blocked());
        assert_eq!(s.regions().unwrap(), &old);
        assert_eq!(s.last_switch(), 0);
    }
    #[test]
    fn queries_metadata_and_point_reads_keep_maps_out() {
        let mut s = HealthState::default();
        apply(
            &mut s,
            Command {
                health: Some(BTreeMap::from([(b"key".to_vec(), h())])),
                ..c(2)
            },
        );
        apply(
            &mut s,
            Command {
                regions: Some(BTreeMap::from([(b"key".to_vec(), g())])),
                ..c(3)
            },
        );
        for kind in 0..4 {
            let r =
                decode_reply(&query(&s, &encode_query(kind, b"key").unwrap()).unwrap()).unwrap();
            assert_eq!(r.found, kind == 1 || kind == 2);
            if kind != 3 {
                assert!(r.state.health().is_none() && r.state.regions().is_none())
            } else {
                assert_eq!(r.state, s)
            }
            if kind == 1 {
                assert_eq!(r.health_record, h())
            }
            if kind == 2 {
                assert_eq!(r.region_record, g())
            }
        }
        let r = decode_reply(&query(&s, &encode_query(1, b"missing").unwrap()).unwrap()).unwrap();
        assert!(!r.found);
        assert_eq!(r.health_record, HealthRecord::default());
    }
    #[test]
    fn malformed_action_query_and_trailing_bytes_are_nonmutating() {
        let mut s = HealthState::default();
        let old = s.clone();
        let mut trailing = encode_command(&c(6)).unwrap();
        trailing.push(0);
        for b in [vec![], vec![16], trailing, vec![0; MAX_HEALTH_BYTES + 1]] {
            assert!(apply_payload(&mut s, &b).is_err());
            assert_eq!(s, old)
        }
        assert!(query(&s, &encode_query(4, b"").unwrap()).is_err());
        let mut b = encode(&s).unwrap();
        b[48] = 2;
        assert!(decode(&b).is_err());
    }
    #[test]
    fn map_order_and_duplicates_rejected_before_apply() {
        for keys in [[b"b".as_slice(), b"a"], [b"a", b"a"]] {
            let mut w = Writer::new();
            w.put(&[2]);
            w.params(&Params::default());
            w.count(true, 2);
            for key in keys {
                w.str(key);
                w.health(&HealthRecord::default())
            }
            w.count(false, 0);
            w.str(b"");
            w.health(&HealthRecord::default());
            w.region(&RegionRecord::default());
            w.job(&None);
            w.flag(false);
            w.str(b"");
            w.str(b"");
            w.n(0);
            w.n(0);
            let mut s = HealthState::default();
            assert!(apply_payload(&mut s, &w.done().unwrap()).is_err());
            assert_eq!(s, HealthState::default());
        }
    }
    #[test]
    fn oversized_narrow_update_rolls_back_and_budget_replacement_shrinks() {
        let mut s = HealthState::default();
        apply(
            &mut s,
            Command {
                text: vec![1; MAX_HEALTH_BYTES / 2],
                ..c(9)
            },
        );
        let old = s.clone();
        let b = encode_command(&Command {
            text: vec![2; MAX_HEALTH_BYTES / 2],
            ..c(10)
        })
        .unwrap();
        assert_eq!(apply_payload(&mut s, &b), Err(Error::Limit));
        assert_eq!(s, old);
        apply(
            &mut s,
            Command {
                text: b"short".to_vec(),
                ..c(9)
            },
        );
        assert_eq!(s.encoded_size(), 97);
    }
    #[test]
    fn last_validation_detects_changed_record_and_accepts_finish_preservation() {
        let mut s = HealthState::default();
        apply(&mut s, c(6));
        let c = Command {
            key: b"a".to_vec(),
            health_record: h(),
            ..c(4)
        };
        apply(&mut s, c.clone());
        let mut changed = c;
        changed.health_record.latency = 123;
        assert_eq!(
            validate_last(&s, &encode_command(&changed).unwrap()),
            Err(Error::Invalid)
        );
    }
    #[test]
    fn hundred_thousand_map_narrow_updates_do_not_clone_other_records() {
        let mut s = HealthState::default();
        let map = (0..100000)
            .map(|i| (format!("{i:06}").into_bytes(), HealthRecord::default()))
            .collect();
        apply(
            &mut s,
            Command {
                health: Some(map),
                ..c(2)
            },
        );
        let keep = s.health().unwrap().get(b"000001".as_slice()).unwrap() as *const HealthRecord;
        for i in 0..1000 {
            let b = encode_command(&Command {
                key: b"000000".to_vec(),
                health_record: HealthRecord {
                    latency: i,
                    ..HealthRecord::default()
                },
                ..c(4)
            })
            .unwrap();
            apply_payload(&mut s, &b).unwrap();
            validate_last(&s, &b).unwrap();
            let r = query(&s, &encode_query(1, b"000000").unwrap()).unwrap();
            assert!(r.len() < 512);
            assert_eq!(
                s.health().unwrap().get(b"000001".as_slice()).unwrap() as *const HealthRecord,
                keep
            )
        }
        assert_eq!(encode(&s).unwrap().len(), s.encoded_size());
        let old = s.encoded_size();
        assert_eq!(
            apply_payload(
                &mut s,
                &encode_command(&Command {
                    key: b"extra".to_vec(),
                    ..c(4)
                })
                .unwrap()
            ),
            Err(Error::Limit)
        );
        assert_eq!(s.encoded_size(), old);
    }
    #[test]
    fn mixed_narrow_mutations_cached_budget_matches_full_encoding() {
        let mut s = HealthState::default();
        apply(&mut s, c(6));
        apply(&mut s, c(7));
        for i in 0..500 {
            let key = format!("k{}", i % 31).into_bytes();
            apply(
                &mut s,
                Command {
                    key: key.clone(),
                    health_record: HealthRecord {
                        name: vec![b'x'; i % 29],
                        ..h()
                    },
                    ..c(4)
                },
            );
            apply(
                &mut s,
                Command {
                    key,
                    region_record: RegionRecord {
                        detail: vec![b'd'; i % 23],
                        ..g()
                    },
                    ..c(5)
                },
            );
            apply(
                &mut s,
                Command {
                    text: vec![b'n'; i % 17],
                    ..c(10)
                },
            );
        }
        assert_eq!(decode(&encode(&s).unwrap()).unwrap(), s);
    }
}

#[cfg(test)]
mod budget_boundary_tests {
    use super::*;
    #[test]
    fn exact_state_budget_keeps_full_reply_encodable_and_one_more_byte_is_atomic() {
        let mut state = HealthState::default();
        let mut command = Command {
            action: 9,
            text: vec![b'x'; MAX_STATE_BYTES - 92],
            ..Command::default()
        };
        let payload = encode_command(&command).unwrap();
        assert!(payload.len() > MAX_STATE_BYTES && payload.len() <= MAX_HEALTH_BYTES);
        apply_payload(&mut state, &payload).unwrap();
        assert_eq!(state.encoded_size(), MAX_STATE_BYTES);
        let encoded = encode(&state).unwrap();
        assert_eq!(encoded.len(), MAX_STATE_BYTES);
        assert_eq!(decode(&encoded).unwrap(), state);
        let reply = query(&state, &encode_query(3, b"").unwrap()).unwrap();
        assert_eq!(reply.len(), MAX_STATE_BYTES + 122);
        assert!(reply.len() <= MAX_HEALTH_BYTES);
        assert_eq!(decode_reply(&reply).unwrap().state, state);
        command.text.push(b'y');
        let payload = encode_command(&command).unwrap();
        assert_eq!(apply_payload(&mut state, &payload), Err(Error::Limit));
        assert_eq!(state.encoded_size(), MAX_STATE_BYTES);
        assert_eq!(encode(&state).unwrap(), encoded);
    }
}
