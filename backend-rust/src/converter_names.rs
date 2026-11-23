//! Byte-preserving converter naming and reference rewriting.
use std::collections::{HashMap, HashSet};
pub(crate) fn unique_name(name: &[u8], used: &mut HashSet<Vec<u8>>) -> Vec<u8> {
    if used.insert(name.to_vec()) {
        return name.to_vec();
    }
    for suffix in 2u64.. {
        let mut candidate = name.to_vec();
        candidate.extend_from_slice(format!(" ({suffix})").as_bytes());
        if used.insert(candidate.clone()) {
            return candidate;
        }
    }
    unreachable!("bounded input cannot exhaust all u64 suffixes")
}
pub(crate) fn rule_target(rule: &[u8]) -> Option<std::ops::Range<usize>> {
    let mut end = rule.len();
    let mut comma = rule.iter().rposition(|&b| b == b',')?;
    if trim(&rule[comma + 1..]) == b"no-resolve" {
        end = comma;
        comma = rule[..end].iter().rposition(|&b| b == b',')?;
    }
    let field = comma + 1..end;
    match trim(&rule[field.clone()]) {
        b"DIRECT" | b"REJECT" => None,
        _ => Some(field),
    }
}
pub(crate) fn rewrite_rule(rule: &[u8], names: &HashMap<Vec<u8>, Vec<u8>>) -> Vec<u8> {
    if let Some(field) = rule_target(rule) {
        if let Some(alias) = names.get(trim(&rule[field.clone()])) {
            return [&rule[..field.start], alias.as_slice(), &rule[field.end..]].concat();
        }
    }
    rule.to_vec()
}
pub(crate) fn trim(v: &[u8]) -> &[u8] {
    fn space(c: char) -> bool {
        matches!(c,'\u{0009}'..='\u{000d}'|' '|'\u{0085}'|'\u{00a0}'|'\u{1680}'|'\u{2000}'..='\u{200a}'|'\u{2028}'|'\u{2029}'|'\u{202f}'|'\u{205f}'|'\u{3000}')
    }
    let (mut start, mut end) = (0, v.len());
    while start < end {
        let mut consumed = 0;
        for n in 1..=4.min(end - start) {
            if let Ok(s) = std::str::from_utf8(&v[start..start + n]) {
                if s.chars().count() == 1 && space(s.chars().next().unwrap()) {
                    consumed = n;
                    break;
                }
            }
        }
        if consumed == 0 {
            break;
        }
        start += consumed;
    }
    while end > start {
        let mut consumed = 0;
        for n in 1..=4.min(end - start) {
            if let Ok(s) = std::str::from_utf8(&v[end - n..end]) {
                if s.chars().count() == 1 && space(s.chars().next().unwrap()) {
                    consumed = n;
                    break;
                }
            }
        }
        if consumed == 0 {
            break;
        }
        end -= consumed;
    }
    &v[start..end]
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn suffix_collisions_and_arbitrary_bytes() {
        let mut u = HashSet::new();
        for (v, w) in [
            (b"Node".as_slice(), b"Node".as_slice()),
            (b"Node (2)", b"Node (2)"),
            (b"Node", b"Node (3)"),
            (b"Node (2)", b"Node (2) (2)"),
        ] {
            assert_eq!(unique_name(v, &mut u), w)
        }
        assert_eq!(unique_name(&[255], &mut u), [255]);
        assert_eq!(unique_name(&[255], &mut u), [255, b' ', b'(', b'2', b')']);
    }
    #[test]
    fn rules_last_target_no_resolve_and_exemptions() {
        let m = HashMap::from([
            (b"A".to_vec(), b"label / A".to_vec()),
            (b"DIRECT".to_vec(), b"bad".to_vec()),
        ]);
        assert_eq!(
            rewrite_rule(b"DOMAIN,x, A , no-resolve", &m),
            b"DOMAIN,x,label / A, no-resolve"
        );
        assert_eq!(rewrite_rule(b"MATCH,A", &m), b"MATCH,label / A");
        assert_eq!(rewrite_rule(b"MATCH,DIRECT", &m), b"MATCH,DIRECT");
        assert_eq!(rewrite_rule(b"A", &m), b"A");
        assert_eq!(rewrite_rule(b"MATCH,unknown", &m), b"MATCH,unknown");
    }
    #[test]
    fn unicode_space_does_not_lossily_decode_invalid_bytes() {
        assert_eq!(trim("\u{3000}N\u{0085}".as_bytes()), b"N");
        assert_eq!(trim(b" \xff "), [255]);
    }
}
