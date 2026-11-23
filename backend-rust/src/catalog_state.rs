//! Ordered catalog authority. InfoHandle refers to Go-owned pointer resources, never quota values.
use std::collections::{BTreeMap, BTreeSet};
pub const MAX_CATALOG_BYTES: usize = 16 << 20;
pub const MAX_STATE_BYTES: usize = MAX_CATALOG_BYTES - 1024;
pub const ALL_FIELDS: u64 = (1 << 11) - 1;
const MAX_ITEMS: usize = 100000;
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Invalid,
    Limit,
    Conflict,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Entry {
    pub id: Vec<u8>,
    pub url: Vec<u8>,
    pub label: Vec<u8>,
    pub prefix: Vec<u8>,
    pub enabled: bool,
    pub updated_at: i64,
    pub node_count: i64,
    pub last_error: Vec<u8>,
    pub info_handle: u64,
    pub quota_updated_at: i64,
    pub quota_error: Vec<u8>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Command {
    pub action: u8,
    pub index: i64,
    pub mask: u64,
    pub entry: Entry,
    pub entries: Option<Vec<Entry>>,
    pub id: Vec<u8>,
    pub url: Vec<u8>,
    pub flag: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Query {
    pub kind: u8,
    pub index: i64,
    pub id: Vec<u8>,
    pub url: Vec<u8>,
    pub handle: u64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Reply {
    pub found: bool,
    pub count: i64,
    pub nil_entries: bool,
    pub from_cache: bool,
    pub has_backup: bool,
    pub entry: Entry,
    pub entries: Option<Vec<Entry>>,
    pub references: u64,
}
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CatalogState {
    entries: Option<Vec<Entry>>,
    from_cache: bool,
    has_backup: bool,
    backup: Option<Vec<Entry>>,
    size: usize,
    entries_size: usize,
    backup_size: usize,
    ids: BTreeMap<Vec<u8>, BTreeSet<usize>>,
    urls: BTreeMap<Vec<u8>, BTreeSet<usize>>,
    refs: BTreeMap<u64, u64>,
}
impl Default for CatalogState {
    fn default() -> Self {
        Self {
            entries: None,
            from_cache: false,
            has_backup: false,
            backup: None,
            size: 20,
            entries_size: 9,
            backup_size: 9,
            ids: BTreeMap::new(),
            urls: BTreeMap::new(),
            refs: BTreeMap::new(),
        }
    }
}
impl CatalogState {
    pub fn entries(&self) -> &Option<Vec<Entry>> {
        &self.entries
    }
    pub fn from_cache(&self) -> bool {
        self.from_cache
    }
    pub fn has_backup(&self) -> bool {
        self.has_backup
    }
    pub fn backup(&self) -> &Option<Vec<Entry>> {
        &self.backup
    }
    pub fn encoded_size(&self) -> usize {
        self.size
    }
    pub fn references(&self, h: u64) -> u64 {
        self.refs.get(&h).copied().unwrap_or(0)
    }
}
fn entry_size(e: &Entry) -> usize {
    81 + e.id.len()
        + e.url.len()
        + e.label.len()
        + e.prefix.len()
        + e.last_error.len()
        + e.quota_error.len()
}
fn list_size(v: &Option<Vec<Entry>>) -> Result<usize, Error> {
    let mut n = 9usize;
    if let Some(v) = v {
        if v.len() > MAX_ITEMS {
            return Err(Error::Limit);
        };
        for e in v {
            n = n.checked_add(entry_size(e)).ok_or(Error::Limit)?;
            if n > MAX_CATALOG_BYTES {
                return Err(Error::Limit);
            }
        }
    }
    Ok(n)
}
fn ref_add(r: &mut BTreeMap<u64, u64>, h: u64) {
    if h != 0 {
        *r.entry(h).or_default() += 1
    }
}
fn ref_remove(r: &mut BTreeMap<u64, u64>, h: u64) {
    if h != 0 {
        let n = r.get_mut(&h).unwrap();
        *n -= 1;
        if *n == 0 {
            r.remove(&h);
        }
    }
}
fn index_add(m: &mut BTreeMap<Vec<u8>, BTreeSet<usize>>, k: &[u8], i: usize) {
    m.entry(k.to_vec()).or_default().insert(i);
}
fn index_remove(m: &mut BTreeMap<Vec<u8>, BTreeSet<usize>>, k: &[u8], i: usize) {
    let set = m.get_mut(k).unwrap();
    set.remove(&i);
    if set.is_empty() {
        m.remove(k);
    }
}
impl CatalogState {
    fn rebuild(&mut self) {
        self.ids.clear();
        self.urls.clear();
        self.refs.clear();
        if let Some(v) = &self.entries {
            for (i, e) in v.iter().enumerate() {
                index_add(&mut self.ids, &e.id, i);
                index_add(&mut self.urls, &e.url, i);
                ref_add(&mut self.refs, e.info_handle)
            }
        }
        if let Some(v) = &self.backup {
            for e in v {
                ref_add(&mut self.refs, e.info_handle)
            }
        }
    }
    fn first_id(&self, id: &[u8]) -> Option<usize> {
        self.ids.get(id).and_then(|v| v.first().copied())
    }
    fn budget(&self, old: usize, new: usize) -> Result<usize, Error> {
        let n = self
            .size
            .checked_sub(old)
            .and_then(|n| n.checked_add(new))
            .ok_or(Error::Limit)?;
        if n > MAX_STATE_BYTES {
            Err(Error::Limit)
        } else {
            Ok(n)
        }
    }
}
struct W {
    b: Vec<u8>,
    bad: bool,
}
impl W {
    fn new() -> Self {
        Self {
            b: vec![],
            bad: false,
        }
    }
    fn put(&mut self, b: &[u8]) {
        if self.bad {
            return;
        };
        if self.b.len().saturating_add(b.len()) > MAX_CATALOG_BYTES {
            self.bad = true;
            return;
        }
        self.b.extend_from_slice(b)
    }
    fn n(&mut self, n: u64) {
        self.put(&n.to_le_bytes())
    }
    fn flag(&mut self, b: bool) {
        self.put(&[u8::from(b)])
    }
    fn bytes(&mut self, b: &[u8]) {
        self.n(b.len() as u64);
        self.put(b)
    }
    fn entry(&mut self, e: &Entry) {
        self.bytes(&e.id);
        self.bytes(&e.url);
        self.bytes(&e.label);
        self.bytes(&e.prefix);
        self.flag(e.enabled);
        self.n(e.updated_at as u64);
        self.n(e.node_count as u64);
        self.bytes(&e.last_error);
        self.n(e.info_handle);
        self.n(e.quota_updated_at as u64);
        self.bytes(&e.quota_error)
    }
    fn list(&mut self, v: &Option<Vec<Entry>>) {
        self.flag(v.is_some());
        self.n(v.as_ref().map_or(0, Vec::len) as u64);
        if let Some(v) = v {
            if v.len() > MAX_ITEMS {
                self.bad = true;
                return;
            }
            for e in v {
                self.entry(e)
            }
        }
    }
    fn done(self) -> Result<Vec<u8>, Error> {
        if self.bad {
            Err(Error::Limit)
        } else {
            Ok(self.b)
        }
    }
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
        if n > MAX_CATALOG_BYTES {
            return Err(Error::Limit);
        }
        Ok(self.take(n)?.to_vec())
    }
    fn entry(&mut self) -> Result<Entry, Error> {
        Ok(Entry {
            id: self.bytes()?,
            url: self.bytes()?,
            label: self.bytes()?,
            prefix: self.bytes()?,
            enabled: self.flag()?,
            updated_at: self.n()? as i64,
            node_count: self.n()? as i64,
            last_error: self.bytes()?,
            info_handle: self.n()?,
            quota_updated_at: self.n()? as i64,
            quota_error: self.bytes()?,
        })
    }
    fn list(&mut self) -> Result<Option<Vec<Entry>>, Error> {
        let p = self.flag()?;
        let n = usize::try_from(self.n()?).map_err(|_| Error::Limit)?;
        if n > MAX_ITEMS || (!p && n != 0) {
            return Err(Error::Invalid);
        }
        if !p {
            return Ok(None);
        }
        let mut v = Vec::with_capacity(n.min(1024));
        for _ in 0..n {
            v.push(self.entry()?)
        }
        Ok(Some(v))
    }
    fn done(&self) -> Result<(), Error> {
        if self.p == self.b.len() {
            Ok(())
        } else {
            Err(Error::Invalid)
        }
    }
}
pub fn decode(b: &[u8]) -> Result<CatalogState, Error> {
    if b.is_empty() {
        return Ok(CatalogState::default());
    }
    if b.len() > MAX_STATE_BYTES {
        return Err(Error::Limit);
    }
    let mut r = R { b, p: 0 };
    let entries = r.list()?;
    let from_cache = r.flag()?;
    let has_backup = r.flag()?;
    let backup = r.list()?;
    r.done()?;
    if !has_backup && backup.is_some() {
        return Err(Error::Invalid);
    }
    let entries_size = list_size(&entries)?;
    let backup_size = list_size(&backup)?;
    let mut s = CatalogState {
        entries,
        from_cache,
        has_backup,
        backup,
        size: 2 + entries_size + backup_size,
        entries_size,
        backup_size,
        ..CatalogState::default()
    };
    if s.size > MAX_STATE_BYTES {
        return Err(Error::Limit);
    }
    s.rebuild();
    Ok(s)
}
pub fn encode(s: &CatalogState) -> Result<Vec<u8>, Error> {
    let mut w = W::new();
    w.list(&s.entries);
    w.flag(s.from_cache);
    w.flag(s.has_backup);
    w.list(&s.backup);
    w.done()
}
fn valid(c: &Command) -> Result<(), Error> {
    if !(1..=10).contains(&c.action)
        || (matches!(c.action, 4 | 10) && c.mask & !ALL_FIELDS != 0)
        || (c.action == 10 && c.mask & !((1 << 8) | (1 << 9) | (1 << 10)) != 0)
    {
        return Err(Error::Invalid);
    }
    Ok(())
}
pub fn encode_command(c: &Command) -> Result<Vec<u8>, Error> {
    valid(c)?;
    let mut w = W::new();
    w.put(&[c.action]);
    w.n(c.index as u64);
    w.n(c.mask);
    w.entry(&c.entry);
    w.list(&c.entries);
    w.bytes(&c.id);
    w.bytes(&c.url);
    w.flag(c.flag);
    w.done()
}
pub fn decode_command(b: &[u8]) -> Result<Command, Error> {
    if b.len() > MAX_CATALOG_BYTES {
        return Err(Error::Limit);
    }
    let mut r = R { b, p: 0 };
    let c = Command {
        action: r.byte()?,
        index: r.n()? as i64,
        mask: r.n()?,
        entry: r.entry()?,
        entries: r.list()?,
        id: r.bytes()?,
        url: r.bytes()?,
        flag: r.flag()?,
    };
    r.done()?;
    valid(&c)?;
    Ok(c)
}
pub fn validate_payload(b: &[u8]) -> Result<(), Error> {
    decode_command(b).map(|_| ())
}
fn patch(e: &mut Entry, c: &Command) {
    macro_rules! set {
        ($i:expr,$f:ident) => {
            if c.mask & (1 << $i) != 0 {
                e.$f = c.entry.$f.clone();
            }
        };
    }
    set!(0, id);
    set!(1, url);
    set!(2, label);
    set!(3, prefix);
    set!(4, enabled);
    set!(5, updated_at);
    set!(6, node_count);
    set!(7, last_error);
    set!(8, info_handle);
    set!(9, quota_updated_at);
    set!(10, quota_error);
}
fn matches(e: &Entry, c: &Command) -> bool {
    let mut p = e.clone();
    patch(&mut p, c);
    p == *e
}
pub fn apply_payload(s: &mut CatalogState, b: &[u8]) -> Result<(), Error> {
    let c = decode_command(b)?;
    match c.action {
        1 => {
            if s.has_backup {
                return Err(Error::Conflict);
            }
            s.size = s.size - s.entries_size + 9;
            s.entries_size = 9;
            s.entries = None;
            s.rebuild()
        }
        2 => {
            if s.entries.as_ref().map_or(0, Vec::len) >= MAX_ITEMS {
                return Err(Error::Limit);
            }
            let n = entry_size(&c.entry);
            let size = s.budget(0, n)?;
            let v = s.entries.get_or_insert_with(Vec::new);
            let i = v.len();
            index_add(&mut s.ids, &c.entry.id, i);
            index_add(&mut s.urls, &c.entry.url, i);
            ref_add(&mut s.refs, c.entry.info_handle);
            v.push(c.entry);
            s.entries_size += n;
            s.size = size
        }
        3 => {
            let v = s.entries.as_mut().ok_or(Error::Conflict)?;
            let e = v.pop().ok_or(Error::Conflict)?;
            let i = v.len();
            index_remove(&mut s.ids, &e.id, i);
            index_remove(&mut s.urls, &e.url, i);
            ref_remove(&mut s.refs, e.info_handle);
            let n = entry_size(&e);
            s.entries_size -= n;
            s.size -= n
        }
        4 | 10 => {
            let i = usize::try_from(c.index).map_err(|_| Error::Invalid)?;
            let old = s
                .entries
                .as_ref()
                .and_then(|v| v.get(i))
                .ok_or(Error::Conflict)?;
            if c.action == 10 && (s.first_id(&c.id) != Some(i) || old.url != c.url) {
                return Err(Error::Conflict);
            }
            let mut next = old.clone();
            patch(&mut next, &c);
            let old_size = entry_size(old);
            let new_size = entry_size(&next);
            let size = s.budget(old_size, new_size)?;
            index_remove(&mut s.ids, &old.id, i);
            index_remove(&mut s.urls, &old.url, i);
            ref_remove(&mut s.refs, old.info_handle);
            index_add(&mut s.ids, &next.id, i);
            index_add(&mut s.urls, &next.url, i);
            ref_add(&mut s.refs, next.info_handle);
            s.entries.as_mut().unwrap()[i] = next;
            s.entries_size = s.entries_size - old_size + new_size;
            s.size = size
        }
        5 => {
            if s.has_backup {
                return Err(Error::Conflict);
            }
            let backup = s.entries.as_ref().filter(|v| !v.is_empty()).cloned();
            let n = list_size(&backup)?;
            let size = s.budget(s.backup_size, n)?;
            if let Some(v) = &backup {
                for e in v {
                    ref_add(&mut s.refs, e.info_handle)
                }
            }
            s.backup = backup;
            s.backup_size = n;
            s.size = size;
            s.has_backup = true
        }
        6 => {
            let mut kept = Vec::new();
            let mut n = 9;
            if let Some(v) = &s.entries {
                for e in v {
                    if e.id != c.id {
                        n += entry_size(e);
                        kept.push(e.clone());
                    }
                }
            }
            s.size = s.size - s.entries_size + n;
            s.entries_size = n;
            s.entries = Some(kept);
            s.rebuild()
        }
        7 => {
            if !s.has_backup {
                return Err(Error::Conflict);
            }
            let size = s.budget(s.entries_size, s.backup_size)?;
            s.entries = s.backup.as_ref().filter(|v| !v.is_empty()).cloned();
            s.entries_size = s.backup_size;
            s.size = size;
            s.rebuild()
        }
        8 => {
            s.size = s.size - s.backup_size + 9;
            s.backup_size = 9;
            s.backup = None;
            s.has_backup = false;
            s.rebuild()
        }
        9 => s.from_cache = c.flag,
        _ => unreachable!(),
    };
    Ok(())
}
pub fn validate_last(s: &CatalogState, b: &[u8]) -> Result<(), Error> {
    let c = decode_command(b)?;
    let yes = match c.action {
        1 => s.entries.is_none(),
        2 => s.entries.as_ref().and_then(|v| v.last()) == Some(&c.entry),
        // A checkpoint has no previous catalog length: only the non-nil postcondition is provable.
        3 => s.entries.is_some(),
        4 | 10 => usize::try_from(c.index)
            .ok()
            .and_then(|i| s.entries.as_ref().and_then(|v| v.get(i)))
            .is_some_and(|e| {
                matches(e, &c)
                    && (c.action != 10
                        || (s.first_id(&c.id) == usize::try_from(c.index).ok() && e.url == c.url))
            }),
        5 => s.has_backup && s.backup == s.entries.as_ref().filter(|v| !v.is_empty()).cloned(),
        6 => s
            .entries
            .as_ref()
            .is_some_and(|v| v.iter().all(|e| e.id != c.id)),
        7 => s.has_backup && s.entries == s.backup.as_ref().filter(|v| !v.is_empty()).cloned(),
        8 => !s.has_backup && s.backup.is_none(),
        9 => s.from_cache == c.flag,
        _ => false,
    };
    if yes {
        Ok(())
    } else {
        Err(Error::Invalid)
    }
}
pub fn encode_query(q: &Query) -> Result<Vec<u8>, Error> {
    let mut w = W::new();
    w.put(&[q.kind]);
    w.n(q.index as u64);
    w.bytes(&q.id);
    w.bytes(&q.url);
    w.n(q.handle);
    w.done()
}
pub fn query(s: &CatalogState, b: &[u8]) -> Result<Vec<u8>, Error> {
    if b.len() > MAX_CATALOG_BYTES {
        return Err(Error::Limit);
    }
    let mut r = R { b, p: 0 };
    let q = Query {
        kind: r.byte()?,
        index: r.n()? as i64,
        id: r.bytes()?,
        url: r.bytes()?,
        handle: r.n()?,
    };
    r.done()?;
    let mut out = Reply {
        count: s.entries.as_ref().map_or(0, Vec::len) as i64,
        nil_entries: s.entries.is_none(),
        from_cache: s.from_cache,
        has_backup: s.has_backup,
        ..Reply::default()
    };
    match q.kind {
        0 => {
            out.count = s.entries.as_ref().map_or(0, Vec::len) as i64;
            out.nil_entries = s.entries.is_none();
            out.from_cache = s.from_cache;
            out.has_backup = s.has_backup
        }
        1 | 3 | 4 => {
            let i = match q.kind {
                1 => usize::try_from(q.index).ok(),
                3 => s.first_id(&q.id),
                _ => {
                    let a = s.urls.get(&q.url).and_then(|v| v.first().copied());
                    let b = s.first_id(&q.id);
                    a.into_iter().chain(b).min()
                }
            };
            if let Some(e) = i.and_then(|i| s.entries.as_ref().and_then(|v| v.get(i))) {
                out.count = i.unwrap() as i64;
                out.found = true;
                out.entry = e.clone()
            }
        }
        2 => out.entries = s.entries.clone(),
        5 => out.references = s.references(q.handle),
        _ => return Err(Error::Invalid),
    }
    encode_reply(&out)
}
pub fn encode_reply(r: &Reply) -> Result<Vec<u8>, Error> {
    let mut w = W::new();
    w.flag(r.found);
    w.n(r.count as u64);
    w.flag(r.nil_entries);
    w.flag(r.from_cache);
    w.flag(r.has_backup);
    w.entry(&r.entry);
    w.list(&r.entries);
    w.n(r.references);
    w.done()
}
pub fn decode_reply(b: &[u8]) -> Result<Reply, Error> {
    if b.len() > MAX_CATALOG_BYTES {
        return Err(Error::Limit);
    }
    let mut r = R { b, p: 0 };
    let v = Reply {
        found: r.flag()?,
        count: r.n()? as i64,
        nil_entries: r.flag()?,
        from_cache: r.flag()?,
        has_backup: r.flag()?,
        entry: r.entry()?,
        entries: r.list()?,
        references: r.n()?,
    };
    r.done()?;
    Ok(v)
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
    fn apply(s: &mut CatalogState, c: Command) {
        let b = encode_command(&c).unwrap();
        apply_payload(s, &b).unwrap();
        validate_last(s, &b).unwrap();
    }
    fn entry(id: &[u8], url: &[u8], h: u64) -> Entry {
        Entry {
            id: id.to_vec(),
            url: url.to_vec(),
            info_handle: h,
            ..Entry::default()
        }
    }
    fn append(s: &mut CatalogState, e: Entry) {
        apply(s, Command { entry: e, ..c(2) })
    }
    fn ask(s: &CatalogState, q: Query) -> Reply {
        decode_reply(&query(s, &encode_query(&q).unwrap()).unwrap()).unwrap()
    }
    #[test]
    fn all_actions_and_ordered_duplicates() {
        let mut s = CatalogState::default();
        append(&mut s, entry(b"dup", b"url", 1));
        append(&mut s, entry(b"dup", b"url", 2));
        apply(
            &mut s,
            Command {
                index: 1,
                mask: 1 << 2,
                entry: Entry {
                    label: vec![255],
                    ..Default::default()
                },
                ..c(4)
            },
        );
        assert_eq!(
            ask(
                &s,
                Query {
                    kind: 3,
                    id: b"dup".to_vec(),
                    ..Default::default()
                }
            )
            .entry
            .info_handle,
            1
        );
        apply(&mut s, c(5));
        apply(
            &mut s,
            Command {
                id: b"dup".to_vec(),
                ..c(6)
            },
        );
        assert_eq!(s.entries, Some(vec![]));
        assert_eq!(s.references(1), 1);
        apply(&mut s, c(7));
        assert_eq!(s.references(1), 2);
        apply(&mut s, c(8));
        assert_eq!(s.references(1), 1);
        apply(&mut s, Command { flag: true, ..c(9) });
        apply(&mut s, c(3));
        apply(&mut s, c(1));
        assert!(s.entries.is_none());
        assert!(s.from_cache);
        assert!(s.refs.is_empty());
    }
    #[test]
    fn first_url_or_hash_uses_earliest_list_position() {
        let mut s = CatalogState::default();
        append(&mut s, entry(b"hash", b"other", 1));
        append(&mut s, entry(b"id", b"url", 2));
        let q = Query {
            kind: 4,
            id: b"hash".to_vec(),
            url: b"url".to_vec(),
            ..Default::default()
        };
        assert_eq!(ask(&s, q).entry.info_handle, 1);
        apply(
            &mut s,
            Command {
                index: 0,
                mask: 3,
                entry: entry(b"changed", b"changed", 0),
                ..c(4)
            },
        );
        assert_eq!(
            ask(
                &s,
                Query {
                    kind: 4,
                    id: b"hash".to_vec(),
                    url: b"url".to_vec(),
                    ..Default::default()
                }
            )
            .entry
            .info_handle,
            2
        )
    }
    #[test]
    fn quota_guard_retains_aba_and_last_completion_wins() {
        let mut s = CatalogState::default();
        append(&mut s, entry(b"id", b"original", 1));
        append(&mut s, entry(b"id", b"original", 2));
        let q = Command {
            index: 0,
            id: b"id".to_vec(),
            url: b"original".to_vec(),
            mask: (1 << 8) | (1 << 9) | (1 << 10),
            entry: Entry {
                info_handle: 9,
                quota_updated_at: i64::MAX,
                quota_error: vec![255],
                ..Default::default()
            },
            ..c(10)
        };
        let mut wrong = q.clone();
        wrong.index = 1;
        let before = s.clone();
        assert!(apply_payload(&mut s, &encode_command(&wrong).unwrap()).is_err());
        assert_eq!(s, before);
        apply(
            &mut s,
            Command {
                index: 0,
                mask: 2,
                entry: entry(b"", b"different", 0),
                ..c(4)
            },
        );
        assert!(apply_payload(&mut s, &encode_command(&q).unwrap()).is_err());
        apply(
            &mut s,
            Command {
                index: 0,
                mask: 2,
                entry: entry(b"", b"original", 0),
                ..c(4)
            },
        );
        apply(&mut s, q.clone());
        let mut late = q;
        late.entry.info_handle = 10;
        apply(&mut s, late);
        assert_eq!(s.references(9), 0);
        assert_eq!(s.references(10), 1);
        assert_eq!(s.entries.as_ref().unwrap()[1].info_handle, 2)
    }
    #[test]
    fn all_eleven_patch_fields_and_legacy_values() {
        let mut s = CatalogState::default();
        append(&mut s, Entry::default());
        let e = Entry {
            id: vec![255],
            url: b"u".to_vec(),
            label: b"l".to_vec(),
            prefix: b"p".to_vec(),
            enabled: true,
            updated_at: i64::MIN,
            node_count: -3,
            last_error: b"e".to_vec(),
            info_handle: u64::MAX,
            quota_updated_at: i64::MAX,
            quota_error: b"q".to_vec(),
        };
        for i in 0..11 {
            apply(
                &mut s,
                Command {
                    index: 0,
                    mask: 1 << i,
                    entry: e.clone(),
                    ..c(4)
                },
            );
        }
        assert_eq!(s.entries.as_ref().unwrap()[0], e);
        assert_eq!(decode(&encode(&s).unwrap()).unwrap(), s)
    }
    #[test]
    fn nil_empty_add_rollback_and_empty_delete_backup() {
        let mut s = CatalogState::default();
        append(&mut s, Entry::default());
        apply(&mut s, c(3));
        assert_eq!(s.entries, Some(vec![]));
        apply(&mut s, c(5));
        assert!(s.backup.is_none());
        apply(&mut s, c(7));
        assert!(s.entries.is_none());
        apply(&mut s, c(8));
        assert!(s.backup.is_none());
        assert!(!s.has_backup)
    }
    #[test]
    fn nested_begin_reset_and_empty_truncate_fail_without_change() {
        let mut s = CatalogState::default();
        assert!(apply_payload(&mut s, &encode_command(&c(3)).unwrap()).is_err());
        append(&mut s, entry(b"a", b"", 8));
        apply(&mut s, c(5));
        let before = s.clone();
        for a in [1, 5] {
            assert!(apply_payload(&mut s, &encode_command(&c(a)).unwrap()).is_err());
            assert_eq!(s, before)
        }
        apply(&mut s, c(8));
        assert!(apply_payload(&mut s, &encode_command(&c(7)).unwrap()).is_err())
    }
    #[test]
    fn shared_handles_backup_and_nil_handle_reference_counts() {
        let mut s = CatalogState::default();
        for h in [0, 7, 7, 9] {
            append(&mut s, entry(b"dup", b"u", h))
        }
        assert_eq!(s.references(7), 2);
        assert_eq!(s.references(0), 0);
        apply(&mut s, c(5));
        assert_eq!(s.references(7), 4);
        apply(
            &mut s,
            Command {
                id: b"dup".to_vec(),
                ..c(6)
            },
        );
        assert_eq!(s.references(7), 2);
        apply(&mut s, c(8));
        assert_eq!(s.references(7), 0);
        assert!(s.refs.is_empty())
    }
    #[test]
    fn live_plus_backup_budget_rejects_before_mutation() {
        let mut s = CatalogState::default();
        append(
            &mut s,
            Entry {
                label: vec![1; 9 << 20],
                ..Default::default()
            },
        );
        let before = s.clone();
        assert_eq!(
            apply_payload(&mut s, &encode_command(&c(5)).unwrap()),
            Err(Error::Limit)
        );
        assert_eq!(s, before);
        assert!(encode(&s).is_ok());
        assert!(ask(
            &s,
            Query {
                kind: 2,
                ..Default::default()
            }
        )
        .entries
        .is_some())
    }
    #[test]
    fn exact_state_limit_and_increment_rejection() {
        let mut s = CatalogState::default();
        let payload = MAX_STATE_BYTES - 20 - 81;
        append(
            &mut s,
            Entry {
                label: vec![1; payload],
                ..Default::default()
            },
        );
        assert_eq!(encode(&s).unwrap().len(), MAX_STATE_BYTES);
        let before_size = s.size;
        assert!(apply_payload(
            &mut s,
            &encode_command(&Command {
                entry: Entry::default(),
                ..c(2)
            })
            .unwrap()
        )
        .is_err());
        assert_eq!(s.size, before_size);
        assert!(query(
            &s,
            &encode_query(&Query {
                kind: 2,
                ..Default::default()
            })
            .unwrap()
        )
        .is_ok())
    }
    #[test]
    fn invalid_payloads_and_masks_are_atomic() {
        let mut s = CatalogState::default();
        let valid = encode_command(&c(1)).unwrap();
        for i in 0..valid.len() {
            assert!(apply_payload(&mut s, &valid[..i]).is_err())
        }
        let mut extra = valid;
        extra.push(0);
        assert!(apply_payload(&mut s, &extra).is_err());
        assert!(encode_command(&Command {
            mask: 1 << 11,
            ..c(4)
        })
        .is_err());
        assert!(encode_command(&Command { mask: 1, ..c(10) }).is_err());
        assert!(query(
            &s,
            &encode_query(&Query {
                kind: 6,
                ..Default::default()
            })
            .unwrap()
        )
        .is_err());
        assert_eq!(s, CatalogState::default())
    }
    #[test]
    fn large_narrow_updates_keep_unrelated_entry_allocations() {
        let mut s = CatalogState::default();
        for i in 0..10000 {
            append(&mut s, entry(format!("id-{i}").as_bytes(), b"url", 1));
        }
        let p = s.entries.as_ref().unwrap()[9999].id.as_ptr();
        for i in 0..10000 {
            let command = Command {
                index: 0,
                mask: 1 << 8,
                entry: Entry {
                    info_handle: (i % 2 + 2) as u64,
                    ..Default::default()
                },
                ..c(4)
            };
            apply_payload(&mut s, &encode_command(&command).unwrap()).unwrap();
            assert_eq!(s.entries.as_ref().unwrap()[9999].id.as_ptr(), p);
            assert_eq!(
                ask(
                    &s,
                    Query {
                        kind: 5,
                        handle: 1,
                        ..Default::default()
                    }
                )
                .references,
                9999
            );
        }
        assert_eq!(s.refs.len(), 2);
        assert_eq!(decode(&encode(&s).unwrap()).unwrap(), s)
    }
    #[test]
    fn checkpoint_changed_fields_are_bound_not_invented_history() {
        let mut s = CatalogState::default();
        append(&mut s, entry(b"id", b"url", 1));
        let c = Command {
            index: 0,
            mask: 1 << 2,
            entry: Entry {
                label: b"new".to_vec(),
                ..Default::default()
            },
            ..c(4)
        };
        apply(&mut s, c.clone());
        let mut corrupted = c;
        corrupted.entry.label = b"wrong".to_vec();
        assert!(validate_last(&s, &encode_command(&corrupted).unwrap()).is_err());
    }
    #[test]
    fn query_metadata_and_count_index_contract() {
        let mut s = CatalogState::default();
        append(&mut s, entry(b"a", b"a", 1));
        append(&mut s, entry(b"b", b"b", 2));
        apply(&mut s, Command { flag: true, ..c(9) });
        apply(&mut s, c(5));
        for kind in [0, 1, 2, 3, 4, 5] {
            let r = ask(
                &s,
                Query {
                    kind,
                    index: 1,
                    id: b"b".to_vec(),
                    url: b"b".to_vec(),
                    handle: 2,
                },
            );
            assert!(!r.nil_entries);
            assert!(r.from_cache);
            assert!(r.has_backup);
            assert_eq!(r.count, if [1, 3, 4].contains(&kind) { 1 } else { 2 });
        }
        let r = ask(
            &s,
            Query {
                kind: 3,
                id: b"missing".to_vec(),
                ..Default::default()
            },
        );
        assert!(!r.found);
        assert_eq!(r.count, 2);
    }
    #[test]
    fn restored_checkpoint_backup_budget_is_checked() {
        let backup = Some(vec![Entry {
            label: vec![1; 9 << 20],
            ..Default::default()
        }]);
        let mut w = W::new();
        w.list(&None);
        w.flag(false);
        w.flag(true);
        w.list(&backup);
        let mut s = decode(&w.done().unwrap()).unwrap();
        let before = s.clone();
        assert_eq!(
            apply_payload(&mut s, &encode_command(&c(7)).unwrap()),
            Err(Error::Limit)
        );
        assert_eq!(s, before);
        let mut w = W::new();
        w.list(&None);
        w.flag(false);
        w.flag(true);
        w.list(&Some(vec![]));
        let mut s = decode(&w.done().unwrap()).unwrap();
        apply(&mut s, c(7));
        assert!(s.entries.is_none());
    }
    #[test]
    fn checkpoint_quota_requires_present_first_id_and_exact_url() {
        let mut s = CatalogState::default();
        append(&mut s, entry(b"id", b"url", 7));
        append(&mut s, entry(b"id", b"url", 7));
        let valid = Command {
            action: 10,
            index: 0,
            id: b"id".to_vec(),
            url: b"url".to_vec(),
            ..Default::default()
        };
        validate_last(&s, &encode_command(&valid).unwrap()).unwrap();
        for bad in [
            Command {
                id: b"absent".to_vec(),
                ..valid.clone()
            },
            Command {
                index: 1,
                ..valid.clone()
            },
            Command {
                url: b"changed".to_vec(),
                ..valid.clone()
            },
        ] {
            assert_eq!(
                validate_last(&s, &encode_command(&bad).unwrap()),
                Err(Error::Invalid)
            );
        }
        assert_eq!(
            validate_last(&CatalogState::default(), &encode_command(&valid).unwrap()),
            Err(Error::Invalid)
        );
    }
    #[test]
    fn checkpoint_delete_nilshape_and_empty_backup_restore() {
        let mut s = CatalogState::default();
        let remove = encode_command(&c(6)).unwrap();
        assert_eq!(validate_last(&s, &remove), Err(Error::Invalid));
        apply(&mut s, c(6));
        validate_last(&s, &remove).unwrap();
        s.has_backup = true;
        s.backup = Some(vec![]);
        s.entries = None;
        validate_last(&s, &encode_command(&c(7)).unwrap()).unwrap();
        s.entries = Some(vec![]);
        assert_eq!(
            validate_last(&s, &encode_command(&c(7)).unwrap()),
            Err(Error::Invalid)
        );
    }
}
