//! Identity-sensitive asynchronous exit cleanup. Identity comparison and lock
//! ownership remain in Go; no runner pointer or object crosses this boundary.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Snapshot {
    pub matches: bool,
    pub relay: bool,
    pub patrol: bool,
    pub proxy_on: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub text: Vec<u8>,
}
#[derive(Clone, Debug, Default)]
pub struct Reply {
    pub kind: u8,
    pub ok: bool,
    pub error: Vec<u8>,
    pub snapshot: Option<Snapshot>,
}
// 1 Lock/current identity,2 CloseAppRelayAndNil,3 StopHistory(no collect),
// 4 ClearKernelAndActive,5 RemoveConfig,6 ClosePatrolAndNil,
// 7 ClearBlock,8 ProxyOff,9 LogProxyError,10 ClearProxyFlag,11 DropTunnel,
// 63 Result(success),0 Done. In particular there is no stop-path or runner-stop.
#[derive(Clone, Debug)]
pub struct Flow {
    s: Snapshot,
    e: Effect,
}
impl Default for Flow {
    fn default() -> Self {
        Self::new()
    }
}
impl Flow {
    pub fn new() -> Self {
        Self {
            s: Snapshot::default(),
            e: Effect {
                kind: 1,
                text: vec![],
            },
        }
    }
    pub fn next(&self) -> Effect {
        self.e.clone()
    }
    fn emit(&mut self, kind: u8) {
        self.e = Effect { kind, text: vec![] };
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), &'static str> {
        if self.e.kind == 0 || r.kind != self.e.kind {
            return Err("unexpected kernel-exit reply");
        }
        if self.e.kind == 1 && r.snapshot.is_none() {
            return Err("missing locked identity snapshot");
        }
        if let Some(s) = r.snapshot {
            self.s = s;
        }
        match self.e.kind {
            1 => {
                if !self.s.matches {
                    self.emit(63);
                } else if self.s.relay {
                    self.emit(2);
                } else {
                    self.emit(3);
                }
            }
            2 => self.emit(3),
            3 => self.emit(4),
            4 => self.emit(5),
            5 => {
                if self.s.patrol {
                    self.emit(6);
                } else {
                    self.emit(7);
                }
            }
            6 => self.emit(7),
            7 => {
                if self.s.proxy_on {
                    self.emit(8);
                } else {
                    self.emit(11);
                }
            }
            8 => {
                if r.ok {
                    self.emit(10);
                } else {
                    self.emit(9);
                    self.e.text = r.error;
                }
            }
            9 => self.emit(10),
            10 => self.emit(11),
            11 => self.emit(63),
            63 => self.emit(0),
            _ => return Err("invalid kernel-exit effect"),
        }
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn run(s: Snapshot, mut alter: impl FnMut(&Effect, &mut Reply)) -> Vec<Effect> {
        let mut f = Flow::new();
        let mut out = vec![];
        for _ in 0..20 {
            let e = f.next();
            if e.kind == 0 {
                return out;
            }
            let mut r = Reply {
                kind: e.kind,
                ok: true,
                snapshot: if e.kind == 1 { Some(s.clone()) } else { None },
                ..Reply::default()
            };
            alter(&e, &mut r);
            out.push(e);
            f.advance(r).unwrap();
        }
        panic!("loop")
    }
    #[test]
    fn stale_is_noop_even_with_live_resources() {
        let t = run(
            Snapshot {
                matches: false,
                relay: true,
                patrol: true,
                proxy_on: true,
            },
            |_, _| {},
        );
        assert_eq!(t.iter().map(|e| e.kind).collect::<Vec<_>>(), vec![1, 63]);
    }
    #[test]
    fn all_resource_combinations_preserve_exact_order() {
        for bits in 0..8 {
            let t = run(
                Snapshot {
                    matches: true,
                    relay: bits & 1 != 0,
                    patrol: bits & 2 != 0,
                    proxy_on: bits & 4 != 0,
                },
                |_, _| {},
            );
            let mut expected = vec![1];
            if bits & 1 != 0 {
                expected.push(2);
            }
            expected.extend([3, 4, 5]);
            if bits & 2 != 0 {
                expected.push(6);
            }
            expected.push(7);
            if bits & 4 != 0 {
                expected.extend([8, 10]);
            }
            expected.extend([11, 63]);
            assert_eq!(t.iter().map(|e| e.kind).collect::<Vec<_>>(), expected);
        }
    }
    #[test]
    fn proxy_failure_logs_once_then_clears_flag_and_drops_tunnel() {
        for error in [vec![], b"proxy-error".to_vec(), vec![255, 0]] {
            let t = run(
                Snapshot {
                    matches: true,
                    proxy_on: true,
                    ..Snapshot::default()
                },
                |e, r| {
                    if e.kind == 8 {
                        r.ok = false;
                        r.error = error.clone();
                    }
                },
            );
            assert_eq!(
                t.iter().map(|e| e.kind).collect::<Vec<_>>(),
                vec![1, 3, 4, 5, 7, 8, 9, 10, 11, 63]
            );
            assert_eq!(t.iter().find(|e| e.kind == 9).unwrap().text, error);
        }
    }
    #[test]
    fn identity_only_gates_initial_lock() {
        let t = run(
            Snapshot {
                matches: true,
                relay: true,
                patrol: true,
                proxy_on: true,
            },
            |e, r| {
                if e.kind == 4 {
                    r.snapshot = Some(Snapshot {
                        matches: false,
                        relay: false,
                        patrol: true,
                        proxy_on: true,
                    });
                }
            },
        );
        assert_eq!(t.last().unwrap().kind, 63);
        assert!(t.iter().any(|e| e.kind == 11));
    }
    #[test]
    fn latest_state_controls_remaining_cleanup() {
        let t = run(
            Snapshot {
                matches: true,
                patrol: true,
                proxy_on: true,
                ..Snapshot::default()
            },
            |e, r| {
                if e.kind == 5 {
                    r.snapshot = Some(Snapshot {
                        matches: true,
                        ..Snapshot::default()
                    });
                }
            },
        );
        assert!(!t.iter().any(|e| e.kind == 6 || e.kind == 8 || e.kind == 10));
        assert_eq!(t.last().unwrap().kind, 63);
    }
    #[test]
    fn rejected_reply_does_not_consume_or_change_snapshot() {
        let mut f = Flow::new();
        let e = f.next();
        for r in [
            Reply {
                kind: 1,
                ..Reply::default()
            },
            Reply {
                kind: 2,
                snapshot: Some(Snapshot {
                    matches: true,
                    ..Snapshot::default()
                }),
                ..Reply::default()
            },
        ] {
            assert!(f.advance(r).is_err());
            assert_eq!(f.next(), e);
        }
        f.advance(Reply {
            kind: 1,
            snapshot: Some(Snapshot::default()),
            ..Reply::default()
        })
        .unwrap();
        assert_eq!(f.next().kind, 63);
        f.advance(Reply {
            kind: 63,
            ..Reply::default()
        })
        .unwrap();
        assert!(f.advance(Reply::default()).is_err());
        assert_eq!(f.next().kind, 0);
    }
}
