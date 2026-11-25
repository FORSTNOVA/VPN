use super::{state::RuntimeState, subs};
use serde_json::{json, Value};
use std::{
    collections::BTreeSet,
    fs,
    io::{self, Read},
    path::Path,
    sync::Arc,
    time::Duration,
};
use tokio::sync::Mutex;

const MAX: usize = 4 << 20;
const MIRRORS: &[&str] = &[
    "https://cdn.jsdelivr.net/gh/gaoyifan/china-operator-ip@ip-lists/",
    "https://testingcf.jsdelivr.net/gh/gaoyifan/china-operator-ip@ip-lists/",
    "https://ghproxy.net/https://raw.githubusercontent.com/gaoyifan/china-operator-ip/ip-lists/",
];
#[derive(Default)]
pub struct China {
    pub rows: Vec<String>,
    updated: String,
    source: String,
    pub refreshing: bool,
    error: String,
    pub active: bool,
}

fn parse(text: &str, v4: Option<bool>) -> Result<Vec<String>, String> {
    if text.len() > MAX {
        return Err("国内地址列表超过 4 MiB".into());
    }
    let mut rows = BTreeSet::new();
    for (i, line) in text.lines().enumerate() {
        let line = line.split('#').next().unwrap().trim();
        if line.is_empty() {
            continue;
        }
        let (ip, bits) =
            smartvpn_engine::domestic::prefix(line).map_err(|e| format!("第 {} 行：{e}", i + 1))?;
        if v4.is_some_and(|family| family != ip.is_ipv4()) {
            return Err(format!("第 {} 行地址族错误", i + 1));
        }
        rows.insert(format!("{ip}/{bits}"));
    }
    if rows.len() < 1000 {
        return Err(format!(
            "仅有 {} 个有效地址段，预期至少 1000 个",
            rows.len()
        ));
    }
    Ok(rows.into_iter().collect())
}

impl China {
    pub fn load(home: &Path) -> Self {
        let path = home.join("providers/china-ip.txt");
        let mut list = Self::default();
        let file = match fs::File::open(&path) {
            Ok(f) => f,
            Err(e) if e.kind() == io::ErrorKind::NotFound => return list,
            Err(e) => {
                list.error = e.to_string();
                return list;
            }
        };
        let result = (|| {
            let modified = file
                .metadata()
                .and_then(|m| m.modified())
                .map_err(|e| e.to_string())?;
            let mut text = String::new();
            file.take((MAX + 1) as u64)
                .read_to_string(&mut text)
                .map_err(|e| e.to_string())?;
            list.rows = parse(&text, None)?;
            list.source = "本地缓存".into();
            list.updated = chrono::DateTime::<chrono::Utc>::from(modified).to_rfc3339();
            Ok::<(), String>(())
        })();
        if let Err(e) = result {
            list.error = e;
        }
        list
    }

    pub fn status(&self, enabled: bool) -> Value {
        json!({"enabled":enabled,"available":!self.rows.is_empty(),"entries":self.rows.len(),"updatedAt":self.updated,
            "source":self.source,"error":self.error,"refreshing":self.refreshing,"active":self.active})
    }
}

async fn download(
    protect: Option<smartvpn_engine::dialer::SocketProtectHook>,
) -> Result<(Vec<String>, String), String> {
    let mut errors = Vec::new();
    for mirror in MIRRORS {
        let fetch = |file: &'static str, v4| async move {
            let text = subs::fetch_https_list(&format!("{mirror}{file}"), protect, MAX).await?;
            parse(&text, Some(v4))
        };
        match tokio::time::timeout(Duration::from_secs(12), async {
            tokio::try_join!(fetch("china.txt", true), fetch("china6.txt", false))
        })
        .await
        {
            Ok(Ok((mut v4, v6))) => {
                v4.extend(v6);
                v4.sort_unstable();
                return Ok((v4, mirror.to_string()));
            }
            Ok(Err(e)) => errors.push(format!("{mirror}: {e}")),
            Err(_) => errors.push(format!("{mirror}: 下载超时")),
        }
    }
    Err(errors.join(" / "))
}

pub async fn start_refresh(state: Arc<Mutex<RuntimeState>>) {
    let (path, protect) = {
        let mut st = state.lock().await;
        if st.china.refreshing {
            return;
        }
        st.china.refreshing = true;
        st.china.error.clear();
        (
            st.home.join("providers/china-ip.txt"),
            st.active_protect_hook(),
        )
    };
    tokio::spawn(async move {
        let result = download(protect).await.and_then(|(rows, source)| {
            let bytes = rows.join("\n") + "\n";
            if bytes.len() > MAX {
                return Err("合并后的列表超过 4 MiB".into());
            }
            fs::create_dir_all(path.parent().unwrap()).map_err(|e| e.to_string())?;
            let tmp = path.with_extension("txt.tmp");
            fs::write(&tmp, bytes)
                .and_then(|_| fs::rename(tmp, path))
                .map_err(|e| e.to_string())?;
            Ok((rows, source))
        });
        let mut st = state.lock().await;
        st.china.refreshing = false;
        match result {
            Ok((rows, source)) => {
                st.china.active &= st.china.rows == rows;
                st.china.rows = rows;
                st.china.source = source;
                st.china.updated = chrono::Utc::now().to_rfc3339();
            }
            Err(e) => st.china.error = e,
        }
    });
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn existing_cache_is_validated_before_use() {
        let home = std::env::temp_dir().join(format!("smartvpn-cn-test-{}", rand::random::<u64>()));
        fs::create_dir_all(home.join("providers")).unwrap();
        let path = home.join("providers/china-ip.txt");
        let rows = (0..1000)
            .map(|i| format!("10.{}.{}.0/24", i / 256, i % 256))
            .collect::<Vec<_>>()
            .join("\n");
        fs::write(&path, &rows).unwrap();
        let loaded = China::load(&home);
        assert_eq!(loaded.rows.len(), 1000);
        assert!(parse(&rows, Some(false)).is_err());
        for bad in ["0.0.0.0/0", "10.0.0.1/24", "2001:db8::/129", "garbage"] {
            fs::write(&path, format!("{rows}\n{bad}")).unwrap();
            let failed = China::load(&home);
            assert!(failed.rows.is_empty());
            assert!(!failed.error.is_empty());
        }
        fs::remove_dir_all(home).unwrap();
    }
    #[tokio::test]
    #[ignore = "fetches the published IPv4 and IPv6 lists"]
    async fn published_lists_classify_known_destinations() {
        let (rows, source) = download(None).await.unwrap();
        let ranges = smartvpn_engine::domestic::Ranges::new(&rows).unwrap();
        assert!(ranges.contains("223.5.5.5".parse().unwrap()));
        assert!(!ranges.contains("1.1.1.1".parse().unwrap()));
        println!("{} ranges from {}", rows.len(), source);
    }
}
