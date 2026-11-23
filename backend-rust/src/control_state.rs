//! Ordinary control authority only. Chain/Proxy graphs remain Go-owned resources.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Invalid,
    Limit,
}
pub const MAX_CONTROL_BYTES: usize = 8 << 20;
pub const ALL_FIELDS: u64 = (1 << 18) - 1;
const MAX_ITEMS: usize = 100000;
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Value {
    pub import_policy: bool,
    pub game_bypass: Option<Vec<Vec<u8>>>,
    pub domestic_web: bool,
    pub webrtc_protection: bool,
    pub dns_protection: bool,
    pub subscription_url: Vec<u8>,
    pub mihomo_path: Vec<u8>,
    pub selected_node: Vec<u8>,
    pub selection_mode: Vec<u8>,
    pub locked_region: Vec<u8>,
    pub locked_node: Vec<u8>,
    pub connection_mode: Vec<u8>,
    pub strict_privacy: bool,
    pub wintun_path: Vec<u8>,
    pub auto_merge_hours: i64,
    pub socks_port: i64,
    pub runtime_locked_region: Vec<u8>,
    pub runtime_locked_node: Vec<u8>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Command {
    pub action: u8,
    pub mask: u64,
    pub value: Value,
}
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ControlState {
    value: Value,
    sizes: [usize; 18],
}
impl Default for ControlState {
    fn default() -> Self {
        Self {
            value: Value::default(),
            sizes: [1, 9, 1, 1, 1, 8, 8, 8, 8, 8, 8, 8, 1, 8, 8, 8, 8, 8],
        }
    }
}
impl ControlState {
    pub fn value(&self) -> &Value {
        &self.value
    }
    pub fn encoded_size(&self) -> usize {
        self.sizes.iter().sum()
    }
}
fn sizes(v: &Value) -> Result<[usize; 18], Error> {
    let mut game = 9usize;
    if let Some(items) = &v.game_bypass {
        if items.len() > MAX_ITEMS {
            return Err(Error::Limit);
        }
        for item in items {
            game = game
                .checked_add(8)
                .and_then(|n| n.checked_add(item.len()))
                .ok_or(Error::Limit)?;
        }
    }
    let s = [
        1,
        game,
        1,
        1,
        1,
        8 + v.subscription_url.len(),
        8 + v.mihomo_path.len(),
        8 + v.selected_node.len(),
        8 + v.selection_mode.len(),
        8 + v.locked_region.len(),
        8 + v.locked_node.len(),
        8 + v.connection_mode.len(),
        1,
        8 + v.wintun_path.len(),
        8,
        8,
        8 + v.runtime_locked_region.len(),
        8 + v.runtime_locked_node.len(),
    ];
    if s.iter()
        .try_fold(0usize, |n, s| n.checked_add(*s))
        .ok_or(Error::Limit)?
        > MAX_CONTROL_BYTES
    {
        return Err(Error::Limit);
    }
    Ok(s)
}
struct Writer(Vec<u8>);
impl Writer {
    fn n(&mut self, n: u64) {
        self.0.extend_from_slice(&n.to_le_bytes())
    }
    fn flag(&mut self, v: bool) {
        self.0.push(u8::from(v))
    }
    fn bytes(&mut self, v: &[u8]) {
        self.n(v.len() as u64);
        self.0.extend_from_slice(v)
    }
    fn value(&mut self, v: &Value) {
        self.value_mode(v, false)
    }
    fn value_mode(&mut self, v: &Value, omit_game: bool) {
        self.flag(v.import_policy);
        let game = if omit_game {
            None
        } else {
            v.game_bypass.as_ref()
        };
        self.flag(game.is_some());
        self.n(game.map_or(0, Vec::len) as u64);
        if let Some(items) = game {
            for item in items {
                self.bytes(item);
            }
        }
        self.flag(v.domestic_web);
        self.flag(v.webrtc_protection);
        self.flag(v.dns_protection);
        for b in [
            &v.subscription_url,
            &v.mihomo_path,
            &v.selected_node,
            &v.selection_mode,
            &v.locked_region,
            &v.locked_node,
            &v.connection_mode,
        ] {
            self.bytes(b)
        }
        self.flag(v.strict_privacy);
        self.bytes(&v.wintun_path);
        self.n(v.auto_merge_hours as u64);
        self.n(v.socks_port as u64);
        self.bytes(&v.runtime_locked_region);
        self.bytes(&v.runtime_locked_node);
    }
}
struct Reader<'a> {
    b: &'a [u8],
    p: usize,
}
impl Reader<'_> {
    fn take(&mut self, n: usize) -> Result<&[u8], Error> {
        let end = self.p.checked_add(n).ok_or(Error::Invalid)?;
        let b = self.b.get(self.p..end).ok_or(Error::Invalid)?;
        self.p = end;
        Ok(b)
    }
    fn byte(&mut self) -> Result<u8, Error> {
        Ok(self.take(1)?[0])
    }
    fn n(&mut self) -> Result<u64, Error> {
        Ok(u64::from_le_bytes(self.take(8)?.try_into().unwrap()))
    }
    fn flag(&mut self) -> Result<bool, Error> {
        match self.byte()? {
            0 => Ok(false),
            1 => Ok(true),
            _ => Err(Error::Invalid),
        }
    }
    fn bytes(&mut self) -> Result<Vec<u8>, Error> {
        let n = usize::try_from(self.n()?).map_err(|_| Error::Limit)?;
        if n > MAX_CONTROL_BYTES {
            return Err(Error::Limit);
        };
        Ok(self.take(n)?.to_vec())
    }
    fn value(&mut self) -> Result<Value, Error> {
        let import_policy = self.flag()?;
        let present = self.flag()?;
        let n = usize::try_from(self.n()?).map_err(|_| Error::Limit)?;
        if n > MAX_ITEMS || (!present && n != 0) {
            return Err(Error::Invalid);
        };
        let game_bypass = if present {
            let mut v = Vec::with_capacity(n.min(1024));
            for _ in 0..n {
                v.push(self.bytes()?)
            }
            Some(v)
        } else {
            None
        };
        Ok(Value {
            import_policy,
            game_bypass,
            domestic_web: self.flag()?,
            webrtc_protection: self.flag()?,
            dns_protection: self.flag()?,
            subscription_url: self.bytes()?,
            mihomo_path: self.bytes()?,
            selected_node: self.bytes()?,
            selection_mode: self.bytes()?,
            locked_region: self.bytes()?,
            locked_node: self.bytes()?,
            connection_mode: self.bytes()?,
            strict_privacy: self.flag()?,
            wintun_path: self.bytes()?,
            auto_merge_hours: self.n()? as i64,
            socks_port: self.n()? as i64,
            runtime_locked_region: self.bytes()?,
            runtime_locked_node: self.bytes()?,
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
pub fn decode(b: &[u8]) -> Result<ControlState, Error> {
    if b.is_empty() {
        return Ok(ControlState::default());
    };
    if b.len() > MAX_CONTROL_BYTES {
        return Err(Error::Limit);
    };
    let mut r = Reader { b, p: 0 };
    let value = r.value()?;
    r.done()?;
    let sizes = sizes(&value)?;
    Ok(ControlState { value, sizes })
}
pub fn encode(s: &ControlState) -> Result<Vec<u8>, Error> {
    if s.encoded_size() > MAX_CONTROL_BYTES {
        return Err(Error::Limit);
    };
    let mut w = Writer(Vec::with_capacity(s.encoded_size()));
    w.value(&s.value);
    Ok(w.0)
}
/// Read-only scalar projection; absent GameBypass here is not an authoritative reset.
pub fn meta(s: &ControlState) -> Result<Vec<u8>, Error> {
    let mut w = Writer(Vec::new());
    w.value_mode(&s.value, true);
    Ok(w.0)
}
fn validate_command(c: &Command) -> Result<(), Error> {
    if !matches!(c.action, 1 | 2)
        || c.mask & !ALL_FIELDS != 0
        || (c.action == 1 && c.mask != ALL_FIELDS)
    {
        return Err(Error::Invalid);
    };
    let s = sizes(&c.value)?;
    if s.iter().sum::<usize>() > MAX_CONTROL_BYTES - 9 {
        return Err(Error::Limit);
    };
    Ok(())
}
pub fn encode_command(c: &Command) -> Result<Vec<u8>, Error> {
    validate_command(c)?;
    let mut w = Writer(vec![c.action]);
    w.n(c.mask);
    w.value(&c.value);
    Ok(w.0)
}
pub fn decode_command(b: &[u8]) -> Result<Command, Error> {
    if b.len() > MAX_CONTROL_BYTES {
        return Err(Error::Limit);
    };
    let mut r = Reader { b, p: 0 };
    let c = Command {
        action: r.byte()?,
        mask: r.n()?,
        value: r.value()?,
    };
    r.done()?;
    validate_command(&c)?;
    Ok(c)
}
pub fn validate_payload(b: &[u8]) -> Result<(), Error> {
    decode_command(b).map(|_| ())
}
pub fn apply_payload(s: &mut ControlState, b: &[u8]) -> Result<(), Error> {
    let c = decode_command(b)?;
    let new_sizes = sizes(&c.value)?;
    let mut resulting = s.sizes;
    for (i, n) in new_sizes.iter().enumerate() {
        if c.mask & (1 << i) != 0 {
            resulting[i] = *n
        }
    }
    if resulting.iter().sum::<usize>() > MAX_CONTROL_BYTES {
        return Err(Error::Limit);
    };
    let v = c.value;
    macro_rules! put {
        ($i:expr,$f:ident) => {
            if c.mask & (1 << $i) != 0 {
                s.value.$f = v.$f;
            }
        };
    }
    put!(0, import_policy);
    put!(1, game_bypass);
    put!(2, domestic_web);
    put!(3, webrtc_protection);
    put!(4, dns_protection);
    put!(5, subscription_url);
    put!(6, mihomo_path);
    put!(7, selected_node);
    put!(8, selection_mode);
    put!(9, locked_region);
    put!(10, locked_node);
    put!(11, connection_mode);
    put!(12, strict_privacy);
    put!(13, wintun_path);
    put!(14, auto_merge_hours);
    put!(15, socks_port);
    put!(16, runtime_locked_region);
    put!(17, runtime_locked_node);
    s.sizes = resulting;
    Ok(())
}
pub fn validate_last(s: &ControlState, b: &[u8]) -> Result<(), Error> {
    let c = decode_command(b)?;
    macro_rules! check {
        ($i:expr,$f:ident) => {
            if c.mask & (1 << $i) != 0 && s.value.$f != c.value.$f {
                return Err(Error::Invalid);
            }
        };
    }
    check!(0, import_policy);
    check!(1, game_bypass);
    check!(2, domestic_web);
    check!(3, webrtc_protection);
    check!(4, dns_protection);
    check!(5, subscription_url);
    check!(6, mihomo_path);
    check!(7, selected_node);
    check!(8, selection_mode);
    check!(9, locked_region);
    check!(10, locked_node);
    check!(11, connection_mode);
    check!(12, strict_privacy);
    check!(13, wintun_path);
    check!(14, auto_merge_hours);
    check!(15, socks_port);
    check!(16, runtime_locked_region);
    check!(17, runtime_locked_node);
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    fn apply(s: &mut ControlState, c: Command) {
        apply_payload(s, &encode_command(&c).unwrap()).unwrap()
    }
    fn populated() -> Value {
        Value {
            import_policy: true,
            game_bypass: Some(vec![vec![], vec![255], b"game".to_vec()]),
            domestic_web: true,
            webrtc_protection: true,
            dns_protection: true,
            subscription_url: b"opaque-url".to_vec(),
            mihomo_path: vec![254],
            selected_node: b"manual".to_vec(),
            selection_mode: b"legacy-mode".to_vec(),
            locked_region: b"JP".to_vec(),
            locked_node: b"persisted".to_vec(),
            connection_mode: b"tun".to_vec(),
            strict_privacy: true,
            wintun_path: b"path".to_vec(),
            auto_merge_hours: i64::MIN,
            socks_port: i64::MAX,
            runtime_locked_region: b"US".to_vec(),
            runtime_locked_node: b"runtime".to_vec(),
        }
    }
    #[test]
    fn default_and_full_restore_exact_raw_values() {
        let mut s = ControlState::default();
        assert_eq!(decode(&[]).unwrap(), s);
        assert_eq!(encode(&s).unwrap().len(), s.encoded_size());
        let v = populated();
        apply(
            &mut s,
            Command {
                action: 1,
                mask: ALL_FIELDS,
                value: v.clone(),
            },
        );
        assert_eq!(s.value(), &v);
        assert_eq!(decode(&encode(&s).unwrap()).unwrap(), s)
    }
    #[test]
    fn every_field_mask_independently_changes_only_one_field() {
        let v = populated();
        let mut s = ControlState::default();
        for i in 0..18 {
            let c = Command {
                action: 2,
                mask: 1 << i,
                value: v.clone(),
            };
            apply(&mut s, c.clone());
            validate_last(&s, &encode_command(&c).unwrap()).unwrap();
            assert_eq!(encode(&s).unwrap().len(), s.encoded_size());
        }
        assert_eq!(s.value(), &v)
    }
    #[test]
    fn stale_patch_does_not_revert_unrelated_fields_or_runtime_pins() {
        let mut s = ControlState::default();
        let stale = populated();
        apply(
            &mut s,
            Command {
                action: 2,
                mask: (1 << 5) | (1 << 16) | (1 << 17),
                value: Value {
                    subscription_url: b"new-url".to_vec(),
                    runtime_locked_region: b"FR".to_vec(),
                    runtime_locked_node: b"new-node".to_vec(),
                    ..Value::default()
                },
            },
        );
        apply(
            &mut s,
            Command {
                action: 2,
                mask: 1 << 15,
                value: stale,
            },
        );
        assert_eq!(s.value().subscription_url, b"new-url");
        assert_eq!(s.value().runtime_locked_region, b"FR");
        assert_eq!(s.value().runtime_locked_node, b"new-node");
        assert_eq!(s.value().socks_port, i64::MAX)
    }
    #[test]
    fn nil_empty_and_false_are_explicit_values_not_absent() {
        let mut s = ControlState::default();
        apply(
            &mut s,
            Command {
                action: 1,
                mask: ALL_FIELDS,
                value: populated(),
            },
        );
        apply(
            &mut s,
            Command {
                action: 2,
                mask: 3,
                value: Value {
                    game_bypass: Some(vec![]),
                    ..Value::default()
                },
            },
        );
        assert!(!s.value().import_policy);
        assert_eq!(s.value().game_bypass, Some(vec![]));
        apply(
            &mut s,
            Command {
                action: 2,
                mask: 2,
                ..Command::default()
            },
        );
        assert_eq!(s.value().game_bypass, None)
    }
    #[test]
    fn zero_mask_is_valid_noop_but_restore_requires_all() {
        let mut s = ControlState::default();
        let before = s.clone();
        apply(
            &mut s,
            Command {
                action: 2,
                mask: 0,
                value: populated(),
            },
        );
        assert_eq!(s, before);
        for c in [
            Command::default(),
            Command {
                action: 1,
                mask: 1,
                ..Command::default()
            },
            Command {
                action: 2,
                mask: 1 << 18,
                ..Command::default()
            },
            Command {
                action: 3,
                ..Command::default()
            },
        ] {
            assert!(encode_command(&c).is_err())
        }
    }
    #[test]
    fn aggregate_patch_limit_is_atomic() {
        let mut s = ControlState::default();
        apply(
            &mut s,
            Command {
                action: 2,
                mask: 1 << 5,
                value: Value {
                    subscription_url: vec![1; 5 << 20],
                    ..Value::default()
                },
            },
        );
        let before = s.clone();
        let c = Command {
            action: 2,
            mask: 1 << 6,
            value: Value {
                mihomo_path: vec![2; 4 << 20],
                ..Value::default()
            },
        };
        assert!(apply_payload(&mut s, &encode_command(&c).unwrap()).is_err());
        assert_eq!(s, before);
        assert!(encode(&s).is_ok())
    }
    #[test]
    fn list_limit_and_wire_boolean_presence_trailing_rejected() {
        let c = Command {
            action: 2,
            mask: 2,
            value: Value {
                game_bypass: Some(vec![vec![]; MAX_ITEMS + 1]),
                ..Value::default()
            },
        };
        assert!(encode_command(&c).is_err());
        let empty = encode(&ControlState::default()).unwrap();
        for i in 0..empty.len() {
            assert!(decode(&empty[..i]).is_err() || i == 0)
        }
        let mut malformed = empty.clone();
        malformed[0] = 2;
        assert!(decode(&malformed).is_err());
        let mut malformed = empty.clone();
        malformed[2] = 1;
        assert!(decode(&malformed).is_err());
        let mut trailing = empty;
        trailing.push(0);
        assert!(decode(&trailing).is_err())
    }
    #[test]
    fn hundred_thousand_entries_survive_scalar_patch_without_copy() {
        let mut s = ControlState::default();
        apply(
            &mut s,
            Command {
                action: 2,
                mask: 2,
                value: Value {
                    game_bypass: Some(vec![b"x".to_vec(); MAX_ITEMS]),
                    ..Value::default()
                },
            },
        );
        let p = s.value().game_bypass.as_ref().unwrap().as_ptr();
        for i in 0..1000 {
            apply(
                &mut s,
                Command {
                    action: 2,
                    mask: 1 << 14,
                    value: Value {
                        auto_merge_hours: i,
                        ..Value::default()
                    },
                },
            );
            assert_eq!(s.value().game_bypass.as_ref().unwrap().as_ptr(), p)
        }
        assert_eq!(s.value().auto_merge_hours, 999)
    }
    #[test]
    fn checkpoint_validates_only_changed_bits_and_exact_nilshape() {
        let mut s = ControlState::default();
        let c = Command {
            action: 2,
            mask: 2,
            value: Value {
                game_bypass: Some(vec![]),
                ..Value::default()
            },
        };
        let payload = encode_command(&c).unwrap();
        apply_payload(&mut s, &payload).unwrap();
        validate_last(&s, &payload).unwrap();
        let wrong = encode_command(&Command {
            value: Value::default(),
            ..c
        })
        .unwrap();
        assert!(validate_last(&s, &wrong).is_err());
        let unchanged = encode_command(&Command {
            action: 2,
            mask: 0,
            value: populated(),
        })
        .unwrap();
        validate_last(&s, &unchanged).unwrap()
    }
    #[test]
    fn partial_decode_restore_keeps_arbitrary_legacy_values() {
        let mut decoded = populated();
        decoded.game_bypass = None;
        decoded.selected_node.clear();
        decoded.socks_port = -1;
        let mut s = ControlState::default();
        apply(
            &mut s,
            Command {
                action: 1,
                mask: ALL_FIELDS,
                value: decoded.clone(),
            },
        );
        assert_eq!(s.value(), &decoded)
    }
    #[test]
    fn exact_aggregate_limit_and_one_extra_byte_are_distinct() {
        let mut s = ControlState::default();
        let base = s.encoded_size();
        apply(
            &mut s,
            Command {
                action: 2,
                mask: 1 << 5,
                value: Value {
                    subscription_url: vec![1; 4 << 20],
                    ..Value::default()
                },
            },
        );
        let remaining = MAX_CONTROL_BYTES - base - (4 << 20);
        apply(
            &mut s,
            Command {
                action: 2,
                mask: 1 << 6,
                value: Value {
                    mihomo_path: vec![2; remaining],
                    ..Value::default()
                },
            },
        );
        assert_eq!(s.encoded_size(), MAX_CONTROL_BYTES);
        assert_eq!(encode(&s).unwrap().len(), MAX_CONTROL_BYTES);
        let payload = encode_command(&Command {
            action: 2,
            mask: 1 << 6,
            value: Value {
                mihomo_path: vec![2; remaining + 1],
                ..Value::default()
            },
        })
        .unwrap();
        assert_eq!(apply_payload(&mut s, &payload), Err(Error::Limit));
        assert_eq!(s.encoded_size(), MAX_CONTROL_BYTES);
        assert_eq!(s.value().mihomo_path.len(), remaining);
        // A full restore needs nine bytes beyond the state: preserve that explicit input limit.
        assert_eq!(
            encode_command(&Command {
                action: 1,
                mask: ALL_FIELDS,
                value: s.value().clone()
            }),
            Err(Error::Limit)
        );
    }
}
