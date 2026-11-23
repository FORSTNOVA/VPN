//! Quota refresh continuation. Registry snapshots are read under each lock;
//! only quota fields are committed, never the pre-network registry.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Entry {
    pub id: Vec<u8>,
    pub url: Vec<u8>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Info {
    pub upload: i64,
    pub download: i64,
    pub total: i64,
    pub expire: i64,
}
#[derive(Clone, Debug, Default)]
pub struct Request {
    pub id: Vec<u8>,
    pub error_mode: bool,
    pub original_url: Vec<u8>,
    pub message: Vec<u8>,
    pub status: u16,
}
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Effect {
    Lock,
    Categorize(Vec<u8>),
    Proxies,
    Unlock,
    Alternate(Vec<u8>),
    Fetch {
        url: Vec<u8>,
        proxy: Vec<u8>,
    },
    Relock,
    Now,
    Commit {
        index: usize,
        info: Option<Info>,
        timestamp: i64,
        error: u8,
        message: Vec<u8>,
    },
    Save,
    Result {
        status: u16,
        code: u8,
        index: Option<usize>,
        message: Vec<u8>,
    },
    Done,
}
#[derive(Clone, Debug)]
pub enum Reply {
    Ack,
    Entries(Vec<Entry>),
    Strings(Vec<Vec<u8>>),
    Fetch {
        ok: bool,
        info: Option<Info>,
        no_quota: bool,
    },
    Time(i64),
    Saved(bool),
}
#[derive(Clone, Debug)]
pub struct Flow {
    request: Request,
    effect: Effect,
    original: Vec<u8>,
    urls: Vec<Vec<u8>>,
    proxies: Vec<Vec<u8>>,
    candidates: Vec<Vec<u8>>,
    u: usize,
    p: usize,
    c: usize,
    info: Option<Info>,
    no_quota: bool,
    error: u8,
    index: usize,
    not_found: bool,
}
impl Flow {
    pub fn new(request: Request) -> Result<Self, &'static str> {
        let effect = if request.error_mode {
            Effect::Relock
        } else if request.id.is_empty() {
            Effect::Result {
                status: 400,
                code: 1,
                index: None,
                message: vec![],
            }
        } else {
            Effect::Lock
        };
        let original = request.original_url.clone();
        let error = if request.error_mode { 10 } else { 0 };
        Ok(Self {
            request,
            effect,
            original,
            urls: vec![],
            proxies: vec![],
            candidates: vec![],
            u: 0,
            p: 0,
            c: 0,
            info: None,
            no_quota: false,
            error,
            index: 0,
            not_found: false,
        })
    }
    pub fn next(&self) -> Effect {
        self.effect.clone()
    }
    fn result(&mut self, status: u16, code: u8) {
        self.effect = Effect::Result {
            status,
            code,
            index: if status == 200 {
                Some(self.index)
            } else {
                None
            },
            message: if code == 10 {
                self.request.message.clone()
            } else {
                vec![]
            },
        };
    }
    fn attempt(&mut self) {
        if self.p >= self.proxies.len() {
            self.u += 1;
            self.p = 0;
            self.c = 0;
            if self.u >= self.urls.len() {
                self.error = if self.no_quota { 4 } else { 5 };
                self.effect = Effect::Relock;
                return;
            }
            self.effect = Effect::Alternate(self.urls[self.u].clone());
            return;
        }
        self.effect = Effect::Fetch {
            url: self.candidates[self.c].clone(),
            proxy: self.proxies[self.p].clone(),
        };
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        // Type-check before changing state; invalid replies do not consume the step.
        let valid = matches!(
            (&self.effect, &r),
            (Effect::Lock | Effect::Relock, Reply::Entries(_))
                | (
                    Effect::Categorize(_) | Effect::Proxies | Effect::Alternate(_),
                    Reply::Strings(_)
                )
                | (Effect::Fetch { .. }, Reply::Fetch { .. })
                | (Effect::Now, Reply::Time(_))
                | (Effect::Save, Reply::Saved(_))
                | (
                    Effect::Unlock | Effect::Commit { .. } | Effect::Result { .. },
                    Reply::Ack
                )
        );
        if !valid {
            return Err("unexpected quota reply");
        }
        match (self.effect.clone(), r) {
            (Effect::Lock, Reply::Entries(entries)) => {
                if let Some(e) = entries.iter().find(|e| e.id == self.request.id) {
                    self.original = e.url.clone();
                    self.effect = Effect::Categorize(e.url.clone());
                } else {
                    self.not_found = true;
                    self.effect = Effect::Unlock;
                }
            }
            (Effect::Categorize(_), Reply::Strings(urls)) => {
                self.urls = urls;
                self.effect = Effect::Proxies;
            }
            (Effect::Proxies, Reply::Strings(proxies)) => {
                self.proxies = proxies;
                self.effect = Effect::Unlock;
            }
            (Effect::Unlock, Reply::Ack) => {
                if self.not_found {
                    self.result(404, 2);
                } else if self.urls.is_empty() {
                    self.error = 3;
                    self.effect = Effect::Relock;
                } else {
                    self.effect = Effect::Alternate(self.urls[0].clone());
                }
            }
            (Effect::Alternate(url), Reply::Strings(alts)) => {
                self.candidates = vec![url];
                self.candidates.extend(alts);
                self.c = 0;
                self.attempt();
            }
            (Effect::Fetch { .. }, Reply::Fetch { ok, info, no_quota }) => {
                if ok && info.is_some() {
                    self.info = info;
                    self.effect = Effect::Relock;
                } else {
                    if !ok && no_quota {
                        self.no_quota = true;
                    }
                    // nil error with nil info breaks only the candidate loop, matching Go.
                    if ok {
                        self.c = 0;
                        self.p += 1;
                    } else {
                        self.c += 1;
                        if self.c == self.candidates.len() {
                            self.c = 0;
                            self.p += 1;
                        }
                    }
                    self.attempt();
                }
            }
            (Effect::Relock, Reply::Entries(entries)) => {
                if let Some((i, e)) = entries
                    .iter()
                    .enumerate()
                    .find(|(_, e)| e.id == self.request.id)
                {
                    if e.url != self.original {
                        self.result(409, 6);
                    } else {
                        self.index = i;
                        if self.info.is_some() {
                            self.effect = Effect::Now;
                        } else {
                            self.effect = Effect::Commit {
                                index: i,
                                info: None,
                                timestamp: 0,
                                error: self.error,
                                message: self.request.message.clone(),
                            };
                        }
                    }
                } else {
                    self.result(409, 7);
                }
            }
            (Effect::Now, Reply::Time(timestamp)) => {
                self.effect = Effect::Commit {
                    index: self.index,
                    info: self.info.clone(),
                    timestamp,
                    error: 0,
                    message: vec![],
                }
            }
            (Effect::Commit { .. }, Reply::Ack) => self.effect = Effect::Save,
            (Effect::Save, Reply::Saved(ok)) => {
                if !ok {
                    self.result(500, if self.info.is_some() { 8 } else { 9 });
                } else if self.info.is_some() {
                    self.result(200, 0);
                } else {
                    self.result(
                        if self.error == 10 {
                            self.request.status
                        } else if self.error == 3 {
                            400
                        } else {
                            502
                        },
                        self.error,
                    );
                }
            }
            (Effect::Result { .. }, Reply::Ack) => self.effect = Effect::Done,
            _ => unreachable!(),
        }
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn e(id: &str, url: &str) -> Entry {
        Entry {
            id: id.as_bytes().to_vec(),
            url: url.as_bytes().to_vec(),
        }
    }
    fn start() -> Flow {
        let mut f = Flow::new(Request {
            id: b"x".to_vec(),
            ..Request::default()
        })
        .unwrap();
        f.advance(Reply::Entries(vec![e("x", "old")])).unwrap();
        f.advance(Reply::Strings(vec![b"u".to_vec(), b"v".to_vec()]))
            .unwrap();
        f.advance(Reply::Strings(vec![b"p".to_vec(), b"q".to_vec()]))
            .unwrap();
        f.advance(Reply::Ack).unwrap();
        f
    }
    fn info() -> Info {
        Info {
            upload: i64::MIN,
            download: i64::MAX,
            total: -7,
            expire: 4294967296,
        }
    }
    fn success() -> Flow {
        let mut f = start();
        f.advance(Reply::Strings(vec![b"alt".to_vec()])).unwrap();
        f.advance(Reply::Fetch {
            ok: true,
            info: Some(info()),
            no_quota: false,
        })
        .unwrap();
        f
    }
    #[test]
    fn retry_order_and_noquota_priority() {
        let mut f = start();
        let mut seen = vec![];
        loop {
            match f.next() {
                Effect::Alternate(_) => f.advance(Reply::Strings(vec![b"alt".to_vec()])).unwrap(),
                Effect::Fetch { url, proxy } => {
                    seen.push((url, proxy));
                    f.advance(Reply::Fetch {
                        ok: false,
                        info: None,
                        no_quota: seen.len() == 1,
                    })
                    .unwrap();
                }
                Effect::Relock => break,
                _ => panic!(),
            }
        }
        assert_eq!(
            seen,
            vec![
                (b"u".to_vec(), b"p".to_vec()),
                (b"alt".to_vec(), b"p".to_vec()),
                (b"u".to_vec(), b"q".to_vec()),
                (b"alt".to_vec(), b"q".to_vec()),
                (b"v".to_vec(), b"p".to_vec()),
                (b"alt".to_vec(), b"p".to_vec()),
                (b"v".to_vec(), b"q".to_vec()),
                (b"alt".to_vec(), b"q".to_vec())
            ]
        );
        f.advance(Reply::Entries(vec![e("x", "old")])).unwrap();
        assert!(matches!(
            f.next(),
            Effect::Commit {
                error: 4,
                info: None,
                ..
            }
        ));
        f.advance(Reply::Ack).unwrap();
        f.advance(Reply::Saved(true)).unwrap();
        assert!(matches!(
            f.next(),
            Effect::Result {
                status: 502,
                code: 4,
                ..
            }
        ));
    }
    #[test]
    fn fresh_index_full_int64_and_save_failure() {
        let mut f = success();
        f.advance(Reply::Entries(vec![
            e("other", "a"),
            e("x", "old"),
            e("x", "changed"),
        ]))
        .unwrap();
        f.advance(Reply::Time(i64::MAX)).unwrap();
        assert_eq!(
            f.next(),
            Effect::Commit {
                index: 1,
                info: Some(info()),
                timestamp: i64::MAX,
                error: 0,
                message: vec![]
            }
        );
        f.advance(Reply::Ack).unwrap();
        f.advance(Reply::Saved(false)).unwrap();
        assert!(matches!(
            f.next(),
            Effect::Result {
                status: 500,
                code: 8,
                ..
            }
        ));
    }
    #[test]
    fn conflicts_first_duplicate_and_aba() {
        for (entries, code) in [
            (vec![], 7),
            (vec![e("x", "new"), e("x", "old")], 6),
            (vec![e("x", "old")], 0),
        ] {
            let mut f = success();
            f.advance(Reply::Entries(entries)).unwrap();
            if code == 0 {
                assert_eq!(f.next(), Effect::Now);
            } else {
                assert!(matches!(f.next(),Effect::Result{status:409,code:c,..} if c==code));
            }
        }
    }
    #[test]
    fn nil_success_skips_candidate_not_proxy() {
        let mut f = start();
        f.advance(Reply::Strings(vec![b"alt".to_vec()])).unwrap();
        f.advance(Reply::Fetch {
            ok: true,
            info: None,
            no_quota: false,
        })
        .unwrap();
        assert_eq!(
            f.next(),
            Effect::Fetch {
                url: b"u".to_vec(),
                proxy: b"q".to_vec()
            }
        );
    }
    #[test]
    fn no_remote_still_gets_proxies() {
        let mut f = Flow::new(Request {
            id: b"x".to_vec(),
            ..Request::default()
        })
        .unwrap();
        f.advance(Reply::Entries(vec![e("x", "old")])).unwrap();
        f.advance(Reply::Strings(vec![])).unwrap();
        assert_eq!(f.next(), Effect::Proxies);
        f.advance(Reply::Strings(vec![])).unwrap();
        assert_eq!(f.next(), Effect::Unlock);
        f.advance(Reply::Ack).unwrap();
        f.advance(Reply::Entries(vec![e("x", "old")])).unwrap();
        assert!(matches!(f.next(), Effect::Commit { error: 3, .. }));
    }
    #[test]
    fn error_subcase_preserves_message_and_save_priority() {
        for ok in [true, false] {
            let mut f = Flow::new(Request {
                id: b"x".to_vec(),
                error_mode: true,
                original_url: b"old".to_vec(),
                message: vec![255, 0],
                status: 418,
            })
            .unwrap();
            assert_eq!(f.next(), Effect::Relock);
            f.advance(Reply::Entries(vec![e("x", "old")])).unwrap();
            assert!(matches!(
                f.next(),
                Effect::Commit {
                    info: None,
                    error: 10,
                    ..
                }
            ));
            f.advance(Reply::Ack).unwrap();
            f.advance(Reply::Saved(ok)).unwrap();
            assert!(
                matches!(f.next(),Effect::Result{status,code,..} if status==if ok{418}else{500} && code==if ok{10}else{9})
            );
        }
    }
    #[test]
    fn invalid_reply_does_not_consume() {
        let mut f = start();
        let before = f.next();
        assert!(f.advance(Reply::Ack).is_err());
        assert_eq!(f.next(), before);
        f.advance(Reply::Strings(vec![])).unwrap();
        let before = f.next();
        assert!(f.advance(Reply::Time(1)).is_err());
        assert_eq!(f.next(), before);
    }
    #[test]
    fn empty_proxy_list_advances_remote_then_relocks() {
        let mut f = start();
        f.proxies.clear();
        f.advance(Reply::Strings(vec![])).unwrap();
        assert_eq!(f.next(), Effect::Alternate(b"v".to_vec()));
        f.advance(Reply::Strings(vec![])).unwrap();
        assert_eq!(f.next(), Effect::Relock);
    }
    #[test]
    fn missing_entry_unlocks_before_result() {
        let mut f = Flow::new(Request {
            id: b"missing".to_vec(),
            ..Request::default()
        })
        .unwrap();
        f.advance(Reply::Entries(vec![])).unwrap();
        assert_eq!(f.next(), Effect::Unlock);
        f.advance(Reply::Ack).unwrap();
        assert!(matches!(
            f.next(),
            Effect::Result {
                status: 404,
                code: 2,
                ..
            }
        ));
    }
}
