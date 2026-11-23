//! Metadata-only operations: the host retains proxy credentials and resources.
use serde_json::{json, Map};

pub(super) fn manual_details(input: &str) -> Option<Vec<u8>> {
    // Rows have exactly the Go-typed facts for name, kind, network, TLS,
    // WS path and header presence. Later manual entries replace earlier ones.
    type Row = (String, String, String, bool, String, bool);
    let rows: Vec<Row> = serde_json::from_str(input).ok()?;
    let mut details = Map::new();
    for (name, kind, network, tls, path, headers) in rows {
        details.insert(
            name.clone(),
            json!({
                "name":name, "type":kind, "network":network, "tls":tls,
                "wsPathConfigured":!path.is_empty(), "wsHostConfigured":headers, "manual":true
            }),
        );
    }
    serde_json::to_vec(&details).ok()
}

pub(super) fn annotation_flags(input: &str) -> Option<Vec<u8>> {
    type Row = (String, bool, bool);
    let (selected, rows): (Option<String>, Vec<Row>) = serde_json::from_str(input).ok()?;
    Some(
        rows.into_iter()
            .map(|(name, manual, labeled)| {
                u8::from(!manual && !labeled) | (u8::from(selected.as_ref() == Some(&name)) << 1)
            })
            .collect(),
    )
}
