use super::{checks, state::RuntimeState};
use serde_json::{json, Value};
use std::hash::{DefaultHasher, Hash, Hasher};

pub fn fingerprint(proxy: &Value) -> u64 {
    let mut hash = DefaultHasher::new();
    proxy.hash(&mut hash);
    hash.finish()
}
pub fn fresh(row: &Value, proxy: &Value) -> bool {
    row["status"] == "verified"
        && row["fingerprint"] == fingerprint(proxy)
        && row["expires"]
            .as_i64()
            .is_some_and(|t| t > chrono::Utc::now().timestamp())
}
pub async fn verify(proxy: &Value) -> Value {
    let result=async {
        let name=proxy["name"].as_str().ok_or("missing node name")?;
        let yaml=serde_yaml_ng::to_string(&json!({"mixed-port":0,"proxies":[proxy],
            "proxy-groups":[{"name":"SmartVPN","type":"select","proxies":[name]}],"rules":["MATCH,SmartVPN"]})).map_err(|e|e.to_string())?;
        let router=smartvpn_engine::config_bridge::build_router_from_clash_yaml(&yaml,None).map_err(|e|e.to_string())?.router;
        let trace=checks::request(&router,"https://www.cloudflare.com/cdn-cgi/trace",None,8192,true).await.map_err(|e|e.to_string())?.1;
        let trace=std::str::from_utf8(&trace).map_err(|e|e.to_string())?;
        let country=trace.lines().find_map(|l|l.strip_prefix("loc=")).ok_or("trace has no country")?;
        let ip=trace.lines().find_map(|l|l.strip_prefix("ip=")).ok_or("trace has no address")?.parse::<std::net::IpAddr>().map_err(|e|e.to_string())?;
        let second=checks::request(&router,"https://ipinfo.io/country",None,64,true).await.map_err(|e|e.to_string())?.1;
        if country.len()!=2 || !country.bytes().all(|b|b.is_ascii_uppercase()) || std::str::from_utf8(&second).map_err(|e|e.to_string())?.trim()!=country {return Err(String::from("出口地区来源不一致"));}
        Ok(json!({"country":country,"exitIp":ip,"status":"verified","sources":["Cloudflare","IPinfo"]}))
    }.await;
    let mut row = match result {
        Ok(v) => v,
        Err(e) => json!({"status":"unreachable","detail":e}),
    };
    row["verifiedAt"] = json!(chrono::Utc::now().to_rfc3339());
    row["expires"] = json!(chrono::Utc::now().timestamp() + 86400);
    row["fingerprint"] = json!(fingerprint(proxy));
    row
}
pub fn pool(st: &RuntimeState, country: &str) -> Vec<String> {
    st.all_proxy_values()
        .into_iter()
        .filter_map(|n| {
            let name = n["name"].as_str()?;
            let r = st.persisted.regions.get(name)?;
            (fresh(r, &n) && r["country"] == country).then(|| name.to_owned())
        })
        .collect()
}
pub fn report(st: &RuntimeState) -> Value {
    let nodes: Vec<_> = st
        .all_proxy_values()
        .iter()
        .map(|n| {
            let name = n["name"].as_str().unwrap();
            let mut r = st
                .persisted
                .regions
                .get(name)
                .cloned()
                .unwrap_or_else(|| json!({"status":"unverified"}));
            r["stale"] = json!(!fresh(&r, n));
            r["name"] = json!(name);
            r.as_object_mut().unwrap().remove("fingerprint");
            r
        })
        .collect();
    let verified = nodes
        .iter()
        .filter(|n| n["status"] == "verified" && n["stale"] == false)
        .count();
    let failed = nodes
        .iter()
        .filter(|n| n["status"] == "unreachable")
        .count();
    json!({"nodes":nodes,"pool":pool(st,&st.persisted.locked_region),"lockedRegion":st.persisted.locked_region,"health":st.health,
       "job":{"running":st.verifying,"done":verified+failed,"total":nodes.len(),"verified":verified,"failed":failed}})
}
