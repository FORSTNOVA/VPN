//! Metadata-only source label restoration. All profile graphs and file bytes stay in Go.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Source {
    pub enabled: bool,
    pub nonempty: bool,
    pub matches: bool,
    pub label: Vec<u8>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Node {
    pub name: Vec<u8>,
    pub label: Vec<u8>,
    pub manual: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Snapshot {
    pub merged: bool,
    pub empty: bool,
    pub default_label: Vec<u8>,
    pub sources: Vec<Source>,
    pub nodes: Vec<Node>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Request {
    pub op: u8,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Reply {
    pub kind: u8,
    pub ok: bool,
    pub flag: bool,
    pub other: bool,
    pub count: i64,
    pub text: Vec<u8>,
    pub snapshot: Option<Snapshot>,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub index: i64,
    pub name: Vec<u8>,
    pub text: Vec<u8>,
}

pub struct Flow {
    e: Effect,
    s: Snapshot,
    index: usize,
    key: usize,
    label: Vec<u8>,
    count: i64,
    merged: bool,
}
const KEYS: [&[u8]; 5] = [
    b"proxy-groups",
    b"smartvpn-source-rules",
    b"smartvpn-source-labels",
    b"smartvpn-policy-sections",
    b"dns",
];
impl Flow {
    pub fn new(q: Request) -> Result<Self, &'static str> {
        if q.op != 0 {
            return Err("operation");
        };
        Ok(Self {
            e: Effect {
                kind: 1,
                ..Effect::default()
            },
            s: Snapshot::default(),
            index: 0,
            key: 0,
            label: vec![],
            count: 0,
            merged: false,
        })
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
    fn done(&mut self) {
        self.s = Snapshot::default();
        self.label.clear();
        self.emit(63)
    }
    fn next_node(&mut self) {
        while self.index < self.s.nodes.len() {
            let n = &self.s.nodes[self.index];
            if !n.manual && (self.merged || n.label.is_empty()) {
                if self.merged {
                    let name = n.name.clone();
                    self.emit(19);
                    self.e.name = name
                } else {
                    self.emit(3);
                    self.e.index = self.index as i64;
                    self.e.text = self.label.clone()
                };
                return;
            }
            self.index += 1
        }
        if self.merged {
            self.emit(20)
        } else {
            self.done()
        }
    }
    fn next_key(&mut self) {
        if self.key < KEYS.len() {
            self.emit(12);
            self.e.name = KEYS[self.key].to_vec()
        } else {
            self.emit(14)
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        if self.e.kind == 0 || r.kind != self.e.kind {
            return Err("reply kind");
        }
        if matches!(r.kind, 1 | 2 | 7 | 18) && r.snapshot.is_none() {
            return Err("snapshot required");
        }
        if let Some(s) = &r.snapshot {
            if s.sources.len() > 100000 || s.nodes.len() > 100000 {
                return Err("limit");
            }
        }
        match r.kind {
            1 => {
                self.s = r.snapshot.unwrap();
                self.merged = self.s.merged;
                if self.merged {
                    self.emit(4)
                } else if self.s.empty {
                    self.done()
                } else {
                    self.emit(2)
                }
            }
            2 => {
                self.s = r.snapshot.unwrap();
                self.label = self
                    .s
                    .sources
                    .iter()
                    .find(|s| s.matches && !s.label.is_empty())
                    .map(|s| s.label.clone())
                    .unwrap_or_else(|| self.s.default_label.clone());
                self.index = 0;
                self.next_node()
            }
            3 => {
                self.index += 1;
                self.next_node()
            }
            4 => {
                if r.ok {
                    self.emit(5)
                } else {
                    self.done()
                }
            }
            5 => {
                if r.ok && r.flag && !r.other {
                    self.emit(6)
                } else {
                    self.done()
                }
            }
            6 => {
                self.count = r.count;
                self.emit(7)
            }
            7 => {
                self.s = r.snapshot.unwrap();
                let enabled = self
                    .s
                    .sources
                    .iter()
                    .filter(|s| s.enabled && s.nonempty)
                    .count();
                if self.count == enabled as i64 {
                    self.emit(8)
                } else {
                    self.done()
                }
            }
            8 => {
                if r.ok {
                    self.emit(9)
                } else {
                    self.done()
                }
            }
            9 => {
                if r.ok {
                    self.emit(10)
                } else {
                    self.done()
                }
            }
            10 => {
                if r.ok && r.flag {
                    self.emit(11)
                } else {
                    self.done()
                }
            }
            11 => {
                if r.ok {
                    self.key = 0;
                    self.next_key()
                } else {
                    self.done()
                }
            }
            12 => {
                if !r.flag && r.other {
                    self.emit(13);
                    self.e.name = KEYS[self.key].to_vec()
                } else {
                    self.key += 1;
                    self.next_key()
                }
            }
            13 => {
                self.key += 1;
                self.next_key()
            }
            14 => {
                if r.ok {
                    self.emit(15)
                } else {
                    self.done()
                }
            }
            15 => {
                if r.ok {
                    self.emit(16)
                } else {
                    self.done()
                }
            }
            16 => {
                if r.ok {
                    self.emit(18)
                } else {
                    self.emit(17)
                }
            }
            17 => self.done(),
            18 => {
                self.s = r.snapshot.unwrap();
                self.index = 0;
                self.next_node()
            }
            19 => {
                self.emit(3);
                self.e.index = self.index as i64;
                self.e.text = r.text
            }
            20 => self.done(),
            63 => self.emit(0),
            _ => return Err("reply kind"),
        };
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn run(mut cb: impl FnMut(&Effect) -> Reply) -> Vec<Effect> {
        let mut f = Flow::new(Request::default()).unwrap();
        let mut out = vec![];
        for _ in 0..1000 {
            let e = f.next();
            if e.kind == 63 {
                return out;
            }
            let mut r = cb(&e);
            r.kind = e.kind;
            out.push(e);
            f.advance(r).unwrap()
        }
        panic!("loop")
    }
    fn merged(e: &Effect) -> Reply {
        Reply {
            kind: e.kind,
            ok: true,
            flag: matches!(e.kind, 5 | 10),
            other: e.kind == 12,
            snapshot: matches!(e.kind, 1 | 2 | 7 | 18).then(|| Snapshot {
                merged: true,
                ..Snapshot::default()
            }),
            ..Reply::default()
        }
    }
    #[test]
    fn empty_single_stops_without_io() {
        let t = run(|e| Reply {
            snapshot: Some(Snapshot {
                empty: true,
                ..Snapshot::default()
            }),
            ..merged(e)
        });
        assert_eq!(t.len(), 1)
    }
    #[test]
    fn single_first_matching_nonempty_even_disabled_and_unlabelled_only() {
        let t = run(|e| Reply {
            snapshot: Some(Snapshot {
                default_label: b"default".to_vec(),
                sources: vec![
                    Source {
                        matches: true,
                        ..Source::default()
                    },
                    Source {
                        matches: true,
                        label: b"chosen".to_vec(),
                        ..Source::default()
                    },
                    Source {
                        matches: true,
                        label: b"later".to_vec(),
                        ..Source::default()
                    },
                ],
                nodes: vec![
                    Node::default(),
                    Node {
                        manual: true,
                        ..Node::default()
                    },
                    Node {
                        label: b"keep".to_vec(),
                        ..Node::default()
                    },
                ],
                ..Snapshot::default()
            }),
            ..merged(e)
        });
        let updates: Vec<_> = t.iter().filter(|e| e.kind == 3).collect();
        assert_eq!(updates.len(), 1);
        assert_eq!(updates[0].index, 0);
        assert_eq!(updates[0].text, b"chosen");
        assert!(!t.iter().any(|e| e.kind == 20));
    }
    #[test]
    fn single_default_label() {
        let t = run(|e| Reply {
            snapshot: Some(Snapshot {
                default_label: "当前订阅".as_bytes().to_vec(),
                nodes: vec![Node::default()],
                ..Snapshot::default()
            }),
            ..merged(e)
        });
        assert_eq!(t.last().unwrap().text, "当前订阅".as_bytes())
    }
    #[test]
    fn merged_only_absent_nonnull_keys_in_fixed_order() {
        let t = run(|e| {
            let mut r = merged(e);
            if e.kind == 12 {
                r.flag = e.name == KEYS[0];
                r.other = e.name != KEYS[4]
            }
            r
        });
        let keys: Vec<_> = t
            .iter()
            .filter(|e| e.kind == 13)
            .map(|e| e.name.as_slice())
            .collect();
        assert_eq!(keys, KEYS[1..4]);
    }
    #[test]
    fn source_count_includes_only_enabled_nonempty() {
        let t = run(|e| {
            let mut r = merged(e);
            if e.kind == 6 {
                r.count = 1
            }
            if e.kind == 7 {
                r.snapshot.as_mut().unwrap().sources = vec![
                    Source {
                        enabled: true,
                        nonempty: true,
                        ..Source::default()
                    },
                    Source {
                        enabled: true,
                        ..Source::default()
                    },
                    Source {
                        nonempty: true,
                        ..Source::default()
                    },
                ]
            }
            r
        });
        assert!(t.iter().any(|e| e.kind == 20));
        let t = run(|e| {
            let mut r = merged(e);
            if e.kind == 6 {
                r.count = -1
            }
            r
        });
        assert_eq!(t.last().unwrap().kind, 7)
    }
    #[test]
    fn every_fallible_stage_silent_and_no_later_writes() {
        for fail in [4, 5, 8, 9, 10, 11, 14, 15, 16] {
            let t = run(|e| {
                let mut r = merged(e);
                if e.kind == fail {
                    r.ok = false
                }
                r
            });
            assert_eq!(t.last().unwrap().kind, if fail == 16 { 17 } else { fail });
            assert!(!t.iter().any(|e| e.kind == 20));
        }
    }
    #[test]
    fn nil_policy_key_and_unequal_proxies_stop() {
        for which in 0..3 {
            let t = run(|e| {
                let mut r = merged(e);
                if which == 0 && e.kind == 5 {
                    r.flag = false
                }
                if which == 1 && e.kind == 5 {
                    r.other = true
                }
                if which == 2 && e.kind == 10 {
                    r.flag = false
                }
                r
            });
            assert_eq!(t.last().unwrap().kind, if which == 2 { 10 } else { 5 })
        }
    }
    #[test]
    fn merged_overwrites_nonmanual_labels_including_empty_then_ignores_save_error() {
        let t = run(|e| {
            let mut r = merged(e);
            if e.kind == 18 {
                r.snapshot.as_mut().unwrap().nodes = vec![
                    Node {
                        label: b"old".to_vec(),
                        name: b"n".to_vec(),
                        ..Node::default()
                    },
                    Node {
                        manual: true,
                        ..Node::default()
                    },
                ]
            };
            if e.kind == 20 {
                r.ok = false
            }
            r
        });
        let updates: Vec<_> = t.iter().filter(|e| e.kind == 3).collect();
        assert_eq!(updates.len(), 1);
        assert!(updates[0].text.is_empty());
        assert_eq!(t.last().unwrap().kind, 20);
    }
    #[test]
    fn malformed_reply_is_not_consumed() {
        let mut f = Flow::new(Request::default()).unwrap();
        let old = f.next();
        assert!(f
            .advance(Reply {
                kind: 1,
                ..Reply::default()
            })
            .is_err());
        assert_eq!(old, f.next());
        assert!(f
            .advance(Reply {
                kind: 2,
                snapshot: Some(Snapshot::default()),
                ..Reply::default()
            })
            .is_err());
        assert_eq!(old, f.next());
    }
}
