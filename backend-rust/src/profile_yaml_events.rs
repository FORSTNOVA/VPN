//! Keep scalar spelling and style until the destination field type is known.
//! Anchors point into an arena; no subtree is cloned while parsing aliases.
use serde_yaml_ng::{Mapping, Value};
use std::collections::{HashMap, HashSet};
use yaml_rust2::{
    parser::{Event, Parser},
    scanner::TScalarStyle,
};

enum Node {
    Scalar(String, bool),
    Seq(Vec<usize>),
    Map(Vec<(usize, usize)>),
    Alias(usize),
}

struct Graph {
    nodes: Vec<Node>,
    anchors: HashMap<usize, usize>,
}

impl Graph {
    fn read(
        &mut self,
        p: &mut Parser<std::str::Chars<'_>>,
        event: Event,
        depth: usize,
    ) -> Option<usize> {
        if depth > 96 || self.nodes.len() > 250_000 {
            return None;
        }
        let (node, anchor) = match event {
            Event::Scalar(text, style, id, None) => {
                (Node::Scalar(text, style == TScalarStyle::Plain), id)
            }
            Event::Alias(id) => (Node::Alias(*self.anchors.get(&id)?), 0),
            Event::SequenceStart(id, None) => {
                let mut items = Vec::new();
                loop {
                    let e = p.next_token().ok()?.0;
                    if e == Event::SequenceEnd {
                        break;
                    }
                    items.push(self.read(p, e, depth + 1)?);
                }
                (Node::Seq(items), id)
            }
            Event::MappingStart(id, None) => {
                let mut fields = Vec::new();
                loop {
                    let e = p.next_token().ok()?.0;
                    if e == Event::MappingEnd {
                        break;
                    }
                    let key = self.read(p, e, depth + 1)?;
                    let e = p.next_token().ok()?.0;
                    fields.push((key, self.read(p, e, depth + 1)?));
                }
                (Node::Map(fields), id)
            }
            _ => return None,
        };
        let index = self.nodes.len();
        self.nodes.push(node);
        if anchor != 0 {
            self.anchors.insert(anchor, index);
        }
        Some(index)
    }
}

#[derive(Clone, Copy)]
enum Field {
    Root,
    Policy,
    Groups,
    Group,
    Sections,
    Section,
    Strings,
    Rows,
    Proxy,
    Ws,
    Labels,
    Text,
    Any,
}
impl Field {
    fn child(self, k: &str) -> Self {
        match (self, k) {
            (Self::Policy, "proxy-groups") | (Self::Section, "groups") => Self::Groups,
            (Self::Policy, "smartvpn-policy-sections") => Self::Sections,
            (Self::Policy, "rules" | "smartvpn-source-rules")
            | (Self::Section, "rules")
            | (Self::Group, "proxies" | "use") => Self::Strings,
            (Self::Group, "name" | "type") | (Self::Section, "label") => Self::Text,
            (Self::Policy | Self::Root, "proxies") => Self::Rows,
            (Self::Root, "smartvpn-source-labels") => Self::Labels,
            (Self::Proxy, "name" | "type" | "server" | "network")
            | (Self::Ws, "path")
            | (Self::Labels, _) => Self::Text,
            (Self::Proxy, "ws-opts") => Self::Ws,
            _ => Self::Any,
        }
    }
}

fn scalar(raw: &str, plain: bool, text: bool) -> Option<Value> {
    if !plain {
        return Some(Value::String(raw.into()));
    }
    if matches!(raw, "" | "~" | "null" | "Null" | "NULL") {
        return Some(Value::Null);
    }
    if text {
        return Some(Value::String(raw.into()));
    }
    match raw {
        "true" | "True" | "TRUE" => return Some(Value::Bool(true)),
        "false" | "False" | "FALSE" => return Some(Value::Bool(false)),
        ".nan" | ".NaN" | ".NAN" => return Some(Value::from(f64::NAN)),
        ".inf" | ".Inf" | ".INF" | "+.inf" | "+.Inf" | "+.INF" => {
            return Some(Value::from(f64::INFINITY))
        }
        "-.inf" | "-.Inf" | "-.INF" => return Some(Value::from(f64::NEG_INFINITY)),
        _ => {}
    }
    let first = raw.as_bytes()[0];
    if !(first.is_ascii_digit() || matches!(first, b'.' | b'+' | b'-')) {
        return Some(Value::String(raw.into()));
    }
    if raw.as_bytes().get(4) == Some(&b'-') && raw.as_bytes()[..4].iter().all(u8::is_ascii_digit) {
        return None;
    }
    // ParseFloat permits underscores between digits in a dot-leading value.
    // Other number hints in yaml.v3 strip underscores before resolution.
    if first == b'.'
        && raw.as_bytes().iter().enumerate().any(|(i, &c)| {
            c == b'_'
                && !(i > 0
                    && raw.as_bytes()[i - 1].is_ascii_digit()
                    && raw.as_bytes().get(i + 1).is_some_and(u8::is_ascii_digit))
        })
    {
        return Some(Value::String(raw.into()));
    }
    let cleaned = raw.replace('_', "");
    let unsigned = cleaned.strip_prefix(['+', '-']).unwrap_or(&cleaned);
    let (base, digits) = if unsigned.starts_with("0x") || unsigned.starts_with("0X") {
        (16, &unsigned[2..])
    } else if unsigned.starts_with("0o") || unsigned.starts_with("0O") {
        (8, &unsigned[2..])
    } else if unsigned.starts_with("0b") || unsigned.starts_with("0B") {
        (2, &unsigned[2..])
    } else if unsigned.starts_with('0') && unsigned.len() > 1 {
        (8, unsigned)
    } else {
        (10, unsigned)
    };
    // yaml.v3 retries signed digits after a lowercase binary/octal prefix.
    if base != 10 && digits.starts_with(['+', '-']) {
        return None;
    }
    if !digits.is_empty() && digits.bytes().all(|b| (b as char).is_digit(base)) {
        if let Ok(n) = u64::from_str_radix(digits, base) {
            if cleaned.starts_with('-') {
                if n <= 1u64 << 63 {
                    return Some(Value::from((-(n as i128)) as i64));
                }
            } else if !cleaned.starts_with('+') || n <= i64::MAX as u64 {
                return Some(Value::from(n));
            }
        }
    }
    // yaml.v3 tries decimal floating point after base-0 integer resolution.
    if cleaned
        .bytes()
        .all(|b| b.is_ascii_digit() || matches!(b, b'+' | b'-' | b'.' | b'e' | b'E'))
    {
        if let Ok(n) = cleaned.parse::<f64>() {
            if n.is_finite() {
                return Some(Value::from(n));
            }
        }
    }
    Some(Value::String(raw.into()))
}

struct Budget {
    visits: usize,
    alias_visits: usize,
    bytes: usize,
}
impl Graph {
    fn value(
        &self,
        id: usize,
        field: Field,
        depth: usize,
        alias: bool,
        b: &mut Budget,
    ) -> Option<Value> {
        b.visits += 1;
        if alias {
            b.alias_visits += 1;
        }
        if depth > 96 || b.visits > 500_000 || (b.visits > 1000 && b.alias_visits * 2 > b.visits) {
            return None;
        }
        match &self.nodes[id] {
            Node::Alias(target) => self.value(*target, field, depth + 1, true, b),
            Node::Scalar(raw, plain) => {
                b.bytes = b.bytes.checked_sub(raw.len())?;
                scalar(raw, *plain, matches!(field, Field::Text))
            }
            Node::Seq(rows) => {
                let child = match field {
                    Field::Rows => Field::Proxy,
                    Field::Groups => Field::Group,
                    Field::Sections => Field::Section,
                    Field::Strings => Field::Text,
                    _ => Field::Any,
                };
                rows.iter()
                    .map(|id| self.value(*id, child, depth + 1, alias, b))
                    .collect::<Option<Vec<_>>>()
                    .map(Value::Sequence)
            }
            Node::Map(fields) => {
                let mut out = Mapping::new();
                let mut merge = None;
                let mut seen = HashSet::new();
                for &(k, v) in fields {
                    let Node::Scalar(key, plain) = &self.nodes[k] else {
                        return None;
                    };
                    if !seen.insert(key) {
                        return None;
                    }
                    b.bytes = b.bytes.checked_sub(key.len())?;
                    if *plain && key == "<<" {
                        merge = Some(v);
                        continue;
                    }
                    if !matches!(scalar(key, *plain, false)?, Value::String(_)) {
                        return None;
                    }
                    out.insert(
                        Value::String(key.clone()),
                        self.value(v, field.child(key), depth + 1, alias, b)?,
                    );
                }
                if let Some(id) = merge {
                    self.merge(id, field, depth + 1, b, &mut out)?;
                }
                Some(Value::Mapping(out))
            }
        }
    }
    fn merge(
        &self,
        id: usize,
        field: Field,
        depth: usize,
        b: &mut Budget,
        out: &mut Mapping,
    ) -> Option<()> {
        // Sequences give precedence to earlier mappings. Local keys already
        // occupy their slots irrespective of where the merge key appeared.
        let ids = match &self.nodes[id] {
            Node::Seq(items) => items.as_slice(),
            _ => std::slice::from_ref(&id),
        };
        for id in ids {
            let Value::Mapping(map) = self.value(*id, field, depth + 1, true, b)? else {
                return None;
            };
            for (k, v) in map {
                if !out.contains_key(&k) {
                    out.insert(k, v);
                }
            }
        }
        Some(())
    }
}

pub(super) fn document(input: &str, metadata: bool) -> Option<Value> {
    read_document(input, if metadata { Field::Root } else { Field::Any })
}

pub(super) fn policy_document(input: &str) -> Option<Value> {
    read_document(input, Field::Policy)
}

fn read_document(input: &str, field: Field) -> Option<Value> {
    let mut p = Parser::new_from_str(input);
    if p.next_token().ok()?.0 != Event::StreamStart
        || p.next_token().ok()?.0 != Event::DocumentStart
    {
        return None;
    }
    let mut graph = Graph {
        nodes: Vec::new(),
        anchors: HashMap::new(),
    };
    let event = p.next_token().ok()?.0;
    let root = graph.read(&mut p, event, 0)?;
    if p.next_token().ok()?.0 != Event::DocumentEnd || p.next_token().ok()?.0 != Event::StreamEnd {
        return None;
    }
    graph.value(
        root,
        field,
        0,
        false,
        &mut Budget {
            visits: 0,
            alias_visits: 0,
            bytes: 16 << 20,
        },
    )
}
