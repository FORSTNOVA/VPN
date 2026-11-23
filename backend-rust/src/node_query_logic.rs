//! Node-list metadata decisions. Opaque tokens refer to Go-owned records; raw
//! proxies, network strings and credentials never enter this module.
use crate::node_query_flow::{Node, Projection, Provider, Snapshot};
use std::collections::{BTreeMap, BTreeSet};

/// Cached order and duplicates are retained. Manual names are checked only
/// against the original cached-name set, not against earlier manual entries.
pub fn offline(s: &Snapshot) -> Vec<Projection> {
    let present: BTreeSet<_> = s.cached.iter().map(|n| n.name.as_slice()).collect();
    s.cached
        .iter()
        .chain(
            s.manual
                .iter()
                .filter(|n| !present.contains(n.name.as_slice())),
        )
        .map(|n| Projection {
            node: n.token,
            detail: 0,
            detail_network: false,
            selected: n.name == s.selected,
            label: n.label.clone(),
        })
        .collect()
}

/// Parsed profile entries use last-wins map insertion. Manual entries then
/// override that map, including labels and the manual flag, without relabeling.
pub fn details(s: &Snapshot, parsed: Vec<Node>) -> BTreeMap<Vec<u8>, Node> {
    let label = if s.subscription == 2 {
        s.sources
            .iter()
            .find(|source| source.matches && !source.label.is_empty())
            .map(|source| source.label.clone())
            .unwrap_or_else(|| "\u{5f53}\u{524d}\u{8ba2}\u{9605}".as_bytes().to_vec())
    } else {
        vec![]
    };
    let mut out = BTreeMap::new();
    for mut n in parsed {
        if !n.manual && n.label.is_empty() && !label.is_empty() {
            n.label = label.clone()
        }
        out.insert(n.name.clone(), n);
    }
    for n in &s.manual {
        out.insert(n.name.clone(), n.clone());
    }
    out
}

/// Share `seen` across provider batches. Filtered entries do not reserve a name.
/// The first accepted provider record wins; only metadata comes from details.
pub fn append_provider(
    details: &BTreeMap<Vec<u8>, Node>,
    selected: &[u8],
    seen: &mut BTreeSet<Vec<u8>>,
    providers: Vec<Provider>,
    out: &mut Vec<Projection>,
) {
    for p in providers {
        if !p.valid
            || p.name.is_empty()
            || crate::subscription::is_informational_node(&String::from_utf8_lossy(&p.name))
        {
            continue;
        }
        if !seen.insert(p.name.clone()) {
            continue;
        }
        let extra = details.get(&p.name);
        out.push(Projection {
            node: p.token,
            detail: extra.map_or(0, |n| n.token),
            detail_network: extra.is_some_and(|n| n.network),
            selected: p.name == selected,
            label: extra.map_or_else(Vec::new, |n| n.label.clone()),
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::node_query_flow::Source;
    fn node(token: i64, name: &[u8]) -> Node {
        Node {
            token,
            name: name.to_vec(),
            ..Node::default()
        }
    }
    fn provider(token: i64, name: &[u8]) -> Provider {
        Provider {
            token,
            name: name.to_vec(),
            valid: true,
            ..Provider::default()
        }
    }
    fn tokens(ps: &[Projection]) -> Vec<i64> {
        ps.iter().map(|p| p.node).collect()
    }
    #[test]
    fn empty_offline_is_empty() {
        assert!(offline(&Snapshot::default()).is_empty());
    }
    #[test]
    fn offline_cached_duplicates_and_original_order() {
        let s = Snapshot {
            cached: vec![node(1, b"z"), node(2, b"a"), node(3, b"z")],
            selected: b"z".to_vec(),
            ..Snapshot::default()
        };
        let out = offline(&s);
        assert_eq!(tokens(&out), [1, 2, 3]);
        assert_eq!(
            out.iter().map(|n| n.selected).collect::<Vec<_>>(),
            [true, false, true]
        );
    }
    #[test]
    fn offline_manual_duplicates_not_deduplicated() {
        let s = Snapshot {
            cached: vec![node(1, b"same")],
            manual: vec![node(2, b"same"), node(3, b"other"), node(4, b"other")],
            ..Snapshot::default()
        };
        assert_eq!(tokens(&offline(&s)), [1, 3, 4]);
    }
    #[test]
    fn offline_no_informational_or_blank_filter() {
        let s = Snapshot {
            cached: vec![node(1, b"")],
            manual: vec![node(2, b""), node(3, b"remaining traffic")],
            ..Snapshot::default()
        };
        assert_eq!(tokens(&offline(&s)), [1, 3]);
    }
    #[test]
    fn offline_preserves_label_and_never_uses_details_projection() {
        let mut n = node(9, b"node");
        n.label = b"label".to_vec();
        n.network = true;
        let out = offline(&Snapshot {
            manual: vec![n],
            ..Snapshot::default()
        });
        assert_eq!(out[0].label, b"label");
        assert_eq!(out[0].detail, 0);
        assert!(!out[0].detail_network);
    }
    #[test]
    fn parsed_last_wins_and_manual_last_override() {
        let mut s = Snapshot {
            manual: vec![node(3, b"x"), node(4, b"x")],
            ..Snapshot::default()
        };
        let parsed = vec![node(1, b"x"), node(2, b"x")];
        assert_eq!(details(&s, parsed.clone())[b"x".as_slice()].token, 4);
        s.manual.clear();
        assert_eq!(details(&s, parsed)[b"x".as_slice()].token, 2);
    }
    #[test]
    fn source_first_matching_nonempty_label() {
        let s = Snapshot {
            subscription: 2,
            sources: vec![
                Source {
                    matches: false,
                    label: b"wrong".to_vec(),
                },
                Source {
                    matches: true,
                    label: vec![],
                },
                Source {
                    matches: true,
                    label: b"first".to_vec(),
                },
                Source {
                    matches: true,
                    label: b"second".to_vec(),
                },
            ],
            ..Snapshot::default()
        };
        assert_eq!(
            details(&s, vec![node(1, b"x")])[b"x".as_slice()].label,
            b"first"
        );
    }
    #[test]
    fn default_label_exact_utf8_and_no_label_for_empty_or_merged() {
        let expected = vec![
            0xe5, 0xbd, 0x93, 0xe5, 0x89, 0x8d, 0xe8, 0xae, 0xa2, 0xe9, 0x98, 0x85,
        ];
        for subscription in 0..=2 {
            let s = Snapshot {
                subscription,
                ..Snapshot::default()
            };
            let d = details(&s, vec![node(1, b"x")]);
            assert_eq!(
                d[b"x".as_slice()].label,
                if subscription == 2 {
                    expected.clone()
                } else {
                    vec![]
                }
            );
        }
    }
    #[test]
    fn manual_and_preexisting_labels_never_replaced() {
        let mut manual = node(1, b"manual");
        manual.manual = true;
        let mut labeled = node(2, b"labeled");
        labeled.label = b"keep".to_vec();
        let s = Snapshot {
            subscription: 2,
            ..Snapshot::default()
        };
        let d = details(&s, vec![manual, labeled]);
        assert!(d[b"manual".as_slice()].label.is_empty());
        assert_eq!(d[b"labeled".as_slice()].label, b"keep");
    }
    #[test]
    fn manual_override_not_relabelled() {
        let s = Snapshot {
            subscription: 2,
            manual: vec![node(9, b"x")],
            ..Snapshot::default()
        };
        let d = details(&s, vec![node(1, b"x")]);
        assert_eq!(d[b"x".as_slice()].token, 9);
        assert!(d[b"x".as_slice()].label.is_empty());
    }
    #[test]
    fn provider_filtered_before_seen() {
        let mut invalid = provider(1, b"x");
        invalid.valid = false;
        let mut info = provider(2, "\u{5269}\u{4f59}\u{6d41}\u{91cf}".as_bytes());
        info.informational = true;
        let mut seen = BTreeSet::new();
        let mut out = vec![];
        append_provider(
            &BTreeMap::new(),
            b"",
            &mut seen,
            vec![invalid, info, provider(3, b""), provider(4, b"x")],
            &mut out,
        );
        assert_eq!(tokens(&out), [4]);
        assert_eq!(seen.len(), 1);
    }
    #[test]
    fn provider_first_wins_across_batches_and_selected_exact() {
        let mut seen = BTreeSet::new();
        let mut out = vec![];
        append_provider(
            &BTreeMap::new(),
            b"X",
            &mut seen,
            vec![provider(1, b"x"), provider(2, b"X")],
            &mut out,
        );
        append_provider(
            &BTreeMap::new(),
            b"X",
            &mut seen,
            vec![provider(3, b"x"), provider(4, b"y")],
            &mut out,
        );
        assert_eq!(tokens(&out), [1, 2, 4]);
        assert_eq!(
            out.iter().map(|n| n.selected).collect::<Vec<_>>(),
            [false, true, false]
        );
    }
    #[test]
    fn provider_network_metadata_fallback_and_label() {
        let mut a = node(11, b"a");
        a.network = true;
        a.label = b"source".to_vec();
        let b = node(12, b"b");
        let d = details(&Snapshot::default(), vec![a, b]);
        let mut out = vec![];
        append_provider(
            &d,
            b"a",
            &mut BTreeSet::new(),
            vec![provider(1, b"a"), provider(2, b"b"), provider(3, b"c")],
            &mut out,
        );
        assert_eq!(
            out.iter()
                .map(|n| (n.node, n.detail, n.detail_network))
                .collect::<Vec<_>>(),
            [(1, 11, true), (2, 12, false), (3, 0, false)]
        );
        assert_eq!(out[0].label, b"source");
        assert!(out[2].label.is_empty());
    }
    #[test]
    fn arbitrary_name_bytes_preserved_in_maps_and_seen() {
        let name = [0xff, 0, 0x80];
        let d = details(&Snapshot::default(), vec![node(88, &name)]);
        let mut out = vec![];
        append_provider(
            &d,
            &name,
            &mut BTreeSet::new(),
            vec![provider(99, &name)],
            &mut out,
        );
        assert_eq!(out[0].detail, 88);
        assert!(out[0].selected);
    }
    #[test]
    fn preset_seen_and_existing_output_are_respected() {
        let mut seen = BTreeSet::from([b"x".to_vec()]);
        let mut out = vec![Projection {
            node: 7,
            ..Projection::default()
        }];
        append_provider(
            &BTreeMap::new(),
            b"y",
            &mut seen,
            vec![provider(1, b"x"), provider(2, b"y")],
            &mut out,
        );
        assert_eq!(tokens(&out), [7, 2]);
    }
    #[test]
    fn informational_is_native_and_handles_invalid_utf8() {
        let marker = "\u{5269}\u{4f59}\u{6d41}\u{91cf}".as_bytes();
        let mut invalid = vec![255];
        invalid.extend_from_slice(marker);
        let mut informational = provider(1, &invalid);
        informational.informational = false;
        let mut usable = provider(2, b"normal");
        usable.informational = true;
        let mut seen = BTreeSet::new();
        let mut out = vec![];
        append_provider(
            &BTreeMap::new(),
            b"",
            &mut seen,
            vec![informational, usable],
            &mut out,
        );
        assert_eq!(tokens(&out), [2]);
        assert!(!seen.contains(&invalid));
    }
}
