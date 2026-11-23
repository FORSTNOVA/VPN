//! China Direct authority. Raw byte strings preserve Go values without resource ownership.
pub const MAX_CHINA_BYTES: usize = 1 << 20;
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Status {
    pub available: bool,
    pub entries: i64,
    pub updated_at: Vec<u8>,
    pub source: Vec<u8>,
    pub error: Vec<u8>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ChinaState {
    pub status: Status,
    pub fetching: bool,
    pub probed: bool,
    pub rule_ok: bool,
    pub active: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Command {
    pub action: u8,
    pub status: Status,
    pub flag: bool,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Invalid,
    Limit,
}
struct R<'a> {
    b: &'a [u8],
    p: usize,
}
impl R<'_> {
    fn take(&mut self, n: usize) -> Result<&[u8], Error> {
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
        Ok(u64::from_le_bytes(self.take(8)?.try_into().unwrap()))
    }
    fn bytes(&mut self) -> Result<Vec<u8>, Error> {
        let n = usize::try_from(self.n()?).map_err(|_| Error::Limit)?;
        Ok(self.take(n)?.to_vec())
    }
    fn status(&mut self) -> Result<Status, Error> {
        Ok(Status {
            available: self.flag()?,
            entries: self.n()? as i64,
            updated_at: self.bytes()?,
            source: self.bytes()?,
            error: self.bytes()?,
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
fn status_bytes(s: &Status, b: &mut Vec<u8>) -> Result<(), Error> {
    let size = 33usize
        .checked_add(s.updated_at.len())
        .and_then(|n| n.checked_add(s.source.len()))
        .and_then(|n| n.checked_add(s.error.len()))
        .ok_or(Error::Limit)?;
    if size + b.len() > MAX_CHINA_BYTES {
        return Err(Error::Limit);
    }
    b.push(u8::from(s.available));
    b.extend_from_slice(&s.entries.to_le_bytes());
    for v in [&s.updated_at, &s.source, &s.error] {
        b.extend_from_slice(&(v.len() as u64).to_le_bytes());
        b.extend_from_slice(v)
    }
    Ok(())
}
pub fn encode(s: &ChinaState) -> Result<Vec<u8>, Error> {
    let mut b = Vec::new();
    status_bytes(&s.status, &mut b)?;
    b.extend([
        s.fetching as u8,
        s.probed as u8,
        s.rule_ok as u8,
        s.active as u8,
    ]);
    if b.len() > MAX_CHINA_BYTES {
        return Err(Error::Limit);
    }
    Ok(b)
}
pub fn decode(b: &[u8]) -> Result<ChinaState, Error> {
    if b.is_empty() {
        return Ok(ChinaState::default());
    }
    if b.len() > MAX_CHINA_BYTES {
        return Err(Error::Limit);
    }
    let mut r = R { b, p: 0 };
    let s = ChinaState {
        status: r.status()?,
        fetching: r.flag()?,
        probed: r.flag()?,
        rule_ok: r.flag()?,
        active: r.flag()?,
    };
    r.done()?;
    Ok(s)
}
pub fn encode_command(c: &Command) -> Result<Vec<u8>, Error> {
    let mut b = vec![c.action];
    status_bytes(&c.status, &mut b)?;
    b.push(c.flag as u8);
    if b.len() > MAX_CHINA_BYTES {
        return Err(Error::Limit);
    }
    Ok(b)
}
pub fn decode_command(b: &[u8]) -> Result<Command, Error> {
    if b.len() > MAX_CHINA_BYTES {
        return Err(Error::Limit);
    }
    let mut r = R { b, p: 0 };
    let c = Command {
        action: r.byte()?,
        status: r.status()?,
        flag: r.flag()?,
    };
    r.done()?;
    if !(1..=5).contains(&c.action) {
        return Err(Error::Invalid);
    }
    Ok(c)
}
pub fn validate_payload(b: &[u8]) -> Result<(), Error> {
    decode_command(b).map(|_| ())
}
pub fn apply_payload(s: &mut ChinaState, b: &[u8]) -> Result<(), Error> {
    let c = decode_command(b)?;
    if c.action == 1 {
        let mut proposed = s.clone();
        proposed.status = c.status;
        encode(&proposed)?;
        *s = proposed;
    } else {
        match c.action {
            2 => s.fetching = c.flag,
            3 => s.probed = c.flag,
            4 => s.rule_ok = c.flag,
            5 => s.active = c.flag,
            _ => unreachable!(),
        }
    }
    Ok(())
}
pub fn validate_last(s: &ChinaState, b: &[u8]) -> Result<(), Error> {
    let c = decode_command(b)?;
    let valid = match c.action {
        1 => s.status == c.status,
        2 => s.fetching == c.flag,
        3 => s.probed == c.flag,
        4 => s.rule_ok == c.flag,
        5 => s.active == c.flag,
        _ => false,
    };
    if valid {
        Ok(())
    } else {
        Err(Error::Invalid)
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn all_actions_preserve_unrelated_fields() {
        let mut s = ChinaState::default();
        for action in 2..=5 {
            let b = encode_command(&Command {
                action,
                flag: true,
                ..Default::default()
            })
            .unwrap();
            apply_payload(&mut s, &b).unwrap();
            validate_last(&s, &b).unwrap();
        }
        let status = Status {
            available: true,
            entries: i64::MIN,
            updated_at: vec![255],
            source: b"source".to_vec(),
            error: b"partial".to_vec(),
        };
        let b = encode_command(&Command {
            action: 1,
            status: status.clone(),
            flag: false,
        })
        .unwrap();
        apply_payload(&mut s, &b).unwrap();
        assert_eq!(s.status, status);
        assert!(s.fetching && s.probed && s.rule_ok && s.active);
        assert_eq!(decode(&encode(&s).unwrap()).unwrap(), s);
    }
    #[test]
    fn invalid_and_aggregate_limit_are_atomic() {
        let mut s = ChinaState::default();
        let before = s.clone();
        for b in [vec![], vec![8], vec![1, 2]] {
            assert!(apply_payload(&mut s, &b).is_err());
            assert_eq!(s, before);
        }
        let c = Command {
            action: 1,
            status: Status {
                error: vec![0; MAX_CHINA_BYTES - 35],
                ..Default::default()
            },
            ..Default::default()
        };
        let b = encode_command(&c).unwrap();
        assert!(apply_payload(&mut s, &b).is_err());
        assert_eq!(s, before);
    }
    #[test]
    fn checkpoint_binding_ignores_unused_fields_but_checks_changed_field() {
        let s = ChinaState::default();
        for action in 1..=5 {
            let c = Command {
                action,
                ..Default::default()
            };
            validate_last(&s, &encode_command(&c).unwrap()).unwrap();
            let bad = if action == 1 {
                Command {
                    status: Status {
                        entries: 1,
                        ..Default::default()
                    },
                    ..c
                }
            } else {
                Command { flag: true, ..c }
            };
            assert!(validate_last(&s, &encode_command(&bad).unwrap()).is_err());
        }
    }
}
