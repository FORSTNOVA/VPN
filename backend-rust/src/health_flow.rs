//! Deterministic health-service continuation. No clocks, IO, strings or global state.
//! Node indices are the caller's bytewise-name-sorted table. Time comparisons are
//! supplied by Go to preserve its monotonic/zero-time semantics.
use std::collections::VecDeque;
#[derive(Clone, Debug, Default)]
pub struct Node {
    pub region_match: bool,
    pub verified: bool,
    pub region_fresh: bool,
    pub cooldown_active: bool,
    pub state: i64,
    pub failures: i64,
    pub successes: i64,
    pub latency: i64,
}
#[derive(Clone, Debug)]
pub struct Snapshot {
    pub nodes: Vec<Node>,
    pub active: Option<usize>,
    pub chain: bool,
    pub has_region: bool,
    pub dwell_elapsed: bool,
    pub blocked: bool,
    pub failure_threshold: i64,
    pub recovery_successes: i64,
}
#[derive(Clone, Debug)]
pub enum Mode {
    Patrol { delays: Vec<i64>, failed: bool },
    Failover,
    Block,
    Unblock,
}
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Effect {
    ClearNote,
    RecordHealth {
        node: usize,
        values: [i64; 4],
        success: bool,
        cooldown: bool,
    },
    MeasureGroup,
    ConfirmTraffic,
    Switch {
        target: Option<usize>,
    },
    SaveSettings,
    Commit {
        kind: u8,
        target: Option<usize>,
    },
    AppendEvent {
        kind: u8,
        from: Option<usize>,
        target: Option<usize>,
        delay: i64,
    },
    NoteAnomaly,
    Done,
}
#[derive(Clone, Debug)]
pub enum Reply {
    Ack,
    Bool(bool),
    Delays { ok: bool, values: Vec<i64> },
}
#[derive(Clone, Debug)]
enum Task {
    Emit(Effect),
    AfterPatrol,
    Pick(bool),
    Block(u8),
    Measure {
        nodes: Vec<usize>,
        recover: bool,
    },
    Switch {
        target: Option<usize>,
        delay: i64,
        kind: u8,
    },
    Confirm(u8),
}
#[derive(Clone, Debug)]
pub struct Flow {
    pub snapshot: Snapshot,
    tasks: VecDeque<Task>,
}
impl Flow {
    pub fn new(snapshot: Snapshot, mode: Mode) -> Result<Self, &'static str> {
        if snapshot.nodes.len() > 100_000
            || snapshot.active.is_some_and(|i| i >= snapshot.nodes.len())
            || snapshot.nodes.iter().any(|n| !(0..=5).contains(&n.state))
        {
            return Err("invalid snapshot");
        }
        let mut f = Self {
            snapshot,
            tasks: VecDeque::new(),
        };
        match mode {
            Mode::Patrol { delays, failed } => {
                if !failed && delays.len() != f.snapshot.nodes.len() {
                    return Err("invalid delay count");
                }
                let mut watched = Vec::new();
                if let Some(i) = f.snapshot.active {
                    watched.push(i);
                }
                if !failed && !f.snapshot.chain {
                    for i in f.pool() {
                        if Some(i) != f.snapshot.active {
                            watched.push(i);
                        }
                    }
                }
                if !failed && watched.iter().any(|&i| delays[i] > 0) {
                    f.emit(Effect::ClearNote);
                }
                for i in watched {
                    let delay = if failed { 0 } else { delays[i] };
                    let n = &f.snapshot.nodes[i];
                    let prior = match n.state {
                        4 => 1,
                        5 => 2,
                        _ => 0,
                    };
                    let v = crate::health_core::transition([
                        prior,
                        n.failures,
                        n.successes,
                        delay,
                        (delay > 0) as i64,
                        n.cooldown_active as i64,
                        f.snapshot.failure_threshold,
                        f.snapshot.recovery_successes,
                    ])
                    .ok_or("invalid transition")?;
                    let n = &mut f.snapshot.nodes[i];
                    n.state = v[0];
                    n.failures = v[1];
                    n.successes = v[2];
                    n.latency = v[3];
                    f.emit(Effect::RecordHealth {
                        node: i,
                        values: v,
                        success: delay > 0,
                        cooldown: false,
                    });
                }
                f.tasks.push_back(Task::AfterPatrol);
            }
            Mode::Failover => f.tasks.push_back(Task::Pick(false)),
            Mode::Block => f.tasks.push_back(Task::Block(2)),
            Mode::Unblock => f.tasks.push_back(Task::Pick(true)),
        }
        f.pump();
        Ok(f)
    }
    fn emit(&mut self, e: Effect) {
        self.tasks.push_back(Task::Emit(e));
    }
    fn pool(&self) -> Vec<usize> {
        if !self.snapshot.has_region {
            return vec![];
        }
        self.snapshot
            .nodes
            .iter()
            .enumerate()
            .filter_map(|(i, n)| {
                (n.region_match && n.verified && n.region_fresh && !n.cooldown_active).then_some(i)
            })
            .collect()
    }
    fn best(&self, nodes: &[usize], delays: Option<&[i64]>) -> Option<(usize, i64)> {
        nodes
            .iter()
            .filter_map(|&i| {
                let d = delays.map_or(self.snapshot.nodes[i].latency, |ds| ds[i]);
                (d > 0).then_some((i, d))
            })
            .min_by_key(|&(i, d)| (d, i))
    }
    fn target(&mut self, target: Option<(usize, i64)>, recover: bool) {
        if let Some((i, d)) = target {
            self.tasks.push_front(Task::Switch {
                target: Some(i),
                delay: d,
                kind: if recover { 3 } else { 1 },
            });
        } else if !recover {
            self.tasks.push_front(Task::Block(2));
        }
    }
    fn pump(&mut self) {
        loop {
            match self.tasks.front().cloned() {
                Some(Task::AfterPatrol) => {
                    self.tasks.pop_front();
                    if !self.snapshot.chain
                        && self.snapshot.has_region
                        && self.snapshot.dwell_elapsed
                        && self
                            .snapshot
                            .active
                            .is_some_and(|i| self.snapshot.nodes[i].state == 3)
                    {
                        self.tasks.push_front(Task::Pick(false));
                    }
                }
                Some(Task::Pick(recover)) => {
                    self.tasks.pop_front();
                    let pool = self.pool();
                    if pool.is_empty() {
                        self.target(None, recover);
                        continue;
                    }
                    let nodes: Vec<_> = pool
                        .into_iter()
                        .filter(|&i| self.snapshot.nodes[i].state != 3)
                        .collect();
                    if let Some(best) = self.best(&nodes, None) {
                        self.target(Some(best), recover);
                    } else {
                        self.tasks.push_front(Task::Measure { nodes, recover });
                    }
                }
                Some(Task::Block(kind)) => {
                    self.tasks.pop_front();
                    self.tasks.push_front(Task::Confirm(kind));
                }
                _ => break,
            }
        }
    }
    pub fn next(&self) -> Effect {
        match self.tasks.front() {
            Some(Task::Emit(e)) => e.clone(),
            Some(Task::Measure { .. }) => Effect::MeasureGroup,
            Some(Task::Switch { target, .. }) => Effect::Switch { target: *target },
            Some(Task::Confirm(_)) => Effect::ConfirmTraffic,
            None => Effect::Done,
            _ => unreachable!("pump invariant"),
        }
    }
    pub fn advance(&mut self, reply: Reply) -> Result<(), &'static str> {
        let task = self.tasks.front().cloned().ok_or("flow complete")?;
        // Validate before consuming a task: malformed replies cannot lose a pending effect.
        let valid = match (&task, &reply) {
            (Task::Emit(_), Reply::Ack)
            | (Task::Switch { .. }, Reply::Bool(_))
            | (Task::Confirm(_), Reply::Bool(_)) => true,
            (Task::Measure { .. }, Reply::Delays { ok, values }) => {
                !*ok || values.len() == self.snapshot.nodes.len()
            }
            _ => false,
        };
        if !valid {
            return Err("wrong reply");
        }
        self.tasks.pop_front();
        match (task, reply) {
            (Task::Emit(_), Reply::Ack) => {}
            (Task::Measure { nodes, recover }, Reply::Delays { ok, values }) => {
                let target = if ok {
                    self.best(&nodes, Some(&values))
                } else {
                    None
                };
                self.target(target, recover);
            }
            (Task::Confirm(kind), Reply::Bool(pass)) => {
                if pass {
                    self.emit(Effect::NoteAnomaly);
                } else {
                    self.tasks.push_front(Task::Switch {
                        target: None,
                        delay: 0,
                        kind,
                    });
                }
            }
            (
                Task::Switch {
                    target,
                    delay,
                    kind,
                },
                Reply::Bool(ok),
            ) => {
                if !ok {
                    if kind == 1 {
                        self.tasks.push_front(Task::Block(4));
                    }
                } else {
                    if kind == 1 {
                        if let Some(i) = self.snapshot.active {
                            let n = &mut self.snapshot.nodes[i];
                            n.state = 4;
                            n.successes = 0;
                            n.cooldown_active = true;
                            let values = [n.state, n.failures, n.successes, n.latency];
                            self.emit(Effect::RecordHealth {
                                node: i,
                                values,
                                success: false,
                                cooldown: true,
                            });
                        }
                        self.emit(Effect::SaveSettings);
                    }
                    self.snapshot.blocked = kind == 2 || kind == 4;
                    self.emit(Effect::Commit { kind, target });
                    if kind == 3 {
                        self.emit(Effect::SaveSettings);
                    }
                    self.emit(Effect::AppendEvent {
                        kind,
                        from: if kind == 3 {
                            None
                        } else {
                            self.snapshot.active
                        },
                        target,
                        delay,
                    });
                }
            }
            _ => unreachable!("validated reply"),
        }
        self.pump();
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn node() -> Node {
        Node {
            region_match: true,
            verified: true,
            region_fresh: true,
            state: 1,
            latency: 50,
            ..Node::default()
        }
    }
    fn snap() -> Snapshot {
        Snapshot {
            nodes: vec![node(), node(), node()],
            active: Some(0),
            chain: false,
            has_region: true,
            dwell_elapsed: true,
            blocked: false,
            failure_threshold: 2,
            recovery_successes: 3,
        }
    }
    fn run(f: &mut Flow, mut respond: impl FnMut(&Effect) -> Reply) -> Vec<Effect> {
        let mut trace = vec![];
        for _ in 0..100 {
            let e = f.next();
            if e == Effect::Done {
                return trace;
            }
            let r = respond(&e);
            trace.push(e);
            f.advance(r).unwrap();
        }
        panic!("nonterminating flow")
    }
    fn successful(e: &Effect) -> Reply {
        match e {
            Effect::Switch { .. } => Reply::Bool(true),
            Effect::ConfirmTraffic => Reply::Bool(false),
            Effect::MeasureGroup => Reply::Delays {
                ok: false,
                values: vec![],
            },
            _ => Reply::Ack,
        }
    }
    #[test]
    fn patrol_failover_order_and_duplicate_active() {
        let mut s = snap();
        s.nodes[0].failures = 1;
        let mut f = Flow::new(
            s,
            Mode::Patrol {
                delays: vec![0, 30, 20],
                failed: false,
            },
        )
        .unwrap();
        let t = run(&mut f, successful);
        assert_eq!(t[0], Effect::ClearNote);
        assert_eq!(
            t.iter()
                .filter(|e| matches!(e, Effect::RecordHealth { node: 0, .. }))
                .count(),
            2
        );
        assert_eq!(t[4], Effect::Switch { target: Some(2) });
        assert!(matches!(t[5], Effect::RecordHealth { cooldown: true, .. }));
        assert_eq!(t[6], Effect::SaveSettings);
        assert_eq!(
            t[7],
            Effect::Commit {
                kind: 1,
                target: Some(2)
            }
        );
        assert_eq!(
            t[8],
            Effect::AppendEvent {
                kind: 1,
                from: Some(0),
                target: Some(2),
                delay: 20
            }
        );
    }
    #[test]
    fn failed_measurement_preserves_candidates() {
        let mut s = snap();
        s.nodes[0].failures = 1;
        let mut f = Flow::new(
            s,
            Mode::Patrol {
                delays: vec![],
                failed: true,
            },
        )
        .unwrap();
        let t = run(&mut f, successful);
        assert!(!t.contains(&Effect::ClearNote));
        assert!(!t.contains(&Effect::MeasureGroup));
        assert_eq!(t[1], Effect::Switch { target: Some(1) });
        assert_eq!(f.snapshot.nodes[1].successes, 0);
    }
    #[test]
    fn chain_and_dwell_guards() {
        for flag in 0..3 {
            let mut s = snap();
            s.nodes[0].failures = 1;
            match flag {
                0 => s.chain = true,
                1 => s.dwell_elapsed = false,
                _ => s.has_region = false,
            };
            let mut f = Flow::new(
                s,
                Mode::Patrol {
                    delays: vec![0, 40, 20],
                    failed: false,
                },
            )
            .unwrap();
            let t = run(&mut f, successful);
            assert!(!t.iter().any(|e| matches!(
                e,
                Effect::Switch { .. } | Effect::MeasureGroup | Effect::ConfirmTraffic
            )));
            if flag == 0 {
                assert_eq!(t.len(), 1);
            }
        }
    }
    #[test]
    fn bulk_death_but_real_traffic_preserved() {
        let mut s = snap();
        for n in &mut s.nodes {
            n.failures = 1;
        }
        let mut f = Flow::new(
            s,
            Mode::Patrol {
                delays: vec![0; 3],
                failed: false,
            },
        )
        .unwrap();
        let t = run(&mut f, |e| {
            if *e == Effect::ConfirmTraffic {
                Reply::Bool(true)
            } else {
                successful(e)
            }
        });
        assert_eq!(
            &t[3..],
            &[
                Effect::MeasureGroup,
                Effect::ConfirmTraffic,
                Effect::NoteAnomaly
            ]
        );
        assert!(!f.snapshot.blocked);
    }
    #[test]
    fn exhaustion_never_crosses_region() {
        let mut s = snap();
        s.nodes[0].state = 3;
        s.nodes[1].region_match = false;
        s.nodes[2].region_fresh = false;
        let mut f = Flow::new(s, Mode::Failover).unwrap();
        let t = run(&mut f, successful);
        assert_eq!(
            t,
            vec![
                Effect::MeasureGroup,
                Effect::ConfirmTraffic,
                Effect::Switch { target: None },
                Effect::Commit {
                    kind: 2,
                    target: None
                },
                Effect::AppendEvent {
                    kind: 2,
                    from: Some(0),
                    target: None,
                    delay: 0
                }
            ]
        );
        assert!(f.snapshot.blocked);
    }
    #[test]
    fn no_pool_skips_extra_measurement() {
        let mut s = snap();
        for n in &mut s.nodes {
            n.verified = false;
        }
        let f = Flow::new(s, Mode::Failover).unwrap();
        assert_eq!(f.next(), Effect::ConfirmTraffic);
        let mut f = Flow::new(f.snapshot, Mode::Unblock).unwrap();
        assert!(run(&mut f, successful).is_empty());
    }
    #[test]
    fn fresh_probe_ties_and_filtering() {
        let mut s = snap();
        for n in &mut s.nodes {
            n.latency = 0;
        }
        s.nodes[0].cooldown_active = true;
        let mut f = Flow::new(s, Mode::Unblock).unwrap();
        assert_eq!(f.next(), Effect::MeasureGroup);
        f.advance(Reply::Delays {
            ok: true,
            values: vec![1, 10, 10],
        })
        .unwrap();
        let t = run(&mut f, successful);
        assert_eq!(t[0], Effect::Switch { target: Some(1) });
        assert_eq!(
            t[1],
            Effect::Commit {
                kind: 3,
                target: Some(1)
            }
        );
        assert_eq!(t[2], Effect::SaveSettings);
        assert_eq!(
            t[3],
            Effect::AppendEvent {
                kind: 3,
                from: None,
                target: Some(1),
                delay: 10
            }
        );
    }
    #[test]
    fn failed_switch_does_not_commit_and_block_failure_stays_unchanged() {
        let mut s = snap();
        s.nodes[0].state = 3;
        let mut f = Flow::new(s, Mode::Failover).unwrap();
        let t = run(&mut f, |e| {
            if matches!(e, Effect::Switch { .. }) {
                Reply::Bool(false)
            } else {
                successful(e)
            }
        });
        assert_eq!(
            t,
            vec![
                Effect::Switch { target: Some(1) },
                Effect::ConfirmTraffic,
                Effect::Switch { target: None }
            ]
        );
        assert!(!f.snapshot.blocked);
        assert_eq!(f.snapshot.nodes[0].state, 3);
    }
    #[test]
    fn failed_target_then_reject_has_distinct_reason() {
        let mut s = snap();
        s.nodes[0].state = 3;
        let mut f = Flow::new(s, Mode::Failover).unwrap();
        let t = run(&mut f, |e| {
            if matches!(e, Effect::Switch { target: Some(_) }) {
                Reply::Bool(false)
            } else {
                successful(e)
            }
        });
        assert!(t.contains(&Effect::Commit {
            kind: 4,
            target: None
        }));
        assert!(f.snapshot.blocked);
    }
    #[test]
    fn invalid_reply_does_not_consume_and_done_is_terminal() {
        let mut f = Flow::new(snap(), Mode::Unblock).unwrap();
        let before = f.next();
        assert!(f.advance(Reply::Ack).is_err());
        assert_eq!(f.next(), before);
        run(&mut f, successful);
        assert!(f.advance(Reply::Ack).is_err());
        let mut s = snap();
        s.active = Some(3);
        assert!(Flow::new(s, Mode::Block).is_err());
        assert!(Flow::new(
            snap(),
            Mode::Patrol {
                delays: vec![],
                failed: false
            }
        )
        .is_err());
    }
    #[test]
    fn recovery_failure_has_no_persistence_or_state_mutation() {
        let mut s = snap();
        s.blocked = true;
        let mut f = Flow::new(s, Mode::Unblock).unwrap();
        let t = run(&mut f, |_| Reply::Bool(false));
        assert_eq!(t.len(), 1);
        assert!(f.snapshot.blocked);
    }
    #[test]
    fn cooldown_recovery_and_overflow() {
        let mut s = snap();
        s.chain = true;
        s.nodes[0].state = 4;
        s.nodes[0].cooldown_active = true;
        s.nodes[0].successes = i64::MAX;
        let mut f = Flow::new(
            s,
            Mode::Patrol {
                delays: vec![30, 0, 0],
                failed: false,
            },
        )
        .unwrap();
        run(&mut f, successful);
        assert_eq!(f.snapshot.nodes[0].state, 4);
        assert_eq!(f.snapshot.nodes[0].successes, i64::MIN);
    }
}
#[cfg(test)]
mod scale_tests {
    use super::*;
    #[test]
    fn full_size_sweep_is_linear_and_bounded() {
        let snapshot = Snapshot {
            nodes: vec![
                Node {
                    region_match: true,
                    verified: true,
                    region_fresh: true,
                    state: 1,
                    ..Node::default()
                };
                100_000
            ],
            active: Some(50_000),
            chain: false,
            has_region: true,
            dwell_elapsed: true,
            blocked: false,
            failure_threshold: 2,
            recovery_successes: 3,
        };
        let mut f = Flow::new(
            snapshot,
            Mode::Patrol {
                delays: vec![1; 100_000],
                failed: false,
            },
        )
        .unwrap();
        let mut seen = vec![false; 100_000];
        let mut count = 0;
        loop {
            match f.next() {
                Effect::Done => break,
                Effect::RecordHealth { node, .. } => {
                    assert!(!seen[node]);
                    seen[node] = true;
                    count += 1;
                }
                Effect::ClearNote => {}
                e => panic!("unexpected {e:?}"),
            };
            f.advance(Reply::Ack).unwrap();
        }
        assert_eq!(count, 100_000);
    }
    #[test]
    fn randomized_candidate_oracle_and_multi_sweep_progression() {
        let mut seed = 0x12345u64;
        let mut rand = || {
            seed = seed.wrapping_mul(6364136223846793005).wrapping_add(1);
            seed
        };
        for _ in 0..2000 {
            let nodes: Vec<_> = (0..12)
                .map(|_| {
                    let b = rand();
                    Node {
                        region_match: b & 1 != 0,
                        verified: b & 2 != 0,
                        region_fresh: b & 4 != 0,
                        cooldown_active: b & 8 != 0,
                        state: ((b >> 4) % 6) as i64,
                        latency: ((b >> 8) % 30) as i64,
                        ..Node::default()
                    }
                })
                .collect();
            let oracle = nodes
                .iter()
                .enumerate()
                .filter(|(_, n)| {
                    n.region_match
                        && n.verified
                        && n.region_fresh
                        && !n.cooldown_active
                        && n.state != 3
                        && n.latency > 0
                })
                .min_by_key(|(i, n)| (n.latency, *i))
                .map(|(i, _)| i);
            let s = Snapshot {
                nodes,
                active: Some(0),
                chain: false,
                has_region: true,
                dwell_elapsed: true,
                blocked: true,
                failure_threshold: 2,
                recovery_successes: 3,
            };
            let f = Flow::new(s, Mode::Unblock).unwrap();
            if let Some(i) = oracle {
                assert_eq!(f.next(), Effect::Switch { target: Some(i) });
            } else {
                assert!(matches!(f.next(), Effect::MeasureGroup | Effect::Done));
            }
        }
        let s = Snapshot {
            nodes: vec![Node {
                state: 1,
                ..Node::default()
            }],
            active: Some(0),
            chain: true,
            has_region: true,
            dwell_elapsed: true,
            blocked: false,
            failure_threshold: 2,
            recovery_successes: 3,
        };
        let mut state = s;
        for (delay, expected) in [(0, 2), (0, 3), (10, 1), (0, 2)] {
            let mut f = Flow::new(
                state,
                Mode::Patrol {
                    delays: vec![delay],
                    failed: false,
                },
            )
            .unwrap();
            while f.next() != Effect::Done {
                f.advance(Reply::Ack).unwrap();
            }
            assert_eq!(f.snapshot.nodes[0].state, expected);
            state = f.snapshot;
        }
    }
}
