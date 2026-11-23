//! Full settings-update decision/continuation, with opaque values and OS facts in Go.
//! No external effect is performed here. Validation precedes the single settings
//! commit. Best-effort cleanup writes remain best-effort; final save never rolls back.
use std::collections::VecDeque;
#[derive(Clone, Debug, Default)]
pub struct Snapshot {
    pub presence: u16,
    pub invalid: u16,
    pub socks_conflict: bool,
    pub domestic: bool,
    pub webrtc: bool,
    pub dns: bool,
    pub strict_value: bool,
    pub domestic_value: bool,
    pub webrtc_value: bool,
    pub dns_value: bool,
    pub url_changed: bool,
    pub url_merged: bool,
    pub store_available: bool,
    pub cache_found: bool,
    pub ensure_needed: bool,
}
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Effect {
    KernelRunning {
        import_policy: bool,
    },
    ApplySettings {
        presence: u16,
        domestic: bool,
        webrtc: bool,
        dns: bool,
    },
    ClearNodeState,
    RemoveCachedNodes,
    ClearHealth,
    ClearRegions,
    SaveSettings {
        final_save: bool,
    },
    SetProfileFromCache(bool),
    UseCachedProfile,
    RemoveProfile {
        suffix: u8,
    },
    AppendSubscription,
    SaveSubscriptions,
    Result {
        code: u8,
    },
    Done,
}
#[derive(Clone, Debug)]
pub enum Reply {
    Ack,
    Bool(bool),
}
#[derive(Clone, Debug)]
pub struct Flow {
    snapshot: Snapshot,
    queue: VecDeque<Effect>,
}
impl Flow {
    pub fn new(snapshot: Snapshot) -> Result<Self, &'static str> {
        if snapshot.presence & !0x7ff != 0 || snapshot.invalid & !0x7ff != 0 {
            return Err("unknown patch field");
        }
        let mut f = Self {
            snapshot,
            queue: VecDeque::new(),
        };
        if f.has(0) {
            f.queue.push_back(Effect::KernelRunning {
                import_policy: true,
            });
        } else {
            f.validate();
        }
        Ok(f)
    }
    fn has(&self, bit: u8) -> bool {
        self.snapshot.presence & (1 << bit) != 0
    }
    fn invalid(&self, bit: u8) -> bool {
        self.has(bit) && self.snapshot.invalid & (1 << bit) != 0
    }
    fn result(&mut self, code: u8) {
        self.queue.push_back(Effect::Result { code });
    }
    fn validate(&mut self) {
        let error = if self.invalid(1) {
            2
        } else if self.has(1) && self.snapshot.socks_conflict {
            3
        } else if self.invalid(2) {
            4
        } else if self.invalid(7) {
            5
        } else if self.invalid(8) {
            6
        } else if self.invalid(9) {
            7
        } else if self.invalid(10) {
            8
        } else {
            0
        };
        if error != 0 {
            self.result(error);
        } else {
            self.queue.push_back(Effect::KernelRunning {
                import_policy: false,
            });
        }
    }
    fn commit(&mut self) {
        let s = &self.snapshot;
        let (mut domestic, mut webrtc, mut dns) = (s.domestic, s.webrtc, s.dns);
        if self.has(3) {
            webrtc = s.strict_value;
            dns = s.strict_value;
        }
        if self.has(4) {
            if s.domestic_value && !domestic {
                webrtc = true;
                dns = true;
            }
            domestic = s.domestic_value;
        }
        if self.has(5) {
            webrtc = s.webrtc_value;
        }
        if self.has(6) {
            dns = s.dns_value;
        }
        self.queue.push_back(Effect::ApplySettings {
            presence: s.presence,
            domestic,
            webrtc,
            dns,
        });
        if s.url_changed && !s.url_merged {
            self.queue
                .extend([Effect::ClearNodeState, Effect::RemoveCachedNodes]);
            if s.store_available {
                self.queue
                    .extend([Effect::ClearHealth, Effect::ClearRegions]);
            }
            self.queue.extend([
                Effect::SaveSettings { final_save: false },
                Effect::SetProfileFromCache(false),
            ]);
            if s.cache_found {
                self.queue.push_back(Effect::UseCachedProfile);
            } else {
                self.remove_profile();
                self.finish();
            }
        } else {
            self.finish();
        }
    }
    fn remove_profile(&mut self) {
        for suffix in 0..3 {
            self.queue.push_back(Effect::RemoveProfile { suffix });
        }
    }
    fn finish(&mut self) {
        if self.snapshot.ensure_needed {
            self.queue
                .extend([Effect::AppendSubscription, Effect::SaveSubscriptions]);
        }
        self.queue
            .push_back(Effect::SaveSettings { final_save: true });
    }
    pub fn next(&self) -> Effect {
        self.queue.front().cloned().unwrap_or(Effect::Done)
    }
    pub fn advance(&mut self, reply: Reply) -> Result<(), &'static str> {
        let effect = self.queue.front().cloned().ok_or("flow complete")?;
        let needs_bool = matches!(
            effect,
            Effect::KernelRunning { .. } | Effect::SaveSettings { .. } | Effect::UseCachedProfile
        );
        if needs_bool != matches!(reply, Reply::Bool(_)) {
            return Err("wrong reply");
        }
        self.queue.pop_front();
        match (effect, reply) {
            (Effect::KernelRunning { import_policy }, Reply::Bool(running)) => {
                if running {
                    self.result(if import_policy { 1 } else { 9 });
                } else if import_policy {
                    self.validate();
                } else {
                    self.commit();
                }
            }
            (Effect::UseCachedProfile, Reply::Bool(ok)) => {
                if ok {
                    self.queue.push_back(Effect::SetProfileFromCache(true));
                } else {
                    self.remove_profile();
                }
                self.finish();
            }
            (Effect::SaveSettings { final_save: true }, Reply::Bool(ok)) => {
                self.result(if ok { 0 } else { 10 })
            }
            _ => {}
        }
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn run(s: Snapshot, mut reply: impl FnMut(&Effect) -> Reply) -> Vec<Effect> {
        let mut f = Flow::new(s).unwrap();
        let mut trace = vec![];
        for _ in 0..40 {
            let e = f.next();
            if e == Effect::Done {
                return trace;
            }
            let r = reply(&e);
            trace.push(e);
            f.advance(r).unwrap();
        }
        panic!("nonterminating flow");
    }
    fn ok(e: &Effect) -> Reply {
        match e {
            Effect::KernelRunning { .. } => Reply::Bool(false),
            Effect::SaveSettings { .. } | Effect::UseCachedProfile => Reply::Bool(true),
            _ => Reply::Ack,
        }
    }
    #[test]
    fn all_validation_priorities() {
        for (bit, code) in [(1, 2), (2, 4), (7, 5), (8, 6), (9, 7), (10, 8)] {
            let s = Snapshot {
                presence: 0x7ff,
                invalid: 0x7ff & !((1 << bit) - 1),
                ..Snapshot::default()
            };
            let t = run(s, ok);
            assert_eq!(
                t,
                vec![
                    Effect::KernelRunning {
                        import_policy: true
                    },
                    Effect::Result { code }
                ]
            );
        }
        let s = Snapshot {
            presence: 0x7ff,
            invalid: 0x7ff,
            ..Snapshot::default()
        };
        assert_eq!(
            run(s, |e| if matches!(e, Effect::KernelRunning { .. }) {
                Reply::Bool(true)
            } else {
                ok(e)
            }),
            vec![
                Effect::KernelRunning {
                    import_policy: true
                },
                Effect::Result { code: 1 }
            ]
        );
    }
    #[test]
    fn socks_conflict_after_range_before_game() {
        for (invalid, code) in [(6, 2), (4, 3)] {
            let s = Snapshot {
                presence: 6,
                invalid,
                socks_conflict: true,
                ..Snapshot::default()
            };
            assert_eq!(run(s, ok), vec![Effect::Result { code }]);
        }
    }
    #[test]
    fn absent_invalid_facts_are_ignored() {
        let s = Snapshot {
            invalid: 0x7ff,
            socks_conflict: true,
            ..Snapshot::default()
        };
        assert!(run(s, ok).contains(&Effect::Result { code: 0 }));
    }
    #[test]
    fn running_queries_are_distinct_and_before_commit() {
        let mut calls = 0;
        let t = run(
            Snapshot {
                presence: 1,
                ..Snapshot::default()
            },
            |e| {
                if matches!(e, Effect::KernelRunning { .. }) {
                    calls += 1;
                    Reply::Bool(calls == 2)
                } else {
                    ok(e)
                }
            },
        );
        assert_eq!(
            t,
            vec![
                Effect::KernelRunning {
                    import_policy: true
                },
                Effect::KernelRunning {
                    import_policy: false
                },
                Effect::Result { code: 9 }
            ]
        );
    }
    #[test]
    fn changed_url_cache_success_complete_order() {
        let s = Snapshot {
            presence: 1 << 9,
            url_changed: true,
            store_available: true,
            cache_found: true,
            ensure_needed: true,
            ..Snapshot::default()
        };
        let t = run(s, ok);
        assert_eq!(
            t,
            vec![
                Effect::KernelRunning {
                    import_policy: false
                },
                Effect::ApplySettings {
                    presence: 1 << 9,
                    domestic: false,
                    webrtc: false,
                    dns: false
                },
                Effect::ClearNodeState,
                Effect::RemoveCachedNodes,
                Effect::ClearHealth,
                Effect::ClearRegions,
                Effect::SaveSettings { final_save: false },
                Effect::SetProfileFromCache(false),
                Effect::UseCachedProfile,
                Effect::SetProfileFromCache(true),
                Effect::AppendSubscription,
                Effect::SaveSubscriptions,
                Effect::SaveSettings { final_save: true },
                Effect::Result { code: 0 }
            ]
        );
    }
    #[test]
    fn cache_missing_or_failed_removes_three_files() {
        for found in [false, true] {
            let s = Snapshot {
                url_changed: true,
                cache_found: found,
                ..Snapshot::default()
            };
            let t = run(s, |e| {
                if *e == Effect::UseCachedProfile {
                    Reply::Bool(false)
                } else {
                    ok(e)
                }
            });
            let removals: Vec<_> = t
                .iter()
                .filter(|e| matches!(e, Effect::RemoveProfile { .. }))
                .cloned()
                .collect();
            assert_eq!(
                removals,
                vec![
                    Effect::RemoveProfile { suffix: 0 },
                    Effect::RemoveProfile { suffix: 1 },
                    Effect::RemoveProfile { suffix: 2 }
                ]
            );
            assert!(!t.contains(&Effect::SetProfileFromCache(true)));
            assert!(!t.contains(&Effect::ClearHealth));
        }
    }
    #[test]
    fn unchanged_or_merged_skips_clear_cache() {
        for (changed, merged) in [(false, false), (true, true)] {
            let s = Snapshot {
                url_changed: changed,
                url_merged: merged,
                store_available: true,
                cache_found: true,
                ..Snapshot::default()
            };
            let t = run(s, ok);
            assert!(!t.contains(&Effect::ClearNodeState));
            assert!(!t.contains(&Effect::UseCachedProfile));
            assert_eq!(t.len(), 4);
        }
    }
    #[test]
    fn ignored_early_save_failure_and_terminal_failure_no_rollback() {
        let t = run(
            Snapshot {
                url_changed: true,
                ensure_needed: true,
                ..Snapshot::default()
            },
            |e| {
                if matches!(e, Effect::SaveSettings { .. }) {
                    Reply::Bool(false)
                } else {
                    ok(e)
                }
            },
        );
        assert_eq!(t.last(), Some(&Effect::Result { code: 10 }));
        assert!(t.contains(&Effect::AppendSubscription));
        assert_eq!(
            t.iter()
                .filter(|e| matches!(e, Effect::ApplySettings { .. }))
                .count(),
            1
        );
    }
    #[test]
    fn privacy_presence_and_override_exhaustive() {
        for initial in 0..8 {
            for present in 0..16 {
                for values in 0..16 {
                    let mut s = Snapshot {
                        domestic: initial & 1 != 0,
                        webrtc: initial & 2 != 0,
                        dns: initial & 4 != 0,
                        strict_value: values & 1 != 0,
                        domestic_value: values & 2 != 0,
                        webrtc_value: values & 4 != 0,
                        dns_value: values & 8 != 0,
                        ..Snapshot::default()
                    };
                    for (bit, field) in [(0, 3), (1, 4), (2, 5), (3, 6)] {
                        if present & (1 << bit) != 0 {
                            s.presence |= 1 << field;
                        }
                    }
                    let (mut d, mut w, mut n) = (s.domestic, s.webrtc, s.dns);
                    if present & 1 != 0 {
                        w = s.strict_value;
                        n = s.strict_value;
                    }
                    if present & 2 != 0 {
                        if s.domestic_value && !d {
                            w = true;
                            n = true;
                        }
                        d = s.domestic_value;
                    }
                    if present & 4 != 0 {
                        w = s.webrtc_value;
                    }
                    if present & 8 != 0 {
                        n = s.dns_value;
                    }
                    let t = run(s.clone(), ok);
                    assert_eq!(
                        t[1],
                        Effect::ApplySettings {
                            presence: s.presence,
                            domestic: d,
                            webrtc: w,
                            dns: n
                        }
                    );
                }
            }
        }
    }
    #[test]
    fn invalid_reply_does_not_consume_and_done_rejects() {
        let mut f = Flow::new(Snapshot::default()).unwrap();
        let e = f.next();
        assert!(f.advance(Reply::Ack).is_err());
        assert_eq!(f.next(), e);
        while f.next() != Effect::Done {
            let e = f.next();
            f.advance(ok(&e)).unwrap();
        }
        assert!(f.advance(Reply::Ack).is_err());
        assert!(Flow::new(Snapshot {
            presence: 0x800,
            ..Snapshot::default()
        })
        .is_err());
    }
}
