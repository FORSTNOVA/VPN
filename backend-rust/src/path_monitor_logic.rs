//! Pure path-monitor normalization and connection-difference decisions.
//! URL equality uses Go-arena intern IDs; raw URLs, credentials and parsers stay in Go.
use std::collections::{BTreeMap, BTreeSet};
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Target {
    pub url: i64,
    pub name: Vec<u8>,
    pub enabled: bool,
    pub builtin: bool,
}
#[derive(Clone, Debug, Default)]
pub struct TargetInput {
    pub url: i64,
    pub nonempty: bool,
    pub label: Vec<u8>,
    pub host: Vec<u8>,
    pub valid: bool,
    pub runes: i64,
    pub enabled: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Settings {
    pub enabled: bool,
    pub interval: i64,
    pub targets: Vec<Target>,
}
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct NormalizeError {
    pub kind: u8,
}
#[derive(Clone, Debug)]
pub struct Normalizer {
    result: Settings,
    presets: Vec<Target>,
    seen: BTreeSet<i64>,
    limit: i64,
}
impl Normalizer {
    pub fn new(enabled: bool, interval: i64, presets: Vec<Target>) -> Self {
        let interval = if interval == 0 {
            120
        } else {
            interval.clamp(30, 3600)
        };
        let limit = 20i64.saturating_sub(i64::try_from(presets.len()).unwrap_or(i64::MAX));
        Self {
            result: Settings {
                enabled,
                interval,
                targets: vec![],
            },
            presets,
            seen: BTreeSet::new(),
            limit,
        }
    }
    /// Feed exactly one Go URL-fact reply at a time, preserving validation and call priority.
    /// `runes` is Go's rune count of the effective label (trimmed label or hostname).
    pub fn push(&mut self, t: TargetInput) -> Result<(), NormalizeError> {
        if !t.nonempty {
            return Ok(());
        }
        if !t.valid {
            return Err(NormalizeError { kind: 1 });
        }
        if self.seen.contains(&t.url) {
            return Ok(());
        }
        self.seen.insert(t.url);
        let name = if t.label.is_empty() { t.host } else { t.label };
        if t.runes > 40 {
            return Err(NormalizeError { kind: 2 });
        }
        if i64::try_from(self.result.targets.len()).unwrap_or(i64::MAX) >= self.limit {
            return Err(NormalizeError { kind: 3 });
        }
        let builtin = self.presets.iter().any(|p| p.url == t.url);
        self.result.targets.push(Target {
            url: t.url,
            name,
            enabled: t.enabled,
            builtin,
        });
        Ok(())
    }
    pub fn finish(mut self) -> Settings {
        for p in self.presets {
            if !self.seen.contains(&p.url) {
                self.result.targets.push(p)
            }
        }
        self.result
    }
}
#[derive(Clone, Debug, Default)]
pub struct Connection {
    pub id: Vec<u8>,
    pub start: Vec<u8>,
    pub host: Vec<u8>,
    pub port: Vec<u8>,
    pub process: Vec<u8>,
    pub upload: i64,
}
#[derive(Clone, Debug, Default)]
pub struct DemandTarget {
    pub label: Vec<u8>,
    pub host: Vec<u8>,
    pub valid: bool,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Sample {
    pub id: Vec<u8>,
    pub upload: i64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct DemandResult {
    pub current: Vec<Sample>,
    pub labels: Vec<Vec<u8>>,
}
pub fn host_matches(host: &[u8], target: &[u8]) -> bool {
    !target.is_empty()
        && (host == target
            || (host.len() > target.len()
                && host.ends_with(target)
                && host[host.len() - target.len() - 1] == b'.'))
}
/// Hosts and process names are Go-normalized facts. Prior samples are immutable
/// throughout this pass; duplicate connection IDs compare against the prior round,
/// not an earlier row in this round. Current samples keep the last matching ID.
pub fn demand(
    previous: &[Sample],
    targets: &[DemandTarget],
    connections: &[Connection],
) -> DemandResult {
    let prior: BTreeMap<_, _> = previous
        .iter()
        .map(|s| (s.id.as_slice(), s.upload))
        .collect();
    let mut current = BTreeMap::new();
    let mut labels = BTreeSet::new();
    for c in connections {
        if c.process.windows(8).any(|part| part == b"smartvpn") || c.host.is_empty() {
            continue;
        }
        let id = if !c.id.is_empty() {
            c.id.clone()
        } else {
            let mut id = c.start.clone();
            id.push(b'|');
            id.extend_from_slice(&c.host);
            id.push(b'|');
            id.extend_from_slice(&c.port);
            id
        };
        current.insert(id.clone(), c.upload);
        if prior
            .get(id.as_slice())
            .is_some_and(|&upload| c.upload <= upload)
        {
            continue;
        }
        for t in targets {
            if t.valid && host_matches(&c.host, &t.host) {
                labels.insert(t.label.clone());
            }
        }
    }
    DemandResult {
        current: current
            .into_iter()
            .map(|(id, upload)| Sample { id, upload })
            .collect(),
        labels: labels.into_iter().collect(),
    }
}
/// Shared wire-facing name used by the orchestration module.
pub type Input = TargetInput;
/// Alias for callers naming the pure evaluation step explicitly.
pub fn evaluate(
    previous: &[Sample],
    targets: &[DemandTarget],
    connections: &[Connection],
) -> DemandResult {
    demand(previous, targets, connections)
}

#[cfg(test)]
mod tests {
    use super::*;
    fn preset(id: i64) -> Target {
        Target {
            url: id,
            name: format!("preset{id}").into_bytes(),
            builtin: true,
            enabled: id < 2,
        }
    }
    fn input(id: i64) -> TargetInput {
        TargetInput {
            url: id,
            nonempty: true,
            label: format!("label{id}").into_bytes(),
            host: b"host".to_vec(),
            valid: true,
            runes: 6,
            enabled: true,
        }
    }
    #[test]
    fn interval_zero_default_and_clamps() {
        for (i, want) in [
            (0, 120),
            (-1, 30),
            (1, 30),
            (30, 30),
            (3600, 3600),
            (3601, 3600),
            (i64::MAX, 3600),
            (i64::MIN, 30),
        ] {
            assert_eq!(Normalizer::new(true, i, vec![]).finish().interval, want)
        }
    }
    #[test]
    fn blank_skips_validation_and_duplicate_still_validates_first() {
        let mut n = Normalizer::new(false, 0, vec![]);
        n.push(TargetInput::default()).unwrap();
        n.push(input(1)).unwrap();
        let mut t = input(1);
        t.valid = false;
        assert_eq!(n.push(t).unwrap_err().kind, 1);
    }
    #[test]
    fn duplicate_skips_label_and_limit_checks() {
        let mut n = Normalizer::new(true, 120, (0..20).map(preset).collect());
        assert_eq!(n.push(input(100)).unwrap_err().kind, 3);
        let mut n = Normalizer::new(true, 120, vec![]);
        n.push(input(1)).unwrap();
        let mut t = input(1);
        t.runes = 100;
        n.push(t).unwrap();
        assert_eq!(n.finish().targets.len(), 1)
    }
    #[test]
    fn label_fallback_exact_bytes_and_rune_boundary() {
        let mut n = Normalizer::new(true, 120, vec![]);
        let mut t = input(1);
        t.label.clear();
        t.host = b"Exact.Host".to_vec();
        t.runes = 40;
        n.push(t).unwrap();
        assert_eq!(n.finish().targets[0].name, b"Exact.Host");
        let mut n = Normalizer::new(true, 120, vec![]);
        let mut t = input(1);
        t.runes = 41;
        assert_eq!(n.push(t).unwrap_err().kind, 2)
    }
    #[test]
    fn input_order_builtin_override_and_missing_presets() {
        let mut n = Normalizer::new(true, 120, vec![preset(0), preset(1), preset(2)]);
        n.push(input(100)).unwrap();
        let mut custom = input(1);
        custom.enabled = false;
        custom.label = b"custom".to_vec();
        n.push(custom).unwrap();
        n.push(input(100)).unwrap();
        let s = n.finish();
        assert_eq!(
            s.targets.iter().map(|t| t.url).collect::<Vec<_>>(),
            [100, 1, 0, 2]
        );
        assert!(!s.targets[0].builtin);
        assert!(s.targets[1].builtin);
        assert!(!s.targets[1].enabled);
        assert_eq!(s.targets[1].name, b"custom")
    }
    #[test]
    fn quota_counts_supplied_presets_too() {
        let mut n = Normalizer::new(true, 120, (0..6).map(preset).collect());
        for i in 0..14 {
            n.push(input(i)).unwrap()
        }
        assert_eq!(n.push(input(14)).unwrap_err().kind, 3);
    }
    #[test]
    fn validation_priority_label_before_quota() {
        let mut n = Normalizer::new(true, 120, (0..20).map(preset).collect());
        let mut t = input(99);
        t.runes = 41;
        assert_eq!(n.push(t).unwrap_err().kind, 2)
    }
    #[test]
    fn raw_url_never_needed_for_equality() {
        let mut n = Normalizer::new(true, 120, vec![preset(42)]);
        n.push(input(42)).unwrap();
        assert_eq!(n.finish().targets.len(), 1);
    }
    fn target(label: &str, host: &str) -> DemandTarget {
        DemandTarget {
            label: label.as_bytes().to_vec(),
            host: host.as_bytes().to_vec(),
            valid: true,
        }
    }
    fn connection(id: &str, host: &str, up: i64) -> Connection {
        Connection {
            id: id.as_bytes().to_vec(),
            host: host.as_bytes().to_vec(),
            upload: up,
            ..Connection::default()
        }
    }
    #[test]
    fn exact_or_subdomain_only() {
        assert!(host_matches(b"a.example.com", b"example.com"));
        assert!(host_matches(b"example.com", b"example.com"));
        assert!(!host_matches(b"notexample.com", b"example.com"));
        assert!(!host_matches(b"example.com.evil", b"example.com"));
        assert!(!host_matches(b"anything", b""));
    }
    #[test]
    fn unchanged_or_decreased_upload_no_demand_but_kept() {
        let prev = [Sample {
            id: b"x".to_vec(),
            upload: 10,
        }];
        for up in [10, 9, -1] {
            let r = demand(
                &prev,
                &[target("L", "site")],
                &[connection("x", "site", up)],
            );
            assert!(r.labels.is_empty());
            assert_eq!(r.current[0].upload, up)
        }
        let r = demand(
            &prev,
            &[target("L", "site")],
            &[connection("x", "site", 11)],
        );
        assert_eq!(r.labels, [b"L".to_vec()]);
    }
    #[test]
    fn unseen_zero_and_negative_are_demand() {
        for up in [0, -1, i64::MIN] {
            assert_eq!(
                demand(
                    &[],
                    &[target("L", "site")],
                    &[connection("new", "site", up)]
                )
                .labels,
                [b"L".to_vec()]
            );
        }
    }
    #[test]
    fn smartvpn_and_blank_hosts_removed_from_current() {
        let mut selfc = connection("self", "site", 100);
        selfc.process = b"my-smartvpn-helper".to_vec();
        let r = demand(
            &[Sample {
                id: b"old".to_vec(),
                upload: 7,
            }],
            &[target("L", "site")],
            &[selfc, connection("blank", "", 1)],
        );
        assert!(r.current.is_empty());
        assert!(r.labels.is_empty());
    }
    #[test]
    fn generated_id_and_sorted_unique_labels() {
        let mut c = connection("", "a.site", 1);
        c.start = b"start".to_vec();
        c.port = b"443".to_vec();
        let r = demand(
            &[],
            &[
                target("z", "site"),
                target("a", "a.site"),
                target("z", "a.site"),
            ],
            &[c],
        );
        assert_eq!(r.current[0].id, b"start|a.site|443");
        assert_eq!(r.labels, [b"a".to_vec(), b"z".to_vec()]);
    }
    #[test]
    fn duplicate_ids_compare_old_snapshot_and_current_last_wins() {
        let r = demand(
            &[Sample {
                id: b"x".to_vec(),
                upload: 10,
            }],
            &[target("A", "one"), target("B", "two")],
            &[
                connection("x", "one", 11),
                connection("x", "two", 12),
                connection("x", "none", 1),
            ],
        );
        assert_eq!(r.labels, [b"A".to_vec(), b"B".to_vec()]);
        assert_eq!(
            r.current,
            vec![Sample {
                id: b"x".to_vec(),
                upload: 1
            }]
        );
    }
    #[test]
    fn invalid_target_and_empty_target_skipped_but_samples_retained() {
        let mut t = target("bad", "site");
        t.valid = false;
        let r = demand(
            &[],
            &[t, target("empty", "")],
            &[connection("x", "site", 1)],
        );
        assert!(r.labels.is_empty());
        assert_eq!(r.current.len(), 1)
    }
    #[test]
    fn byte_order_and_non_utf8_ids_preserved() {
        let mut c = connection("x", "site", 1);
        c.id = vec![255];
        let mut t = target("L", "site");
        t.label = vec![255];
        let r = demand(
            &[],
            &[t, target("A", "site")],
            &[c, connection("a", "other", 1)],
        );
        assert_eq!(r.labels, [b"A".to_vec(), vec![255]]);
        assert_eq!(r.current[1].id, [255]);
    }
}
