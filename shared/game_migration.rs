use serde_json::{json, Value};
use std::collections::HashSet;

/// Append after existing rules so manually configured groups retain priority.
pub fn migrated_group<'a>(
    games: Vec<String>,
    ids: impl Iterator<Item = &'a str>,
) -> Result<Option<Value>, String> {
    let mut apps = Vec::new();
    let mut seen = HashSet::new();
    for game in games {
        let app = game.trim();
        if app.is_empty() || app.contains([',', '*']) || app.chars().any(char::is_control) {
            return Err(format!("游戏直连名单包含无效条目: {app:?}"));
        }
        if seen.insert(app.to_lowercase()) {
            apps.push(app.to_owned());
        }
    }
    if apps.is_empty() {
        return Ok(None);
    }
    let ids: HashSet<_> = ids.collect();
    let mut id = "legacy-games-direct".to_owned();
    while ids.contains(id.as_str()) {
        id.push('_');
    }
    Ok(Some(
        json!({"id":id,"name":"游戏直连","apps":apps,"targetType":"direct","enabled":true}),
    ))
}

/// Compare Windows process names with exact paths. Two distinct exact paths can
/// be routed independently; a bare process name matches both.
fn overlaps(a: &str, b: &str, windows: bool) -> bool {
    let a = a.trim().to_lowercase();
    let b = b.trim().to_lowercase();
    if !windows {
        return a == b;
    }
    let a = a.replace('\\', "/");
    let b = b.replace('\\', "/");
    a == b
        || (!a.contains('/') && b.rsplit('/').next() == Some(&a))
        || (!b.contains('/') && a.rsplit('/').next() == Some(&b))
}

pub fn membership(
    groups: &[(&str, bool, &[String])],
    windows: bool,
    migrate: bool,
) -> Result<Vec<Vec<String>>, String> {
    let mut kept = vec![Vec::<String>::new(); groups.len()];
    let mut order: Vec<_> = (0..groups.len()).collect();
    // Enabled rules were evaluated first, in list order. A disabled rule can
    // keep an app only if no enabled group already claimed it.
    order.sort_by_key(|&i| !groups[i].1);
    let mut owners: Vec<(&str, usize)> = Vec::new();
    for i in order {
        for app in groups[i].2 {
            if let Some(&(_, owner)) = owners
                .iter()
                .find(|(other, j)| *j != i && overlaps(app, other, windows))
            {
                if !migrate {
                    return Err(format!("{app} 已属于分组「{}」", groups[owner].0));
                }
            } else {
                owners.push((app, i));
                kept[i].push(app.clone());
            }
        }
    }
    Ok(kept)
}
